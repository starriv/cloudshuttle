// Package engine transfers files with rclone, compiled into the app as a
// library: no rclone executable and no rclone.conf. Each run builds its
// remotes from the app's storages in memory.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"path"
	"runtime/debug"
	"strconv"
	"strings"
	stdsync "sync"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fs/rc"
	"github.com/rclone/rclone/fs/sync"

	// The backends the app uses.
	_ "github.com/rclone/rclone/backend/local"
	_ "github.com/rclone/rclone/backend/s3"
	_ "github.com/rclone/rclone/backend/smb"
)

// S3 is the connection to an S3-compatible bucket.
type S3 struct {
	Provider        string
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
}

// SMB is the connection to an SMB server. It signs in with NTLM only:
// rclone's Kerberos reads its configuration from the environment, which the
// app keeps out of.
type SMB struct {
	Host     string // a host name or an IP address; IPv6 in brackets
	Port     int    // 0 for 445
	Username string
	Password string // empty for a guest
}

// Location is a folder on this computer when S3 and SMB are both nil, a
// path in the bucket of S3, or a path on the server of SMB, which begins
// with the share: at the top, an SMB location holds the server's shares.
type Location struct {
	S3   *S3
	SMB  *SMB
	Path string
}

// Job is one transfer.
type Job struct {
	ID          string // names the job's statistics
	Source      Location
	Destination Location
	// Sync makes the destination identical to the source, deleting what
	// the source does not have; otherwise files are copied only.
	Sync bool
	// DryRun reports what would be transferred without changing anything.
	DryRun bool
	// Force lets a sync delete most of its destination, or all of it when
	// the source holds nothing. Without it, Run stops such a sync for a
	// person to confirm it: a scheduled run must never empty a backup.
	Force bool
	// Trash, when set, is the folder of the destination, relative to it,
	// that the job moves the files it deletes or replaces into (see
	// TrashFolder), and TrashDays how long the trash keeps a run's folder:
	// the job deletes the older ones once it is done. A dry run has none.
	Trash     string
	TrashDays int
	// Transfers, Checkers and Retries say how hard the job works; zero
	// leaves rclone's defaults. BandwidthMBps caps the run's speed, zero
	// for none.
	Transfers     int
	Checkers      int
	Retries       int
	BandwidthMBps int
	// Log, when set, gets the job's log as it goes, from any goroutine:
	// the app's lines about it and rclone's, at INFO and above.
	Log func(Line)
}

// Progress is what a job has done so far.
type Progress struct {
	Bytes          int64
	TotalBytes     int64
	Transfers      int64
	TotalTransfers int64
	Checks         int64
	Deletes        int64
	Errors         int64
	Speed          float64
	// Trashed is the files a job moved into the trash: those a sync
	// deleted, and the earlier versions of those it replaced.
	Trashed int64
}

// ErrIsFile is returned for a location that names a file, not a folder.
var ErrIsFile = errors.New("location is a file, not a folder")

// ErrFolderNotFound is returned by Folders for a folder that does not exist.
var ErrFolderNotFound = errors.New("folder not found")

// Errors connecting to a server. The error returned wraps one of them and
// rclone's own error.
var (
	ErrUnreachable   = errors.New("server unreachable")
	ErrAuthFailed    = errors.New("authentication failed")
	ErrShareNotFound = errors.New("share not found")
)

// Errors that stop a sync before it changes anything: an empty source would
// empty the destination, and a run that would delete most of a destination
// waits for a person to confirm it.
var (
	ErrEmptySource = errors.New("the source is empty, so the destination was left alone")
	ErrLargeDelete = errors.New("the sync would delete most of the destination")
)

// guardDeletes is how many files a sync may delete before it stops for a
// person to confirm it, as long as they are more than half the destination.
const guardDeletes = 10

// Version is the version of rclone compiled into the app, as its module
// says: the library's own fs.Version marks every build as a development one.
func Version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/rclone/rclone" {
				return dep.Version
			}
		}
	}
	return strings.TrimSuffix(fs.Version, "-DEV")
}

// Check lists the top of a location, to tell whether it can be reached: for
// an SMB server, its shares, which takes signing in.
func Check(ctx context.Context, loc Location) error {
	ctx = probing(ctx)
	f, err := newFs(ctx, loc)
	if err != nil {
		return classify(err)
	}
	defer shutdown(f)
	_, err = f.List(ctx, "")
	if errors.Is(err, fs.ErrorDirNotFound) {
		// A bucket or folder that does not exist yet is created by the
		// first run; reaching the service is what counts.
		return nil
	}
	return classify(err)
}

// Folders lists the names of the folders in a location, to choose one. At
// the top of an SMB server they are its shares, but for special ones such
// as IPC$, and for those it cannot open.
func Folders(ctx context.Context, loc Location) ([]string, error) {
	names, err := folders(ctx, loc)
	if err != nil || loc.SMB == nil || strings.Trim(loc.Path, "/") != "" {
		return names, err
	}
	return openShares(ctx, *loc.SMB, names), nil
}

// How long opening a share to tell whether it still exists may take: a
// share that never answers must not hold the browser.
const (
	shareProbes      = 4
	shareProbeTime   = 3 * time.Second
	shareProbeBudget = 6 * time.Second
)

// shareProbe is one share's answer: whether the server still opens it.
type shareProbe struct {
	index int
	open  bool
}

// openShares keeps the shares the server lets one into. A server answers
// what shares it has from one list and opens them by another: a NAS may go
// on listing a share by its old name after it was renamed, though opening
// it fails as for a share that does not exist. Each share is opened once,
// a few at a time, with a short time of its own and a budget for them all:
// what the budget leaves unprobed stays, for the browser to open and say
// what is wrong if it cannot.
func openShares(ctx context.Context, server SMB, shares []string) []string {
	probeCtx, cancel := context.WithTimeout(ctx, shareProbeBudget)
	defer cancel()
	slots := make(chan struct{}, shareProbes)
	results := make(chan shareProbe, len(shares))
	for i, share := range shares {
		go func() {
			select {
			case slots <- struct{}{}:
			case <-probeCtx.Done():
				return
			}
			defer func() { <-slots }()
			one, cancel := context.WithTimeout(probeCtx, shareProbeTime)
			defer cancel()
			_, err := folders(one, Location{SMB: &server, Path: share})
			results <- shareProbe{index: i, open: !errors.Is(err, ErrShareNotFound)}
		}()
	}
	open := make([]bool, len(shares))
	for i := range open {
		open[i] = true
	}
	seen := 0
	for seen < len(shares) {
		select {
		case probe := <-results:
			open[probe.index] = probe.open
			seen++
		case <-probeCtx.Done():
			slog.Warn("probe shares", "server", server.Host, "probed", seen, "of", len(shares))
			seen = len(shares)
		}
	}
	var out []string
	for i, share := range shares {
		if open[i] {
			out = append(out, share)
		}
	}
	return out
}

func folders(ctx context.Context, loc Location) ([]string, error) {
	ctx = probing(ctx)
	f, err := newDirFs(ctx, loc)
	if err != nil {
		return nil, classify(err)
	}
	defer shutdown(f)
	entries, err := f.List(ctx, "")
	if errors.Is(err, fs.ErrorDirNotFound) {
		// S3 has no folders to miss: a prefix with nothing under it lists
		// as empty.
		return nil, ErrFolderNotFound
	}
	if err != nil {
		return nil, classify(err)
	}
	var names []string
	for _, e := range entries {
		if _, ok := e.(fs.Directory); ok {
			names = append(names, path.Base(e.Remote()))
		}
	}
	return names, nil
}

// probing configures a quick look at a server, which a person waits for:
// one attempt, as retrying a wrong password or a server that is off only
// delays the answer. Runs keep rclone's retries, which ride out a NAS
// waking up.
func probing(ctx context.Context) context.Context {
	ctx, ci := fs.AddConfig(ctx)
	ci.LowLevelRetries = 1
	ci.ConnectTimeout = fs.Duration(10 * time.Second)
	ci.LogLevel = fs.LogLevelError
	return ctx
}

// NTSTATUS codes an SMB server answers with ([MS-ERREF] 2.3.1).
const (
	statusAccessDenied   = 0xC0000022
	statusLogonFailure   = 0xC000006D
	statusAccountLocked  = 0xC0000234
	statusBadNetworkName = 0xC00000CC
)

// classify marks the errors of connecting to a server with what went wrong,
// keeping rclone's error for the details.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var status *smb2.ResponseError
	if errors.As(err, &status) {
		switch status.Code {
		case statusLogonFailure, statusAccessDenied, statusAccountLocked:
			return fmt.Errorf("%w: %w", ErrAuthFailed, err)
		case statusBadNetworkName:
			return fmt.Errorf("%w: %w", ErrShareNotFound, err)
		}
		return err
	}
	var netErr net.Error
	var dnsErr *net.DNSError
	var opErr *net.OpError
	if errors.As(err, &dnsErr) || errors.As(err, &opErr) || errors.As(err, &netErr) ||
		errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	return err
}

// Run transfers the job's files, calling progress about twice a second from
// another goroutine, and returns the final counts. Canceling ctx stops it.
var runMu stdsync.Mutex // rclone's bandwidth limiter and log sink are process-global

func Run(ctx context.Context, job Job, progress func(Progress)) (Progress, error) {
	runMu.Lock()
	defer runMu.Unlock()
	for _, loc := range []Location{job.Source, job.Destination} {
		if err := validateEndpoint(loc); err != nil {
			return Progress{}, err
		}
	}
	ctx, ci := fs.AddConfig(ctx)
	ci.DryRun = job.DryRun
	if job.Transfers > 0 {
		ci.Transfers = job.Transfers
	}
	if job.Checkers > 0 {
		ci.Checkers = job.Checkers
	}
	if job.Retries > 0 {
		ci.LowLevelRetries = job.Retries
	}
	if job.BandwidthMBps > 0 {
		if err := ci.BwLimit.Set(fmt.Sprintf("%dM", job.BandwidthMBps)); err != nil {
			return Progress{}, fmt.Errorf("bandwidth limit: %w", err)
		}
	}
	ctx, err := excludeTrash(ctx)
	if err != nil {
		return Progress{}, fmt.Errorf("filter: %w", err)
	}
	accounting.TokenBucket.SetBwLimit(ci.BwLimit.LimitAt(time.Now()).Bandwidth)
	defer accounting.TokenBucket.SetBwLimit(fs.BwPair{})
	stop := listen(job.ID, job.Log)
	defer stop()
	log := func(l Line) {
		if job.Log != nil {
			job.Log(l)
		}
	}

	group := "cloudshuttle-" + job.ID
	ctx = accounting.WithStatsGroup(ctx, group)
	stats := accounting.StatsGroup(ctx, group)
	defer deleteStats(group)

	log(appLine("INFO", "rclone %s", Version()))
	log(appLine("INFO", "opening the source: %s", describe(job.Source)))
	src, err := newFs(ctx, job.Source)
	if err != nil {
		err = classify(err)
		log(appLine("ERROR", "opening the source failed: %v", err))
		return Progress{}, fmt.Errorf("source: %w", err)
	}
	defer shutdown(src)
	log(appLine("INFO", "source opened: %s", src))
	log(appLine("INFO", "opening the destination: %s", describe(job.Destination)))
	dst, err := newFs(ctx, job.Destination)
	if err != nil {
		err = classify(err)
		log(appLine("ERROR", "opening the destination failed: %v", err))
		return Progress{}, fmt.Errorf("destination: %w", err)
	}
	defer shutdown(dst)
	log(appLine("INFO", "destination opened: %s", dst))
	op := "copy: new and changed files, no deletes"
	if job.Sync {
		op = "sync: the destination becomes the source, deleting what the source lacks"
	}
	if job.DryRun {
		op += "; dry run, nothing changes"
	}
	log(appLine("INFO", "%s", op))

	// A sync may delete what the source does not have. Count that first, so
	// that a source that holds nothing, or a run that would delete most of
	// the destination, stops before touching a file: something emptied the
	// source, and the destination is the only copy left.
	if job.Sync && !job.DryRun {
		deletes, err := checkDeletes(ctx, job, src, dst, group, log)
		if err != nil {
			return Progress{}, err
		}
		if deletes >= 0 {
			// No more than the preview counted, should the source change
			// as the run goes. A preview that found no deletes allows
			// none: a file that turned up as one stops the run, rather
			// than go unannounced.
			ci.MaxDelete = deletes
		}
	}
	var trash *trashFs
	if !job.DryRun && job.Trash != "" {
		trash, err = useTrash(ctx, ci, job)
		if err != nil {
			log(appLine("ERROR", "opening the trash failed: %v", err))
			return Progress{}, err
		}
		defer shutdown(trash.Fs)
		log(appLine("INFO", "the files the run deletes or replaces go into %s, kept %d days", job.Trash, job.TrashDays))
	}
	measure := func() Progress {
		p := snapshot(stats)
		if trash != nil {
			// Each move into the trash counts a check of rclone's: the
			// checks tell the files compared.
			p.Checks = max(p.Checks-trash.moves.Load(), 0)
			p.Trashed = trash.moved.Load()
		}
		return p
	}

	started := time.Now()

	done := make(chan struct{})
	if progress != nil {
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					progress(measure())
				}
			}
		}()
	}

	if job.Sync {
		err = sync.Sync(ctx, dst, src, false)
	} else {
		err = sync.CopyDir(ctx, dst, src, false)
	}
	close(done)
	final := measure()
	log(appLine("INFO", "%d of %d files, %d of %d bytes; %d checks, %d deletes, %d errors, in %s",
		final.Transfers, final.TotalTransfers, final.Bytes, final.TotalBytes,
		final.Checks, final.Deletes, final.Errors, time.Since(started).Round(time.Millisecond)))
	if trash != nil && final.Trashed > 0 {
		log(appLine("INFO", "%d files went into %s", final.Trashed, job.Trash))
	}
	if ctx.Err() != nil {
		log(appLine("WARN", "stopped: %v", ctx.Err()))
		return final, ctx.Err()
	}
	if err != nil {
		err = classify(err)
		log(appLine("ERROR", "failed: %v", err))
	}
	if trash != nil {
		pruneTrash(ctx, job, group, log)
	}
	return final, err
}

// checkDeletes runs a sync without changing anything first, and returns what
// it would delete. An empty source, or a run that would delete most of the
// destination, fails: the app asks a person to confirm it, rather than let a
// scheduled run empty a backup.
func checkDeletes(ctx context.Context, job Job, src, dst fs.Fs, group string, log func(Line)) (int64, error) {
	if job.Force {
		log(appLine("WARN", "the delete check is off: a forced run may delete most of the destination"))
		return -1, nil
	}
	preview, listed, err := previewSync(ctx, src, dst, group)
	if err != nil {
		err = classify(err)
		log(appLine("ERROR", "the preview failed: %v", err))
		return 0, fmt.Errorf("preview: %w", err)
	}
	if preview.Deletes == 0 {
		return 0, nil
	}
	// rclone counts a check for each file it sees on both sides, and a
	// delete counts one too; a file it copies counts a transfer. So the
	// checks count the destination's files, less its deletes for the
	// source.
	source := preview.Checks - preview.Deletes + preview.Transfers
	destination := preview.Checks
	log(appLine("INFO", "the preview would delete %d of the destination's %d files; the source has %d",
		preview.Deletes, destination, source))
	stopped := &StoppedSync{Deletes: preview.Deletes, Destination: destination, Paths: listed}
	switch {
	case source <= 0:
		log(appLine("ERROR", "the source holds nothing to sync: stopping before it deletes the destination (a forced run allows it)"))
		stopped.Err = ErrEmptySource
		return 0, stopped
	case preview.Deletes >= guardDeletes && preview.Deletes*2 > destination:
		log(appLine("ERROR", "most of the destination would go: stopping for a person to confirm it (a forced run allows it)"))
		stopped.Err = ErrLargeDelete
		return 0, stopped
	}
	return preview.Deletes, nil
}

// previewSync is the job's sync run without changing anything, counted apart
// from the run's statistics, with some of the files it would delete. Its
// rclone lines are dropped, so that the run's log tells what happened and
// not what could have happened.
func previewSync(ctx context.Context, src, dst fs.Fs, group string) (Progress, []string, error) {
	defer muteRclone()()
	pctx, ci := fs.AddConfig(ctx)
	ci.DryRun = true
	ci.LogLevel = fs.LogLevelError
	name := group + "/preview"
	pctx = accounting.WithStatsGroup(pctx, name)
	defer deleteStats(name)
	var deletes deleteList
	pctx = operations.WithLogger(pctx, deletes.logger)
	if err := sync.Sync(pctx, dst, src, false); err != nil {
		return Progress{}, nil, err
	}
	return snapshot(accounting.StatsGroup(pctx, name)), deletes.paths, nil
}

func describe(loc Location) string {
	switch {
	case loc.SMB != nil:
		r := loc.SMB
		port := r.Port
		if port == 0 {
			port = 445
		}
		return fmt.Sprintf("smb://%s@%s:%d/%s", r.Username, r.Host, port, loc.Path)
	case loc.S3 != nil:
		r := loc.S3
		endpoint := safeEndpoint(r.Endpoint)
		if endpoint == "" {
			endpoint = "the provider's default"
		}
		return fmt.Sprintf("s3 %s bucket %q at %s, region %q, path %q", r.Provider, r.Bucket, endpoint, r.Region, loc.Path)
	}
	return "local " + loc.Path
}

// shutdown closes what a backend keeps open, such as SMB's pool of
// connections, rather than leave it to an idle timer.
func shutdown(f fs.Fs) {
	if s, ok := f.(fs.Shutdowner); ok {
		if err := s.Shutdown(context.Background()); err != nil {
			fs.Debugf(f, "shutdown: %v", err)
		}
	}
}

// newDirFs is newFs for a location known to be a folder: the backend skips
// looking for a file of its name, which S3 may mistake one for.
func newDirFs(ctx context.Context, loc Location) (fs.Fs, error) {
	return makeFs(ctx, loc, true)
}

func newFs(ctx context.Context, loc Location) (fs.Fs, error) {
	return makeFs(ctx, loc, false)
}

func makeFs(ctx context.Context, loc Location, dir bool) (fs.Fs, error) {
	if err := validateEndpoint(loc); err != nil {
		return nil, err
	}
	var (
		backend string
		root    string
		cfg     = configmap.Simple{}
	)
	switch {
	case loc.SMB != nil:
		r := loc.SMB
		backend, root = "smb", loc.Path
		cfg["host"] = r.Host
		cfg["user"] = r.Username
		cfg["use_kerberos"] = "false"
		if r.Port != 0 {
			cfg["port"] = strconv.Itoa(r.Port)
		}
		if r.Password != "" {
			// The backend takes the password obscured, as rclone.conf
			// keeps it; this one stays in memory.
			cfg["pass"] = obscure.MustObscure(r.Password)
		}
	case loc.S3 == nil:
		backend, root = "local", loc.Path
	default:
		r := loc.S3
		backend, root = "s3", path.Join(r.Bucket, loc.Path)
		cfg["provider"] = r.Provider
		cfg["env_auth"] = "false"
		cfg["access_key_id"] = r.AccessKeyID
		cfg["secret_access_key"] = r.SecretAccessKey
		if r.Region != "" {
			cfg["region"] = r.Region
		}
		if r.Endpoint != "" {
			cfg["endpoint"] = r.Endpoint
		}
	}
	info, err := fs.Find(backend)
	if err != nil {
		return nil, err
	}
	// The backend reads every option from the map, so it must hold the
	// defaults too. Nothing else feeds it: no config file and no
	// environment variables, unlike rclone's command line.
	m := configmap.New()
	m.AddGetter(cfg, configmap.PriorityNormal)
	m.AddGetter(defaults(info.Options), configmap.PriorityDefault)
	if dir && root != "" && !strings.HasSuffix(root, "/") {
		root += "/"
	}
	f, err := info.NewFs(ctx, backend, root, m)
	if errors.Is(err, fs.ErrorIsFile) {
		return nil, ErrIsFile
	}
	return f, err
}

// defaults gives a backend's options their default values.
type defaults fs.Options

func (d defaults) Get(key string) (string, bool) {
	opt := fs.Options(d).Get(key)
	if opt == nil {
		return "", false
	}
	return opt.String(), true
}

func snapshot(s *accounting.StatsInfo) Progress {
	p := Progress{
		Bytes:     s.GetBytes(),
		Transfers: s.GetTransfers(),
		Checks:    s.GetChecks(),
		Deletes:   s.GetDeletes(),
		Errors:    s.GetErrors(),
	}
	if out, err := s.RemoteStats(true); err == nil {
		p.TotalBytes = asInt64(out["totalBytes"])
		p.TotalTransfers = asInt64(out["totalTransfers"])
		p.Speed = asFloat64(out["speed"])
	}
	if p.TotalBytes < p.Bytes {
		p.TotalBytes = p.Bytes
	}
	if p.TotalTransfers < p.Transfers {
		p.TotalTransfers = p.Transfers
	}
	return p
}

func deleteStats(group string) {
	if call := rc.Calls.Get("core/stats-delete"); call != nil {
		_, _ = call.Fn(context.Background(), rc.Params{"group": group})
	}
}

func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func asFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}

// Never include the rejected URL in the error: it may contain credentials.
func validateEndpoint(loc Location) error {
	if loc.S3 == nil || loc.S3.Endpoint == "" {
		return nil
	}
	u, err := url.Parse(loc.S3.Endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("invalid S3 endpoint")
	}
	return nil
}

func safeEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid endpoint]"
	}
	u.User, u.RawQuery, u.Fragment, u.ForceQuery = nil, "", "", false
	return u.String()
}

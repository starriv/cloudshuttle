// Package service is CloudShuttle's core: it manages storages and tasks,
// runs tasks with the engine, by hand or on their schedules, and records
// their runs. The UI talks to it alone.
//
// Two processes of the app use it on one database: the window (OwnerApp),
// and the scheduler the system starts at the times of the schedules
// (OwnerScheduler), which runs the tasks that are due and quits. See
// schedule.go.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/starriv/cloudshuttle/internal/discovery"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine"
	"github.com/starriv/cloudshuttle/internal/icloud"
	"github.com/starriv/cloudshuttle/internal/seal"
	"github.com/starriv/cloudshuttle/internal/store"
)

// Secrets keeps the storages' secrets: S3 secret access keys and SMB
// passwords. Get returns
// domain.ErrMissingSecret for a key it does not have.
type Secrets interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
}

// Options configure a Service.
type Options struct {
	Store   *store.Store
	Secrets Secrets
	// OnChange is called, from any goroutine, whenever storages, tasks or
	// runs change: a run starts or ends.
	OnChange func()
	// OnProgress is called, from a run's goroutine, as the progress of a
	// run in progress changes; Active returns it.
	OnProgress func()
	// OnRunFinished is called, from any goroutine, as a run ends: one of
	// this process's, or, in the window, one the scheduler ran.
	OnRunFinished func(domain.Run)
	// OnNotify is called, in the window, from any goroutine, with a run
	// that failed, which the person asked to be told about: one of the
	// window's, or one the scheduler ran, as soon as the window sees it.
	OnNotify func(domain.Run)
	// OnRefused is called, from any goroutine, when a scheduled run the
	// window starts cannot start because another task is writing to its
	// destination. The run is not recorded: nothing started.
	OnRefused func(domain.Task, error)
	// Owner is the process this is: OwnerApp, the default, or
	// OwnerScheduler.
	Owner string
	// Registrar has the system start the scheduler on the schedules; nil
	// where the system has none, and the window runs them while it is
	// open.
	Registrar Registrar
	// WakeApp opens the window's app in the background, for the scheduler
	// to have a failure told while the app is not running; nil does not.
	WakeApp func()
	// Now returns the time; time.Now when nil.
	Now func() time.Time
	// Browse searches the network for SMB servers; discovery.Browse when
	// nil.
	Browse func(context.Context) ([]discovery.Server, error)
	// DataDir is where the app keeps its database and logs. No task may
	// sync into it, or into a folder that holds it.
	DataDir string
	// BackupFolder is the folder the configuration is copied into;
	// icloud.Folder when nil.
	BackupFolder func() (string, error)
}

// Active is a run in progress.
type Active struct {
	Run      domain.Run
	Progress engine.Progress
	// Queued is a run waiting for the one transfer the app runs at a
	// time to finish.
	Queued bool
}

// Service is safe for use from any goroutine.
type Service struct {
	store      *store.Store
	secrets    Secrets
	onChange   func()
	onProgress func()
	onDone     func(domain.Run)
	onNotify   func(domain.Run)
	onRefused  func(domain.Task, error)
	owner      string
	registrar  Registrar
	wakeApp    func()
	now        func() time.Time
	settingsMu sync.RWMutex
	settings   domain.Settings
	browse     func(context.Context) ([]discovery.Server, error)
	dataDir    string
	backupDir  func() (string, error)

	// backupMu makes the copies of the configuration one at a time:
	// backupAgain is set when a change arrives while one is being written,
	// so the change after it is copied too.
	backupMu      sync.Mutex
	backupRunning bool
	backupAgain   bool
	backupKey     *seal.Key // read from the credential store once; nil before

	ctx    context.Context
	cancel context.CancelFunc
	runs   sync.WaitGroup
	bg     sync.WaitGroup // the app's own work at start and on save

	// slot is where a transfer runs: one at a time, because rclone logs
	// through one logger for the whole process, and two at once would mix
	// their lines. transferLock takes turns with the app's other process.
	slot         chan struct{}
	transferLock *fileLock
	ownLock      *fileLock // held while this process lives: see alive

	maintenance chan struct{} // pairs run admission with configuration restore
	mu          sync.Mutex
	closed      bool
	active      map[string]*activeRun // by task ID
	foreign     map[string]Active     // the other process's runs, by task ID

	// How the schedules run (window only): see Background.
	bgMu        sync.Mutex
	background  Background
	bgErr       error
	syncPending bool
	syncNow     chan struct{}
}

type activeRun struct {
	Active
	dest   domain.Location
	mode   domain.Mode
	queued bool
	cancel context.CancelFunc
	log    *runLog
	ctx    context.Context // ends as the run is stopped
	done   chan struct{}   // closed once the run is recorded
	// deletes is what a sync the delete check stopped would have deleted,
	// kept with its log.
	deletes *domain.StoppedDeletes
}

// New makes a service. The window's then Starts; the scheduler's runs
// RunDue.
func New(opts Options) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		store:       opts.Store,
		secrets:     newCachedSecrets(opts.Secrets),
		onChange:    opts.OnChange,
		onProgress:  opts.OnProgress,
		onDone:      opts.OnRunFinished,
		onNotify:    opts.OnNotify,
		onRefused:   opts.OnRefused,
		owner:       opts.Owner,
		registrar:   opts.Registrar,
		wakeApp:     opts.WakeApp,
		now:         opts.Now,
		browse:      opts.Browse,
		dataDir:     opts.DataDir,
		backupDir:   opts.BackupFolder,
		ctx:         ctx,
		cancel:      cancel,
		active:      map[string]*activeRun{},
		foreign:     map[string]Active{},
		slot:        make(chan struct{}, 1),
		maintenance: make(chan struct{}, 1),
		syncNow:     make(chan struct{}, 1),
		settings:    domain.DefaultSettings(),
	}
	if st, err := opts.Store.Settings(context.Background()); err != nil {
		slog.Warn("read settings", "err", err)
	} else {
		s.settings = st
	}
	if s.onChange == nil {
		s.onChange = func() {}
	}
	if s.onProgress == nil {
		s.onProgress = func() {}
	}
	if s.onDone == nil {
		s.onDone = func(domain.Run) {}
	}
	if s.onNotify == nil {
		s.onNotify = func(domain.Run) {}
	}
	if s.onRefused == nil {
		s.onRefused = func(domain.Task, error) {}
	}
	if s.owner == "" {
		s.owner = OwnerApp
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.browse == nil {
		s.browse = discovery.Browse
	}
	if s.backupDir == nil {
		s.backupDir = icloud.Folder
	}
	return s
}

// Start begins the window's work: it ends the runs a window that quit
// left behind, registers the schedules with the system, and watches for
// the runs of the scheduler; it prunes the history that is old enough to
// go.
func (s *Service) Start(ctx context.Context) error {
	tasks, err := s.store.Tasks(ctx)
	if err != nil {
		return err
	}
	if !s.lockOwn(ctx) {
		s.cancel()
		return errors.New("cannot acquire the application process lock")
	}
	s.cancelOrphans(ctx, true)
	s.goBackground(func() { s.readScheduledSecrets(s.ctx, tasks) })
	s.goBackground(func() { s.pruneRuns(s.ctx) })
	s.goBackground(func() { s.watch(s.ctx) })
	s.requestSync()
	s.backupIfEnabled()
	return nil
}

// goBackground runs work that belongs to the app rather than to a run, and
// lets Close wait for it: the database must not close under it.
func (s *Service) goBackground(fn func()) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.bg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.bg.Done()
		fn()
	}()
}

// pruneRuns deletes the runs and logs older than the retention setting, but
// runs in progress, and tells the window if it took any.
func (s *Service) pruneRuns(ctx context.Context) {
	settings := s.Settings()
	if settings.RetentionDays <= 0 {
		return
	}
	before := s.now().AddDate(0, 0, -settings.RetentionDays)
	n, err := s.store.PruneRuns(ctx, before)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("prune runs", "err", err)
		}
		return
	}
	if n > 0 {
		slog.Info("pruned runs", "count", n, "before", before)
		s.onChange()
	}
}

// closeWait is how long Close waits for the runs in progress to record
// their outcome: a server that stopped answering may hold one for good.
const closeWait = 30 * time.Second

// Close cancels the runs in progress and waits for them to record their
// outcome.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.runs.Wait()
		s.bg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeWait):
		slog.Error("runs still going at quit: their outcome may not be recorded")
		return // retain ownership until the process exits
	}
	s.ownLock.unlock()
}

// Settings returns the app's settings.
func (s *Service) Settings() domain.Settings {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.settings
}

// SaveSettings keeps st and uses it for the runs that start after it. The
// history is pruned again at once, as its retention may have changed, and
// the configuration is copied to iCloud Drive when the person allows it.
func (s *Service) SaveSettings(ctx context.Context, st domain.Settings) (domain.Settings, error) {
	if err := st.Validate(); err != nil {
		return st, err
	}
	if st.BackupICloud && !s.HasBackupPassword() {
		// A backup is encrypted: there is nothing to write it with yet.
		return st, domain.Problems{{Field: domain.FieldBackupPassword, Code: domain.CodeRequired}}
	}
	if st.BackupICloud && !s.Settings().BackupICloud {
		// Turning it on where iCloud Drive is off would keep a setting
		// that never copies anything: say so before it is saved.
		if _, err := s.backupDir(); err != nil {
			return st, domain.Problems{{Field: domain.FieldSettings, Code: domain.CodeICloudUnavailable}}
		}
	}
	if err := s.store.PutSettings(ctx, st); err != nil {
		return st, err
	}
	s.settingsMu.Lock()
	background := s.settings.Background != st.Background
	s.settings = st
	s.settingsMu.Unlock()
	if background {
		s.requestSync()
	}
	s.onChange()
	s.goBackground(func() { s.pruneRuns(s.ctx) })
	s.backupIfEnabled()
	return st, nil
}

// SetAppearance keeps the window's appearance, and nothing else of the
// settings: the settings page may hold changes not saved yet.
func (s *Service) SetAppearance(ctx context.Context, a domain.Appearance) error {
	st := s.Settings()
	st.Appearance = a
	_, err := s.SaveSettings(ctx, st)
	return err
}

// SetUpdates keeps whether the app checks for updates on its own and
// whether it installs them by itself, and nothing else of the settings.
func (s *Service) SetUpdates(ctx context.Context, check, install bool) error {
	st := s.Settings()
	st.CheckUpdates, st.InstallUpdates = check, install
	_, err := s.SaveSettings(ctx, st)
	return err
}

// backupIfEnabled copies the configuration into the person's iCloud Drive
// when they allow it. It returns at once; a change that arrives while a
// copy is being written queues one more copy, so the latest change reaches
// iCloud too.
func (s *Service) backupIfEnabled() {
	if !s.Settings().BackupICloud {
		return
	}
	s.backupMu.Lock()
	if s.backupRunning {
		s.backupAgain = true
		s.backupMu.Unlock()
		return
	}
	s.backupRunning = true
	s.backupMu.Unlock()
	s.goBackground(func() {
		for {
			if !s.Settings().BackupICloud {
				break
			}
			if err := s.copyConfig(s.ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("back up the configuration to iCloud", "err", err)
			}
			s.backupMu.Lock()
			again := s.backupAgain
			s.backupAgain = false
			if !again {
				s.backupRunning = false
			}
			s.backupMu.Unlock()
			if !again {
				return
			}
		}
		s.backupMu.Lock()
		s.backupRunning, s.backupAgain = false, false
		s.backupMu.Unlock()
	})
}

// Storages returns every storage, by name.
func (s *Service) Storages(ctx context.Context) ([]domain.Storage, error) {
	return s.store.Storages(ctx)
}

// CreateStorage validates in, keeps its secret and adds the storage.
func (s *Service) CreateStorage(ctx context.Context, in domain.StorageInput) (domain.Storage, error) {
	in = in.Normalize()
	if err := in.Validate(true); err != nil {
		return domain.Storage{}, err
	}
	now := s.now()
	st := storageFrom(domain.Storage{ID: domain.NewID(), CreatedAt: now}, in, now)
	// A share open to guests has no password to keep.
	if in.Secret != "" {
		if err := s.secrets.Set(st.SecretKey(), in.Secret); err != nil {
			return domain.Storage{}, fmt.Errorf("keep secret: %w", err)
		}
	}
	if err := s.store.PutStorage(ctx, st); err != nil {
		_ = s.secrets.Delete(st.SecretKey())
		return domain.Storage{}, nameProblem(err)
	}
	s.onChange()
	s.backupIfEnabled()
	return st, nil
}

// UpdateStorage validates in and changes the storage with id. An empty
// secret keeps the current one.
func (s *Service) UpdateStorage(ctx context.Context, id string, in domain.StorageInput) (domain.Storage, error) {
	in = in.Normalize()
	if err := in.Validate(false); err != nil {
		return domain.Storage{}, err
	}
	old, err := s.store.Storage(ctx, id)
	if err != nil {
		return domain.Storage{}, err
	}
	kindChanged := old.Kind != in.Kind
	if kindChanged {
		// The secret kept for the other kind means nothing to this one.
		if err := secretProblem(in, ""); err != nil {
			return domain.Storage{}, err
		}
	}
	st := storageFrom(old, in, s.now())
	if in.Secret == "" && st.SameConnection(old) {
		// Renamed only: the last test still holds.
		st.Check = old.Check
	}
	if err := s.store.PutStorage(ctx, st); err != nil {
		return domain.Storage{}, nameProblem(err)
	}
	if in.Secret != "" {
		if err := s.secrets.Set(st.SecretKey(), in.Secret); err != nil {
			// The secret the storage uses is still the old one, so the row
			// that names it goes back: a locked keychain leaves the
			// storage as it was.
			if back := s.store.PutStorage(ctx, old); back != nil {
				slog.Warn("put the storage back", "storage", id, "err", back)
			}
			return domain.Storage{}, fmt.Errorf("keep secret: %w", err)
		}
	}
	if kindChanged {
		if err := s.secrets.Delete(old.SecretKey()); err != nil {
			slog.Warn("delete secret", "storage", id, "err", err)
		}
	}
	s.onChange()
	s.backupIfEnabled()
	return st, nil
}

// storageFrom sets st to in, keeping only the fields of in's kind.
func storageFrom(st domain.Storage, in domain.StorageInput, now time.Time) domain.Storage {
	st = domain.Storage{ID: st.ID, Name: in.Name, Kind: in.Kind, CreatedAt: st.CreatedAt, UpdatedAt: now}
	switch in.Kind {
	case domain.KindSMB:
		st.Host, st.Port, st.Username = in.Host, in.PortNumber(), in.Username
	default:
		st.Provider, st.Endpoint, st.Region = in.Provider, in.Endpoint, in.Region
		st.Bucket, st.AccessKeyID = in.Bucket, in.AccessKeyID
	}
	return st
}

// DeleteStorage deletes the storage with id and its secret. It fails with
// domain.ErrStorageInUse while a task uses it.
func (s *Service) DeleteStorage(ctx context.Context, id string) error {
	st, err := s.store.Storage(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteStorage(ctx, id); err != nil {
		return err
	}
	if err := s.secrets.Delete(st.SecretKey()); err != nil {
		slog.Warn("delete secret", "storage", id, "err", err)
	}
	s.onChange()
	s.backupIfEnabled()
	return nil
}

// CheckStorage tests the connection of the storage with id as it is
// saved, and records how it went, which Storages then tells.
func (s *Service) CheckStorage(ctx context.Context, id string) (domain.Check, error) {
	st, err := s.store.Storage(ctx, id)
	if err != nil {
		return domain.Check{}, err
	}
	secret, err := s.secret(st)
	if err == nil {
		err = connectionError(engine.Check(ctx, remote(st, secret, "")))
	}
	check := checkOf(s.now(), err)
	// A test that ran out of time, as the window closed, still records.
	if err := s.store.SetStorageCheck(context.WithoutCancel(ctx), id, check); err != nil {
		return check, err
	}
	s.onChange()
	return check, nil
}

func checkOf(at time.Time, err error) domain.Check {
	c := domain.Check{At: at, OK: err == nil}
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrUnreachable):
		c.Problem = domain.ProblemUnreachable
	case errors.Is(err, domain.ErrAuthFailed):
		c.Problem = domain.ProblemAuthFailed
	case errors.Is(err, domain.ErrShareNotFound):
		c.Problem = domain.ProblemShareNotFound
	}
	if err != nil {
		c.Message = err.Error()
	}
	return c
}

// TestConnection checks that the storage in describes can be reached: for
// an SMB server, that it lets the user in to list its shares. With
// an empty secret it uses the one kept for the storage with id, when id is
// not empty.
func (s *Service) TestConnection(ctx context.Context, id string, in domain.StorageInput) error {
	in = in.Normalize()
	if err := in.Validate(id == ""); err != nil {
		return err
	}
	st := storageFrom(domain.Storage{ID: id}, in, s.now())
	secret, err := s.inputSecret(ctx, id, in)
	if err != nil {
		return err
	}
	if err := secretProblem(in, secret); err != nil {
		return err
	}
	return connectionError(engine.Check(ctx, remote(st, secret, "")))
}

// Folders lists the folders at path in the storage with id, by name, to
// choose where a task reads or writes: at the top of an SMB server, its
// shares. A folder that does not exist yet fails with
// domain.ErrFolderNotFound, and a share with domain.ErrShareNotFound.
func (s *Service) Folders(ctx context.Context, id, path string) ([]string, error) {
	st, err := s.store.Storage(ctx, id)
	if err != nil {
		return nil, err
	}
	secret, err := s.secret(st)
	if err != nil {
		return nil, err
	}
	folders, err := engine.Folders(ctx, remote(st, secret, strings.Trim(path, "/")))
	if errors.Is(err, engine.ErrFolderNotFound) {
		return nil, domain.ErrFolderNotFound
	}
	if err != nil {
		// The view says what went wrong in a word; the log keeps what the
		// server answered.
		slog.Warn("list folders", "storage", st.Name, "path", path, "err", err)
		return nil, connectionError(err)
	}
	slices.SortFunc(folders, func(a, b string) int {
		return strings.Compare(strings.ToLower(a), strings.ToLower(b))
	})
	return folders, nil
}

// FoundServer is an SMB server of the local network that announced itself.
type FoundServer struct {
	Name string // as it announces itself, such as "DiskStation"
	Host string // a host name this computer resolves, or an IPv4 address
	Port int
}

// browseFor is how long FindServers listens for answers.
const browseFor = 3 * time.Second

// FindServers asks the local network for SMB servers, which answer with
// Bonjour (mDNS), and returns those that did within a few seconds, by
// name. Servers that do not announce themselves are not found: their
// address is entered by hand.
func (s *Service) FindServers(ctx context.Context) ([]FoundServer, error) {
	ctx, cancel := context.WithTimeout(ctx, browseFor)
	defer cancel()
	found, err := s.browse(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]FoundServer, len(found))
	for i, f := range found {
		out[i] = FoundServer{Name: f.Name, Host: f.Host, Port: f.Port}
	}
	return out, nil
}

// secretProblem reports a missing secret: a connection without one is
// bound to fail, unless it is a guest's of an SMB share.
func secretProblem(in domain.StorageInput, secret string) error {
	if secret == "" && in.Secret == "" && !(in.Kind == domain.KindSMB && in.Guest()) {
		return domain.Problems{{Field: domain.FieldSecret, Code: domain.CodeRequired}}
	}
	return nil
}

// inputSecret is the secret a form's input connects with: the one it holds,
// or the one kept for the storage with id when it was left empty.
func (s *Service) inputSecret(ctx context.Context, id string, in domain.StorageInput) (string, error) {
	if in.Secret != "" || id == "" {
		return in.Secret, nil
	}
	old, err := s.store.Storage(ctx, id)
	if err != nil {
		return "", err
	}
	if old.Kind != in.Kind {
		return "", nil // the kept secret belongs to the other kind
	}
	return s.secret(old)
}

// secret reads the secret kept for st. An SMB share may have none.
func (s *Service) secret(st domain.Storage) (string, error) {
	secret, err := s.secrets.Get(st.SecretKey())
	if errors.Is(err, domain.ErrMissingSecret) && st.Kind == domain.KindSMB {
		return "", nil
	}
	return secret, err
}

// remote is how the engine reaches path in st.
func remote(st domain.Storage, secret, path string) engine.Location {
	if st.Kind == domain.KindSMB {
		return engine.Location{Path: path, SMB: &engine.SMB{
			Host: st.Host, Port: st.Port, Username: st.Username, Password: secret,
		}}
	}
	return engine.Location{Path: path, S3: &engine.S3{
		Provider: string(st.Provider), Endpoint: st.Endpoint, Region: st.Region,
		Bucket: st.Bucket, AccessKeyID: st.AccessKeyID, SecretAccessKey: secret,
	}}
}

// connectionError marks err with the domain's error for what went wrong,
// for the view to explain.
func connectionError(err error) error {
	switch {
	case errors.Is(err, engine.ErrUnreachable):
		return fmt.Errorf("%w: %w", domain.ErrUnreachable, err)
	case errors.Is(err, engine.ErrAuthFailed):
		return fmt.Errorf("%w: %w", domain.ErrAuthFailed, err)
	case errors.Is(err, engine.ErrShareNotFound):
		return fmt.Errorf("%w: %w", domain.ErrShareNotFound, err)
	}
	return err
}

// Tasks returns every task, by name.
func (s *Service) Tasks(ctx context.Context) ([]domain.Task, error) {
	return s.store.Tasks(ctx)
}

// CreateTask validates in and adds the task.
func (s *Service) CreateTask(ctx context.Context, in domain.TaskInput) (domain.Task, error) {
	in, err := s.validTask(ctx, "", in, false)
	if err != nil {
		return domain.Task{}, err
	}
	now := s.now()
	t := taskFrom(domain.Task{ID: domain.NewID(), CreatedAt: now}, in, now)
	if err := s.store.PutTask(ctx, t); err != nil {
		return domain.Task{}, nameProblem(err)
	}
	s.requestSync()
	s.onChange()
	s.backupIfEnabled()
	return t, nil
}

// UpdateTask validates in and changes the task with id.
func (s *Service) UpdateTask(ctx context.Context, id string, in domain.TaskInput) (domain.Task, error) {
	in, err := s.validTask(ctx, id, in, false)
	if err != nil {
		return domain.Task{}, err
	}
	old, err := s.store.Task(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	t := taskFrom(old, in, s.now())
	if err := s.store.PutTask(ctx, t); err != nil {
		return domain.Task{}, nameProblem(err)
	}
	s.requestSync()
	s.onChange()
	s.backupIfEnabled()
	return t, nil
}

func (s *Service) validTask(ctx context.Context, id string, in domain.TaskInput, dryRun bool) (domain.TaskInput, error) {
	in = in.Normalize()
	storages, err := s.store.Storages(ctx)
	if err != nil {
		return in, err
	}
	problems := domain.AsProblems(in.Validate(storages))
	if len(problems) > 0 {
		return in, problems
	}
	resolved, err := resolveTask(in)
	if err != nil {
		return in, err
	}
	problems = domain.AsProblems(resolved.Validate(storages))
	problems = s.protectDestination(problems, resolved)
	if !dryRun {
		problems, err = s.checkSharedDestination(ctx, problems, id, resolved, storages)
	}
	if err != nil {
		return in, err
	}
	if len(problems) > 0 {
		return in, problems
	}
	return resolved, nil
}

func taskFrom(t domain.Task, in domain.TaskInput, now time.Time) domain.Task {
	if t.Schedule != in.Schedule || t.ScheduleSince.IsZero() {
		// The times a new schedule passed before it was set are not runs
		// the task missed.
		t.ScheduleSince = now
	}
	t.Name, t.Source, t.Destination, t.Mode, t.Schedule = in.Name, in.Source, in.Destination, in.Mode, in.Schedule
	t.TrashDays = in.TrashDays
	t.UpdatedAt = now
	return t
}

// systemRoots are folders a sync must not use as a destination: what is
// below them is not the task's to match and empty.
var systemRoots = []string{
	"/System", "/Library", "/Applications", "/Users", "/Volumes", "/Network",
	"/private", "/etc", "/var", "/usr", "/bin", "/sbin", "/opt", "/tmp", "/cores", "/dev",
	`C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`, `C:\ProgramData`,
}

// protectDestination refuses a sync that would empty a folder the app, the
// person or the system needs: the home folder, a folder holding the app's
// own data, or one of the system's own.
func (s *Service) protectDestination(p domain.Problems, in domain.TaskInput) domain.Problems {
	if p.For(domain.FieldDestination) != "" || !in.Destination.Local() {
		return p
	}
	dst := filepath.Clean(in.Destination.Path)
	forbid := false
	if s.dataDir != "" {
		// The app's data is never a destination, and a sync must not hold
		// a folder that does: either would delete the database.
		data, err := realPath(s.dataDir)
		if err != nil {
			return append(p, domain.Problem{Field: domain.FieldDestination, Code: domain.CodeForbiddenDestination})
		}
		forbid = sameFolder(dst, data) || withinFolder(dst, data)
		if in.Mode == domain.ModeSync {
			forbid = forbid || withinFolder(data, dst)
		}
	}
	if in.Mode == domain.ModeSync {
		home, _ := os.UserHomeDir()
		if real, err := realPath(home); err == nil {
			home = real
		}
		forbid = forbid || sameFolder(dst, home) || withinFolder(home, dst)
		for _, root := range systemRoots {
			real, err := realPath(root)
			if sameFolder(dst, root) || (err == nil && sameFolder(dst, real)) {
				forbid = true
			}
		}
	}
	if forbid {
		return append(p, domain.Problem{Field: domain.FieldDestination, Code: domain.CodeForbiddenDestination})
	}
	return p
}

// sameFolder reports whether a and b name the same folder, as the system
// matches names.
func sameFolder(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return foldFolder(a) == foldFolder(b)
}

// withinFolder reports whether path is inside parent, or is parent.
func withinFolder(path, parent string) bool {
	if path == "" || parent == "" {
		return false
	}
	path, parent = foldFolder(path), foldFolder(parent)
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// foldFolder lowers a folder path where the system ignores case in names.
func foldFolder(path string) string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(filepath.Clean(path))
	}
	return filepath.Clean(path)
}

// checkSharedDestination refuses a destination another task also writes to,
// where a sync would delete what the other put there, or a copy of it.
func (s *Service) checkSharedDestination(ctx context.Context, p domain.Problems, id string, in domain.TaskInput, storages []domain.Storage) (domain.Problems, error) {
	if p.For(domain.FieldDestination) != "" {
		return p, nil
	}
	tasks, err := s.store.Tasks(ctx)
	if err != nil {
		return p, err
	}
	for _, t := range tasks {
		if t.ID == id || (in.Mode != domain.ModeSync && t.Mode != domain.ModeSync) {
			continue
		}
		other := t.Destination
		if other.Local() {
			real, err := realPath(other.Path)
			if err != nil {
				return p, err
			}
			other.Path = real
		}
		if domain.Overlap(in.Destination, other, storages) {
			return append(p, domain.Problem{Field: domain.FieldDestination, Code: domain.CodeSharedDestination}), nil
		}
	}
	return p, nil
}

// DeleteTask deletes the task with id. It fails with
// domain.ErrAlreadyActive while the task runs.
func (s *Service) DeleteTask(ctx context.Context, id string) error {
	s.mu.Lock()
	_, here := s.active[id]
	_, elsewhere := s.foreign[id]
	if here || elsewhere {
		s.mu.Unlock()
		return domain.ErrAlreadyActive
	}
	if err := s.store.DeleteTask(ctx, id); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	s.requestSync()
	s.onChange()
	s.backupIfEnabled()
	return nil
}

// NextRun returns when the task runs next on its schedule, or the zero time.
func (s *Service) NextRun(t domain.Task) time.Time { return t.Schedule.Next(s.now()) }

// Runs returns recorded runs, the newest first.
func (s *Service) Runs(ctx context.Context, f store.RunFilter) ([]domain.Run, error) {
	return s.store.Runs(ctx, f)
}

// DeleteRuns deletes the runs with ids from the history, with their logs,
// and returns how many it deleted: runs in progress stay.
func (s *Service) DeleteRuns(ctx context.Context, ids []string) (int, error) {
	n, err := s.store.DeleteRuns(ctx, ids)
	if n > 0 {
		s.onChange()
	}
	return n, err
}

// LastRuns returns the latest finished run of each task, by task ID.
func (s *Service) LastRuns(ctx context.Context) (map[string]domain.Run, error) {
	return s.store.LastRuns(ctx)
}

// Active returns the runs in progress, the oldest first.
func (s *Service) Active() []Active {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Active, 0, len(s.active)+len(s.foreign))
	for _, a := range s.active {
		active := a.Active
		active.Queued = a.queued
		out = append(out, active)
	}
	for id, a := range s.foreign {
		if _, here := s.active[id]; !here {
			out = append(out, a)
		}
	}
	// The one transferring comes first; the others wait their turn.
	slices.SortFunc(out, func(a, b Active) int {
		if a.Queued != b.Queued {
			if a.Queued {
				return 1
			}
			return -1
		}
		return a.Run.StartedAt.Compare(b.Run.StartedAt)
	})
	return out
}

// RunOptions say how to run a task.
type RunOptions struct {
	Trigger domain.Trigger
	// DryRun reports what the run would do without changing anything.
	DryRun bool
	// Force lets a sync delete most of its destination, or all of it when
	// the source is empty: a person confirmed it. Scheduled runs never
	// force.
	Force bool
}

// Run starts the task with id and returns its run, which goes on in the
// background. It fails with domain.ErrAlreadyActive while the task runs,
// here or in the scheduler.
func (s *Service) Run(ctx context.Context, id string, opts RunOptions) (domain.Run, error) {
	a, err := s.start(ctx, id, opts, time.Time{})
	if err != nil {
		return domain.Run{}, err
	}
	return a.Run, nil
}

// start starts a run of the task with id, for its scheduled time slot
// when it is not zero: it fails with store.ErrSlotTaken when another
// process ran the task since.
func (s *Service) start(ctx context.Context, id string, opts RunOptions, slot time.Time) (*activeRun, error) {
	unlock, err := s.lockMaintenance(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	t, err := s.store.Task(ctx, id)
	if err != nil {
		return nil, err
	}
	storages, err := s.store.Storages(ctx)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveTask(taskInput(t))
	if err != nil {
		return nil, err
	}
	t.Source, t.Destination = resolved.Source, resolved.Destination
	opts.Trigger = triggerOr(opts.Trigger)
	run := domain.Run{
		ID: domain.NewID(), TaskID: t.ID, TaskName: t.Name, Mode: t.Mode,
		Trigger: opts.Trigger, DryRun: opts.DryRun, Status: domain.StatusRunning, StartedAt: s.now(),
	}

	s.mu.Lock()
	if _, ok := s.active[id]; ok {
		s.mu.Unlock()
		return nil, domain.ErrAlreadyActive
	}
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return nil, context.Canceled
	}
	// A task saved before the checks on shared destinations existed could
	// still run beside another into one place, and the two would delete
	// what the other wrote. One of them waits.
	if !opts.DryRun {
		for _, a := range s.active {
			if a.Run.DryRun || (t.Mode != domain.ModeSync && a.mode != domain.ModeSync) {
				continue
			}
			if domain.Overlap(t.Destination, a.dest, storages) {
				s.mu.Unlock()
				return nil, domain.ErrDestinationBusy
			}
		}
	}
	// The database says whether the other process runs the task, or ran
	// it for this slot already, in the one statement that records the run.
	if err := s.store.StartRun(ctx, run, s.owner, true, slot); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	runCtx, cancel := context.WithCancel(s.ctx)
	settings := s.Settings()
	log := &runLog{}
	log.app(s.now(), "INFO", "run %s of task %q: %s, %s%s%s, in the %s", run.ID, t.Name, t.Mode, opts.Trigger,
		map[bool]string{true: ", dry run"}[opts.DryRun], map[bool]string{true: ", forced"}[opts.Force], s.owner)
	a := &activeRun{Active: Active{Run: run}, dest: t.Destination, mode: t.Mode,
		queued: true, cancel: cancel, log: log, ctx: runCtx, done: make(chan struct{})}
	s.active[id] = a
	s.runs.Add(1)
	s.mu.Unlock()

	s.onChange()
	go s.execute(runCtx, cancel, t, a, opts.Force, settings)
	return a, nil
}

// triggerOr is a trigger to record: manual when none was given.
func triggerOr(t domain.Trigger) domain.Trigger {
	if t == "" {
		return domain.TriggerManual
	}
	return t
}

// Cancel stops the run of the task with id.
func (s *Service) Cancel(id string) error {
	s.mu.Lock()
	a, here := s.active[id]
	f, elsewhere := s.foreign[id]
	s.mu.Unlock()
	switch {
	case here:
		a.cancel()
		return nil
	case elsewhere:
		// The scheduler reads the request with its next progress.
		if err := s.store.RequestCancel(context.Background(), f.Run.ID); errors.Is(err, domain.ErrNotFound) {
			return domain.ErrNotActive
		} else if err != nil {
			return err
		}
		return nil
	}
	return domain.ErrNotActive
}

// beforeRun is called by a run that holds the transfer slot, before it
// starts: a test holds a run there to watch another wait for its turn.
var beforeRun = func() {}

func (s *Service) execute(ctx context.Context, cancel context.CancelFunc, t domain.Task, a *activeRun, force bool, settings domain.Settings) {
	defer s.runs.Done()
	defer cancel()
	run, log := a.Run, a.log
	stopPublishing := s.publish(a)

	finished := false
	defer func() {
		if r := recover(); r != nil {
			slog.Error("run panicked", "run", run.ID, "task", t.Name, "panic", r, "stack", string(debug.Stack()))
			if finished {
				return
			}
			run.FinishedAt = s.now()
			run.Status, run.Message = domain.StatusFailed, fmt.Sprintf("the run stopped unexpectedly: %v", r)
			log.app(s.now(), "ERROR", "crashed: %v", r)
			finished = true
			stopPublishing()
			s.recordRun(ctx, a, run, settings)
		}
	}()

	var (
		progress engine.Progress
		err      error
	)
	if slotErr := s.takeSlot(ctx); slotErr != nil {
		err = slotErr
		log.app(s.now(), "WARN", "stopped before it started: %v", err)
	} else {
		defer s.releaseSlot()
		s.setQueued(t.ID, false)
		beforeRun()
		progress, err = s.transfer(ctx, t, run, force, settings, trashFolder(t, run, settings), log, func(p engine.Progress) {
			s.mu.Lock()
			a.Progress = p
			s.mu.Unlock()
			s.onProgress()
		})
	}

	run.FinishedAt = s.now()
	run.Stats = domain.Stats(progress)
	if progress.Trashed > 0 {
		run.Trash = trashFolder(t, run, settings)
	}
	if err != nil {
		run.Message = err.Error()
	}
	if stopped := (*engine.StoppedSync)(nil); errors.As(err, &stopped) {
		a.deletes = &domain.StoppedDeletes{Total: int(stopped.Deletes), Paths: stopped.Paths}
	}
	switch {
	case errors.Is(err, context.Canceled):
		run.Status = domain.StatusCanceled
	case errors.Is(err, engine.ErrEmptySource):
		run.Status, run.Problem = domain.StatusFailed, domain.RunProblemEmptySource
	case errors.Is(err, engine.ErrLargeDelete):
		run.Status, run.Problem = domain.StatusFailed, domain.RunProblemLargeDelete
	case err != nil:
		run.Status = domain.StatusFailed
	default:
		run.Status = domain.StatusSuccess
	}
	finished = true
	stopPublishing()
	s.recordRun(ctx, a, run, settings)
}

// recordRun writes what a run did and takes it out of the runs in progress.
// A failure the person wants to hear of is told by the window: at once for
// its own runs, and for the scheduler's as soon as it sees them.
func (s *Service) recordRun(ctx context.Context, a *activeRun, run domain.Run, settings domain.Settings) {
	log, taskID := a.log, run.TaskID
	log.app(s.now(), "INFO", "finished: %s", run.Status)
	tell := run.Status == domain.StatusFailed && settings.NotifyFailed
	// The run's own context may be canceled; recording it must not be. The
	// log is kept first, so that it is there once the run no longer is in
	// progress. The database may be busy with the window's reads, so both
	// are tried a few times: a run whose outcome is lost would show as
	// running for good.
	record := context.WithoutCancel(ctx)
	text := log.String()
	if err := retryRecord(func() error { return s.store.PutRunLog(record, run.ID, text) }); err != nil {
		slog.Error("record run log", "run", run.ID, "err", err)
	}
	if a.deletes != nil {
		if err := retryRecord(func() error { return s.store.PutRunDeletes(record, run.ID, *a.deletes) }); err != nil {
			slog.Error("record what a stopped sync would delete", "run", run.ID, "err", err)
		}
	}
	if tell && s.owner != OwnerApp {
		if err := retryRecord(func() error { return s.store.MarkNotify(record, run.ID) }); err != nil {
			slog.Error("mark a failure to tell", "run", run.ID, "err", err)
		}
	}
	if err := retryRecord(func() error { return s.store.FinishRun(record, run) }); err != nil {
		slog.Error("record run", "run", run.ID, "err", err)
	}
	if run.Status == domain.StatusFailed {
		slog.Warn("run failed", "task", run.TaskName, "run", run.ID, "trigger", run.Trigger, "err", run.Message)
	}
	s.mu.Lock()
	delete(s.active, taskID)
	s.mu.Unlock()
	close(a.done)
	s.onChange()
	s.onDone(run)
	switch {
	case !tell:
	case s.owner == OwnerApp:
		s.onNotify(run)
	case s.wakeApp != nil && !s.alive(OwnerApp):
		// No window to tell it: open the app, in the background, which
		// tells it as it starts.
		s.wakeApp()
	}
}

// takeSlot waits for the one transfer the app runs at a time, so that
// rclone's lines belong to the run that wrote them. It returns an error when
// the file lock fails or ctx ends first, as for a run still queued at quit.
func (s *Service) takeSlot(ctx context.Context) error {
	select {
	case s.slot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	// The other process of the app may be transferring: it waits for that
	// too, and the destination is never written by both.
	path := s.lockPath("transfer")
	if path == "" {
		return nil
	}
	for {
		l, ok, err := tryLock(path)
		if err != nil {
			<-s.slot
			return fmt.Errorf("acquire transfer lock: %w", err)
		}
		if ok {
			s.transferLock = l
			return nil
		}
		select {
		case <-time.After(lockPoll):
		case <-ctx.Done():
			<-s.slot
			return ctx.Err()
		}
	}
}

// releaseSlot lets the next run go.
func (s *Service) releaseSlot() {
	s.transferLock.unlock()
	s.transferLock = nil
	<-s.slot
}

// setQueued records whether a run waits for the slot.
func (s *Service) setQueued(id string, queued bool) {
	s.mu.Lock()
	if a, ok := s.active[id]; ok {
		a.queued = queued
	}
	s.mu.Unlock()
	s.onProgress()
}

// retryRecord tries a write of a finished run's outcome a few times before
// giving up on it.
func retryRecord(write func() error) error {
	var err error
	for i, delay := range []time.Duration{0, 200 * time.Millisecond, time.Second} {
		if i > 0 {
			time.Sleep(delay)
		}
		if err = write(); err == nil || errors.Is(err, domain.ErrNotFound) {
			return err
		}
	}
	return err
}

// trashFolder is the folder of the run's destination that it moves what it
// deletes or replaces into: "" for a dry run, or when the trash is off for
// the task.
func trashFolder(t domain.Task, run domain.Run, settings domain.Settings) string {
	if run.DryRun || t.Trash(settings) <= 0 {
		return ""
	}
	return engine.TrashFolder(run.StartedAt)
}

func (s *Service) transfer(ctx context.Context, t domain.Task, run domain.Run, force bool, settings domain.Settings, trash string, log *runLog, progress func(engine.Progress)) (engine.Progress, error) {
	in, err := s.validTask(ctx, t.ID, taskInput(t), run.DryRun)
	if err != nil {
		return engine.Progress{}, err
	}
	src, err := s.location(ctx, in.Source)
	if err != nil {
		log.app(s.now(), "ERROR", "the source: %v", err)
		return engine.Progress{}, fmt.Errorf("source: %w", err)
	}
	dst, err := s.location(ctx, in.Destination)
	if err != nil {
		log.app(s.now(), "ERROR", "the destination: %v", err)
		return engine.Progress{}, fmt.Errorf("destination: %w", err)
	}
	return engine.Run(ctx, engine.Job{
		ID: run.ID, Source: src, Destination: dst, Sync: t.Mode == domain.ModeSync, DryRun: run.DryRun,
		Force: force, Transfers: settings.Transfers, Checkers: settings.Checkers,
		Retries: settings.Retries, BandwidthMBps: settings.BandwidthMBps, Log: log.add,
		Trash: trash, TrashDays: t.Trash(settings),
	}, progress)
}

func (s *Service) location(ctx context.Context, l domain.Location) (engine.Location, error) {
	if l.Local() {
		return engine.Location{Path: l.Path}, nil
	}
	st, err := s.store.Storage(ctx, l.StorageID)
	if err != nil {
		return engine.Location{}, err
	}
	secret, err := s.secret(st)
	if err != nil {
		return engine.Location{}, fmt.Errorf("%s: %w", st.Name, err)
	}
	return remote(st, secret, l.Path), nil
}

// nameProblem reports a name already used as a problem of the name field,
// for the form to show beside it.
func nameProblem(err error) error {
	if errors.Is(err, domain.ErrNameTaken) {
		return domain.Problems{{Field: domain.FieldName, Code: domain.CodeTaken}}
	}
	return err
}

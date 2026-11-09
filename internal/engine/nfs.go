package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/pacer"

	"github.com/starriv/cloudshuttle/internal/engine/nfs3"
)

// NFS is the connection to an export of an NFS server, in version 3. The
// server trusts the user and group the client names; there is no password.
type NFS struct {
	Host   string // a host name or an IP address; IPv6 in brackets
	Port   int    // where it serves both MOUNT and NFS; 0 asks the portmapper
	Export string // the exported folder, as /volume1/backup
	UID    uint32
	GID    uint32
}

func (n NFS) config() nfs3.Config {
	return nfs3.Config{Host: n.Host, Port: n.Port, UID: n.UID, GID: n.GID}
}

// NFSExports lists the folders the server exports, to choose one. A server
// that keeps the list to itself fails with ErrExportsHidden; its exports
// are then typed in.
func NFSExports(ctx context.Context, server NFS) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cfg := server.config()
	cfg.Timeout = 10 * time.Second
	exports, err := nfs3.Exports(ctx, cfg)
	var rpcErr *nfs3.RPCError
	if errors.As(err, &rpcErr) && !rpcErr.Denied && rpcErr.Prog == 100005 && rpcErr.Accept != acceptProgramUnavailable {
		// MOUNT is registered and answers, but will not run EXPORT: servers
		// keep the list to themselves in their own ways (PROC_UNAVAIL, or a
		// version mismatch, as go-nfs answers). A program that is not
		// registered at all is not this: version 3 needs MOUNT, and
		// classify says so.
		return nil, fmt.Errorf("%w: %w", ErrExportsHidden, err)
	}
	if err != nil {
		return nil, classify(err)
	}
	return exports, nil
}

// ErrExportsHidden is a server that does not list its exports.
var ErrExportsHidden = errors.New("the server does not list its exports")

// acceptProgramUnavailable is the accept_stat of an RPC call to a program the
// server does not have at all. The others say it has the program, but not
// the version or the procedure asked for.
const acceptProgramUnavailable = 1

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "nfs",
		Description: "NFS version 3 export (CloudShuttle)",
		NewFs:       newNFSFs,
		Options: []fs.Option{
			{Name: "host", Help: "NFS server.", Required: true},
			{Name: "port", Help: "Port of MOUNT and NFS; 0 asks the portmapper.", Default: 0},
			{Name: "export", Help: "Exported folder.", Required: true},
			{Name: "uid", Help: "User id to act as.", Default: 0},
			{Name: "gid", Help: "Group id to act as.", Default: 0},
		},
	})
}

type nfsOptions struct {
	Host   string `config:"host"`
	Port   int    `config:"port"`
	Export string `config:"export"`
	UID    uint32 `config:"uid"`
	GID    uint32 `config:"gid"`
}

// nfsFs is a folder of an NFS export, as rclone sees a remote.
type nfsFs struct {
	name     string
	root     string // the folder within the export, without slashes around it
	opt      nfsOptions
	m        *nfs3.Mount
	features *fs.Features
	pacer    *fs.Pacer
	timeout  time.Duration

	dirsMu sync.Mutex
	dirs   map[string]nfs3.FH // folders found, by path within the export
}

// nfsObject is a file of an nfsFs.
type nfsObject struct {
	fs      *nfsFs
	remote  string
	fh      nfs3.FH
	size    int64
	modTime time.Time
}

func newNFSFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	var opt nfsOptions
	if err := configstruct.Set(m, &opt); err != nil {
		return nil, err
	}
	opt.Export = "/" + strings.Trim(opt.Export, "/")
	ci := fs.GetConfig(ctx)
	cfg := nfs3.Config{Host: opt.Host, Port: opt.Port, UID: opt.UID, GID: opt.GID,
		Timeout: time.Duration(ci.ConnectTimeout)}
	mount, err := nfs3.Dial(ctx, cfg, opt.Export)
	if err != nil {
		return nil, err
	}
	// rclone compares Name and Root to decide whether it can skip a transfer.
	identity := sha256.Sum256(fmt.Appendf(nil, "%q %d %q", strings.TrimSuffix(strings.ToLower(opt.Host), "."), opt.Port, path.Clean(opt.Export)))
	f := &nfsFs{
		name: fmt.Sprintf("%s-%x", name, identity[:16]), root: strings.Trim(path.Clean("/"+root), "/"), opt: opt, m: mount,
		pacer:   fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(10*time.Millisecond), pacer.MaxSleep(2*time.Second))),
		timeout: time.Duration(ci.Timeout),
		dirs:    map[string]nfs3.FH{"": mount.Root},
	}
	if f.timeout <= 0 {
		f.timeout = 5 * time.Minute
	}
	f.features = (&fs.Features{CanHaveEmptyDirectories: true, IsLocal: false}).Fill(ctx, f)
	if f.root != "" {
		// A root that names a file makes the Fs its folder, as rclone
		// expects of every backend.
		_, attr, err := f.lookup(ctx, f.root)
		if err == nil && attr.Type != nfs3.TypeDir {
			f.root = parentOf(f.root)
			return f, fs.ErrorIsFile
		}
	}
	return f, nil
}

func parentOf(p string) string {
	if dir := path.Dir(p); dir != "." {
		return dir
	}
	return ""
}

func (f *nfsFs) Name() string { return f.name }
func (f *nfsFs) Root() string { return f.root }
func (f *nfsFs) String() string {
	return fmt.Sprintf("nfs://%s%s", f.opt.Host, path.Join(f.opt.Export, f.root))
}
func (f *nfsFs) Features() *fs.Features { return f.features }
func (f *nfsFs) Hashes() hash.Set       { return hash.Set(hash.None) }

// Precision is the server's: what it says of the times it keeps, and a
// second when it says nothing.
func (f *nfsFs) Precision() time.Duration {
	if d := f.m.Info.TimeDelta; d > 0 {
		return d
	}
	return time.Second
}

// Shutdown closes the connection to the server.
func (f *nfsFs) Shutdown(context.Context) error {
	f.m.Close()
	return nil
}

// full is remote's path within the export.
func (f *nfsFs) full(remote string) string {
	return strings.Trim(path.Join(f.root, remote), "/")
}

// call runs fn, an NFS call, with the pacer: a connection that broke is
// dialed again and the call tried again, as is one the server asks to
// wait for. A call that hangs past the timeout drops the connection.
func (f *nfsFs) call(ctx context.Context, fn func(context.Context) error) error {
	_, err := f.callAgain(ctx, fn)
	return err
}

// callAgain is call, and says whether an earlier attempt failed with a
// transport error. NFS version 3 has no transactions: a procedure whose
// reply was lost may have run, so the retry can answer as if it had already
// happened (a REMOVE says the file is gone). The caller then has another
// way to tell success from failure than the retry's error.
func (f *nfsFs) callAgain(ctx context.Context, fn func(context.Context) error) (again bool, err error) {
	attempts := 0
	err = f.pacer.Call(func() (bool, error) {
		attempts++
		cctx, cancel := context.WithTimeout(ctx, f.timeout)
		err := fn(cctx)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			f.m.Reset() // the next call dials again
			return true, err
		}
		return nfsRetry(ctx, err), err
	})
	return attempts > 1, err
}

func nfsRetry(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var netErr net.Error
	return errors.Is(err, nfs3.ErrClosed) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) || errors.As(err, &netErr) ||
		nfs3.IsStatus(err, nfs3.ErrJukebox)
}

// forget drops the folders at p and below from the cache, after they
// moved or went.
func (f *nfsFs) forget(p string) {
	f.dirsMu.Lock()
	defer f.dirsMu.Unlock()
	for k := range f.dirs {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(f.dirs, k)
		}
	}
}

func (f *nfsFs) cached(p string) (nfs3.FH, bool) {
	f.dirsMu.Lock()
	defer f.dirsMu.Unlock()
	fh, ok := f.dirs[p]
	return fh, ok
}

func (f *nfsFs) remember(p string, fh nfs3.FH) {
	f.dirsMu.Lock()
	defer f.dirsMu.Unlock()
	if len(f.dirs) > 10000 {
		f.dirs = map[string]nfs3.FH{"": f.m.Root}
	}
	f.dirs[p] = fh
}

// dir finds the folder at p, a path within the export.
func (f *nfsFs) dir(ctx context.Context, p string) (nfs3.FH, error) {
	if fh, ok := f.cached(p); ok {
		return fh, nil
	}
	fh, attr, err := f.lookup(ctx, p)
	if err != nil {
		return nil, err
	}
	if attr.Type != nfs3.TypeDir {
		return nil, &nfs3.StatusError{Proc: "lookup", Status: nfs3.ErrNotDir}
	}
	return fh, nil
}

// lookup finds p, a path within the export, a step at a time from the
// nearest folder it knows.
func (f *nfsFs) lookup(ctx context.Context, p string) (nfs3.FH, nfs3.Attr, error) {
	if p == "" {
		var attr nfs3.Attr
		err := f.call(ctx, func(ctx context.Context) (err error) {
			attr, err = f.m.Getattr(ctx, f.m.Root)
			return err
		})
		return f.m.Root, attr, err
	}
	parent, err := f.dir(ctx, parentOf(p))
	if err != nil {
		return nil, nfs3.Attr{}, err
	}
	var (
		fh   nfs3.FH
		attr nfs3.Attr
	)
	err = f.call(ctx, func(ctx context.Context) (err error) {
		fh, attr, err = f.m.Lookup(ctx, parent, path.Base(p))
		return err
	})
	if nfs3.IsStatus(err, nfs3.ErrStale) || nfs3.IsStatus(err, nfs3.ErrBadHandle) {
		// A folder of the cache went: look it up again, from the top.
		f.forget(parentOf(p))
		if parent, err = f.dirUncached(ctx, parentOf(p)); err != nil {
			return nil, nfs3.Attr{}, err
		}
		err = f.call(ctx, func(ctx context.Context) (err error) {
			fh, attr, err = f.m.Lookup(ctx, parent, path.Base(p))
			return err
		})
	}
	if err != nil {
		return nil, nfs3.Attr{}, err
	}
	if attr.Type == nfs3.TypeDir {
		f.remember(p, fh)
	}
	return fh, attr, nil
}

func (f *nfsFs) dirUncached(ctx context.Context, p string) (nfs3.FH, error) {
	if p == "" {
		return f.m.Root, nil
	}
	return f.dir(ctx, p)
}

// mkdirs makes the folder at p and those above it, as needed.
func (f *nfsFs) mkdirs(ctx context.Context, p string) (nfs3.FH, error) {
	fh, err := f.dir(ctx, p)
	if err == nil || !notFound(err) || p == "" {
		return fh, err
	}
	parent, err := f.mkdirs(ctx, parentOf(p))
	if err != nil {
		return nil, err
	}
	err = f.call(ctx, func(ctx context.Context) (err error) {
		fh, err = f.m.Mkdir(ctx, parent, path.Base(p), nfs3.SetAttr{Mode: new(uint32(0o755))})
		return err
	})
	if nfs3.IsStatus(err, nfs3.ErrExist) {
		return f.dir(ctx, p) // made by another transfer meanwhile
	}
	if err != nil {
		return nil, err
	}
	f.remember(p, fh)
	return fh, nil
}

func notFound(err error) bool {
	return nfs3.IsStatus(err, nfs3.ErrNoEnt) || nfs3.IsStatus(err, nfs3.ErrNotDir)
}

// List lists the folder dir.
func (f *nfsFs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	p := f.full(dir)
	entries, err := f.readDir(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make(fs.DirEntries, 0, len(entries))
	for _, e := range entries {
		remote := path.Join(dir, e.Name)
		switch e.Attr.Type {
		case nfs3.TypeDir:
			f.remember(path.Join(p, e.Name), e.FH)
			out = append(out, fs.NewDir(remote, e.Attr.Mtime))
		case nfs3.TypeReg:
			out = append(out, &nfsObject{fs: f, remote: remote, fh: e.FH, size: int64(e.Attr.Size), modTime: e.Attr.Mtime})
		default:
			// Links and devices are not copied, as rclone's local backend leaves out links.
			fs.Debugf(f, "skipping %q, which is not a file or a folder", remote)
		}
	}
	return out, nil
}

func (f *nfsFs) readDir(ctx context.Context, p string) ([]nfs3.Entry, error) {
	fh, err := f.dir(ctx, p)
	if notFound(err) {
		return nil, fs.ErrorDirNotFound
	}
	if err != nil {
		return nil, err
	}
	var entries []nfs3.Entry
	err = f.call(ctx, func(ctx context.Context) (err error) {
		entries, err = f.m.ReadDir(ctx, fh)
		return err
	})
	if notFound(err) {
		return nil, fs.ErrorDirNotFound
	}
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// NewObject finds the file remote.
func (f *nfsFs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	fh, attr, err := f.lookup(ctx, f.full(remote))
	if notFound(err) {
		return nil, fs.ErrorObjectNotFound
	}
	if err != nil {
		return nil, err
	}
	if attr.Type == nfs3.TypeDir {
		return nil, fs.ErrorIsDir
	}
	if attr.Type != nfs3.TypeReg {
		return nil, fs.ErrorObjectNotFound
	}
	return &nfsObject{fs: f, remote: remote, fh: fh, size: int64(attr.Size), modTime: attr.Mtime}, nil
}

// Put writes a new file: into a hidden sibling first, renamed over the
// name once whole, so a transfer cut short never leaves part of a file
// where a file was.
func (f *nfsFs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	return f.upload(ctx, in, src.Remote(), src.ModTime(ctx), true)
}

// PutStream is Put of a file of unknown size.
func (f *nfsFs) PutStream(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	return f.Put(ctx, in, src, options...)
}

// putNew writes a file only if the name is free: it fails with
// errExists, and never replaces a file another client put there.
func (f *nfsFs) putNew(ctx context.Context, in io.Reader, remote string, modTime time.Time) (fs.Object, error) {
	return f.upload(ctx, in, remote, modTime, false)
}

var errNFSExists = errors.New("file exists")

// The names a write passes through: a transfer cut short leaves one of
// these, never part of a file where a file was. Runs leave them out on both
// sides (withFilter), so a leftover never becomes part of a backup.
const (
	partialPrefix = ".cloudshuttle-partial-"
	uploadPrefix  = ".cloudshuttle-upload-"
)

func (f *nfsFs) createNew(ctx context.Context, dir nfs3.FH, name string) (fh nfs3.FH, err error) {
	var verifier [8]byte
	_, _ = rand.Read(verifier[:])
	err = f.call(ctx, func(ctx context.Context) (err error) {
		fh, err = f.m.CreateExclusive(ctx, dir, name, verifier)
		return err
	})
	if !nfs3.IsStatus(err, nfs3.ErrNotSupp) {
		return fh, err
	}

	// Some user-space servers support only GUARDED. Tag the empty file at
	// creation, so a lost reply can be reconciled without claiming a file
	// another client created. Stay within the range of 32-bit Unix clocks.
	const firstSecond = 946684800 // 2000-01-01
	const spanSeconds = 1 << 30
	value := binary.BigEndian.Uint64(verifier[:]) % (spanSeconds * 1e9)
	marker := time.Unix(firstSecond+int64(value/1e9), int64(value%1e9)).Truncate(f.Precision())
	attr := nfs3.SetAttr{Mode: new(uint32(0o644)), Mtime: marker, SetTime: true}
	uncertain := false
	err = f.call(ctx, func(ctx context.Context) (err error) {
		fh, err = f.m.Create(ctx, dir, name, true, attr)
		uncertain = uncertain || nfsRetry(context.Background(), err)
		return err
	})
	if err == nil || !uncertain || ctx.Err() != nil {
		return fh, err
	}
	var found nfs3.FH
	var got nfs3.Attr
	if lookupErr := f.call(ctx, func(ctx context.Context) (err error) {
		found, got, err = f.m.Lookup(ctx, dir, name)
		return err
	}); lookupErr == nil && got.Type == nfs3.TypeReg && got.Size == 0 && got.Mtime.Equal(marker) {
		return found, nil
	}
	return nil, err
}

func (f *nfsFs) upload(ctx context.Context, in io.Reader, remote string, modTime time.Time, replace bool) (*nfsObject, error) {
	p := f.full(remote)
	if p == "" {
		return nil, fs.ErrorIsDir
	}
	dir, err := f.mkdirs(ctx, parentOf(p))
	if err != nil {
		return nil, err
	}
	name := path.Base(p)
	if replace {
		var b [6]byte
		_, _ = rand.Read(b[:])
		name = partialPrefix + hex.EncodeToString(b[:])
	}
	var fh nfs3.FH
	if replace {
		err = f.call(ctx, func(ctx context.Context) (err error) {
			fh, err = f.m.Create(ctx, dir, name, false, nfs3.SetAttr{Mode: new(uint32(0o644))})
			return err
		})
	} else {
		fh, err = f.createNew(ctx, dir, name)
	}
	if !replace && nfs3.IsStatus(err, nfs3.ErrExist) {
		return nil, errNFSExists
	}
	if err != nil {
		return nil, err
	}
	if !replace {
		if modTime.IsZero() {
			modTime = time.Now()
		}
		err = f.call(ctx, func(ctx context.Context) error {
			return f.m.Setattr(ctx, fh, nfs3.SetAttr{Mode: new(uint32(0o644))})
		})
	}
	var written int64
	if err == nil {
		written, err = f.write(ctx, fh, in)
	}
	if err == nil && !modTime.IsZero() {
		err = f.call(ctx, func(ctx context.Context) error {
			return f.m.Setattr(ctx, fh, nfs3.SetAttr{Mtime: modTime, SetTime: true})
		})
	}
	if err == nil && replace {
		again, renameErr := f.callAgain(ctx, func(ctx context.Context) error {
			return f.m.Rename(ctx, dir, name, dir, path.Base(p))
		})
		if renameErr != nil && again && nfs3.IsStatus(renameErr, nfs3.ErrNoEnt) {
			// The rename may have happened, and only its reply been lost:
			// the target is there and the name the write went through is
			// gone. Then there is nothing left to undo.
			if _, _, terr := f.lookup(ctx, p); terr == nil {
				if _, _, left := f.lookup(ctx, path.Join(parentOf(p), name)); left != nil && notFound(left) {
					renameErr = nil
				}
			}
		}
		err = renameErr
	}
	if err != nil {
		// What this call made goes: the hidden sibling, or the new
		// file no one else had a name for yet.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = f.m.Remove(cleanup, dir, name)
		cancel()
		return nil, err
	}
	o := &nfsObject{fs: f, remote: remote, fh: fh, size: written, modTime: modTime}
	if attr, err := f.attr(ctx, fh); err == nil {
		o.size, o.modTime = int64(attr.Size), attr.Mtime
	}
	return o, nil
}

func (f *nfsFs) attr(ctx context.Context, fh nfs3.FH) (attr nfs3.Attr, err error) {
	err = f.call(ctx, func(ctx context.Context) (err error) {
		attr, err = f.m.Getattr(ctx, fh)
		return err
	})
	return attr, err
}

// write writes in to the file fh from its start, unstably, then commits:
// a server that restarted meanwhile may have lost what it had not kept,
// which its write verifier tells, and the write then fails.
func (f *nfsFs) write(ctx context.Context, fh nfs3.FH, in io.Reader) (int64, error) {
	size := f.m.Info.WritePref
	if size == 0 || size > f.m.Info.WriteMax && f.m.Info.WriteMax > 0 {
		size = f.m.Info.WriteMax
	}
	if size == 0 || size > 4<<20 {
		size = 1 << 20
	}
	buf := make([]byte, size)
	var (
		off     uint64
		verf    [8]byte
		hasVerf bool
	)
	for {
		n, readErr := io.ReadFull(in, buf)
		chunk := buf[:n]
		for len(chunk) > 0 {
			var (
				wrote     uint32
				committed uint32
				v         [8]byte
			)
			err := f.call(ctx, func(ctx context.Context) (err error) {
				wrote, committed, v, err = f.m.Write(ctx, fh, off, chunk, nfs3.Unstable)
				return err
			})
			if err != nil {
				return int64(off), err
			}
			if wrote == 0 {
				return int64(off), errors.New("nfs write: the server took nothing")
			}
			if committed == nfs3.Unstable {
				if hasVerf && v != verf {
					return int64(off), errors.New("nfs write: the server restarted during the write")
				}
				verf, hasVerf = v, true
			}
			off += uint64(wrote)
			chunk = chunk[wrote:]
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return int64(off), readErr
		}
	}
	if hasVerf {
		var v [8]byte
		err := f.call(ctx, func(ctx context.Context) (err error) {
			v, err = f.m.Commit(ctx, fh)
			return err
		})
		if err != nil {
			return int64(off), err
		}
		if v != verf {
			return int64(off), errors.New("nfs commit: the server restarted during the write")
		}
	}
	return int64(off), nil
}

// Mkdir makes the folder dir, and those above it.
func (f *nfsFs) Mkdir(ctx context.Context, dir string) error {
	_, err := f.mkdirs(ctx, f.full(dir))
	return err
}

// claimDestination creates the final component without accepting an existing one.
func (f *nfsFs) claimDestination(ctx context.Context, name string) error {
	if name == "." || name == "/" {
		return ErrDestinationExists
	}
	parent, err := f.mkdirs(ctx, f.root)
	if err != nil {
		return err
	}
	err = f.call(ctx, func(ctx context.Context) error {
		_, err := f.m.Mkdir(ctx, parent, name, nfs3.SetAttr{Mode: new(uint32(0o755))})
		return err
	})
	if nfs3.IsStatus(err, nfs3.ErrExist) {
		return ErrDestinationExists
	}
	return err
}

// Rmdir deletes the empty folder dir.
func (f *nfsFs) Rmdir(ctx context.Context, dir string) error {
	p := f.full(dir)
	if p == "" {
		return errors.New("nfs: the top of an export cannot be deleted")
	}
	parent, err := f.dir(ctx, parentOf(p))
	if notFound(err) {
		return fs.ErrorDirNotFound
	}
	if err != nil {
		return err
	}
	again, err := f.callAgain(ctx, func(ctx context.Context) error {
		return f.m.Rmdir(ctx, parent, path.Base(p))
	})
	switch {
	case err == nil:
		f.forget(p)
		return nil
	case again && notFound(err):
		// The first attempt removed it, and only its reply was lost.
		f.forget(p)
		return nil
	case notFound(err):
		return fs.ErrorDirNotFound
	case nfs3.IsStatus(err, nfs3.ErrNotEmpty) || nfs3.IsStatus(err, nfs3.ErrExist):
		return fs.ErrorDirectoryNotEmpty
	}
	return err
}

// sameExport reports whether o is of the same export of the same server,
// where a rename can move between them.
func (f *nfsFs) sameExport(o *nfsFs) bool {
	return strings.EqualFold(f.opt.Host, o.opt.Host) && f.opt.Port == o.opt.Port && f.opt.Export == o.opt.Export
}

// Move renames src to remote on the server, replacing what is there.
func (f *nfsFs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	o, ok := src.(*nfsObject)
	if !ok || !f.sameExport(o.fs) {
		return nil, fs.ErrorCantMove
	}
	from, to := o.fs.full(o.remote), f.full(remote)
	fromDir, err := f.dir(ctx, parentOf(from))
	if err != nil {
		return nil, err
	}
	toDir, err := f.mkdirs(ctx, parentOf(to))
	if err != nil {
		return nil, err
	}
	again, err := f.callAgain(ctx, func(ctx context.Context) error {
		return f.m.Rename(ctx, fromDir, path.Base(from), toDir, path.Base(to))
	})
	if nfs3.IsStatus(err, nfs3.ErrXDev) {
		return nil, fs.ErrorCantMove
	}
	if err != nil && again && nfs3.IsStatus(err, nfs3.ErrNoEnt) {
		// The rename may have happened, and only its reply been lost: the
		// new name is there and the old one is gone.
		if _, _, terr := f.lookup(ctx, to); terr == nil {
			if _, _, left := f.lookup(ctx, from); left != nil && notFound(left) {
				err = nil
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return f.NewObject(ctx, remote)
}

// DirMove renames the folder srcRemote of src to dstRemote.
func (f *nfsFs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
	s, ok := src.(*nfsFs)
	if !ok || !f.sameExport(s) {
		return fs.ErrorCantDirMove
	}
	from, to := s.full(srcRemote), f.full(dstRemote)
	if from == "" || to == "" {
		return fs.ErrorCantDirMove
	}
	if _, _, err := f.lookup(ctx, to); err == nil {
		return fs.ErrorDirExists
	} else if !notFound(err) {
		return err
	}
	fromDir, err := f.dir(ctx, parentOf(from))
	if err != nil {
		return err
	}
	toDir, err := f.mkdirs(ctx, parentOf(to))
	if err != nil {
		return err
	}
	again, err := f.callAgain(ctx, func(ctx context.Context) error {
		return f.m.Rename(ctx, fromDir, path.Base(from), toDir, path.Base(to))
	})
	if nfs3.IsStatus(err, nfs3.ErrXDev) {
		return fs.ErrorCantDirMove
	}
	if err != nil && again && nfs3.IsStatus(err, nfs3.ErrNoEnt) {
		// The rename may have happened, and only its reply been lost.
		if _, _, terr := f.lookup(ctx, to); terr == nil {
			if _, _, left := f.lookup(ctx, from); left != nil && notFound(left) {
				err = nil
			}
		}
	}
	f.forget(from)
	s.forget(from)
	return err
}

func (o *nfsObject) Fs() fs.Info                       { return o.fs }
func (o *nfsObject) Remote() string                    { return o.remote }
func (o *nfsObject) String() string                    { return o.remote }
func (o *nfsObject) ModTime(context.Context) time.Time { return o.modTime }
func (o *nfsObject) Size() int64                       { return o.size }
func (o *nfsObject) Storable() bool                    { return true }
func (o *nfsObject) Hash(context.Context, hash.Type) (string, error) {
	return "", hash.ErrUnsupported
}

// SetModTime sets the file's modification time.
func (o *nfsObject) SetModTime(ctx context.Context, t time.Time) error {
	err := o.fs.call(ctx, func(ctx context.Context) error {
		return o.fs.m.Setattr(ctx, o.fh, nfs3.SetAttr{Mtime: t, SetTime: true})
	})
	if err == nil {
		o.modTime = t
	}
	return err
}

// Update replaces the file's content, as Put does.
func (o *nfsObject) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	n, err := o.fs.upload(ctx, in, o.remote, src.ModTime(ctx), true)
	if err != nil {
		return err
	}
	*o = *n
	return nil
}

// Remove deletes the file.
func (o *nfsObject) Remove(ctx context.Context) error {
	p := o.fs.full(o.remote)
	dir, err := o.fs.dir(ctx, parentOf(p))
	if notFound(err) {
		return fs.ErrorObjectNotFound
	}
	if err != nil {
		return err
	}
	again, err := o.fs.callAgain(ctx, func(ctx context.Context) error {
		return o.fs.m.Remove(ctx, dir, path.Base(p))
	})
	if again && nfs3.IsStatus(err, nfs3.ErrNoEnt) {
		// The first attempt removed it, and only its reply was lost.
		return nil
	}
	if notFound(err) {
		return fs.ErrorObjectNotFound
	}
	return err
}

// Open reads the file, or the part of it the options ask for.
func (o *nfsObject) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	var off, limit int64 = 0, -1
	for _, option := range options {
		switch x := option.(type) {
		case *fs.SeekOption:
			off = x.Offset
		case *fs.RangeOption:
			off, limit = x.Decode(o.size)
		default:
			if option.Mandatory() {
				fs.Logf(o, "unsupported mandatory option: %v", option)
			}
		}
	}
	end := int64(-1)
	if limit >= 0 {
		end = off + limit
	}
	size := o.fs.m.Info.ReadPref
	if size == 0 || size > o.fs.m.Info.ReadMax && o.fs.m.Info.ReadMax > 0 {
		size = o.fs.m.Info.ReadMax
	}
	if size == 0 || size > 4<<20 {
		size = 1 << 20
	}
	return &nfsReader{ctx: ctx, o: o, off: uint64(off), end: end, chunk: size}, nil
}

type nfsReader struct {
	ctx   context.Context
	o     *nfsObject
	off   uint64
	end   int64 // -1 for the end of the file
	chunk uint32
	buf   []byte
	eof   bool
}

func (r *nfsReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		if r.eof || r.end >= 0 && int64(r.off) >= r.end {
			return 0, io.EOF
		}
		want := r.chunk
		if r.end >= 0 && int64(want) > r.end-int64(r.off) {
			want = uint32(r.end - int64(r.off))
		}
		var data []byte
		var eof bool
		err := r.o.fs.call(r.ctx, func(ctx context.Context) (err error) {
			data, eof, err = r.o.fs.m.Read(ctx, r.o.fh, r.off, want)
			return err
		})
		if err != nil {
			return 0, err
		}
		if len(data) == 0 && !eof {
			return 0, io.ErrUnexpectedEOF
		}
		r.buf, r.eof = data, eof
		r.off += uint64(len(data))
		if len(data) == 0 {
			return 0, io.EOF
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *nfsReader) Close() error { return nil }

var (
	_ fs.Fs          = (*nfsFs)(nil)
	_ fs.Mover       = (*nfsFs)(nil)
	_ fs.DirMover    = (*nfsFs)(nil)
	_ fs.PutStreamer = (*nfsFs)(nil)
	_ fs.Shutdowner  = (*nfsFs)(nil)
	_ fs.Object      = (*nfsObject)(nil)
)

// classifyNFS marks the errors of an NFS server with what went wrong, or
// returns nil for an error that is not one.
func classifyNFS(err error) error {
	var (
		mountErr  *nfs3.MountError
		rpcErr    *nfs3.RPCError
		statusErr *nfs3.StatusError
	)
	switch {
	case errors.As(err, &mountErr) && mountErr.NotFound():
		return fmt.Errorf("%w: %w", ErrShareNotFound, err)
	case errors.As(err, &mountErr) && mountErr.Denied():
		return fmt.Errorf("%w: %w", ErrAuthFailed, err)
	case errors.As(err, &rpcErr) && rpcErr.Unsupported() && rpcErr.Prog == 100005:
		// NFS version 3 needs MOUNT to find the export's root. Answering
		// for NFS but not for MOUNT on the port given is what a port that
		// carries NFS alone does, as 2049 does on most servers.
		return fmt.Errorf("%w: %w", ErrNoMount, err)
	case errors.As(err, &rpcErr) && rpcErr.Unsupported() && rpcErr.Prog != 100000:
		return fmt.Errorf("%w: %w", ErrNoNFSv3, err)
	case errors.As(err, &rpcErr) && rpcErr.Denied:
		// The credentials, or the port they came from, refused.
		return fmt.Errorf("%w: %w", ErrAuthFailed, err)
	case errors.As(err, &statusErr) && (statusErr.Status == nfs3.ErrAcces || statusErr.Status == nfs3.ErrPerm):
		return fmt.Errorf("%w: %w", ErrAuthFailed, err)
	case errors.Is(err, nfs3.ErrClosed):
		return fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	return nil
}

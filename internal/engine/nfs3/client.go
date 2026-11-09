// Package nfs3 is a small NFS version 3 client (RFC 1813) in pure Go, for
// the engine's NFS backend: the portmapper, the MOUNT protocol, and the
// NFS procedures a backup needs, over TCP. Every call takes a context, and
// a broken connection is dialed again on the next call: NFSv3 is stateless,
// so the file handles stay good.
package nfs3

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RPC programs and versions.
const (
	progPortmap = 100000
	progNFS     = 100003
	progMount   = 100005

	versPortmap = 2
	versNFS     = 3
	versMount   = 3

	protoTCP = 6

	// PortmapPort is where the portmapper listens.
	PortmapPort = 111
)

// portmapPort is PortmapPort, but where tests run a portmapper of their own.
var portmapPort = PortmapPort

// maxPath bounds a name or path in a reply (NFS3_MAXPATHLEN is server
// defined; 4096 is Linux's).
const maxPath = 4096

// FH is an NFS file handle, at most 64 bytes in version 3.
type FH []byte

// Config says how to reach a server and who the client is to it.
type Config struct {
	Host string // a host name or an IP address; IPv6 in brackets or not
	// Port is where the server serves both MOUNT and NFS, as servers in
	// user space do; 0 asks the portmapper where each is, as for the
	// NFS servers of NAS and operating systems.
	Port int
	// UID and GID are who the client acts as (AUTH_SYS): the server
	// checks permissions, and owns new files, by them.
	UID, GID uint32
	// Timeout bounds connecting; calls are bounded by their contexts.
	Timeout time.Duration
}

func (c Config) addr(port int) string {
	return net.JoinHostPort(strings.Trim(c.Host, "[]"), strconv.Itoa(port))
}

func (c Config) dialer() dialer {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	return dialer{timeout: timeout, privileged: true}
}

func (c Config) cred() cred {
	machine, err := os.Hostname()
	if err != nil || machine == "" {
		machine = "cloudshuttle"
	}
	return sysAuth(machine, c.UID, c.GID)
}

// ports finds the MOUNT and NFS ports of the server.
func (c Config) ports(ctx context.Context) (mount, nfs int, err error) {
	if c.Port != 0 {
		return c.Port, c.Port, nil
	}
	pm, err := c.dialer().dial(ctx, c.addr(portmapPort))
	if err != nil {
		return 0, 0, err
	}
	defer pm.close()
	if mount, err = getPort(ctx, pm, progMount, versMount); err != nil {
		return 0, 0, err
	}
	if nfs, err = getPort(ctx, pm, progNFS, versNFS); err != nil {
		return 0, 0, err
	}
	switch {
	case nfs == 0:
		// Registered in no version 3: the server speaks another, or none.
		// Check this before MOUNT so a version 4-only server, which has
		// no MOUNT either, is reported as having no NFS version 3 rather
		// than as having no MOUNT.
		return 0, 0, &RPCError{Prog: progNFS, Vers: versNFS, Accept: acceptProgMismatch}
	case mount == 0:
		// NFS version 3 is there but MOUNT is not: it is elsewhere, or
		// this server has none.
		return 0, 0, &RPCError{Prog: progMount, Vers: versMount, Accept: acceptProgUnavail}
	}
	return mount, nfs, nil
}

// getPort asks the portmapper where a program listens on TCP: 0 when it
// is not registered.
func getPort(ctx context.Context, pm *conn, prog, vers uint32) (int, error) {
	d, err := pm.call(ctx, progPortmap, versPortmap, 3, noAuth, func(e *encoder) {
		e.u32(prog)
		e.u32(vers)
		e.u32(protoTCP)
		e.u32(0)
	})
	if err != nil {
		return 0, err
	}
	port := d.u32()
	return int(port), d.err
}

// MountError is a MOUNT the server refused, with its mountstat3.
type MountError struct {
	Path   string
	Status uint32
}

// mountstat3 values.
const (
	mntPerm  = 1
	mntNoEnt = 2
	mntAcces = 13
)

func (e *MountError) Error() string {
	what := map[uint32]string{
		mntPerm: "not permitted", mntNoEnt: "no such export", 5: "I/O error", mntAcces: "access denied",
		20: "not a directory", 22: "invalid argument", 63: "name too long", 10004: "not supported",
		10006: "server fault",
	}[e.Status]
	if what == "" {
		what = "error " + strconv.Itoa(int(e.Status))
	}
	return fmt.Sprintf("mount %s: %s", e.Path, what)
}

// NotFound reports whether the server has no such export.
func (e *MountError) NotFound() bool { return e.Status == mntNoEnt || e.Status == 20 }

// Denied reports whether the server refused this client the export.
func (e *MountError) Denied() bool { return e.Status == mntPerm || e.Status == mntAcces }

// Exports lists the paths the server exports. Some servers keep the list
// to themselves, or allow it only to some clients: the error then says
// so, and an export is still mounted by its path.
func Exports(ctx context.Context, cfg Config) ([]string, error) {
	mountPort, _, err := cfg.ports(ctx)
	if err != nil {
		return nil, err
	}
	cn, err := cfg.dialer().dial(ctx, cfg.addr(mountPort))
	if err != nil {
		return nil, err
	}
	defer cn.close()
	d, err := cn.call(ctx, progMount, versMount, 5, noAuth, nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for d.bool() {
		out = append(out, d.string(maxPath))
		for d.bool() { // the groups allowed to mount it
			d.string(maxPath)
		}
		if len(out) > 10000 {
			return nil, errors.New("mount: export list too long")
		}
	}
	return out, d.err
}

// Mount is an export of a server, mounted: its root's handle and the
// connection its calls take, dialed again when it breaks.
type Mount struct {
	cfg  Config
	cred cred
	port int
	Path string
	Root FH
	Info FSInfo

	mu     sync.Mutex
	cn     *conn
	closed bool // Close ran: the mount is finished, and never dials again
}

// Dial mounts the export at path.
func Dial(ctx context.Context, cfg Config, path string) (*Mount, error) {
	mountPort, nfsPort, err := cfg.ports(ctx)
	if err != nil {
		return nil, err
	}
	m := &Mount{cfg: cfg, cred: cfg.cred(), port: nfsPort, Path: path}
	mc, err := cfg.dialer().dial(ctx, cfg.addr(mountPort))
	if err != nil {
		return nil, err
	}
	defer mc.close()
	d, err := mc.call(ctx, progMount, versMount, 1, m.cred, func(e *encoder) { e.string(path) })
	if err != nil {
		return nil, err
	}
	if status := d.u32(); status != 0 {
		if d.err != nil {
			return nil, d.err
		}
		return nil, &MountError{Path: path, Status: status}
	}
	m.Root = FH(d.opaque(64))
	if d.err != nil {
		return nil, d.err
	}
	// The flavors the export accepts follow; AUTH_SYS is the one this
	// client has, and the server says so if it will not take it.
	if m.Info, err = m.FSInfo(ctx); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

// Close closes the connection and ends the mount: calls made after it
// return ErrClosed, without dialing again. The server keeps no state to
// release: MOUNT's UMNT only edits the list showmount prints, so it is not
// sent.
func (m *Mount) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.cn != nil {
		m.cn.close()
		m.cn = nil
	}
}

// Reset drops the connection but leaves the mount usable: the next call
// dials again. A call that timed out leaves its reply to come, which the
// connection would hand to no one and never read, so the engine drops the
// connection rather than the mount.
func (m *Mount) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cn != nil {
		m.cn.close()
		m.cn = nil
	}
}

// conn returns the connection to send on, dialing the server when there is
// none or the last one broke. A Close that races the dial closes the new
// connection and fails the call with ErrClosed.
func (m *Mount) conn(ctx context.Context) (*conn, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if m.cn != nil && !m.cn.broken() {
		cn := m.cn
		m.mu.Unlock()
		return cn, nil
	}
	m.mu.Unlock()

	// Dial without the lock: a server slow to answer must not hold up
	// Close, and Close must still stop a dial already under way.
	cn, err := m.cfg.dialer().dial(ctx, m.cfg.addr(m.port))
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cn.close()
		return nil, ErrClosed
	}
	if cur := m.cn; cur != nil && !cur.broken() {
		m.mu.Unlock()
		cn.close() // another call dialed first
		return cur, nil
	}
	m.cn = cn
	m.mu.Unlock()
	return cn, nil
}

// call runs an NFS procedure, dialing the server when there is no
// connection or the last one broke.
func (m *Mount) call(ctx context.Context, proc uint32, args func(*encoder)) (*decoder, error) {
	cn, err := m.conn(ctx)
	if err != nil {
		return nil, err
	}
	d, err := cn.call(ctx, progNFS, versNFS, proc, m.cred, args)
	if err != nil {
		return nil, err
	}
	if status := d.u32(); status != 0 {
		if d.err != nil {
			return nil, d.err
		}
		return d, &StatusError{Proc: procNames[proc], Status: status}
	}
	return d, d.err
}

// NFS procedures.
const (
	procGetattr     = 1
	procSetattr     = 2
	procLookup      = 3
	procRead        = 6
	procWrite       = 7
	procCreate      = 8
	procMkdir       = 9
	procRemove      = 12
	procRmdir       = 13
	procRename      = 14
	procLink        = 15
	procReaddirplus = 17
	procFSInfo      = 19
	procCommit      = 21
)

var procNames = map[uint32]string{
	procGetattr: "getattr", procSetattr: "setattr", procLookup: "lookup", procRead: "read",
	procWrite: "write", procCreate: "create", procMkdir: "mkdir", procRemove: "remove",
	procRmdir: "rmdir", procRename: "rename", procLink: "link", procReaddirplus: "readdirplus",
	procFSInfo: "fsinfo", procCommit: "commit",
}

// StatusError is an NFS procedure that failed with an nfsstat3.
type StatusError struct {
	Proc   string
	Status uint32
}

// nfsstat3 values.
const (
	ErrPerm        = 1
	ErrNoEnt       = 2
	ErrIO          = 5
	ErrAcces       = 13
	ErrExist       = 17
	ErrXDev        = 18
	ErrNotDir      = 20
	ErrIsDir       = 21
	ErrInval       = 22
	ErrFBig        = 27
	ErrNoSpc       = 28
	ErrROFS        = 30
	ErrNameTooLong = 63
	ErrNotEmpty    = 66
	ErrDQuot       = 69
	ErrStale       = 70
	ErrBadHandle   = 10001
	ErrBadCookie   = 10003
	ErrNotSupp     = 10004
	ErrServerFault = 10006
	ErrJukebox     = 10008
)

var statusText = map[uint32]string{
	ErrPerm: "not owner", ErrNoEnt: "no such file or directory", ErrIO: "I/O error",
	ErrAcces: "permission denied", ErrExist: "file exists", ErrXDev: "cross-device link",
	ErrNotDir: "not a directory", ErrIsDir: "is a directory", ErrInval: "invalid argument",
	ErrFBig: "file too large", ErrNoSpc: "no space left on device", ErrROFS: "read-only file system",
	ErrNameTooLong: "name too long", ErrNotEmpty: "directory not empty", ErrDQuot: "quota exceeded",
	ErrStale: "stale file handle", ErrBadHandle: "bad file handle", ErrBadCookie: "bad cookie",
	ErrNotSupp:     "operation not supported",
	ErrServerFault: "server fault", ErrJukebox: "try again later",
}

func (e *StatusError) Error() string {
	text := statusText[e.Status]
	if text == "" {
		text = "error " + strconv.Itoa(int(e.Status))
	}
	return "nfs " + e.Proc + ": " + text
}

// Is lets errors.Is match the os errors of the same meaning.
func (e *StatusError) Is(target error) bool {
	switch target {
	case os.ErrNotExist:
		return e.Status == ErrNoEnt
	case os.ErrExist:
		return e.Status == ErrExist
	case os.ErrPermission:
		return e.Status == ErrPerm || e.Status == ErrAcces
	}
	return false
}

// IsStatus reports whether err is an NFS error with status.
func IsStatus(err error, status uint32) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == status
}

// File types (ftype3).
const (
	TypeReg = 1
	TypeDir = 2
	TypeLnk = 5
)

// Attr is what the client reads of a file's attributes (fattr3).
type Attr struct {
	Type   uint32
	Mode   uint32
	Size   uint64
	FileID uint64
	Mtime  time.Time
}

func readTime(d *decoder) time.Time {
	sec, nsec := d.u32(), d.u32()
	return time.Unix(int64(sec), int64(nsec))
}

func readAttr(d *decoder) Attr {
	var a Attr
	a.Type = d.u32()
	a.Mode = d.u32()
	d.u32() // nlink
	d.u32() // uid
	d.u32() // gid
	a.Size = d.u64()
	d.u64() // used
	d.u64() // rdev
	d.u64() // fsid
	a.FileID = d.u64()
	readTime(d) // atime
	a.Mtime = readTime(d)
	readTime(d) // ctime
	return a
}

// readPostOpAttr reads post_op_attr: attributes that may follow.
func readPostOpAttr(d *decoder) (Attr, bool) {
	if !d.bool() {
		return Attr{}, false
	}
	return readAttr(d), true
}

// skipWcc reads past wcc_data.
func skipWcc(d *decoder) {
	if d.bool() { // pre_op_attr: size, mtime, ctime
		d.u64()
		d.u64()
		d.u64()
	}
	readPostOpAttr(d)
}

// SetAttr is what SETATTR, CREATE and MKDIR set: each field only when
// asked.
type SetAttr struct {
	Mode    *uint32
	Size    *uint64
	Mtime   time.Time // zero leaves it
	SetTime bool      // set Mtime, and atime with it
}

func (s SetAttr) encode(e *encoder) {
	e.bool(s.Mode != nil)
	if s.Mode != nil {
		e.u32(*s.Mode)
	}
	e.bool(false) // uid
	e.bool(false) // gid
	e.bool(s.Size != nil)
	if s.Size != nil {
		e.u64(*s.Size)
	}
	if s.SetTime {
		for range 2 { // atime, then mtime: both the client's time
			e.u32(2)
			e.u32(uint32(s.Mtime.Unix()))
			e.u32(uint32(s.Mtime.Nanosecond()))
		}
	} else {
		e.u32(0)
		e.u32(0)
	}
}

func diropargs(e *encoder, dir FH, name string) {
	e.opaque(dir)
	e.string(name)
}

// FSInfo is what the server says of its transfers (FSINFO).
type FSInfo struct {
	ReadMax, ReadPref   uint32
	WriteMax, WritePref uint32
	DirPref             uint32
	// TimeDelta is the precision of the times the server keeps.
	TimeDelta time.Duration
}

// FSInfo asks the server how much to read and write at a time.
func (m *Mount) FSInfo(ctx context.Context) (FSInfo, error) {
	d, err := m.call(ctx, procFSInfo, func(e *encoder) { e.opaque(m.Root) })
	if err != nil {
		return FSInfo{}, err
	}
	readPostOpAttr(d)
	var i FSInfo
	i.ReadMax, i.ReadPref = d.u32(), d.u32()
	d.u32() // rtmult
	i.WriteMax, i.WritePref = d.u32(), d.u32()
	d.u32() // wtmult
	i.DirPref = d.u32()
	d.u64() // maxfilesize
	sec, nsec := d.u32(), d.u32()
	i.TimeDelta = time.Duration(sec)*time.Second + time.Duration(nsec)
	d.u32() // properties
	return i, d.err
}

// Getattr reads a file's attributes.
func (m *Mount) Getattr(ctx context.Context, fh FH) (Attr, error) {
	d, err := m.call(ctx, procGetattr, func(e *encoder) { e.opaque(fh) })
	if err != nil {
		return Attr{}, err
	}
	a := readAttr(d)
	return a, d.err
}

// Setattr changes a file's attributes.
func (m *Mount) Setattr(ctx context.Context, fh FH, s SetAttr) error {
	_, err := m.call(ctx, procSetattr, func(e *encoder) {
		e.opaque(fh)
		s.encode(e)
		e.bool(false) // no guard
	})
	return err
}

// Lookup finds name in the folder dir.
func (m *Mount) Lookup(ctx context.Context, dir FH, name string) (FH, Attr, error) {
	d, err := m.call(ctx, procLookup, func(e *encoder) { diropargs(e, dir, name) })
	if err != nil {
		return nil, Attr{}, err
	}
	fh := FH(d.opaque(64))
	attr, ok := readPostOpAttr(d)
	if d.err == nil && !ok {
		attr, err = m.Getattr(ctx, fh)
		if err != nil {
			return nil, Attr{}, err
		}
	}
	return fh, attr, d.err
}

// Read reads up to n bytes of the file at off. eof says the read reached
// the end.
func (m *Mount) Read(ctx context.Context, fh FH, off uint64, n uint32) ([]byte, bool, error) {
	d, err := m.call(ctx, procRead, func(e *encoder) {
		e.opaque(fh)
		e.u64(off)
		e.u32(n)
	})
	if err != nil {
		return nil, false, err
	}
	readPostOpAttr(d)
	d.u32() // count
	eof := d.bool()
	data := d.opaque(int(n))
	return data, eof, d.err
}

// Stable says how WRITE keeps the data (stable_how).
const (
	Unstable = 0
	FileSync = 2
)

// Write writes p at off. It returns how much the server took, how stably,
// and its write verifier, which changes when the server restarts and so
// may have lost unstable data.
func (m *Mount) Write(ctx context.Context, fh FH, off uint64, p []byte, stable uint32) (n uint32, committed uint32, verf [8]byte, err error) {
	d, err := m.call(ctx, procWrite, func(e *encoder) {
		e.opaque(fh)
		e.u64(off)
		e.u32(uint32(len(p)))
		e.u32(stable)
		e.opaque(p)
	})
	if err != nil {
		return 0, 0, verf, err
	}
	skipWcc(d)
	n, committed = d.u32(), d.u32()
	copy(verf[:], d.fixed(8))
	if d.err != nil {
		return n, committed, verf, d.err
	}
	// A server may not take more than was sent. Believe one that says it
	// did: the caller advances its buffer by n, and would slice past the
	// end of it.
	if n > uint32(len(p)) {
		return 0, 0, [8]byte{}, fmt.Errorf("nfs write: the server took %d bytes of %d", n, len(p))
	}
	// stable_how is UNSTABLE, DATA_SYNC or FILE_SYNC.
	if committed > FileSync {
		return 0, 0, [8]byte{}, fmt.Errorf("nfs write: the server answered stable_how %d, not 0 to %d", committed, FileSync)
	}
	return n, committed, verf, nil
}

// Commit asks the server to keep what was written unstably, and returns
// its write verifier.
func (m *Mount) Commit(ctx context.Context, fh FH) ([8]byte, error) {
	var verf [8]byte
	d, err := m.call(ctx, procCommit, func(e *encoder) {
		e.opaque(fh)
		e.u64(0)
		e.u32(0) // to the end
	})
	if err != nil {
		return verf, err
	}
	skipWcc(d)
	copy(verf[:], d.fixed(8))
	return verf, d.err
}

// createResult reads what CREATE and MKDIR return: the new handle, which
// the server may leave out, so it is then looked up.
func (m *Mount) createResult(ctx context.Context, d *decoder, dir FH, name string) (FH, error) {
	var fh FH
	if d.bool() {
		fh = FH(d.opaque(64))
	}
	readPostOpAttr(d)
	skipWcc(d)
	if d.err != nil {
		return nil, d.err
	}
	if fh == nil {
		var err error
		fh, _, err = m.Lookup(ctx, dir, name)
		if err != nil {
			return nil, err
		}
	}
	return fh, nil
}

// Create makes a file name in dir. Guarded fails with ErrExist when the
// name is taken. Otherwise (UNCHECKED) an existing file is taken over: it is
// not truncated unless attr sets Size, and only the attributes attr names
// are set.
func (m *Mount) Create(ctx context.Context, dir FH, name string, guarded bool, attr SetAttr) (FH, error) {
	d, err := m.call(ctx, procCreate, func(e *encoder) {
		diropargs(e, dir, name)
		if guarded {
			e.u32(1)
		} else {
			e.u32(0)
		}
		attr.encode(e)
	})
	if err != nil {
		return nil, err
	}
	return m.createResult(ctx, d, dir, name)
}

// CreateExclusive uses a verifier that must stay the same when the request
// is retried. Attributes are set separately after the create succeeds.
func (m *Mount) CreateExclusive(ctx context.Context, dir FH, name string, verifier [8]byte) (FH, error) {
	d, err := m.call(ctx, procCreate, func(e *encoder) {
		diropargs(e, dir, name)
		e.u32(2)
		e.fixed(verifier[:])
	})
	if err != nil {
		return nil, err
	}
	return m.createResult(ctx, d, dir, name)
}

// Mkdir makes a folder name in dir.
func (m *Mount) Mkdir(ctx context.Context, dir FH, name string, attr SetAttr) (FH, error) {
	d, err := m.call(ctx, procMkdir, func(e *encoder) {
		diropargs(e, dir, name)
		attr.encode(e)
	})
	if err != nil {
		return nil, err
	}
	return m.createResult(ctx, d, dir, name)
}

// Remove deletes the file name in dir.
func (m *Mount) Remove(ctx context.Context, dir FH, name string) error {
	_, err := m.call(ctx, procRemove, func(e *encoder) { diropargs(e, dir, name) })
	return err
}

// Rmdir deletes the empty folder name in dir.
func (m *Mount) Rmdir(ctx context.Context, dir FH, name string) error {
	_, err := m.call(ctx, procRmdir, func(e *encoder) { diropargs(e, dir, name) })
	return err
}

// Rename moves from in fromDir to to in toDir, replacing what is there.
func (m *Mount) Rename(ctx context.Context, fromDir FH, from string, toDir FH, to string) error {
	_, err := m.call(ctx, procRename, func(e *encoder) {
		diropargs(e, fromDir, from)
		diropargs(e, toDir, to)
	})
	return err
}

// Link gives the file fh a second name, to in toDir; it fails with
// ErrExist rather than replace what is there.
func (m *Mount) Link(ctx context.Context, fh FH, toDir FH, to string) error {
	_, err := m.call(ctx, procLink, func(e *encoder) {
		e.opaque(fh)
		diropargs(e, toDir, to)
	})
	return err
}

// Entry is a name in a folder, with its handle and attributes when the
// server gave them.
type Entry struct {
	Name   string
	FH     FH
	Attr   Attr
	HasAll bool // FH and Attr are both there
}

// Limits of a READDIRPLUS traversal. A server that keeps handing out pages
// must not make the client grow without bound or spin forever.
const (
	// nfsNameMax is NFS3_MAXNAMLEN: a name in a folder is at most this
	// many bytes.
	nfsNameMax = 255
	// maxReadDirEntries and maxReadDirBytes bound what one listing may
	// put in memory.
	maxReadDirEntries = 1 << 20
	maxReadDirBytes   = 256 << 20
	// maxEmptyPages bounds consecutive pages with no entries and eof
	// false.
	maxEmptyPages = 8
	// maxBadCookieRestarts bounds restarts of a traversal the server no
	// longer knows the cookie of.
	maxBadCookieRestarts = 2
	// maxReadDirPages is the last backstop for a listing that ends
	// neither with eof nor by making progress.
	maxReadDirPages = 1_000_000
)

// ReadDir lists the folder dir, but for "." and "..".
func (m *Mount) ReadDir(ctx context.Context, dir FH) ([]Entry, error) {
	var (
		out      []Entry
		cookie   uint64
		verf     [8]byte
		used     int // estimated bytes of out
		restarts int
		empties  int // consecutive pages with no entries and eof false
	)
	dirCount := m.Info.DirPref
	if dirCount == 0 || dirCount > 1<<20 {
		dirCount = 32 << 10
	}
	for pages := 0; ; pages++ {
		if pages > maxReadDirPages {
			return nil, errors.New("nfs readdirplus: listing does not end")
		}
		d, err := m.call(ctx, procReaddirplus, func(e *encoder) {
			e.opaque(dir)
			e.u64(cookie)
			e.fixed(verf[:])
			e.u32(dirCount)
			e.u32(dirCount * 4)
		})
		if err != nil {
			// The server lost the cookie it gave out, as when its cache
			// was dropped: start over a bounded number of times.
			if IsStatus(err, ErrBadCookie) && restarts < maxBadCookieRestarts {
				restarts++
				out, used = nil, 0
				cookie, verf, empties = 0, [8]byte{}, 0
				continue
			}
			return nil, err
		}
		readPostOpAttr(d)
		copy(verf[:], d.fixed(8))
		got := 0
		for d.bool() {
			d.u64() // fileid
			name := d.string(maxPath)
			cookie = d.u64()
			attr, hasAttr := readPostOpAttr(d)
			var fh FH
			hasFH := d.bool()
			if hasFH {
				fh = FH(d.opaque(64))
			}
			if d.err != nil {
				return nil, d.err
			}
			got++
			if name == "." || name == ".." {
				continue
			}
			if len(name) > nfsNameMax || strings.ContainsAny(name, "/\x00") {
				return nil, fmt.Errorf("nfs readdirplus: the server sent an impossible name: %q", name)
			}
			if len(out) >= maxReadDirEntries {
				return nil, fmt.Errorf("nfs readdirplus: the listing has more than %d entries", maxReadDirEntries)
			}
			if used += len(name) + 64; used > maxReadDirBytes {
				return nil, fmt.Errorf("nfs readdirplus: the listing is larger than %d bytes", maxReadDirBytes)
			}
			out = append(out, Entry{Name: name, FH: fh, Attr: attr, HasAll: hasAttr && hasFH})
		}
		eof := d.bool()
		if d.err != nil {
			return nil, d.err
		}
		if eof {
			break
		}
		if got == 0 {
			// RFC 1813 lets a server answer a page with no entries and
			// eof false: more entries may follow, asked for with the
			// same cookie. Retry, but do not spin if it never moves on.
			if empties++; empties >= maxEmptyPages {
				return nil, fmt.Errorf("nfs readdirplus: the server sent %d empty pages", empties)
			}
			continue
		}
		empties = 0
	}
	// Fill in what the server left out of the listing.
	for i := range out {
		if !out[i].HasAll {
			fh, attr, err := m.Lookup(ctx, dir, out[i].Name)
			if err != nil {
				if IsStatus(err, ErrNoEnt) {
					continue // gone since
				}
				return nil, err
			}
			out[i].FH, out[i].Attr, out[i].HasAll = fh, attr, true
		}
	}
	kept := out[:0]
	for _, e := range out {
		if e.HasAll {
			kept = append(kept, e)
		}
	}
	return kept, nil
}

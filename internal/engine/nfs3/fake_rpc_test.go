package nfs3

import (
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rawCall is one RPC call as the fake server saw it.
type rawCall struct {
	conn             int64 // 1 for the first connection accepted, and so on
	xid              uint32
	prog, vers, proc uint32
	args             []byte // the procedure's arguments, after the header
}

// rawReply is what the fake server answers: success with the result body, an
// accepted failure, or nothing at all.
type rawReply struct {
	body      encoder
	accept    uint32 // accept_stat: 0 is success
	low, high uint32 // versions, for accept_stat 2
	drop      bool   // answer nothing
}

// rawServer is a fake ONC RPC server on the loopback address. Every call is
// handed to handle, which fills in the reply. Unlike nfstest's server it can
// answer as a broken, slow or hostile server does.
type rawServer struct {
	t        *testing.T
	l        net.Listener
	accepted atomic.Int64
	calls    atomic.Int64

	mu   sync.Mutex
	open []net.Conn
}

func startRawServer(t *testing.T, handle func(c rawCall, r *rawReply)) *rawServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &rawServer{t: t, l: l}
	t.Cleanup(func() {
		l.Close()
		s.mu.Lock()
		for _, c := range s.open {
			c.Close()
		}
		s.open = nil
		s.mu.Unlock()
	})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			n := s.accepted.Add(1)
			s.mu.Lock()
			s.open = append(s.open, c)
			s.mu.Unlock()
			go s.serve(c, n, handle)
		}
	}()
	return s
}

func (s *rawServer) port() int {
	_, port, err := net.SplitHostPort(s.l.Addr().String())
	if err != nil {
		s.t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		s.t.Fatal(err)
	}
	return n
}

func (s *rawServer) serve(c net.Conn, n int64, handle func(rawCall, *rawReply)) {
	defer c.Close()
	for {
		rec, err := readRecord(c)
		if err != nil {
			return
		}
		s.calls.Add(1)
		d := &decoder{b: rec}
		c2 := rawCall{conn: n}
		c2.xid = d.u32()
		d.u32() // msgCall
		d.u32() // RPC version
		c2.prog, c2.vers, c2.proc = d.u32(), d.u32(), d.u32()
		d.u32()
		d.opaque(400) // credential
		d.u32()
		d.opaque(400) // verifier
		if d.err != nil {
			return
		}
		c2.args = d.b
		var r rawReply
		handle(c2, &r)
		if r.drop {
			continue
		}
		var e encoder
		e.u32(0) // the record mark, set below
		e.u32(c2.xid)
		e.u32(msgReply)
		e.u32(0) // accepted
		e.u32(authNone)
		e.opaque(nil)
		e.u32(r.accept)
		if r.accept == acceptProgMismatch {
			e.u32(r.low)
			e.u32(r.high)
		}
		e.b = append(e.b, r.body.b...)
		binary.BigEndian.PutUint32(e.b, uint32(len(e.b)-4)|lastFragment)
		if _, err := c.Write(e.b); err != nil {
			return
		}
	}
}

// connCount is how many connections the server has accepted so far.
func (s *rawServer) connCount() int64 { return s.accepted.Load() }

// waitConns waits for the server to have accepted want connections.
func (s *rawServer) waitConns(t *testing.T, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.accepted.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("the server accepted %d connections, want %d", s.accepted.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitNoConns asserts that the server accepts no more than want connections
// for a while: a call that was turned away must not have dialed.
func (s *rawServer) waitNoConns(t *testing.T, want int64) {
	t.Helper()
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n := s.accepted.Load(); n > want {
			t.Fatalf("the server accepted %d connections, want %d", n, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// testFH is the root handle fakeNFS gives a freshly mounted export.
var testFH = []byte{0xde, 0xad, 0xbe, 0xef}

// fakeNFS is a fake server that mounts any export and answers FSINFO, so a
// Mount can be dialed against it, and hands every other NFS call to proc.
type fakeNFS struct {
	*rawServer
	proc func(c rawCall, r *rawReply)

	mu  sync.Mutex
	nfs map[int64]bool // the connections that have carried an NFS call
}

func startFakeNFS(t *testing.T, proc func(c rawCall, r *rawReply)) *fakeNFS {
	t.Helper()
	f := &fakeNFS{proc: proc, nfs: map[int64]bool{}}
	f.rawServer = startRawServer(t, func(c rawCall, r *rawReply) {
		if c.prog == progNFS {
			f.mu.Lock()
			f.nfs[c.conn] = true
			f.mu.Unlock()
		}
		switch {
		case c.prog == progMount && c.vers == versMount && c.proc == 1:
			// MNT: handle 1, and no auth flavors after it.
			r.body.u32(0)
			r.body.opaque(testFH)
			r.body.bool(false)
		case c.prog == progNFS && c.proc == procFSInfo:
			r.body.u32(0)
			r.body.bool(false) // post_op_attr
			r.body.u32(8192)   // rtmax
			r.body.u32(8192)   // rtpref
			r.body.u32(1)      // rtmult
			r.body.u32(8192)   // wtmax
			r.body.u32(8192)   // wtpref
			r.body.u32(1)      // wtmult
			r.body.u32(8192)   // dtpref
			r.body.u64(1 << 62)
			r.body.u32(0) // time_delta seconds
			r.body.u32(1) // time_delta nanoseconds
			r.body.u32(0) // properties
		default:
			f.proc(c, r)
		}
	})
	return f
}

// nfsConnections is how many connections have carried an NFS call.
func (f *fakeNFS) nfsConnections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.nfs)
}

// dialFake mounts "/export" on the fake server, so the calls under test have
// a Mount with an established connection.
func dialFake(t *testing.T, s *fakeNFS) *Mount {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, err := Dial(ctx, Config{Host: "127.0.0.1", Port: s.port()}, "/export")
	if err != nil {
		t.Fatalf("dial against the fake server: %v", err)
	}
	t.Cleanup(m.Close)
	s.waitConns(t, 2) // the MOUNT connection, then the NFS one
	return m
}

// writeAttr writes an fattr3.
func writeAttr(e *encoder, typ, mode uint32, size uint64) {
	e.u32(typ)
	e.u32(mode)
	e.u32(1) // nlink
	e.u32(0) // uid
	e.u32(0) // gid
	e.u64(size)
	e.u64(size)   // used
	e.u64(0)      // rdev
	e.u64(1)      // fsid
	e.u64(42)     // fileid
	for range 3 { // atime, mtime, ctime
		e.u32(0)
		e.u32(0)
	}
}

// writeEntry writes one entry3 of a READDIRPLUS listing, attributes and
// handle included.
func writeEntry(e *encoder, name string, cookie uint64) {
	e.bool(true) // entry present
	e.u64(42)    // fileid
	e.string(name)
	e.u64(cookie)
	e.bool(true) // post_op_attr
	writeAttr(e, TypeReg, 0o644, 0)
	e.bool(true) // name_handle present
	e.opaque([]byte{1, 2, 3, 4})
}

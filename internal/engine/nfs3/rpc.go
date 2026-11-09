package nfs3

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net"
	"sync"
	"syscall"
	"time"
)

// ONC RPC version 2 (RFC 5531) over TCP, with record marking. One
// connection carries many calls at once, told apart by their xid.

const (
	rpcVersion = 2
	msgCall    = 0
	msgReply   = 1

	authNone = 0
	authSys  = 1

	// lastFragment marks the last fragment of a record.
	lastFragment = 1 << 31
	// maxRecord bounds a reply: data comes in pieces of at most the
	// server's rtmax, and listings in its dtpref.
	maxRecord = 64 << 20
)

// ErrClosed is a call on a connection that closed, or broke: the caller
// may dial again.
var ErrClosed = errors.New("nfs: connection closed")

// RPCError is a call the server would not run: the program, version or
// procedure is not there, it could not read the arguments, or it refused
// the credentials.
type RPCError struct {
	Prog, Vers uint32
	// Accept is the accept_stat of an accepted call that failed: 1 the
	// program is not there, 2 not in this version (Low and High say
	// which it has), 3 not this procedure, 4 garbage arguments, 5 a
	// system error. 0 for a denied call.
	Accept uint32
	// Low and High are the versions the server has, for Accept 2 or a
	// denied RPC version.
	Low, High uint32
	// Denied is a call the server rejected; Auth is why, for an
	// authentication error (Auth > 0), else the RPC version mismatched.
	Denied bool
	Auth   uint32
}

// RPC accept and authentication statuses.
const (
	acceptProgUnavail  = 1
	acceptProgMismatch = 2
)

func (e *RPCError) Error() string {
	switch {
	case e.Denied && e.Auth > 0:
		return fmt.Sprintf("rpc: program %d refused the credentials (auth_stat %d)", e.Prog, e.Auth)
	case e.Denied:
		return fmt.Sprintf("rpc: server speaks RPC versions %d to %d", e.Low, e.High)
	case e.Accept == acceptProgUnavail:
		return fmt.Sprintf("rpc: program %d is not available", e.Prog)
	case e.Accept == acceptProgMismatch:
		return fmt.Sprintf("rpc: program %d has versions %d to %d, not %d", e.Prog, e.Low, e.High, e.Vers)
	}
	return fmt.Sprintf("rpc: program %d version %d failed (accept_stat %d)", e.Prog, e.Vers, e.Accept)
}

// Unsupported reports whether the server does not offer the program or
// the version asked for.
func (e *RPCError) Unsupported() bool {
	return !e.Denied && (e.Accept == acceptProgUnavail || e.Accept == acceptProgMismatch)
}

// cred is an RPC credential: AUTH_NONE, or AUTH_SYS with a uid and gid.
type cred struct {
	flavor uint32
	body   []byte
}

var noAuth = cred{flavor: authNone}

// sysAuth is AUTH_SYS (RFC 5531, appendix A): the client's name, a user
// and its group. The server trusts what the client says.
func sysAuth(machine string, uid, gid uint32) cred {
	if len(machine) > 255 {
		machine = machine[:255]
	}
	var e encoder
	e.u32(uint32(time.Now().Unix()))
	e.string(machine)
	e.u32(uid)
	e.u32(gid)
	e.u32(1) // the group list: the group alone
	e.u32(gid)
	return cred{flavor: authSys, body: e.b}
}

// conn is one TCP connection to an RPC program.
type conn struct {
	c       net.Conn
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[uint32]chan []byte
	err     error // why it closed; nil while open
	done    chan struct{}
}

// dialer opens connections from a privileged port when it can: NFS
// servers refuse others unless an export allows "insecure" ports. macOS
// (since 10.14) and Windows let any program bind one; Linux and others
// only root, and there the dial falls back to an ordinary port.
type dialer struct {
	timeout    time.Duration
	privileged bool
}

func (d dialer) dial(ctx context.Context, addr string) (*conn, error) {
	nd := net.Dialer{Timeout: d.timeout}
	if d.privileged {
		// A port in use, or one still in TIME_WAIT towards this
		// server, fails the bind; try a few others.
		for range 8 {
			nd.LocalAddr = &net.TCPAddr{Port: 600 + mrand.IntN(424)}
			c, err := nd.DialContext(ctx, "tcp", addr)
			if err == nil {
				return newConn(c), nil
			}
			if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
				break // not allowed here: an ordinary port
			}
			if !errors.Is(err, syscall.EADDRINUSE) && !errors.Is(err, syscall.EADDRNOTAVAIL) {
				return nil, err
			}
		}
		nd.LocalAddr = nil
	}
	c, err := nd.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return newConn(c), nil
}

func newConn(c net.Conn) *conn {
	cn := &conn{c: c, pending: map[uint32]chan []byte{}, done: make(chan struct{})}
	go cn.read()
	return cn
}

// read hands each reply to its call, until the connection fails.
func (cn *conn) read() {
	var err error
	for {
		var record []byte
		record, err = readRecord(cn.c)
		if err != nil {
			break
		}
		if len(record) < 4 {
			continue
		}
		xid := binary.BigEndian.Uint32(record)
		cn.mu.Lock()
		ch := cn.pending[xid]
		delete(cn.pending, xid)
		cn.mu.Unlock()
		if ch != nil {
			ch <- record[4:]
		}
	}
	cn.fail(err)
}

func readRecord(r io.Reader) ([]byte, error) {
	var record []byte
	var head [4]byte
	for {
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return nil, err
		}
		h := binary.BigEndian.Uint32(head[:])
		n := int(h &^ lastFragment)
		if len(record)+n > maxRecord {
			return nil, errors.New("rpc: reply too large")
		}
		start := len(record)
		record = append(record, make([]byte, n)...)
		if _, err := io.ReadFull(r, record[start:]); err != nil {
			return nil, err
		}
		if h&lastFragment != 0 {
			return record, nil
		}
	}
}

// fail closes the connection, once, and ends the calls waiting on it.
func (cn *conn) fail(err error) {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	if cn.err != nil {
		return
	}
	if err == nil || errors.Is(err, net.ErrClosed) {
		err = ErrClosed
	} else {
		err = fmt.Errorf("%w: %w", ErrClosed, err)
	}
	cn.err = err
	cn.c.Close()
	close(cn.done)
	for xid := range cn.pending {
		delete(cn.pending, xid) // their callers see done
	}
}

func (cn *conn) close() { cn.fail(nil) }

// broken reports whether the connection failed, so a new one is needed.
func (cn *conn) broken() bool {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	return cn.err != nil
}

func newXID() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}

// call runs procedure proc of program prog, version vers, with the
// arguments args writes, and returns a decoder of the results. ctx bounds
// the wait; a call that times out leaves the connection open, as the reply
// may still come and is then dropped.
func (cn *conn) call(ctx context.Context, prog, vers, proc uint32, c cred, args func(*encoder)) (*decoder, error) {
	// A call that is over before it starts must not touch the connection:
	// the connection is shared, and failure would end the other calls on
	// it. Its context alone says it is over.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	xid := newXID()
	var e encoder
	e.u32(0) // the record mark, set below
	e.u32(xid)
	e.u32(msgCall)
	e.u32(rpcVersion)
	e.u32(prog)
	e.u32(vers)
	e.u32(proc)
	e.u32(c.flavor)
	e.opaque(c.body)
	e.u32(authNone) // the verifier
	e.opaque(nil)
	if args != nil {
		args(&e)
	}
	binary.BigEndian.PutUint32(e.b, uint32(len(e.b)-4)|lastFragment)

	ch := make(chan []byte, 1)
	cn.mu.Lock()
	if cn.err != nil {
		err := cn.err
		cn.mu.Unlock()
		return nil, err
	}
	cn.pending[xid] = ch
	cn.mu.Unlock()
	forget := func() {
		cn.mu.Lock()
		delete(cn.pending, xid)
		cn.mu.Unlock()
	}

	cn.writeMu.Lock()
	// The context may have run out while this call waited for the writer.
	// Nothing has been written, so nothing is out of step: forget the xid
	// and leave the connection to the calls still using it.
	if err := ctx.Err(); err != nil {
		cn.writeMu.Unlock()
		forget()
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = cn.c.SetWriteDeadline(deadline)
	} else {
		_ = cn.c.SetWriteDeadline(time.Time{})
	}
	_, err := cn.c.Write(e.b)
	cn.writeMu.Unlock()
	if err != nil {
		cn.fail(err) // a write cut short leaves the stream out of step
		forget()
		cn.mu.Lock()
		err = cn.err
		cn.mu.Unlock()
		return nil, err
	}

	select {
	case reply := <-ch:
		return replyBody(reply, prog, vers)
	case <-cn.done:
		cn.mu.Lock()
		err := cn.err
		cn.mu.Unlock()
		return nil, err
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}

// replyBody checks an RPC reply, past its xid, and returns a decoder of
// the procedure's results.
func replyBody(b []byte, prog, vers uint32) (*decoder, error) {
	d := &decoder{b: b}
	if d.u32() != msgReply {
		if d.err != nil {
			return nil, d.err
		}
		return nil, errors.New("rpc: message is not a reply")
	}
	switch d.u32() {
	case 0: // accepted
		d.u32()       // the verifier's flavor
		d.opaque(400) // and body
		accept := d.u32()
		if d.err != nil {
			return nil, d.err
		}
		if accept == 0 {
			return d, nil
		}
		e := &RPCError{Prog: prog, Vers: vers, Accept: accept}
		if accept == acceptProgMismatch {
			e.Low, e.High = d.u32(), d.u32()
		}
		return nil, e
	case 1: // denied
		e := &RPCError{Prog: prog, Vers: vers, Denied: true}
		if d.u32() == 0 {
			e.Low, e.High = d.u32(), d.u32()
		} else {
			e.Auth = d.u32()
		}
		if d.err != nil {
			return nil, d.err
		}
		return nil, e
	}
	if d.err != nil {
		return nil, d.err
	}
	return nil, errors.New("rpc: unknown reply status")
}

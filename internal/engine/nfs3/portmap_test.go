package nfs3

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/engine/nfs3/nfstest"
)

// fakePortmapper answers GETPORT with ports[prog], 0 for a program it does
// not know, and becomes the portmapper the client asks until the test ends.
func fakePortmapper(t *testing.T, ports map[uint32]int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				for {
					var head [4]byte
					if _, err := io.ReadFull(c, head[:]); err != nil {
						return
					}
					call := make([]byte, binary.BigEndian.Uint32(head[:])&^lastFragment)
					if _, err := io.ReadFull(c, call); err != nil {
						return
					}
					d := &decoder{b: call}
					xid := d.u32()
					d.u32() // call
					d.u32() // rpc version
					d.u32() // program: the portmapper
					d.u32() // its version
					d.u32() // GETPORT
					d.u32()
					d.opaque(400) // credential
					d.u32()
					d.opaque(400) // verifier
					prog := d.u32()
					var e encoder
					e.u32(0)
					e.u32(xid)
					e.u32(msgReply)
					e.u32(0) // accepted
					e.u32(authNone)
					e.opaque(nil)
					e.u32(0) // success
					e.u32(uint32(ports[prog]))
					binary.BigEndian.PutUint32(e.b, uint32(len(e.b)-4)|lastFragment)
					if _, err := c.Write(e.b); err != nil {
						return
					}
				}
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	n, _ := strconv.Atoi(port)
	old := portmapPort
	portmapPort = n
	t.Cleanup(func() { portmapPort = old })
}

// Without a port, the client asks the portmapper where MOUNT and NFS are,
// as for a NAS.
func TestThePortmapperSaysWhereTheServerIs(t *testing.T) {
	srv := nfstest.Start(t, t.TempDir())
	fakePortmapper(t, map[uint32]int{progMount: srv.Port, progNFS: srv.Port})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := Dial(ctx, Config{Host: "127.0.0.1", UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}, "/")
	if err != nil {
		t.Fatalf("dial through the portmapper = %v", err)
	}
	defer m.Close()
	if _, err := m.ReadDir(ctx, m.Root); err != nil {
		t.Fatal(err)
	}
}

// A server whose portmapper has MOUNT but no NFS version 3 says so.
func TestAServerWithoutNFSv3(t *testing.T) {
	fakePortmapper(t, map[uint32]int{progMount: 1234})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Dial(ctx, Config{Host: "127.0.0.1"}, "/x")
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Prog != progNFS || !rpcErr.Unsupported() {
		t.Fatalf("dial = %v, want NFS version 3 missing", err)
	}
}

// An NFSv4-only server registers neither NFS version 3 nor MOUNT. The
// error must name the NFS program, so the engine says "no NFS version 3"
// rather than blame MOUNT.
func TestAnNFSv4OnlyServer(t *testing.T) {
	fakePortmapper(t, map[uint32]int{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Dial(ctx, Config{Host: "127.0.0.1"}, "/x")
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Prog != progNFS || !rpcErr.Unsupported() {
		t.Fatalf("dial = %v, want NFS version 3 missing before MOUNT", err)
	}
}

// NFS version 3 is registered but MOUNT is not: the error names MOUNT, so
// the engine can tell the user the port carries no MOUNT.
func TestAServerWithoutMountProgram(t *testing.T) {
	fakePortmapper(t, map[uint32]int{progNFS: 2049})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Dial(ctx, Config{Host: "127.0.0.1"}, "/x")
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Prog != progMount || !rpcErr.Unsupported() {
		t.Fatalf("dial = %v, want MOUNT missing", err)
	}
}

// An explicit port has no portmapper to ask: a server there that answers
// PROG_UNAVAIL to MOUNT is a missing MOUNT, not a missing NFS.
func TestAnExplicitPortWithoutMount(t *testing.T) {
	s := startRawServer(t, func(c rawCall, r *rawReply) {
		r.accept = acceptProgUnavail
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Dial(ctx, Config{Host: "127.0.0.1", Port: s.port()}, "/x")
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Prog != progMount || !rpcErr.Unsupported() {
		t.Fatalf("dial = %v, want MOUNT missing on the explicit port", err)
	}
	if rpcErr.Accept != acceptProgUnavail {
		t.Fatalf("accept_stat = %d, want %d", rpcErr.Accept, acceptProgUnavail)
	}
}

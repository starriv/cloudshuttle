package nfs3_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/engine/nfs3"
	"github.com/starriv/cloudshuttle/internal/engine/nfs3/nfstest"
)

func mount(t *testing.T) (*nfs3.Mount, string) {
	t.Helper()
	dir := t.TempDir()
	srv := nfstest.Start(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := nfs3.Dial(ctx, nfs3.Config{Host: srv.Host, Port: srv.Port, UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}, "/")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m, dir
}

func TestAFileGoesThereAndBack(t *testing.T) {
	m, dir := mount(t)
	ctx := context.Background()
	sub, err := m.Mkdir(ctx, m.Root, "photos", nfs3.SetAttr{})
	if err != nil {
		t.Fatal(err)
	}
	fh, err := m.Create(ctx, sub, "a.txt", true, nfs3.SetAttr{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, sub, "a.txt", true, nfs3.SetAttr{}); !nfs3.IsStatus(err, nfs3.ErrExist) {
		t.Fatalf("a guarded create of a taken name = %v, want EXIST", err)
	}
	body := bytes.Repeat([]byte("cloudshuttle "), 10000) // past one write
	for off := 0; off < len(body); {
		end := min(off+int(m.Info.WritePref), len(body))
		n, _, _, err := m.Write(ctx, fh, uint64(off), body[off:end], nfs3.Unstable)
		if err != nil {
			t.Fatal(err)
		}
		off += int(n)
	}
	if _, err := m.Commit(ctx, fh); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := m.Setattr(ctx, fh, nfs3.SetAttr{Mtime: when, SetTime: true}); err != nil {
		t.Fatal(err)
	}
	on, err := os.ReadFile(filepath.Join(dir, "photos", "a.txt"))
	if err != nil || !bytes.Equal(on, body) {
		t.Fatalf("on disk: %d bytes, %v; want %d", len(on), err, len(body))
	}
	_, attr, err := m.Lookup(ctx, sub, "a.txt")
	if err != nil || attr.Size != uint64(len(body)) || !attr.Mtime.Equal(when) || attr.Type != nfs3.TypeReg {
		t.Fatalf("lookup = %+v, %v", attr, err)
	}
	var back []byte
	for off := uint64(0); ; {
		data, eof, err := m.Read(ctx, fh, off, m.Info.ReadPref)
		if err != nil {
			t.Fatal(err)
		}
		back = append(back, data...)
		off += uint64(len(data))
		if eof || len(data) == 0 {
			break
		}
	}
	if !bytes.Equal(back, body) {
		t.Fatalf("read back %d bytes, want %d", len(back), len(body))
	}
}

func TestAFolderLists(t *testing.T) {
	m, dir := mount(t)
	ctx := context.Background()
	for i := range 300 { // more than one page of readdirplus
		if err := os.WriteFile(filepath.Join(dir, "f"+strconv.Itoa(i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := m.ReadDir(ctx, m.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 301 {
		t.Fatalf("listed %d entries, want 301", len(entries))
	}
	dirs := 0
	for _, e := range entries {
		if e.Name == "." || e.Name == ".." {
			t.Fatalf("listed %q", e.Name)
		}
		if e.Attr.Type == nfs3.TypeDir {
			dirs++
		}
	}
	if dirs != 1 {
		t.Errorf("listed %d folders, want 1", dirs)
	}
}

func TestRenameRemoveAndErrors(t *testing.T) {
	m, dir := mount(t)
	ctx := context.Background()
	fh, err := m.Create(ctx, m.Root, "old", true, nfs3.SetAttr{})
	if err != nil {
		t.Fatal(err)
	}
	// A link never replaces what is there. go-nfs does not make links
	// (INVAL); a NAS answers EXIST.
	if err := m.Link(ctx, fh, m.Root, "old"); err == nil {
		t.Errorf("a link onto a taken name succeeded")
	}
	if err := m.Rename(ctx, m.Root, "old", m.Root, "new"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Lookup(ctx, m.Root, "old"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookup of a renamed file = %v, want not found", err)
	}
	if err := m.Remove(ctx, m.Root, "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new")); !os.IsNotExist(err) {
		t.Fatalf("the removed file is still there: %v", err)
	}
	sub, err := m.Mkdir(ctx, m.Root, "full", nfs3.SetAttr{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, sub, "x", true, nfs3.SetAttr{}); err != nil {
		t.Fatal(err)
	}
	// A NAS answers NOTEMPTY; go-nfs an I/O error. Either way the
	// folder stays.
	if err := m.Rmdir(ctx, m.Root, "full"); err == nil {
		t.Fatal("rmdir of a full folder succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "full", "x")); err != nil {
		t.Fatalf("rmdir took the folder's file: %v", err)
	}
}

// A connection the server drops is dialed again on the next call.
func TestADroppedConnectionIsDialedAgain(t *testing.T) {
	dir := t.TempDir()
	srv := nfstest.Start(t, dir)
	// A proxy in front of the server, whose connections the test cuts.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	conns := make(chan net.Conn, 16)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			conns <- c
			s, err := net.Dial("tcp", net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port)))
			if err != nil {
				c.Close()
				continue
			}
			go pipe(c, s)
			go pipe(s, c)
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	p, _ := strconv.Atoi(port)
	ctx := context.Background()
	m, err := nfs3.Dial(ctx, nfs3.Config{Host: "127.0.0.1", Port: p}, "/")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for len(conns) > 0 {
		(<-conns).Close() // the MOUNT connection and the first NFS one
	}
	if _, err := m.Getattr(ctx, m.Root); err == nil {
		// The call may have raced the close; the next one must work.
		t.Log("the first call after the cut succeeded")
	}
	if _, err := m.Getattr(ctx, m.Root); err != nil {
		t.Fatalf("a call after the connection dropped = %v, want it dialed again", err)
	}
}

func pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	dst.Close()
	src.Close()
}

// A server that never answers does not hold a call past its context.
func TestACallEndsWithItsContext(t *testing.T) {
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
			t.Cleanup(func() { c.Close() }) // read nothing, answer nothing
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	p, _ := strconv.Atoi(port)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = nfs3.Dial(ctx, nfs3.Config{Host: "127.0.0.1", Port: p}, "/")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial = %v, want the deadline", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("dial took %v", time.Since(start))
	}
}

func TestNothingListensOnThePortmapper(t *testing.T) {
	// 127.0.0.2 is a loopback address nothing serves on, on Linux; on
	// macOS it is not configured, which fails the dial as well.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := nfs3.Exports(ctx, nfs3.Config{Host: "127.0.0.2", Timeout: time.Second}); err == nil {
		t.Fatal("exports of a server that is not there succeeded")
	}
}

// fakeMountd answers MOUNT's EXPORT with exports, and any other call with
// PROC_UNAVAIL, on a port of its own.
func fakeMountd(t *testing.T, exports []string) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	u32 := func(b []byte, v uint32) []byte { return binary.BigEndian.AppendUint32(b, v) }
	str := func(b []byte, s string) []byte {
		b = u32(b, uint32(len(s)))
		b = append(b, s...)
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
		return b
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var head [4]byte
				if _, err := io.ReadFull(c, head[:]); err != nil {
					return
				}
				call := make([]byte, binary.BigEndian.Uint32(head[:])&^(1<<31))
				if _, err := io.ReadFull(c, call); err != nil {
					return
				}
				xid, proc := binary.BigEndian.Uint32(call), binary.BigEndian.Uint32(call[20:])
				body := u32(u32(u32(u32(u32(nil, xid), 1), 0), 0), 0) // reply, accepted, null verifier
				if proc != 5 {
					body = u32(body, 3) // PROC_UNAVAIL
				} else {
					body = u32(body, 0)
					for _, e := range exports {
						body = str(u32(body, 1), e)
						body = u32(str(u32(body, 1), "*"), 0) // one group
					}
					body = u32(body, 0)
				}
				_, _ = c.Write(append(u32(nil, uint32(len(body))|1<<31), body...))
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	p, _ := strconv.Atoi(port)
	return p
}

func TestExportsAreListed(t *testing.T) {
	port := fakeMountd(t, []string{"/volume1/backup", "/volume1/photos"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := nfs3.Exports(ctx, nfs3.Config{Host: "127.0.0.1", Port: port})
	if err != nil || len(got) != 2 || got[0] != "/volume1/backup" || got[1] != "/volume1/photos" {
		t.Fatalf("exports = %q, %v", got, err)
	}
	// go-nfs keeps the list to itself: the error says the procedure is
	// not there.
	srv := nfstest.Start(t, t.TempDir())
	_, err = nfs3.Exports(ctx, nfs3.Config{Host: srv.Host, Port: srv.Port})
	var rpcErr *nfs3.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("exports of a server without the list = %v, want an RPC error", err)
	}
}

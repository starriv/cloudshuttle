package nfs3

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// A call whose deadline has already passed must not close the shared
// connection: another call on it is still waiting for its reply.
func TestACallWithAnExpiredDeadlineKeepsTheConnection(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	releaseCall := func() { once.Do(func() { close(release) }) }
	t.Cleanup(releaseCall)

	f := startFakeNFS(t, func(c rawCall, r *rawReply) {
		if c.proc != procGetattr {
			r.body.u32(ErrNotSupp)
			return
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release // a slow server
		r.body.u32(0)
		writeAttr(&r.body, TypeDir, 0o755, 0)
	})
	m := dialFake(t, f)
	before := f.connCount()

	type result struct {
		attr Attr
		err  error
	}
	done := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		attr, err := m.Getattr(ctx, m.Root)
		done <- result{attr, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the healthy call never reached the server")
	}

	// Already over when it starts: it must return at once without writing,
	// and without failing the connection the other call is on.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	start := time.Now()
	_, err := m.Getattr(ctx, m.Root)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a call with an expired deadline = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrClosed) {
		t.Fatalf("the expired call failed the connection: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the expired call took %v", d)
	}

	releaseCall()
	res := <-done
	if res.err != nil {
		t.Fatalf("the call already waiting on the connection = %v, want its reply", res.err)
	}
	if res.attr.Type != TypeDir {
		t.Fatalf("the waiting call's attributes = %+v, want a folder", res.attr)
	}
	if got := f.connCount(); got != before {
		t.Fatalf("connections = %d, want %d: the expired call dialed again", got, before)
	}
	if got := f.nfsConnections(); got != 1 {
		t.Fatalf("NFS connections = %d, want 1: the call did not reuse the connection", got)
	}
}

// Reset drops the connection but leaves the mount usable; Close ends it.
func TestCloseEndsTheMountAndResetKeepsIt(t *testing.T) {
	f := startFakeNFS(t, func(c rawCall, r *rawReply) {
		if c.proc != procGetattr {
			r.body.u32(ErrNotSupp)
			return
		}
		r.body.u32(0)
		writeAttr(&r.body, TypeDir, 0o755, 0)
	})
	m := dialFake(t, f)
	ctx := context.Background()
	if _, err := m.Getattr(ctx, m.Root); err != nil {
		t.Fatalf("getattr before a reset = %v", err)
	}
	if got := f.connCount(); got != 2 {
		t.Fatalf("connections = %d, want 2", got)
	}

	// Reset drops the connection only: the mount dials again.
	m.Reset()
	if _, err := m.Getattr(ctx, m.Root); err != nil {
		t.Fatalf("getattr after a reset = %v, want it dialed again", err)
	}
	f.waitConns(t, 3)

	// Close ends the mount: no call dials again, and it is idempotent.
	m.Close()
	m.Close()
	if _, err := m.Getattr(ctx, m.Root); !errors.Is(err, ErrClosed) {
		t.Fatalf("getattr after close = %v, want ErrClosed", err)
	}
	f.waitNoConns(t, 3)
}

// writeCountServer answers WRITE with replyCount(requested) as the count and
// committed as the stable_how.
func writeCountServer(t *testing.T, replyCount func(requested uint32) uint32, committed uint32) *fakeNFS {
	t.Helper()
	return startFakeNFS(t, func(c rawCall, r *rawReply) {
		if c.proc != procWrite {
			r.body.u32(ErrNotSupp)
			return
		}
		d := &decoder{b: c.args}
		d.opaque(64) // handle
		d.u64()      // offset
		requested := d.u32()
		d.u32() // stable_how
		d.opaque(1 << 20)
		if d.err != nil {
			t.Errorf("the fake server could not read the WRITE arguments: %v", d.err)
			r.body.u32(ErrInval)
			return
		}
		r.body.u32(0)      // status
		r.body.bool(false) // wcc_data pre_op_attr
		r.body.bool(false) // wcc_data post_op_attr
		r.body.u32(replyCount(requested))
		r.body.u32(committed)
		r.body.fixed(make([]byte, 8)) // write verifier
	})
}

// A WRITE the server says took more than was sent is an error, not a count
// the caller may slice its buffer by.
func TestAWriteCannotTakeMoreThanWasSent(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		f := writeCountServer(t, func(requested uint32) uint32 { return requested + 1 }, Unstable)
		m := dialFake(t, f)
		p := []byte("abc")
		n, committed, verf, err := m.Write(context.Background(), m.Root, 0, p, Unstable)
		if err == nil {
			t.Fatal("a WRITE the server overcounted succeeded")
		}
		var zero [8]byte
		if n != 0 || committed != 0 || verf != zero {
			t.Fatalf("write = %d, %d, %x with %v; want zeroes with the error", n, committed, verf, err)
		}
		// The engine advances its buffer by n after a successful write.
		// The error makes it stop first, and the zero n keeps the slice
		// in range either way.
		if got := len(p[n:]); got != len(p) {
			t.Fatalf("p[%d:] is %d bytes, want %d", n, got, len(p))
		}
	})
	t.Run("stable_how", func(t *testing.T) {
		f := writeCountServer(t, func(requested uint32) uint32 { return requested }, 3)
		m := dialFake(t, f)
		if _, _, _, err := m.Write(context.Background(), m.Root, 0, []byte("abc"), Unstable); err == nil {
			t.Fatal("a WRITE with stable_how 3 succeeded")
		}
	})
}

// readdirServer answers READDIRPLUS with pages from reply, recording the
// cookie and verifier each call carried.
type readdirServer struct {
	*fakeNFS
	mu       sync.Mutex
	cookies  []uint64
	verfs    [][8]byte
	requests int
}

func startReaddirServer(t *testing.T, reply func(n int, r *rawReply)) *readdirServer {
	t.Helper()
	s := &readdirServer{}
	s.fakeNFS = startFakeNFS(t, func(c rawCall, r *rawReply) {
		if c.proc != procReaddirplus {
			r.body.u32(ErrNotSupp)
			return
		}
		d := &decoder{b: c.args}
		d.opaque(64) // handle
		cookie := d.u64()
		var verf [8]byte
		copy(verf[:], d.fixed(8))
		d.u32() // count
		d.u32() // maxcount
		if d.err != nil {
			t.Errorf("the fake server could not read the READDIRPLUS arguments: %v", d.err)
			r.body.u32(ErrInval)
			return
		}
		s.mu.Lock()
		s.requests++
		n := s.requests
		s.cookies = append(s.cookies, cookie)
		s.verfs = append(s.verfs, verf)
		s.mu.Unlock()
		reply(n, r)
	})
	return s
}

func (s *readdirServer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// writePage writes the post_op_attr and cookie verifier that start every
// READDIRPLUS reply, then lets entry write the page.
func writePage(r *rawReply, verf [8]byte, eof bool, entries ...func(*encoder)) {
	r.body.u32(0)      // status
	r.body.bool(false) // post_op_attr for the folder
	r.body.fixed(verf[:])
	for _, e := range entries {
		e(&r.body)
	}
	r.body.bool(false) // no more entries
	r.body.bool(eof)   // eof
}

// RFC 1813 lets a server answer a page with no entries and eof false. The
// client retries the same cookie instead of ending the listing there.
func TestReadDirRetriesAnEmptyPage(t *testing.T) {
	s := startReaddirServer(t, func(n int, r *rawReply) {
		if n == 1 {
			writePage(r, [8]byte{1, 2, 3}, false) // no entries, not the end
			return
		}
		writePage(r, [8]byte{1, 2, 3}, true, func(e *encoder) { writeEntry(e, "a.txt", 1) })
	})
	m := dialFake(t, s.fakeNFS)
	entries, err := m.ReadDir(context.Background(), m.Root)
	if err != nil {
		t.Fatalf("readdir with an empty first page = %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("readdir = %+v, want a.txt", entries)
	}
	if got := s.calls(); got != 2 {
		t.Fatalf("readdirplus calls = %d, want 2 (the empty page retried once)", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cookies[1] != 0 {
		t.Fatalf("the retry sent cookie %d, want the same cookie 0", s.cookies[1])
	}
}

// A server that never moves past an empty page is an error, not the end of
// a shortened listing.
func TestReadDirGivesUpOnEndlessEmptyPages(t *testing.T) {
	s := startReaddirServer(t, func(n int, r *rawReply) {
		writePage(r, [8]byte{}, false) // no entries, never eof
	})
	m := dialFake(t, s.fakeNFS)
	entries, err := m.ReadDir(context.Background(), m.Root)
	if err == nil {
		t.Fatal("readdir of a server that sends only empty pages succeeded")
	}
	if entries != nil {
		t.Fatalf("readdir returned a partial listing %+v with %v", entries, err)
	}
	if got := s.calls(); got != maxEmptyPages {
		t.Fatalf("readdirplus calls = %d, want the bound of %d", got, maxEmptyPages)
	}
}

// A BAD_COOKIE restarts the traversal from the beginning, once.
func TestReadDirRestartsAfterABadCookie(t *testing.T) {
	s := startReaddirServer(t, func(n int, r *rawReply) {
		if n == 1 {
			r.body.u32(ErrBadCookie)
			return
		}
		writePage(r, [8]byte{4, 5, 6}, true, func(e *encoder) { writeEntry(e, "a.txt", 1) })
	})
	m := dialFake(t, s.fakeNFS)
	entries, err := m.ReadDir(context.Background(), m.Root)
	if err != nil {
		t.Fatalf("readdir after one BAD_COOKIE = %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("readdir = %+v, want a.txt", entries)
	}
	if got := s.calls(); got != 2 {
		t.Fatalf("readdirplus calls = %d, want 2", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cookies[1] != 0 {
		t.Fatalf("the restart sent cookie %d, want 0", s.cookies[1])
	}
	if s.verfs[1] != [8]byte{} {
		t.Fatalf("the restart sent verifier %x, want zeroes", s.verfs[1])
	}
}

// A server that keeps answering BAD_COOKIE is an error after a bounded
// number of restarts.
func TestReadDirGivesUpOnEndlessBadCookies(t *testing.T) {
	s := startReaddirServer(t, func(n int, r *rawReply) {
		r.body.u32(ErrBadCookie)
	})
	m := dialFake(t, s.fakeNFS)
	if _, err := m.ReadDir(context.Background(), m.Root); err == nil {
		t.Fatal("readdir of a server that always answers BAD_COOKIE succeeded")
	}
	if got, want := s.calls(), maxBadCookieRestarts+1; got != want {
		t.Fatalf("readdirplus calls = %d, want %d", got, want)
	}
}

// A name the server could not have sent is an error, never a listing entry.
func TestReadDirRejectsImpossibleNames(t *testing.T) {
	long := make([]byte, nfsNameMax+1)
	for i := range long {
		long[i] = 'a'
	}
	cases := map[string]string{
		"slash":   "a/b",
		"nul":     "a\x00b",
		"tooLong": string(long),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			s := startReaddirServer(t, func(n int, r *rawReply) {
				writePage(r, [8]byte{}, true, func(e *encoder) { writeEntry(e, bad, 1) })
			})
			m := dialFake(t, s.fakeNFS)
			entries, err := m.ReadDir(context.Background(), m.Root)
			if err == nil {
				t.Fatalf("readdir took the impossible name %q: %+v", bad, entries)
			}
		})
	}
}

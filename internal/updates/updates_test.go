package updates

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRelease struct {
	version string
	err     error
	gate    chan struct{} // Install waits for it when not nil
}

func (r *fakeRelease) Version() string { return r.version }

func (r *fakeRelease) Install(ctx context.Context, progress func(int64, int64)) error {
	if r.gate != nil {
		<-r.gate
	}
	if progress != nil {
		progress(50, 100)
		progress(100, 100)
	}
	return r.err
}

type fakeSource struct {
	enabled bool
	mu      sync.Mutex
	release *fakeRelease
	err     error
	checks  int
}

func (s *fakeSource) Enabled() bool { return s.enabled }

func (s *fakeSource) Check(context.Context) (Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks++
	if s.err != nil {
		return nil, s.err
	}
	if s.release == nil {
		return nil, nil
	}
	return s.release, nil
}

type harness struct {
	u         *Updater
	runs      atomic.Int32
	quiet     atomic.Bool
	relaunch  atomic.Int32
	available []string
	mu        sync.Mutex
}

func newHarness(src *fakeSource) *harness {
	h := &harness{}
	h.u = New(Options{
		Source:   src,
		Runs:     func() int { return int(h.runs.Load()) },
		Quiet:    h.quiet.Load,
		Relaunch: func() { h.relaunch.Add(1) },
		OnAvailable: func(v string) {
			h.mu.Lock()
			h.available = append(h.available, v)
			h.mu.Unlock()
		},
	})
	return h
}

func TestUnavailable(t *testing.T) {
	src := &fakeSource{}
	h := newHarness(src)
	h.u.check(context.Background(), true)
	if s := h.u.State(); s.Phase != PhaseUnavailable || src.checks != 0 {
		t.Fatalf("state %+v, checks %d", s, src.checks)
	}
}

func TestUpToDate(t *testing.T) {
	h := newHarness(&fakeSource{enabled: true})
	h.u.check(context.Background(), true)
	if s := h.u.State(); s.Phase != PhaseUpToDate || s.LastCheck.IsZero() {
		t.Fatalf("state %+v", s)
	}
}

func TestBackgroundCheckNotifiesOnce(t *testing.T) {
	src := &fakeSource{enabled: true, release: &fakeRelease{version: "1.3.0"}}
	h := newHarness(src)
	h.u.check(context.Background(), false)
	h.u.check(context.Background(), false)
	if s := h.u.State(); s.Phase != PhaseAvailable || s.Version != "1.3.0" {
		t.Fatalf("state %+v", s)
	}
	if len(h.available) != 1 {
		t.Fatalf("notified %v, want once", h.available)
	}
	// A check the person asked for tells them in the window, not again.
	h.u.check(context.Background(), true)
	if len(h.available) != 1 {
		t.Fatalf("notified %v after a check asked for", h.available)
	}
}

func TestBackgroundFailureKeepsWhatWasFound(t *testing.T) {
	src := &fakeSource{enabled: true, release: &fakeRelease{version: "1.3.0"}}
	h := newHarness(src)
	h.u.check(context.Background(), false)
	src.err = errors.New("offline")
	h.u.check(context.Background(), false)
	if s := h.u.State(); s.Phase != PhaseAvailable || s.Version != "1.3.0" {
		t.Fatalf("state %+v", s)
	}
	h.u.check(context.Background(), true)
	if s := h.u.State(); s.Phase != PhaseCheckFailed || s.Err == nil {
		t.Fatalf("state %+v", s)
	}
}

func TestInstallWaitsForRuns(t *testing.T) {
	src := &fakeSource{enabled: true, release: &fakeRelease{version: "1.3.0"}}
	h := newHarness(src)
	h.u.check(context.Background(), true)
	h.runs.Store(2)
	h.u.install(context.Background(), false)
	s := h.u.State()
	if s.Phase != PhaseReady || !s.Waiting || s.Runs != 2 || s.Downloaded != 100 {
		t.Fatalf("state %+v", s)
	}
	if h.relaunch.Load() != 0 {
		t.Fatal("restarted while tasks run")
	}
	h.runs.Store(1)
	h.u.RunsChanged()
	if h.relaunch.Load() != 0 {
		t.Fatal("restarted while a task runs")
	}
	h.runs.Store(0)
	h.u.RunsChanged()
	h.u.RunsChanged()
	if h.relaunch.Load() != 1 {
		t.Fatalf("restarted %d times, want once", h.relaunch.Load())
	}
}

func TestCancelRestart(t *testing.T) {
	src := &fakeSource{enabled: true, release: &fakeRelease{version: "1.3.0"}}
	h := newHarness(src)
	h.u.check(context.Background(), true)
	h.runs.Store(1)
	h.u.install(context.Background(), false)
	h.u.CancelRestart()
	h.runs.Store(0)
	h.u.RunsChanged()
	if h.relaunch.Load() != 0 || h.u.State().Waiting {
		t.Fatal("restarted after the restart was canceled")
	}
	h.u.Restart()
	if h.relaunch.Load() != 1 {
		t.Fatal("did not restart when asked with no task running")
	}
}

func TestAutomaticInstallRestartsQuietly(t *testing.T) {
	src := &fakeSource{enabled: true, release: &fakeRelease{version: "1.3.0"}}
	h := newHarness(src)
	h.u.SetAutomatic(true, true)
	h.u.check(context.Background(), false)
	s := h.u.State()
	if s.Phase != PhaseReady || s.Waiting {
		t.Fatalf("state %+v", s)
	}
	if len(h.available) != 0 {
		t.Fatal("notified of an update installed by itself")
	}
	// The window is in use: no restart until it hides.
	h.u.RunsChanged()
	if h.relaunch.Load() != 0 {
		t.Fatal("restarted while the window is in use")
	}
	h.quiet.Store(true)
	h.runs.Store(1)
	h.u.RunsChanged()
	if h.relaunch.Load() != 0 {
		t.Fatal("restarted while a task runs")
	}
	h.runs.Store(0)
	h.u.RunsChanged()
	if h.relaunch.Load() != 1 {
		t.Fatal("did not restart once quiet")
	}
}

func TestInstallFailure(t *testing.T) {
	src := &fakeSource{enabled: true, release: &fakeRelease{version: "1.3.0", err: errors.New("bad signature")}}
	h := newHarness(src)
	h.u.check(context.Background(), true)
	h.u.install(context.Background(), false)
	if s := h.u.State(); s.Phase != PhaseInstallFailed || s.Err == nil || s.Waiting {
		t.Fatalf("state %+v", s)
	}
	src.release.err = nil
	h.u.install(context.Background(), false)
	if s := h.u.State(); s.Phase != PhaseReady {
		t.Fatalf("retry: state %+v", s)
	}
}

func TestNoCheckWhileReady(t *testing.T) {
	src := &fakeSource{enabled: true, release: &fakeRelease{version: "1.3.0"}}
	h := newHarness(src)
	h.u.check(context.Background(), true)
	h.runs.Store(1)
	h.u.install(context.Background(), false)
	h.u.check(context.Background(), true)
	if src.checks != 1 {
		t.Fatalf("checked %d times, want 1", src.checks)
	}
}

func TestLoopChecksWhenOn(t *testing.T) {
	src := &fakeSource{enabled: true}
	u := New(Options{Source: src, Delay: time.Millisecond, Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u.Start(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for u.State().Phase != PhaseUpToDate {
		if time.Now().After(deadline) {
			t.Fatal("no automatic check")
		}
		time.Sleep(5 * time.Millisecond)
	}

	off := &fakeSource{enabled: true}
	u = New(Options{Source: off, Delay: time.Millisecond, Interval: time.Hour})
	u.SetAutomatic(false, false)
	u.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	off.mu.Lock()
	defer off.mu.Unlock()
	if off.checks != 0 {
		t.Fatal("checked with automatic checks off")
	}
}

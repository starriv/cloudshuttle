// Package updates keeps the app up to date: it checks for a newer version,
// downloads it, and restarts the app into it once no task is running. The
// download, its signature and the swap of the app are MyGo's (Source); this
// package decides when, and holds the state the settings page and the
// status menu show. It imports nothing else of the app.
package updates

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ReleasesURL is where the app's versions are published, for people whose
// copy cannot update itself.
const ReleasesURL = "https://github.com/starriv/cloudshuttle/releases"

// ReleaseURL is the page of a version, with its release notes.
func ReleaseURL(version string) string { return ReleasesURL + "/tag/v" + version }

// Release is a newer version that Source.Check found.
type Release interface {
	Version() string
	// Install downloads the version, checks its signature and leaves it to
	// replace the app when the app exits; progress may be nil.
	Install(ctx context.Context, progress func(downloaded, total int64)) error
}

// Source finds and installs newer versions: mygo.Updater in the app, a fake
// in tests.
type Source interface {
	// Enabled reports whether the app can update itself: built with
	// updates, run from its bundle, in a place it can write to.
	Enabled() bool
	// Check returns a newer version, or nil when the app is up to date.
	Check(ctx context.Context) (Release, error)
}

// Phase is where the updater is.
type Phase int

const (
	// PhaseIdle is before the first check.
	PhaseIdle Phase = iota
	// PhaseUnavailable is an app that cannot update itself (Source.Enabled).
	PhaseUnavailable
	PhaseChecking
	PhaseUpToDate
	// PhaseAvailable is a newer version, not downloaded.
	PhaseAvailable
	PhaseDownloading
	// PhaseReady is a newer version installed: it runs once the app
	// restarts, or quits and opens again.
	PhaseReady
	// PhaseCheckFailed and PhaseInstallFailed keep the error in State.Err.
	PhaseCheckFailed
	PhaseInstallFailed
)

// State is what the updater shows.
type State struct {
	// Current is the version running now.
	Current string
	Phase   Phase
	// Version is the newer version: available, downloading or ready.
	Version string
	// Downloaded and Total are the bytes of the download in progress;
	// Total is 0 until it is known.
	Downloaded, Total int64
	// LastCheck is when the last check ended, zero before one did.
	LastCheck time.Time
	Err       error
	// Waiting is a restart asked for, which waits for the runs to end;
	// Runs is how many run now.
	Waiting bool
	Runs    int
}

// Options configure an Updater.
type Options struct {
	Source Source
	// Current is the version running now.
	Current string
	// Runs reports how many tasks run now, in this process or the
	// scheduler's. It is called from any goroutine.
	Runs func() int
	// Quiet reports whether the app may restart without being asked: its
	// window is hidden, so nobody is using it. It is called from any
	// goroutine.
	Quiet func() bool
	// Relaunch quits the app and opens it again.
	Relaunch func()
	// OnChange is called, from any goroutine, after the state changed.
	OnChange func()
	// OnAvailable is called, from any goroutine, once for each newer
	// version an automatic check finds and does not install by itself.
	OnAvailable func(version string)
	// Interval is the time between automatic checks, a day when zero;
	// Delay is the time from Start to the first, 10 seconds when zero.
	Interval, Delay time.Duration
}

// Updater checks for, downloads and restarts into newer versions. Its
// methods may be called from any goroutine.
type Updater struct {
	opts Options

	mu        sync.Mutex
	state     State
	release   Release
	busy      bool // a check or a download runs
	autoCheck bool
	autoApply bool
	// quietRestart is a restart the app may do by itself: an update it
	// installed on its own, applied while nobody uses the window.
	quietRestart bool
	notified     string // the version OnAvailable was last called for
	relaunched   bool
	wake         chan struct{}
}

// New returns an updater that checks for updates on its own, once started,
// until SetAutomatic says otherwise.
func New(opts Options) *Updater {
	if opts.Interval <= 0 {
		opts.Interval = 24 * time.Hour
	}
	if opts.Delay <= 0 {
		opts.Delay = 10 * time.Second
	}
	if opts.Runs == nil {
		opts.Runs = func() int { return 0 }
	}
	if opts.Quiet == nil {
		opts.Quiet = func() bool { return false }
	}
	u := &Updater{opts: opts, autoCheck: true, wake: make(chan struct{}, 1)}
	u.state.Current = opts.Current
	if !opts.Source.Enabled() {
		u.state.Phase = PhaseUnavailable
	}
	return u
}

// State returns what the updater shows now.
func (u *Updater) State() State {
	u.mu.Lock()
	s := u.state
	u.mu.Unlock()
	s.Runs = u.opts.Runs()
	return s
}

// Enabled reports whether the app can update itself.
func (u *Updater) Enabled() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state.Phase != PhaseUnavailable
}

// SetAutomatic sets whether the app checks for updates on its own, and
// whether it then downloads them and restarts into them by itself.
func (u *Updater) SetAutomatic(check, apply bool) {
	u.mu.Lock()
	changed := u.autoCheck != check || u.autoApply != apply
	u.autoCheck, u.autoApply = check, apply
	if !apply {
		u.quietRestart = false // the person no longer lets it restart by itself
	}
	u.mu.Unlock()
	if changed {
		u.poke()
	}
}

// Start checks for updates on its own until ctx is done: after Delay, then
// every Interval, while automatic checks are on.
func (u *Updater) Start(ctx context.Context) {
	if !u.Enabled() {
		return
	}
	go u.loop(ctx)
}

func (u *Updater) loop(ctx context.Context) {
	next := time.Now().Add(u.opts.Delay)
	for {
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-u.wake:
			timer.Stop()
			continue // the settings changed: the time of the next check stays
		case <-timer.C:
		}
		u.mu.Lock()
		on := u.autoCheck
		u.mu.Unlock()
		if on {
			u.check(ctx, false)
		}
		next = time.Now().Add(u.opts.Interval)
	}
}

func (u *Updater) poke() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// Check checks for a newer version now, as the person asked. It returns at
// once; the state says how it went.
func (u *Updater) Check() {
	go u.check(context.Background(), true)
}

// check asks the source for a newer version; user is a check the person
// asked for, which also shows a failure and offers what it found even when
// the app would install it by itself.
func (u *Updater) check(ctx context.Context, user bool) {
	u.mu.Lock()
	switch {
	case u.state.Phase == PhaseUnavailable, u.busy, u.state.Phase == PhaseReady:
		// A version is ready: the next is checked for after the restart.
		u.mu.Unlock()
		return
	}
	u.busy = true
	previous := u.state
	u.state.Phase, u.state.Err = PhaseChecking, nil
	u.mu.Unlock()
	u.changed()

	r, err := u.opts.Source.Check(ctx)

	u.mu.Lock()
	u.busy = false
	u.state.LastCheck = time.Now()
	notify, apply := "", false
	switch {
	case err != nil && !user && (previous.Phase == PhaseAvailable || previous.Phase == PhaseInstallFailed):
		// A background check that failed, offline say, keeps offering
		// the version found before.
		u.state.Phase, u.state.Err = previous.Phase, previous.Err
		slog.Warn("check for updates", "err", err)
	case err != nil:
		u.state.Phase, u.state.Err = PhaseCheckFailed, err
		slog.Warn("check for updates", "err", err)
	case r == nil:
		u.release = nil
		u.state.Phase, u.state.Version = PhaseUpToDate, ""
	default:
		u.release = r
		u.state.Phase, u.state.Version = PhaseAvailable, r.Version()
		switch {
		case u.autoApply && !user:
			apply = true
		case !user && u.notified != r.Version():
			u.notified, notify = r.Version(), r.Version()
		}
	}
	u.mu.Unlock()
	u.changed()
	if notify != "" && u.opts.OnAvailable != nil {
		u.opts.OnAvailable(notify)
	}
	if apply {
		u.install(ctx, true)
	}
}

// Install downloads the version found, as the person asked. It returns at
// once; the state says how it went. Once it is installed, the app restarts
// into it when no task runs.
func (u *Updater) Install() {
	go u.install(context.Background(), false)
}

// install downloads the release found; automatic is one the app installs
// by itself, which then restarts the app only while nobody uses it.
func (u *Updater) install(ctx context.Context, automatic bool) {
	u.mu.Lock()
	r := u.release
	if r == nil || u.busy || (u.state.Phase != PhaseAvailable && u.state.Phase != PhaseInstallFailed) {
		u.mu.Unlock()
		return
	}
	u.busy = true
	u.state.Phase, u.state.Err, u.state.Downloaded, u.state.Total = PhaseDownloading, nil, 0, 0
	u.mu.Unlock()
	u.changed()

	err := r.Install(ctx, func(downloaded, total int64) {
		u.mu.Lock()
		u.state.Downloaded, u.state.Total = downloaded, total
		u.mu.Unlock()
		u.changed()
	})

	u.mu.Lock()
	u.busy = false
	if err != nil {
		u.state.Phase, u.state.Err = PhaseInstallFailed, err
		u.mu.Unlock()
		slog.Warn("install the update", "version", r.Version(), "err", err)
		u.changed()
		return
	}
	u.state.Phase = PhaseReady
	// The person asked for it: restart as soon as the runs allow. One the
	// app installed by itself waits, besides, for nobody to use the window.
	u.state.Waiting, u.quietRestart = !automatic, automatic
	u.mu.Unlock()
	slog.Info("update installed", "version", r.Version())
	u.changed()
	u.RunsChanged()
}

// Restart restarts the app into the version installed once no task runs:
// at once when none does. A quiet restart becomes one the person asked for.
func (u *Updater) Restart() {
	u.mu.Lock()
	if u.state.Phase != PhaseReady {
		u.mu.Unlock()
		return
	}
	u.state.Waiting, u.quietRestart = true, false
	u.mu.Unlock()
	u.changed()
	u.RunsChanged()
}

// CancelRestart stops waiting to restart: the version installed runs the
// next time the app opens.
func (u *Updater) CancelRestart() {
	u.mu.Lock()
	if !u.state.Waiting {
		u.mu.Unlock()
		return
	}
	u.state.Waiting = false
	u.mu.Unlock()
	u.changed()
}

// RunsChanged restarts the app when it waits to and no task runs any
// more. Call it whenever a run starts or ends, and when the window hides.
func (u *Updater) RunsChanged() {
	u.mu.Lock()
	waiting, quiet := u.state.Waiting, u.quietRestart
	u.mu.Unlock()
	if !waiting && !quiet {
		return
	}
	// Asked outside the lock: they read the service and the window.
	if u.opts.Runs() > 0 || (!waiting && !u.opts.Quiet()) {
		if waiting {
			u.changed() // the count of runs shown changed
		}
		return
	}
	u.mu.Lock()
	if u.relaunched {
		u.mu.Unlock()
		return
	}
	u.relaunched = true
	u.mu.Unlock()
	slog.Info("restart into the update")
	u.opts.Relaunch()
}

func (u *Updater) changed() {
	if u.opts.OnChange != nil {
		u.opts.OnChange()
	}
}

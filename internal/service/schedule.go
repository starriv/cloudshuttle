package service

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine"
	"github.com/starriv/cloudshuttle/internal/store"
)

// How scheduled runs happen
//
// The window registers the schedules with the system (Registrar): on
// macOS, launchd starts the app's scheduler, a process of the app without
// a window, at the times the schedules need and as the user logs in. The
// scheduler runs, one after another, the tasks that are due, including
// those the computer missed while it was off or asleep, and quits
// (RunDue). Whether the window is open, closed, quit or stuck changes
// nothing.
//
// Both processes run tasks on one database. A run records the process
// that runs it (its owner); the database starts a task once however many
// processes try (store.StartRun), and the processes take turns at
// transfers with a file lock. Each holds a lock of its own while it lives,
// which tells the other whether the runs it recorded are still going.
// The scheduler keeps the progress and the log of its runs in the
// database; the window watches the database and shows them, stops them
// on request, and tells the person of their failures.
//
// The window registers nothing before the person allows it (Settings
// .Background). Until then, and where the system has no scheduler for the
// app or the person turned it off there, the window runs the schedules
// itself while it is open.

// The processes of the app that run tasks, as the runs they record say.
const (
	OwnerApp       = "app"
	OwnerScheduler = "scheduler"
)

// Registrar has the system start the scheduler at wakes, and as the user
// logs in; no wakes stops it. It fails with errors.ErrUnsupported where
// the system has no scheduler for the app.
type Registrar interface {
	Register(wakes []domain.Wake) error
}

// Background is how the tasks' schedules run.
type Background int

const (
	// BackgroundNone is no task on a schedule.
	BackgroundNone Background = iota
	// BackgroundSystem is the system running them, whether the app is open
	// or not.
	BackgroundSystem
	// BackgroundApp is the app running them while it is open, its window
	// closed or not: the system has no scheduler for it.
	BackgroundApp
	// BackgroundBlocked is the app running them while it is open, because
	// the system refused the scheduler, as when the person did not allow
	// the app in the background.
	BackgroundBlocked
	// BackgroundOff is the app running them while it is open, because the
	// person did not let the system run them, or was not asked yet.
	BackgroundOff
)

// Intervals of the processes' work; tests shorten them.
var (
	// watchEvery is how often the window looks for the scheduler's
	// changes, and publishEvery how often the scheduler writes them.
	watchEvery   = time.Second
	publishEvery = time.Second
	// tickEvery is how often the window runs the due tasks itself, where
	// the system does not.
	tickEvery = 30 * time.Second
	// lockPoll is how often a run waiting for the other process's
	// transfer tries again.
	lockPoll = time.Second
	// retryBlocked is how often the window registers the schedules again
	// while the system refuses them: the person may have allowed the app
	// in the background since.
	retryBlocked = time.Minute
)

// dueAhead is how far ahead the scheduler waits for a task due soon
// rather than quit: the system may start it a little early.
const dueAhead = 90 * time.Second

// notifyWithin is how old a failure the window tells about can be: one
// from days ago, found as the app opens, is history.
const notifyWithin = 24 * time.Hour

// Background reports how the schedules run, and why the system refused
// the scheduler when it did.
func (s *Service) Background() (Background, error) {
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	return s.background, s.bgErr
}

// due is a task whose scheduled time has come.
type due struct {
	task domain.Task
	slot time.Time // the time it was due
}

// dueTasks returns the tasks due at now, the earliest first, and when the
// next one is due after now (zero for none). A task is due when the first
// time of its schedule after its last run has come: a computer that was
// off at several of them runs it once.
func (s *Service) dueTasks(ctx context.Context, now time.Time) ([]due, time.Time, error) {
	tasks, err := s.store.Tasks(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	starts, err := s.store.LastStarts(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	var out []due
	var soonest time.Time
	for _, t := range tasks {
		if !t.Schedule.On() {
			continue
		}
		since := starts[t.ID]
		if t.ScheduleSince.After(since) {
			since = t.ScheduleSince
		}
		if since.IsZero() {
			since = now
		}
		next := t.Schedule.Next(since.In(now.Location()))
		switch {
		case next.IsZero():
		case !next.After(now):
			out = append(out, due{t, next})
		case soonest.IsZero() || next.Before(soonest):
			soonest = next
		}
	}
	slices.SortStableFunc(out, func(a, b due) int { return a.slot.Compare(b.slot) })
	return out, soonest, nil
}

// RunDue is the scheduler's work: it runs the tasks that are due, one after
// another, and returns once none is, nor will be within a little while. A
// task the window runs at the time is left to it; another scheduler
// already at work leaves this one nothing to do.
func (s *Service) RunDue(ctx context.Context) error {
	if !s.lockOwn(ctx) {
		slog.Info("another scheduler is running")
		return nil
	}
	s.cancelOrphans(ctx, true)
	for ctx.Err() == nil {
		list, soonest, err := s.dueTasks(ctx, s.now())
		if err != nil {
			return err
		}
		started := 0
		for _, d := range list {
			a, err := s.start(ctx, d.task.ID, RunOptions{Trigger: domain.TriggerScheduled}, d.slot)
			if err != nil {
				s.notStarted(d.task, err)
				continue
			}
			started++
			select {
			case <-a.done:
			case <-ctx.Done():
				a.cancel()
				select {
				case <-a.done:
				case <-time.After(closeWait):
				}
				return ctx.Err()
			}
		}
		if started > 0 {
			continue // time went by: more may be due
		}
		wait := soonest.Sub(s.now())
		if soonest.IsZero() || wait > dueAhead {
			return nil
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
	return ctx.Err()
}

// startDue starts the tasks that are due, in the window, where the system
// does not run the scheduler: they wait their turn in the window's queue.
func (s *Service) startDue(ctx context.Context) {
	list, _, err := s.dueTasks(ctx, s.now())
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("find the tasks that are due", "err", err)
		}
		return
	}
	for _, d := range list {
		if _, err := s.start(ctx, d.task.ID, RunOptions{Trigger: domain.TriggerScheduled}, d.slot); err != nil {
			s.notStarted(d.task, err)
		}
	}
}

// notStarted handles a scheduled run that did not start. The task running
// already, or run since it was due, is how it should be; a destination
// another task is writing to is told to the person, not just logged.
func (s *Service) notStarted(t domain.Task, err error) {
	switch {
	case errors.Is(err, domain.ErrAlreadyActive), errors.Is(err, store.ErrSlotTaken), errors.Is(err, context.Canceled):
	default:
		slog.Error("scheduled run", "task", t.Name, "err", err)
		if errors.Is(err, domain.ErrDestinationBusy) {
			s.onRefused(t, err)
		}
	}
}

// requestSync has the window register the schedules with the system, from
// its watch: registering runs the system's tools, which the main thread
// must not wait for.
func (s *Service) requestSync() {
	if s.owner != OwnerApp {
		return
	}
	s.bgMu.Lock()
	s.syncPending = true
	s.bgMu.Unlock()
	select {
	case s.syncNow <- struct{}{}:
	default:
	}
}

// syncSchedules registers the times the schedules need with the system.
// While the scheduler runs, it waits: the system would stop the scheduler
// to load its new times, and the run with it.
func (s *Service) syncSchedules(ctx context.Context) {
	s.bgMu.Lock()
	pending := s.syncPending
	s.bgMu.Unlock()
	if !pending || s.alive(OwnerScheduler) {
		return
	}
	tasks, err := s.store.Tasks(ctx)
	if err != nil {
		slog.Warn("read the schedules", "err", err)
		return
	}
	wakes := scheduleWakes(tasks)
	status, problem := BackgroundNone, error(nil)
	switch {
	case s.registrar == nil && len(wakes) > 0:
		status = BackgroundApp
	case s.registrar == nil:
	default:
		allowed := s.Settings().Background == domain.ConsentGiven
		register := wakes
		if !allowed {
			register = nil // take back what was allowed before
		}
		err := s.registrar.Register(register)
		switch {
		case !allowed:
			if err != nil && !errors.Is(err, errors.ErrUnsupported) {
				slog.Warn("remove the scheduler from the system", "err", err)
			}
			if len(wakes) > 0 {
				status = BackgroundOff
			}
		case len(wakes) == 0:
			if err != nil && !errors.Is(err, errors.ErrUnsupported) {
				slog.Warn("remove the scheduler from the system", "err", err)
			}
		case err == nil:
			status = BackgroundSystem
		case errors.Is(err, errors.ErrUnsupported):
			status = BackgroundApp
		default:
			slog.Warn("register the scheduler with the system", "err", err)
			status, problem = BackgroundBlocked, err
		}
	}
	s.bgMu.Lock()
	changed := s.background != status || (s.bgErr == nil) != (problem == nil)
	s.background, s.bgErr, s.syncPending = status, problem, false
	s.bgMu.Unlock()
	if changed {
		s.onChange()
	}
}

// scheduleWakes returns the times the system must start the scheduler at,
// each once.
func scheduleWakes(tasks []domain.Task) []domain.Wake {
	var out []domain.Wake
	for _, t := range tasks {
		for _, w := range t.Schedule.Wakes() {
			if !slices.Contains(out, w) {
				out = append(out, w)
			}
		}
	}
	slices.SortFunc(out, func(a, b domain.Wake) int {
		return cmp.Or(cmp.Compare(a.Day, b.Day), cmp.Compare(a.Weekday, b.Weekday),
			cmp.Compare(a.Hour, b.Hour), cmp.Compare(a.Minute, b.Minute))
	})
	return out
}

// runsSchedules reports whether the window runs the schedules itself.
func (s *Service) runsSchedules() bool {
	b, _ := s.Background()
	return b == BackgroundApp || b == BackgroundBlocked || b == BackgroundOff
}

// SystemScheduler reports whether the system can run the schedules while
// the app is closed, once the person allows it: what the app asks them.
func (s *Service) SystemScheduler() bool {
	return s.registrar != nil
}

// AllowBackground keeps the person's answer to whether the system may
// start the app for its schedules, and registers them, or takes them back,
// accordingly.
func (s *Service) AllowBackground(ctx context.Context, allow bool) error {
	st := s.Settings()
	st.Background = domain.ConsentRefused
	if allow {
		st.Background = domain.ConsentGiven
	}
	_, err := s.SaveSettings(ctx, st)
	return err
}

// watch is the window's loop: it shows the scheduler's runs as they
// change, tells their failures, registers the schedules, and runs them
// itself where the system does not.
func (s *Service) watch(ctx context.Context) {
	tick := time.NewTicker(watchEvery)
	defer tick.Stop()
	version := int64(-1)
	lastDue, lastRetry := time.Time{}, s.now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.syncNow:
		}
		changed := false
		if v, err := s.store.DataVersion(ctx); err == nil && v != version {
			version, changed = v, true
		}
		if s.hasForeign() && !s.alive(OwnerScheduler) {
			// The scheduler quit or crashed with runs in progress.
			s.cancelOrphans(ctx, false)
			changed = true
		}
		if changed || version < 0 {
			s.refreshForeign(ctx)
			s.tellFailures(ctx)
		}
		if b, _ := s.Background(); b == BackgroundBlocked && s.now().Sub(lastRetry) >= retryBlocked {
			lastRetry = s.now()
			s.requestSync()
		}
		s.syncSchedules(ctx)
		if s.runsSchedules() && s.now().Sub(lastDue) >= tickEvery {
			lastDue = s.now()
			s.startDue(ctx)
		}
	}
}

func (s *Service) hasForeign() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.foreign) > 0
}

// refreshForeign reads the scheduler's runs in progress, and tells the
// window what changed: a run that came or went, or only progress.
func (s *Service) refreshForeign(ctx context.Context) {
	live, err := s.store.LiveRuns(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("read the scheduler's runs", "err", err)
		}
		return
	}
	next := map[string]Active{}
	for _, l := range live {
		if l.Owner != s.owner && l.Owner != "" {
			next[l.Run.TaskID] = Active{Run: l.Run, Progress: engine.Progress(l.Run.Stats), Queued: l.Queued}
		}
	}
	s.mu.Lock()
	prev := s.foreign
	s.foreign = next
	s.mu.Unlock()

	shape, progress := len(prev) != len(next), false
	for id, n := range next {
		p, ok := prev[id]
		switch {
		case !ok || p.Run.ID != n.Run.ID || p.Queued != n.Queued:
			shape = true
		case p.Progress != n.Progress:
			progress = true
		}
	}
	var ended []domain.Run
	for id, p := range prev {
		if n, ok := next[id]; ok && n.Run.ID == p.Run.ID {
			continue
		}
		shape = true
		if r, err := s.store.Run(ctx, p.Run.ID); err == nil && r.Status != domain.StatusRunning {
			ended = append(ended, r)
		}
	}
	switch {
	case shape:
		s.onChange()
	case progress:
		s.onProgress()
	}
	for _, r := range ended {
		s.onDone(r)
	}
}

// tellFailures tells the failures of the scheduler's runs, once each.
func (s *Service) tellFailures(ctx context.Context) {
	runs, err := s.store.TakeNotifications(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("read the failures to tell", "err", err)
		}
		return
	}
	for _, r := range runs {
		if s.now().Sub(r.FinishedAt) <= notifyWithin {
			s.onNotify(r)
		}
	}
}

// publish keeps a run of the scheduler's in the database as it goes, for
// the window to show, and stops it when the window asks. It returns the
// function that stops publishing, which waits for the last write: the
// run's outcome is recorded after it, not under it.
func (s *Service) publish(a *activeRun) (stop func()) {
	if s.owner == OwnerApp {
		return func() {}
	}
	quit := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx := context.WithoutCancel(s.ctx)
		tick := time.NewTicker(publishEvery)
		defer tick.Stop()
		lines, n := 0, 0
		for {
			select {
			case <-quit:
				return
			case <-tick.C:
			}
			s.mu.Lock()
			stats, queued := domain.Stats(a.Progress), a.queued
			s.mu.Unlock()
			if cancel, err := s.store.UpdateProgress(ctx, a.Run.ID, stats, queued); err != nil {
				slog.Warn("publish a run's progress", "run", a.Run.ID, "err", err)
			} else if cancel {
				a.cancel()
			}
			if n++; n%2 == 0 {
				if text, l := a.log.Live(); l != lines {
					lines = l
					if err := s.store.PutRunLog(ctx, a.Run.ID, text); err != nil {
						slog.Warn("publish a run's log", "run", a.Run.ID, "err", err)
					}
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(quit)
			wg.Wait()
		})
	}
}

// foreignLog returns the log so far of the scheduler's run with id, and
// whether it is one, for the window to show it as it goes.
func (s *Service) foreignLog(ctx context.Context, id string) (string, int, bool) {
	s.mu.Lock()
	found := false
	for _, a := range s.foreign {
		found = found || a.Run.ID == id
	}
	s.mu.Unlock()
	if !found {
		return "", 0, false
	}
	text, err := s.store.RunLog(ctx, id)
	if err != nil {
		return "", 0, true // not written yet
	}
	return text, strings.Count(text, "\n"), true
}

// lockPath is the file of a lock the processes share, or "" where the
// service keeps no files, as in tests: there is no other process then.
func (s *Service) lockPath(name string) string {
	if s.dataDir == "" {
		return ""
	}
	return filepath.Join(s.dataDir, name+".lock")
}

// lockOwn takes this process's lock, and reports false when another
// process of its kind holds it. The other process may be holding it a
// moment to see whether this one is alive: it tries for a little while.
func (s *Service) lockOwn(ctx context.Context) bool {
	path := s.lockPath(s.owner)
	if path == "" {
		return true
	}
	for i := 0; ; i++ {
		l, ok, err := tryLock(path)
		if err != nil {
			slog.Warn("take the process's lock", "err", err)
			return false
		}
		if ok {
			s.ownLock = l
			return true
		}
		if i == 20 {
			return false
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return false
		}
	}
}

// alive reports whether the process owner is running.
func (s *Service) alive(owner string) bool {
	if owner == s.owner {
		return true
	}
	path := s.lockPath(owner)
	if path == "" {
		return false
	}
	l, ok, err := tryLock(path)
	if err != nil {
		return true // unknown is not proof of death
	}
	if ok {
		l.unlock()
		return false
	}
	return true
}

// cancelOrphans ends the runs recorded by processes that are no more: this
// one's kind before it started, the other when it is not running, and runs
// from before runs had owners.
func (s *Service) cancelOrphans(ctx context.Context, startup bool) {
	unlock, err := s.lockMaintenance(ctx)
	if err != nil {
		slog.Warn("lock orphan cleanup", "err", err)
		return
	}
	defer unlock()
	owners := []string{}
	if startup {
		owners = append(owners, "", s.owner)
	}
	other := OwnerScheduler
	if s.owner == OwnerScheduler {
		other = OwnerApp
	}
	// Keep the probe locked through the update: a new owner cannot start
	// between our death check and marking its old records canceled.
	if path := s.lockPath(other); path != "" {
		l, ok, err := tryLock(path)
		if err != nil {
			slog.Warn("check process ownership", "err", err)
			return
		}
		if ok {
			defer l.unlock()
			owners = append(owners, other)
		}
	}
	n, err := s.store.CancelOrphanRuns(ctx, owners...)
	if err != nil {
		slog.Warn("end the runs left running", "err", err)
		return
	}
	if n > 0 {
		slog.Info("ended runs left running", "count", n)
		s.onChange()
	}
}

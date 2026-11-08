package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/store"
)

func TestMain(m *testing.M) {
	watchEvery, publishEvery, lockPoll, tickEvery = 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond
	os.Exit(m.Run())
}

// fakeRegistrar stands for the system's scheduler.
type fakeRegistrar struct {
	mu    sync.Mutex
	wakes [][]domain.Wake
	err   error
}

func (r *fakeRegistrar) Register(wakes []domain.Wake) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wakes = append(r.wakes, wakes)
	return r.err
}

func (r *fakeRegistrar) last() ([]domain.Wake, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.wakes) == 0 {
		return nil, 0
	}
	return r.wakes[len(r.wakes)-1], len(r.wakes)
}

// eventually fails the test unless ok becomes true within a few seconds.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// process is one process of the app on a shared database and data folder.
type process struct {
	*Service
	finished chan domain.Run
	told     chan domain.Run
}

func openProcess(t *testing.T, dir string, opts Options) *process {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "cloudshuttle.db"))
	if err != nil {
		t.Fatal(err)
	}
	p := &process{finished: make(chan domain.Run, 8), told: make(chan domain.Run, 8)}
	opts.Store, opts.DataDir = st, dir
	if opts.Registrar != nil {
		// The person allowed the system to run the schedules, as a test of
		// the system's scheduler needs; TestNothingIsRegisteredUnasked
		// starts without it.
		allowBackground(t, st, domain.ConsentGiven)
	}
	if opts.Secrets == nil {
		opts.Secrets = &memSecrets{m: map[string]string{}}
	}
	opts.OnRunFinished = func(r domain.Run) { p.finished <- r }
	opts.OnNotify = func(r domain.Run) { p.told <- r }
	p.Service = New(opts)
	t.Cleanup(func() {
		p.Close()
		st.Close()
	})
	return p
}

func allowBackground(t *testing.T, st *store.Store, c domain.Consent) {
	t.Helper()
	settings, err := st.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.Background = c
	if err := st.PutSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
}

func TestNothingIsRegisteredUnasked(t *testing.T) {
	ctx := context.Background()
	reg := &fakeRegistrar{}
	app := openProcess(t, t.TempDir(), Options{Registrar: reg})
	allowBackground(t, app.store, domain.ConsentUnasked)
	app.settings.Background = domain.ConsentUnasked
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !app.SystemScheduler() {
		t.Fatal("the system's scheduler is not offered")
	}
	if _, err := app.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Nightly", Source: domain.Location{Path: "/a"},
		Destination: domain.Location{Path: "/b"}, Mode: domain.ModeCopy,
		Schedule: domain.Schedule{Repeat: domain.RepeatDaily, Hour: 2}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the schedules run by the app", func() bool { b, _ := app.Background(); return b == BackgroundOff })
	if w, _ := reg.last(); len(w) != 0 {
		t.Fatalf("registered %v before the person allowed it", w)
	}

	if err := app.AllowBackground(ctx, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the schedule registered", func() bool { w, _ := reg.last(); return len(w) == 1 })
	if b, _ := app.Background(); b != BackgroundSystem {
		t.Fatalf("background = %v, want the system's", b)
	}

	if err := app.AllowBackground(ctx, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the schedule taken back", func() bool {
		w, _ := reg.last()
		b, _ := app.Background()
		return len(w) == 0 && b == BackgroundOff
	})
	if got := app.Settings().Background; got != domain.ConsentRefused {
		t.Fatalf("consent = %v, want refused", got)
	}
}

func TestScheduleIsRegisteredWithTheSystem(t *testing.T) {
	ctx := context.Background()
	reg := &fakeRegistrar{}
	app := openProcess(t, t.TempDir(), Options{Registrar: reg})
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first registration", func() bool { _, n := reg.last(); return n == 1 })
	if b, _ := app.Background(); b != BackgroundNone {
		t.Fatalf("background = %v with no schedule", b)
	}

	_, err := app.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Bad", Source: domain.Location{Path: "/a"},
		Destination: domain.Location{Path: "/b"}, Mode: domain.ModeCopy,
		Schedule: domain.Schedule{Repeat: domain.RepeatWeekly, Hour: 9}})
	if domain.AsProblems(err).For(domain.FieldSchedule) != domain.CodeNoDays {
		t.Fatalf("err = %v, want a weekly schedule without days refused", err)
	}

	task, err := app.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Evenings", Source: domain.Location{Path: "/a"},
		Destination: domain.Location{Path: "/b"}, Mode: domain.ModeCopy,
		Schedule: domain.Schedule{Repeat: domain.RepeatWeekly, Days: domain.WeekdaysOf(time.Monday, time.Friday), Hour: 21}})
	if err != nil {
		t.Fatal(err)
	}
	if next := app.NextRun(task); next.Hour() != 21 || (next.Weekday() != time.Monday && next.Weekday() != time.Friday) {
		t.Fatalf("next run = %v", next)
	}
	want := []domain.Wake{{Day: -1, Weekday: 1, Hour: 21}, {Day: -1, Weekday: 5, Hour: 21}}
	eventually(t, "the schedule registered", func() bool { w, _ := reg.last(); return slices.Equal(w, want) })
	if b, err := app.Background(); b != BackgroundSystem || err != nil {
		t.Fatalf("background = %v, %v; want the system's", b, err)
	}

	if _, err := app.UpdateTask(ctx, task.ID, domain.TaskInput{AllowRoot: true, Name: task.Name, Source: task.Source,
		Destination: task.Destination, Mode: task.Mode}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the scheduler removed", func() bool { w, n := reg.last(); return n >= 3 && len(w) == 0 })
	if b, _ := app.Background(); b != BackgroundNone {
		t.Fatalf("background = %v, want none", b)
	}
}

func TestARefusedRegistrationRunsSchedulesInTheApp(t *testing.T) {
	ctx := context.Background()
	reg := &fakeRegistrar{err: errors.New("Bootstrap failed: 5: Input/output error")}
	app := openProcess(t, t.TempDir(), Options{Registrar: reg})
	task, err := app.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Nightly", Mode: domain.ModeCopy,
		Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: t.TempDir()},
		Schedule: domain.Schedule{Repeat: domain.RepeatDaily, Hour: 1}})
	if err != nil {
		t.Fatal(err)
	}
	// It was due two days ago, before the app started.
	task.ScheduleSince = time.Now().AddDate(0, 0, -2)
	if err := app.store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	run := wait(t, app.finished)
	if run.Trigger != domain.TriggerScheduled || run.TaskID != task.ID || run.Status != domain.StatusSuccess {
		t.Fatalf("run = %+v, want the missed run, by the app", run)
	}
	if b, err := app.Background(); b != BackgroundBlocked || err == nil {
		t.Fatalf("background = %v, %v; want blocked, with why", b, err)
	}
	// Once only: the run took the time it was due.
	select {
	case again := <-app.finished:
		t.Fatalf("ran again: %+v", again)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDueTasks(t *testing.T) {
	ctx := context.Background()
	s := openProcess(t, t.TempDir(), Options{})            // not started: it runs nothing itself
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local) // a Tuesday
	put := func(name string, sched domain.Schedule, since time.Time, lastRun time.Time) domain.Task {
		t.Helper()
		task := domain.Task{ID: domain.NewID(), Name: name, Mode: domain.ModeCopy, Schedule: sched,
			Source: domain.Location{Path: "/" + name}, Destination: domain.Location{Path: "/" + name + "-copy"},
			ScheduleSince: since, CreatedAt: since, UpdatedAt: since}
		if err := s.store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if !lastRun.IsZero() {
			r := domain.Run{ID: domain.NewID(), TaskID: task.ID, TaskName: name, Mode: domain.ModeCopy,
				Trigger: domain.TriggerScheduled, Status: domain.StatusSuccess, StartedAt: lastRun, FinishedAt: lastRun}
			if err := s.store.InsertRun(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		return task
	}
	daily := domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3}
	weekAgo := now.AddDate(0, 0, -7)
	missed := put("missed", daily, weekAgo, now.AddDate(0, 0, -3)) // off for three days: due once
	put("ran", daily, weekAgo, now.Add(-6*time.Hour))              // ran at 04:00 today
	put("new", daily, now.Add(-time.Hour), time.Time{})            // set at 09:00: 03:00 had passed
	put("manual", domain.Schedule{}, weekAgo, time.Time{})
	never := put("never ran", domain.Schedule{Repeat: domain.RepeatHourly, Every: 1}, now.Add(-90*time.Minute), time.Time{})
	put("later", domain.Schedule{Repeat: domain.RepeatDaily, Hour: 10, Minute: 30}, weekAgo, now.Add(-20*time.Hour))

	list, soonest, err := s.dueTasks(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range list {
		names = append(names, d.task.Name)
	}
	if !slices.Equal(names, []string{missed.Name, never.Name}) {
		t.Fatalf("due = %v, want %v then %v", names, missed.Name, never.Name)
	}
	if want := time.Date(2026, 11, 1, 3, 0, 0, 0, time.Local); !list[0].slot.Equal(want) {
		t.Errorf("slot = %v, want the first time missed, %v", list[0].slot, want)
	}
	if want := now.Add(30 * time.Minute); !soonest.Equal(want) {
		t.Errorf("soonest = %v, want %v", soonest, want)
	}
}

// TestTheSchedulerRunsBesideTheWindow runs a task in the scheduler, as the
// system starts it, while the window shows the run, stops it, and is told
// of the failure of the next.
func TestTheSchedulerRunsBesideTheWindow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// The system runs the schedules: the window leaves them to it.
	app := openProcess(t, dir, Options{Registrar: &fakeRegistrar{}})
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	task, err := app.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Nightly", Mode: domain.ModeCopy,
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: t.TempDir()},
		Schedule: domain.Schedule{Repeat: domain.RepeatDaily, Hour: 2}})
	if err != nil {
		t.Fatal(err)
	}
	task.ScheduleSince = time.Now().AddDate(0, 0, -2)
	if err := app.store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	hold := make(chan struct{})
	old := beforeRun
	beforeRun = func() { <-hold }
	t.Cleanup(func() { beforeRun = old })

	woken := make(chan struct{}, 1)
	scheduler := openProcess(t, dir, Options{Owner: OwnerScheduler, WakeApp: func() { woken <- struct{}{} }})
	result := make(chan error, 1)
	go func() { result <- scheduler.RunDue(ctx) }()

	eventually(t, "the window to show the scheduler's run", func() bool {
		active := app.Active()
		return len(active) == 1 && active[0].Run.TaskID == task.ID && !active[0].Queued
	})
	if _, err := app.Run(ctx, task.ID, RunOptions{}); !errors.Is(err, domain.ErrAlreadyActive) {
		t.Fatalf("running it by hand meanwhile: %v, want ErrAlreadyActive", err)
	}
	if err := app.DeleteTask(ctx, task.ID); !errors.Is(err, domain.ErrAlreadyActive) {
		t.Fatalf("deleting it meanwhile: %v, want ErrAlreadyActive", err)
	}
	if err := app.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	// The scheduler reads the request with its next progress, which it
	// publishes while the transfer is held.
	eventually(t, "the run to be asked to stop", func() bool {
		scheduler.mu.Lock()
		defer scheduler.mu.Unlock()
		a, ok := scheduler.active[task.ID]
		return ok && a.ctx.Err() != nil
	})
	close(hold)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if run := wait(t, scheduler.finished); run.Status != domain.StatusCanceled {
		t.Fatalf("the scheduler's run = %+v, want canceled", run)
	}
	if run := wait(t, app.finished); run.Status != domain.StatusCanceled || run.Trigger != domain.TriggerScheduled {
		t.Fatalf("the window saw = %+v, want the canceled scheduled run", run)
	}
	eventually(t, "the run to leave the window", func() bool { return len(app.Active()) == 0 })

	// A failure: the source is gone. The window tells it, and the
	// scheduler does not wake an app that is running.
	beforeRun = old
	gone := domain.Task{ID: domain.NewID(), Name: "Gone", Mode: domain.ModeCopy,
		Source: domain.Location{Path: filepath.Join(dir, "missing")}, Destination: domain.Location{Path: t.TempDir()},
		Schedule:      domain.Schedule{Repeat: domain.RepeatDaily, Hour: 1},
		ScheduleSince: time.Now().AddDate(0, 0, -2), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := app.store.PutTask(ctx, gone); err != nil {
		t.Fatal(err)
	}
	scheduler.Close() // its process quits as RunDue returns
	again := openProcess(t, dir, Options{Owner: OwnerScheduler, WakeApp: func() { woken <- struct{}{} }})
	if err := again.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, app.told); run.TaskID != gone.ID || run.Status != domain.StatusFailed {
		t.Fatalf("told = %+v, want the failed run", run)
	}
	select {
	case <-woken:
		t.Fatal("woke the app that was running")
	default:
	}
}

// TestAFailureWakesTheApp has the scheduler open the app when no window is
// there to tell a failure, which the window then tells as it starts.
func TestAFailureWakesTheApp(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	woken := make(chan struct{}, 1)
	scheduler := openProcess(t, dir, Options{Owner: OwnerScheduler, WakeApp: func() { woken <- struct{}{} }})
	task := domain.Task{ID: domain.NewID(), Name: "Gone", Mode: domain.ModeCopy,
		Source: domain.Location{Path: filepath.Join(dir, "missing")}, Destination: domain.Location{Path: t.TempDir()},
		Schedule:      domain.Schedule{Repeat: domain.RepeatDaily, Hour: 1},
		ScheduleSince: time.Now().AddDate(0, 0, -2), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := scheduler.store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, scheduler.finished); run.Status != domain.StatusFailed {
		t.Fatalf("run = %+v, want a failure", run)
	}
	select {
	case <-woken:
	case <-time.After(time.Second):
		t.Fatal("the app was not woken")
	}
	app := openProcess(t, dir, Options{Registrar: &fakeRegistrar{}})
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, app.told); run.TaskID != task.ID {
		t.Fatalf("told = %+v", run)
	}
}

func TestOneSchedulerAtATime(t *testing.T) {
	dir := t.TempDir()
	first := openProcess(t, dir, Options{Owner: OwnerScheduler})
	if !first.lockOwn(context.Background()) {
		t.Fatal("the first scheduler did not get its lock")
	}
	second := openProcess(t, dir, Options{Owner: OwnerScheduler})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if second.lockOwn(ctx) {
		t.Fatal("two schedulers at once")
	}
	if !second.alive(OwnerScheduler) || second.alive(OwnerApp) {
		t.Fatal("alive is wrong")
	}
}

func TestAScheduledRunThatCannotStartIsTold(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	var told domain.Task
	var toldErr error
	s.onRefused = func(task domain.Task, err error) { told, toldErr = task, err }

	dst := t.TempDir()
	a, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "A", Mode: domain.ModeSync,
		Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: dst}})
	if err != nil {
		t.Fatal(err)
	}
	b := domain.Task{ID: domain.NewID(), Name: "B", Mode: domain.ModeCopy,
		Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: dst},
		Schedule:      domain.Schedule{Repeat: domain.RepeatDaily, Hour: 1},
		ScheduleSince: time.Now().AddDate(0, 0, -2), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.store.PutTask(ctx, b); err != nil {
		t.Fatal(err)
	}

	// As if A were running.
	s.mu.Lock()
	s.active[a.ID] = &activeRun{
		Active: Active{Run: domain.Run{ID: "a-run", TaskID: a.ID}},
		dest:   a.Destination, mode: a.Mode, log: &runLog{},
	}
	s.mu.Unlock()

	s.startDue(ctx)
	if told.ID != b.ID || !errors.Is(toldErr, domain.ErrDestinationBusy) {
		t.Fatalf("told = %+v, %v; want B and ErrDestinationBusy", told, toldErr)
	}

	// A task already running is expected, and not announced.
	told = domain.Task{}
	s.notStarted(a, domain.ErrAlreadyActive)
	s.notStarted(a, store.ErrSlotTaken)
	if told.ID != "" {
		t.Fatalf("told about a run that is fine: %+v", told)
	}
}

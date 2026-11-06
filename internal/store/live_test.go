package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
)

func TestStartRunStartsATaskOnce(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	slot := time.Now().Add(-time.Minute)
	run := func(id string) domain.Run {
		return domain.Run{ID: id, TaskID: "t", TaskName: "T", Mode: domain.ModeCopy,
			Trigger: domain.TriggerScheduled, Status: domain.StatusRunning, StartedAt: time.Now()}
	}
	if err := s.StartRun(ctx, run("a"), "scheduler", true, slot); err != nil {
		t.Fatal(err)
	}
	// The window's in-app scheduler is late to the same time.
	if err := s.StartRun(ctx, run("b"), "app", true, slot); !errors.Is(err, domain.ErrAlreadyActive) {
		t.Fatalf("a second start while the first runs: %v, want ErrAlreadyActive", err)
	}
	a := run("a")
	a.Status, a.FinishedAt = domain.StatusSuccess, time.Now()
	if err := s.FinishRun(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRun(ctx, run("c"), "app", true, slot); !errors.Is(err, ErrSlotTaken) {
		t.Fatalf("a start for a time already run: %v, want ErrSlotTaken", err)
	}
	// A person can still run it by hand.
	manual := run("d")
	manual.Trigger = domain.TriggerManual
	if err := s.StartRun(ctx, manual, "app", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	starts, err := s.LastStarts(ctx)
	if err != nil || !starts["t"].Equal(time.UnixMilli(manual.StartedAt.UnixMilli())) {
		t.Fatalf("last starts = %v, %v", starts, err)
	}
}

func TestProgressAndCancelAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shared.db")
	scheduler, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.Close()
	window, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer window.Close()

	v0, err := window.DataVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "r", TaskID: "t", TaskName: "T", Mode: domain.ModeSync,
		Trigger: domain.TriggerScheduled, Status: domain.StatusRunning, StartedAt: time.Now()}
	if err := scheduler.StartRun(ctx, run, "scheduler", true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if cancel, err := scheduler.UpdateProgress(ctx, "r", domain.Stats{Bytes: 10, TotalBytes: 40, Speed: 2.5}, false); err != nil || cancel {
		t.Fatalf("update = %v, %v", cancel, err)
	}
	if v, _ := window.DataVersion(ctx); v == v0 {
		t.Fatal("the window's data version did not change as the scheduler wrote")
	}
	live, err := window.LiveRuns(ctx)
	if err != nil || len(live) != 1 {
		t.Fatalf("live = %+v, %v", live, err)
	}
	if got := live[0]; got.Queued || got.Run.Stats.Bytes != 10 || got.Run.Stats.Speed != 2.5 {
		t.Fatalf("live run = %+v", got)
	}

	if err := window.RequestCancel(ctx, "r"); err != nil {
		t.Fatal(err)
	}
	if cancel, err := scheduler.UpdateProgress(ctx, "r", domain.Stats{Bytes: 11}, false); err != nil || !cancel {
		t.Fatalf("the scheduler was not told to stop: %v, %v", cancel, err)
	}

	run.Status, run.FinishedAt = domain.StatusFailed, time.Now()
	if err := scheduler.MarkNotify(ctx, "r"); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.FinishRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	told, err := window.TakeNotifications(ctx)
	if err != nil || len(told) != 1 || told[0].ID != "r" || told[0].Status != domain.StatusFailed {
		t.Fatalf("notifications = %+v, %v", told, err)
	}
	if again, err := window.TakeNotifications(ctx); err != nil || len(again) != 0 {
		t.Fatalf("told twice: %+v, %v", again, err)
	}
	if _, err := scheduler.UpdateProgress(ctx, "r", domain.Stats{}, false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("progress of a finished run: %v, want ErrNotFound", err)
	}
}

func TestScheduleFromCron(t *testing.T) {
	cases := []struct {
		expr  string
		want  domain.Schedule
		exact bool
	}{
		{"0 3 * * *", domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3}, true},
		{"30 21 * * 1-5", domain.Schedule{Repeat: domain.RepeatWeekly, Days: domain.WorkDays, Hour: 21, Minute: 30}, true},
		{"0 9 * * MON,thu", domain.Schedule{Repeat: domain.RepeatWeekly, Days: domain.WeekdaysOf(time.Monday, time.Thursday), Hour: 9}, true},
		{"0 9 * * 6-7", domain.Schedule{Repeat: domain.RepeatWeekly, Days: domain.Weekends, Hour: 9}, true},
		{"15 2 1 * *", domain.Schedule{Repeat: domain.RepeatMonthly, Day: 1, Hour: 2, Minute: 15}, true},
		{"5 * * * *", domain.Schedule{Repeat: domain.RepeatHourly, Every: 1, Minute: 5}, true},
		{"0 */6 * * *", domain.Schedule{Repeat: domain.RepeatHourly, Every: 6}, true},
		{"@daily", domain.Schedule{Repeat: domain.RepeatDaily}, true},
		{"@weekly", domain.Schedule{Repeat: domain.RepeatWeekly, Days: domain.WeekdaysOf(time.Sunday)}, true},
		// More than a schedule can say: the closest daily run.
		{"*/5 * * * *", domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3}, false},
		{"0 4 * 3 *", domain.Schedule{Repeat: domain.RepeatDaily, Hour: 4}, false},
		{"0 4 1 * 1", domain.Schedule{Repeat: domain.RepeatDaily, Hour: 4}, false},
		{"0 */5 * * *", domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3}, false},
		{"@every 1h", domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3}, false},
	}
	for _, c := range cases {
		got, exact := scheduleFromCron(c.expr)
		if got != c.want || exact != c.exact {
			t.Errorf("scheduleFromCron(%q) = %+v, %v; want %+v, %v", c.expr, got, exact, c.want, c.exact)
		}
	}
}

func TestScheduleEncoding(t *testing.T) {
	for _, s := range []domain.Schedule{
		{},
		{Repeat: domain.RepeatHourly, Every: 4, Minute: 30},
		{Repeat: domain.RepeatDaily, Hour: 7, Minute: 5},
		{Repeat: domain.RepeatWeekly, Days: domain.Weekends, Hour: 23, Minute: 59},
		{Repeat: domain.RepeatMonthly, Day: 31},
	} {
		got, err := decodeSchedule(encodeSchedule(s))
		if err != nil || got != s {
			t.Errorf("round trip of %+v = %+v, %v (%s)", s, got, err, encodeSchedule(s))
		}
	}
	if got := encodeSchedule(domain.Schedule{Repeat: domain.RepeatWeekly, Days: domain.WeekdaysOf(time.Monday), Hour: 9}); got != `{"repeat":"weekly","time":"09:00","days":[1]}` {
		t.Errorf("encoded = %s", got)
	}
}

// TestMigrateCronSchedules opens a database of the version before
// schedules changed: the cron expressions of its tasks become schedules.
func TestMigrateCronSchedules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v5.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// Back to version 5: the columns of the versions after it go, and the
	// schedules are cron expressions.
	if _, err := db.Exec(`
		ALTER TABLE tasks DROP COLUMN schedule_since;
		ALTER TABLE runs DROP COLUMN owner;
		ALTER TABLE runs DROP COLUMN queued;
		ALTER TABLE runs DROP COLUMN speed;
		ALTER TABLE runs DROP COLUMN cancel_requested;
		ALTER TABLE runs DROP COLUMN notify;
		ALTER TABLE settings DROP COLUMN background;
		ALTER TABLE settings DROP COLUMN appearance;
		ALTER TABLE settings DROP COLUMN check_updates;
		ALTER TABLE settings DROP COLUMN install_updates;
		ALTER TABLE settings DROP COLUMN trash_days;
		ALTER TABLE settings DROP COLUMN exclude_enabled;
		ALTER TABLE settings DROP COLUMN global_excludes;
		ALTER TABLE tasks DROP COLUMN trash_days;
		ALTER TABLE tasks DROP COLUMN excludes;
		ALTER TABLE tasks DROP COLUMN max_size;
		ALTER TABLE runs DROP COLUMN trashed;
		ALTER TABLE runs DROP COLUMN trash;
		ALTER TABLE run_logs DROP COLUMN deletes;
		ALTER TABLE run_logs DROP COLUMN deletes_total;
		INSERT INTO tasks (id, name, source_path, dest_path, mode, schedule, created_at, updated_at) VALUES
			('a', 'Nightly', '/a', '/b', 'copy', '0 3 * * *', 1, 5000),
			('b', 'Workdays', '/c', '/d', 'copy', '30 18 * * 1-5', 1, 1),
			('c', 'Odd', '/e', '/f', 'copy', '*/10 * * * *', 1, 1),
			('d', 'By hand', '/g', '/h', 'copy', '', 1, 1);
		INSERT INTO runs (id, task_id, task_name, mode, trigger_kind, dry_run, status, message, started_at)
			VALUES ('r', 'a', 'Nightly', 'copy', 'manual', 0, 'running', '', 1);
		PRAGMA user_version = 5;`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	want := map[string]domain.Schedule{
		"a": {Repeat: domain.RepeatDaily, Hour: 3},
		"b": {Repeat: domain.RepeatWeekly, Days: domain.WorkDays, Hour: 18, Minute: 30},
		"c": {Repeat: domain.RepeatDaily, Hour: 3},
		"d": {},
	}
	for id, w := range want {
		task, err := s.Task(ctx, id)
		if err != nil || task.Schedule != w {
			t.Errorf("task %s: schedule = %+v, %v; want %+v", id, task.Schedule, err, w)
		}
	}
	if a, _ := s.Task(ctx, "a"); !a.ScheduleSince.Equal(time.UnixMilli(5000)) {
		t.Errorf("schedule since = %v, want when the task was last changed", a.ScheduleSince)
	}
	// The run left running has no owner: whichever process starts next
	// ends it.
	if n, err := s.CancelOrphanRuns(ctx, ""); err != nil || n != 1 {
		t.Errorf("orphans canceled: %d, %v", n, err)
	}
}

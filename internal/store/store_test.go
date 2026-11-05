package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStoragesAndTasks(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	now := time.UnixMilli(time.Now().UnixMilli())

	r2 := domain.Storage{ID: "s1", Name: "R2", Provider: domain.ProviderCloudflare, Bucket: "b",
		AccessKeyID: "k", CreatedAt: now, UpdatedAt: now}
	if err := s.PutStorage(ctx, r2); err != nil {
		t.Fatal(err)
	}
	dup := r2
	dup.ID, dup.Name = "s2", "r2"
	if err := s.PutStorage(ctx, dup); !errors.Is(err, domain.ErrNameTaken) {
		t.Fatalf("duplicate name, ignoring case: %v, want ErrNameTaken", err)
	}

	task := domain.Task{ID: "t1", Name: "Photos", Source: domain.Location{Path: "/p"},
		Destination: domain.Location{StorageID: "s1", Path: "x"}, Mode: domain.ModeCopy,
		Schedule:      domain.Schedule{Repeat: domain.RepeatWeekly, Days: domain.WorkDays, Hour: 21, Minute: 30},
		ScheduleSince: now, TrashDays: 7, CreatedAt: now, UpdatedAt: now}
	if err := s.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	got, err := s.Task(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got != task {
		t.Errorf("task round trip:\n got %+v\nwant %+v", got, task)
	}

	if err := s.DeleteStorage(ctx, "s1"); !errors.Is(err, domain.ErrStorageInUse) {
		t.Fatalf("deleting a storage in use: %v, want ErrStorageInUse", err)
	}
	if err := s.DeleteTask(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteStorage(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Storage(ctx, "s1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleted storage: %v, want ErrNotFound", err)
	}
}

func TestRuns(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	base := time.UnixMilli(1_700_000_000_000)

	for i, status := range []domain.Status{domain.StatusSuccess, domain.StatusFailed, domain.StatusSuccess} {
		r := domain.Run{ID: string(rune('a' + i)), TaskID: "t1", TaskName: "Photos", Mode: domain.ModeCopy,
			Trigger: domain.TriggerManual, Status: domain.StatusRunning, StartedAt: base.Add(time.Duration(i) * time.Minute)}
		if err := s.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		r.Status, r.FinishedAt, r.Stats.Bytes = status, r.StartedAt.Add(time.Second), 42
		if i == 2 {
			r.Problem = domain.RunProblemLargeDelete
			r.Stats.Trashed, r.Trash = 3, ".cloudshuttle-trash/2026-11-05 14.30.12"
		}
		if err := s.FinishRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	all, err := s.Runs(ctx, RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID != "c" || all[0].Stats.Bytes != 42 || all[0].Problem != domain.RunProblemLargeDelete {
		t.Fatalf("runs = %+v, want 3, newest first, with c's problem", all)
	}
	if all[0].Stats.Trashed != 3 || all[0].Trash != ".cloudshuttle-trash/2026-11-05 14.30.12" || all[1].Trash != "" {
		t.Fatalf("trash = %d in %q, then %q", all[0].Stats.Trashed, all[0].Trash, all[1].Trash)
	}
	failed, err := s.Runs(ctx, RunFilter{Status: domain.StatusFailed})
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].ID != "b" {
		t.Fatalf("failed runs = %+v", failed)
	}
	last, err := s.LastRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if last["t1"].Status != domain.StatusSuccess || !last["t1"].StartedAt.Equal(base.Add(2*time.Minute)) {
		t.Fatalf("last run = %+v", last["t1"])
	}
}

func TestCancelOrphanRuns(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, r := range []struct{ id, owner string }{{"legacy", ""}, {"app", "app"}, {"scheduler", "scheduler"}} {
		run := domain.Run{ID: r.id, TaskID: r.id, Status: domain.StatusRunning, StartedAt: time.Now()}
		if err := s.StartRun(ctx, run, r.owner, false, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	// The window starts again while the scheduler runs: its own runs and
	// those from before owners were recorded are left over.
	if n, err := s.CancelOrphanRuns(ctx, "app", ""); err != nil || n != 2 {
		t.Fatalf("canceled %d, %v; want 2", n, err)
	}
	live, err := s.LiveRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Run.ID != "scheduler" || live[0].Owner != "scheduler" {
		t.Fatalf("live = %+v, want the scheduler's run only", live)
	}
}

func TestSMBStorageRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	now := time.UnixMilli(time.Now().UnixMilli())
	nas := domain.Storage{ID: "n1", Name: "NAS", Kind: domain.KindSMB, Host: "nas.local", Port: 445,
		Username: "me", CreatedAt: now, UpdatedAt: now}
	if err := s.PutStorage(ctx, nas); err != nil {
		t.Fatal(err)
	}
	got, err := s.Storage(ctx, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if got != nas {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, nas)
	}
}

// runsV0 is the runs table of the first version, which had no problem
// column: a migration adding one must find it.
const runsV0 = `CREATE TABLE runs (
	id TEXT PRIMARY KEY, task_id TEXT NOT NULL, task_name TEXT NOT NULL, mode TEXT NOT NULL,
	trigger_kind TEXT NOT NULL, dry_run INTEGER NOT NULL, status TEXT NOT NULL, message TEXT NOT NULL,
	bytes INTEGER NOT NULL DEFAULT 0, total_bytes INTEGER NOT NULL DEFAULT 0,
	transfers INTEGER NOT NULL DEFAULT 0, total_transfers INTEGER NOT NULL DEFAULT 0,
	checks INTEGER NOT NULL DEFAULT 0, deletes INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0,
	started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL DEFAULT 0);`

// TestMigrateVersion0 opens a database made before storages had kinds.
func TestMigrateVersion0(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE storages (
			id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE, provider TEXT NOT NULL,
			endpoint TEXT NOT NULL, region TEXT NOT NULL, bucket TEXT NOT NULL,
			access_key_id TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
		CREATE TABLE tasks (
			id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE,
			source_storage_id TEXT REFERENCES storages (id) ON DELETE RESTRICT, source_path TEXT NOT NULL,
			dest_storage_id TEXT REFERENCES storages (id) ON DELETE RESTRICT, dest_path TEXT NOT NULL,
			mode TEXT NOT NULL, schedule TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
		INSERT INTO storages VALUES ('s1', 'R2', 'Cloudflare', 'https://r2.example', 'auto', 'b', 'k', 1, 1);`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(runsV0); err != nil {
		t.Fatal(err)
	}
	db.Close()

	for range 2 { // the second open finds nothing left to do
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		st, err := s.Storage(context.Background(), "s1")
		if _, runsErr := s.Runs(context.Background(), RunFilter{}); runsErr != nil {
			t.Errorf("runs after migrating: %v", runsErr)
		}
		s.Close()
		if err != nil {
			t.Fatal(err)
		}
		if st.Kind != domain.KindS3 || st.Bucket != "b" || st.Host != "" {
			t.Errorf("migrated storage = %+v", st)
		}
	}
	if _, err := os.Stat(path + ".v0.bak"); err != nil {
		t.Errorf("no copy of the database before migrating: %v", err)
	}
}

func TestNewerDatabaseIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations)+1)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := Open(path); !errors.Is(err, ErrNewerDatabase) {
		t.Fatalf("open = %v, want ErrNewerDatabase", err)
	}
}

// TestMigrateSharesIntoTasks opens a database whose SMB storages held a
// share: the share moves to the front of the paths of their tasks.
func TestMigrateSharesIntoTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE storages (
			id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE, provider TEXT NOT NULL,
			endpoint TEXT NOT NULL, region TEXT NOT NULL, bucket TEXT NOT NULL,
			access_key_id TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
		CREATE TABLE tasks (
			id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE,
			source_storage_id TEXT REFERENCES storages (id) ON DELETE RESTRICT, source_path TEXT NOT NULL,
			dest_storage_id TEXT REFERENCES storages (id) ON DELETE RESTRICT, dest_path TEXT NOT NULL,
			mode TEXT NOT NULL, schedule TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
		ALTER TABLE storages ADD COLUMN kind TEXT NOT NULL DEFAULT 's3';
		ALTER TABLE storages ADD COLUMN host TEXT NOT NULL DEFAULT '';
		ALTER TABLE storages ADD COLUMN port INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE storages ADD COLUMN username TEXT NOT NULL DEFAULT '';
		ALTER TABLE storages ADD COLUMN share TEXT NOT NULL DEFAULT '';
		PRAGMA user_version = 1;
		INSERT INTO storages (id, name, kind, provider, endpoint, region, bucket, access_key_id, host, port, username, share, created_at, updated_at)
			VALUES ('a', 'NAS A', 'smb', '', '', '', '', '', 'a.local', 445, 'me', 'photos', 1, 1),
			       ('b', 'NAS B', 'smb', '', '', '', '', '', 'b.local', 445, 'me', 'backup', 1, 1),
			       ('r', 'R2', 's3', 'Cloudflare', 'https://r2', 'auto', 'bucket', 'k', '', 0, '', '', 1, 1);
		INSERT INTO tasks VALUES
			('t1', 'NAS to NAS', 'a', '2024', 'b', '', 'sync', '', 1, 1),
			('t2', 'NAS to R2', 'a', '', 'r', 'photos', 'copy', '', 1, 1);`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(runsV0); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	t1, _ := s.Task(ctx, "t1")
	t2, _ := s.Task(ctx, "t2")
	if t1.Source.Path != "photos/2024" || t1.Destination.Path != "backup" {
		t.Errorf("t1 = %+v", t1)
	}
	if t2.Source.Path != "photos" || t2.Destination.Path != "photos" {
		t.Errorf("t2: the bucket's path changed, or the share was not added: %+v", t2)
	}
	if st, err := s.Storage(ctx, "a"); err != nil || st.Host != "a.local" {
		t.Errorf("storage = %+v, %v", st, err)
	}
}

func TestRunLogs(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if _, err := s.RunLog(ctx, "r1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("no log yet: %v", err)
	}
	if err := s.PutRunLog(ctx, "r1", "line 1\nline 2"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRunLog(ctx, "r1", "line 1\nline 2\nline 3"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RunLog(ctx, "r1"); err != nil || got != "line 1\nline 2\nline 3" {
		t.Fatalf("log = %q, %v", got, err)
	}
}

func TestRunDeletes(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.PutRunLog(ctx, "r1", "log"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunDeletes(ctx, "r1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a run with a log alone: %v", err)
	}
	want := domain.StoppedDeletes{Total: 120, Paths: []string{"a.txt", "photos/line\nbreak.jpg"}}
	if err := s.PutRunDeletes(ctx, "r1", want); err != nil {
		t.Fatal(err)
	}
	// The log is written again after, as a run ends: the deletes stay.
	if err := s.PutRunLog(ctx, "r1", "log, to the end"); err != nil {
		t.Fatal(err)
	}
	got, err := s.RunDeletes(ctx, "r1")
	if err != nil || got.Total != 120 || !slices.Equal(got.Paths, want.Paths) {
		t.Fatalf("deletes = %+v, %v", got, err)
	}
	if log, err := s.RunLog(ctx, "r1"); err != nil || log != "log, to the end" {
		t.Fatalf("log = %q, %v", log, err)
	}
	// Kept before the log, they make no log of their own to lose.
	if err := s.PutRunDeletes(ctx, "r2", want); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRunLog(ctx, "r2", "log"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RunDeletes(ctx, "r2"); err != nil || got.Total != 120 {
		t.Fatalf("deletes = %+v, %v", got, err)
	}
}

func TestDeleteRuns(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	now := time.UnixMilli(time.Now().UnixMilli())
	for _, r := range []domain.Run{
		{ID: "done", TaskID: "t", TaskName: "T", Mode: domain.ModeCopy, Trigger: domain.TriggerManual, Status: domain.StatusSuccess, StartedAt: now},
		{ID: "failed", TaskID: "t", TaskName: "T", Mode: domain.ModeCopy, Trigger: domain.TriggerManual, Status: domain.StatusFailed, StartedAt: now},
		{ID: "running", TaskID: "t", TaskName: "T", Mode: domain.ModeCopy, Trigger: domain.TriggerManual, Status: domain.StatusRunning, StartedAt: now},
		{ID: "kept", TaskID: "t", TaskName: "T", Mode: domain.ModeCopy, Trigger: domain.TriggerManual, Status: domain.StatusSuccess, StartedAt: now},
	} {
		if err := s.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := s.PutRunLog(ctx, r.ID, "log of "+r.ID); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.DeleteRuns(ctx, []string{"done", "failed", "running", "gone"})
	if err != nil || n != 2 {
		t.Fatalf("deleted %d, %v; want 2", n, err)
	}
	runs, err := s.Runs(ctx, RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, r := range runs {
		left = append(left, r.ID)
	}
	slices.Sort(left)
	if !slices.Equal(left, []string{"kept", "running"}) {
		t.Errorf("left %q", left)
	}
	if _, err := s.RunLog(ctx, "done"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("the log of a deleted run stayed: %v", err)
	}
	if _, err := s.RunLog(ctx, "running"); err != nil {
		t.Errorf("the log of a run in progress went: %v", err)
	}
}

func TestRunsSince(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	now := time.UnixMilli(time.Now().UnixMilli())
	for i, at := range []time.Time{now.AddDate(0, 0, -10), now.AddDate(0, 0, -2), now} {
		if err := s.InsertRun(ctx, domain.Run{ID: fmt.Sprint(i), TaskID: "t", TaskName: "T", Mode: domain.ModeCopy,
			Trigger: domain.TriggerManual, Status: domain.StatusSuccess, StartedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs(ctx, RunFilter{Since: now.AddDate(0, 0, -7)})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %d, %v; want the two of the last week", len(runs), err)
	}
}

func TestPruneRuns(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	now := time.Now()
	old := now.AddDate(0, 0, -100)
	for _, r := range []domain.Run{
		{ID: "old", TaskID: "t", TaskName: "T", Mode: domain.ModeCopy, Trigger: domain.TriggerManual, Status: domain.StatusSuccess, StartedAt: old},
		{ID: "recent", TaskID: "t", TaskName: "T", Mode: domain.ModeCopy, Trigger: domain.TriggerManual, Status: domain.StatusSuccess, StartedAt: now},
		{ID: "running", TaskID: "t", TaskName: "T", Mode: domain.ModeCopy, Trigger: domain.TriggerManual, Status: domain.StatusRunning, StartedAt: old},
	} {
		if err := s.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := s.PutRunLog(ctx, r.ID, "log of "+r.ID); err != nil {
			t.Fatal(err)
		}
	}
	// A log whose run is gone already.
	if err := s.PutRunLog(ctx, "orphan", "orphan log"); err != nil {
		t.Fatal(err)
	}

	n, err := s.PruneRuns(ctx, now.AddDate(0, 0, -90))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v; want 1", n, err)
	}
	for _, id := range []string{"recent", "running"} {
		if _, err := s.RunLog(ctx, id); err != nil {
			t.Errorf("log of %s: %v", id, err)
		}
	}
	if _, err := s.RunLog(ctx, "old"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("the old log stayed: %v", err)
	}
	if _, err := s.RunLog(ctx, "orphan"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("the orphan log stayed: %v", err)
	}
	runs, err := s.Runs(ctx, RunFilter{})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %d, %v; want 2", len(runs), err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	got, err := s.Settings(ctx)
	if err != nil || got != domain.DefaultSettings() {
		t.Fatalf("settings = %+v, %v; want the defaults", got, err)
	}
	want := domain.Settings{Transfers: 2, Checkers: 4, Retries: 5, BandwidthMBps: 10,
		NotifyFailed: false, NotifySkipped: false, RetentionDays: 30, TrashDays: 7}
	if err := s.PutSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Settings(ctx); err != nil || got != want {
		t.Fatalf("settings = %+v, %v; want %+v", got, err, want)
	}
}

func TestSchedulesThatRanInTheBackgroundStayAllowed(t *testing.T) {
	ctx := context.Background()
	for _, scheduled := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "old.db")
		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		schedule := ""
		if scheduled {
			schedule = `{"repeat":"daily","time":"03:00"}`
		}
		// Back to the database as version 8 left it, with a task.
		for _, q := range []string{
			`ALTER TABLE settings DROP COLUMN background`,
			`ALTER TABLE settings DROP COLUMN appearance`,
			`ALTER TABLE settings DROP COLUMN check_updates`,
			`ALTER TABLE settings DROP COLUMN install_updates`,
			`ALTER TABLE settings DROP COLUMN trash_days`,
			`ALTER TABLE tasks DROP COLUMN trash_days`,
			`ALTER TABLE runs DROP COLUMN trashed`,
			`ALTER TABLE runs DROP COLUMN trash`,
			`ALTER TABLE run_logs DROP COLUMN deletes`,
			`ALTER TABLE run_logs DROP COLUMN deletes_total`,
			`PRAGMA user_version = 8`,
		} {
			if _, err := st.db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.db.Exec(`INSERT INTO tasks (id, name, source_path, dest_path, mode, schedule, created_at, updated_at)
			VALUES ('t', 'T', '/a', '/b', 'copy', ?, 0, 0)`, schedule); err != nil {
			t.Fatal(err)
		}
		st.Close()
		st, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := st.Settings(ctx)
		st.Close()
		want := domain.ConsentUnasked
		if scheduled {
			want = domain.ConsentGiven
		}
		if got.Background != want {
			t.Errorf("scheduled %v: consent = %v, want %v", scheduled, got.Background, want)
		}
		// Updates came after: checked for on its own, installed when asked.
		if !got.CheckUpdates || got.InstallUpdates {
			t.Errorf("scheduled %v: updates check %v, install %v; want true, false", scheduled, got.CheckUpdates, got.InstallUpdates)
		}
		// The trash came after too: a month, as for a new database.
		if got.TrashDays != 30 {
			t.Errorf("scheduled %v: trash days = %d, want 30", scheduled, got.TrashDays)
		}
	}
}

package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
)

// openForBackup opens a database of its own, apart from open in the other
// tests, so the backup tests stay readable beside the growing schema.
func openForBackup(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSettingsKeepTheICloudChoice(t *testing.T) {
	ctx := context.Background()
	s := openForBackup(t)
	want := domain.DefaultSettings()
	want.BackupICloud = true
	if err := s.PutSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings(ctx)
	if err != nil || got != want {
		t.Fatalf("settings = %+v, %v; want %+v", got, err, want)
	}
}

func TestBackupConfig(t *testing.T) {
	ctx := context.Background()
	s := openForBackup(t)
	now := time.UnixMilli(1_700_000_000_000)

	settings := domain.DefaultSettings()
	settings.BackupICloud = true
	if err := s.PutSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	st := domain.Storage{ID: "s1", Name: "R2", Provider: domain.ProviderCloudflare, Bucket: "b",
		AccessKeyID: "k", CreatedAt: now, UpdatedAt: now}
	if err := s.PutStorage(ctx, st); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "t1", Name: "Photos", Source: domain.Location{Path: "/p"},
		Destination: domain.Location{StorageID: "s1", Path: "x"}, Mode: domain.ModeCopy,
		CreatedAt: now, UpdatedAt: now}
	if err := s.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	// The history is not part of the configuration and must not travel
	// with it.
	run := domain.Run{ID: "r1", TaskID: "t1", TaskName: "Photos", Mode: domain.ModeCopy,
		Trigger: domain.TriggerManual, Status: domain.StatusSuccess,
		StartedAt: now, FinishedAt: now.Add(time.Minute)}
	if err := s.InsertRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRunLog(ctx, run.ID, strings.Repeat("a line of the run\n", 1<<16)); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "cloudshuttle.db")
	if err := s.BackupConfig(ctx, path); err != nil {
		t.Fatal(err)
	}
	// A later backup writes over the earlier one.
	if err := s.BackupConfig(ctx, path); err != nil {
		t.Fatalf("a second backup: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	// The copy is one small file: the pages the history used are gone with
	// it, so iCloud does not carry the history's size.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > 256<<10 {
		t.Fatalf("the copy is %d bytes, want the configuration alone", fi.Size())
	}
	// The copy is one file: a journal or WAL left beside it would be
	// synced to iCloud too, and opened with it.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "cloudshuttle.db" {
		t.Fatalf("the copy left %d files beside it, want the database alone", len(entries))
	}

	copied, err := Open(path)
	if err != nil {
		t.Fatalf("open the copy: %v", err)
	}
	defer copied.Close()

	if got, err := copied.Settings(ctx); err != nil || got != settings {
		t.Fatalf("settings in the copy = %+v, %v; want %+v", got, err, settings)
	}
	if got, err := copied.Storages(ctx); err != nil || len(got) != 1 || got[0].Name != "R2" {
		t.Fatalf("storages in the copy = %+v, %v", got, err)
	}
	if got, err := copied.Tasks(ctx); err != nil || len(got) != 1 || got[0].Name != "Photos" {
		t.Fatalf("tasks in the copy = %+v, %v", got, err)
	}
	if got, err := copied.Runs(ctx, RunFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("runs in the copy = %+v, %v; want none", got, err)
	}
	if _, err := copied.RunLog(ctx, run.ID); err == nil {
		t.Fatal("the copy kept a run's log")
	}
}

func TestRestoreConfig(t *testing.T) {
	ctx := context.Background()
	now := time.UnixMilli(time.Now().UnixMilli())
	from := openForBackup(t)
	st := domain.Storage{ID: "s1", Name: "NAS", Kind: domain.KindSMB, Host: "nas.local", Port: 445, Username: "me", CreatedAt: now, UpdatedAt: now}
	task := domain.Task{ID: "t1", Name: "Photos", Source: domain.Location{Path: "/p"}, Destination: domain.Location{StorageID: "s1", Path: "Photos"},
		Mode: domain.ModeCopy, Schedule: domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3}, ScheduleSince: now, CreatedAt: now, UpdatedAt: now}
	settings := domain.DefaultSettings()
	settings.RetentionDays = 30
	settings.Background = domain.ConsentGiven   // the other computer's answer
	settings.Appearance = domain.AppearanceDark // a preference, restored
	settings.CheckUpdates, settings.InstallUpdates = false, true
	if err := from.PutStorage(ctx, st); err != nil {
		t.Fatal(err)
	}
	if err := from.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := from.PutSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := from.BackupConfig(ctx, backup); err != nil {
		t.Fatal(err)
	}

	to := openForBackup(t)
	refused := domain.DefaultSettings()
	refused.Background = domain.ConsentRefused
	if err := to.PutSettings(ctx, refused); err != nil {
		t.Fatal(err)
	}
	if err := to.PutStorage(ctx, domain.Storage{ID: "old", Name: "Old", Provider: domain.ProviderAWS, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "r", TaskID: "x", TaskName: "X", Mode: domain.ModeCopy, Trigger: domain.TriggerManual, Status: domain.StatusSuccess, StartedAt: now}
	if err := to.InsertRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := to.RestoreConfig(ctx, backup); err != nil {
		t.Fatal(err)
	}
	storages, _ := to.Storages(ctx)
	tasks, _ := to.Tasks(ctx)
	got, _ := to.Settings(ctx)
	runs, _ := to.Runs(ctx, RunFilter{})
	if len(storages) != 1 || storages[0] != st {
		t.Errorf("storages = %+v", storages)
	}
	if len(tasks) != 1 || tasks[0] != task {
		t.Errorf("tasks = %+v", tasks)
	}
	want := settings
	want.Background = domain.ConsentRefused // this computer's answer stays
	if got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
	if len(runs) != 1 {
		t.Errorf("the history went: %+v", runs)
	}
	if err := to.RestoreConfig(ctx, filepath.Join(t.TempDir(), "missing", "x.db")); err == nil {
		t.Error("restored from nothing")
	}
}

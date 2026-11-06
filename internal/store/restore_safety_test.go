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

func TestRejectUnrelatedAndInvalidBackupWithoutReplacingConfig(t *testing.T) {
	ctx := context.Background()
	to := openForBackup(t)
	keep := domain.Task{ID: "keep", Name: "keep", Source: domain.Location{Path: "/src"}, Destination: domain.Location{Path: "/dst"}, Mode: domain.ModeCopy}
	if err := to.PutTask(ctx, keep); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"unrelated", "invalid-task", "unsafe-endpoint", "modified-trigger", "missing-settings"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "backup.db")
			if kind == "unrelated" {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("CREATE TABLE unrelated(x TEXT)")
				if err != nil {
					t.Fatal(err)
				}
				db.Close()
			} else {
				from, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "invalid-task":
					bad := keep
					bad.Destination.Path = "/"
					bad.Mode = domain.ModeSync
					err = from.PutTask(ctx, bad)
				case "unsafe-endpoint":
					err = from.PutStorage(ctx, domain.Storage{ID: "s", Name: "s", Kind: domain.KindS3, Provider: domain.ProviderAWS, Endpoint: "https://user:secret@host", Bucket: "bucket", AccessKeyID: "key"})
				case "missing-settings":
					_, err = from.db.Exec(`DROP TABLE settings`)
				case "modified-trigger":
					_, err = from.db.Exec(`DROP TRIGGER remember_run_start; CREATE TRIGGER remember_run_start AFTER INSERT ON runs BEGIN DELETE FROM tasks; END;`)
				}
				if err != nil {
					t.Fatal(err)
				}
				from.Close()
			}
			if err := to.RestoreConfig(ctx, path); !errors.Is(err, domain.ErrNotBackup) {
				t.Fatalf("restore=%v", err)
			}
			tasks, err := to.Tasks(ctx)
			if err != nil || len(tasks) != 1 || tasks[0].ID != "keep" {
				t.Fatalf("config lost: %+v, %v", tasks, err)
			}
		})
	}
}

func TestScheduleStartsSurviveHistoryRemovalAndBackup(t *testing.T) {
	ctx := context.Background()
	s := openForBackup(t)
	at := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	for _, id := range []string{"deleted", "pruned"} {
		run := domain.Run{ID: id, TaskID: id, Status: domain.StatusSuccess, StartedAt: at}
		if err := s.InsertRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DeleteRuns(ctx, []string{"deleted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneRuns(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"deleted", "pruned"} {
		starts, err := s.LastStarts(ctx)
		if err != nil || !starts[id].Equal(at) {
			t.Fatalf("start lost: %+v %v", starts, err)
		}
		if err := s.StartRun(ctx, domain.Run{ID: id + "-again", TaskID: id, StartedAt: time.Now(), Status: domain.StatusRunning}, "app", true, at); !errors.Is(err, ErrSlotTaken) {
			t.Fatalf("repeated slot: %v", err)
		}
	}
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := s.BackupConfig(ctx, path); err != nil {
		t.Fatal(err)
	}
	target := openForBackup(t)
	if err := target.RestoreConfig(ctx, path); err != nil {
		t.Fatal(err)
	}
	starts, err := target.LastStarts(ctx)
	if err != nil || !starts["deleted"].Equal(at) || !starts["pruned"].Equal(at) {
		t.Fatalf("restored starts: %+v %v", starts, err)
	}
}

func TestScheduleStartsMigratedFromVersionSeven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Back to version 7: the columns of the versions after it go too.
	if _, err := s.db.Exec(`DROP TRIGGER remember_run_start; DROP TABLE schedule_starts;
		ALTER TABLE settings DROP COLUMN background; ALTER TABLE settings DROP COLUMN appearance; ALTER TABLE settings DROP COLUMN check_updates; ALTER TABLE settings DROP COLUMN install_updates; ALTER TABLE settings DROP COLUMN trash_days; ALTER TABLE settings DROP COLUMN exclude_enabled; ALTER TABLE settings DROP COLUMN global_excludes; ALTER TABLE tasks DROP COLUMN trash_days; ALTER TABLE tasks DROP COLUMN excludes; ALTER TABLE tasks DROP COLUMN max_size; ALTER TABLE runs DROP COLUMN trashed; ALTER TABLE runs DROP COLUMN trash; ALTER TABLE run_logs DROP COLUMN deletes; ALTER TABLE run_logs DROP COLUMN deletes_total; PRAGMA user_version=7;`); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Millisecond)
	if err := s.InsertRun(context.Background(), domain.Run{ID: "old", TaskID: "t", StartedAt: at, Status: domain.StatusSuccess}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	starts, err := s.LastStarts(context.Background())
	if err != nil || !starts["t"].Equal(at) {
		t.Fatalf("migration lost starts: %+v %v", starts, err)
	}
}

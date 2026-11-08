package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/starriv/cloudshuttle/internal/domain"
)

func TestDestinationMigrationPreservesExistingPath(t *testing.T) {
	s := openForBackup(t)
	ctx := context.Background()
	if err := s.PutTask(ctx, domain.Task{ID: "old", Name: "Old", Source: domain.Location{Path: "/source"}, Destination: domain.Location{Path: "/target"}, Mode: domain.ModeCopy}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE tasks DROP COLUMN allow_root; ALTER TABLE tasks DROP COLUMN destination_folder; PRAGMA user_version=18`); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "backup.db")
	if err := s.BackupConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	got, err := migrated.Task(ctx, "old")
	if err != nil || !got.AllowRoot || got.DestinationFolder != "" || got.Destination.Path != "/target" {
		t.Fatalf("migrated = %+v, %v", got, err)
	}
}

func TestDestinationPendingMigrationAndBackup(t *testing.T) {
	s := openForBackup(t)
	ctx := context.Background()
	task := domain.Task{ID: "pending", Name: "Pending", Source: domain.Location{Path: "/source/devops"}, Destination: domain.Location{Path: "/target/devops"}, DestinationFolder: "devops", DestinationPending: true, Mode: domain.ModeCopy}
	if err := s.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "pending.db")
	if err := s.BackupConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	copied, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	got, err := copied.Task(ctx, task.ID)
	if err != nil || !got.DestinationPending {
		t.Fatalf("backup lost pending: %+v, %v", got, err)
	}
	if err := copied.MarkDestinationReady(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, err = copied.Task(ctx, task.ID)
	if err != nil || got.DestinationPending {
		t.Fatalf("initialization not persisted: %+v, %v", got, err)
	}
	if _, err := s.db.Exec(`ALTER TABLE tasks DROP COLUMN destination_pending; PRAGMA user_version=19`); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(t.TempDir(), "old.db")
	if err := s.BackupConfig(ctx, oldFile); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(oldFile)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	got, err = migrated.Task(ctx, task.ID)
	if err != nil || !got.DestinationPending || got.Destination != task.Destination {
		t.Fatalf("old target changed: %+v,%v", got, err)
	}
}

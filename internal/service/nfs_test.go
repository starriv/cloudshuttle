package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine/nfs3/nfstest"
)

// An NFS storage keeps no secret, tests its connection, and runs a task
// into its export.
func TestATaskRunsIntoAnNFSExport(t *testing.T) {
	ctx := context.Background()
	s, secrets, done := newService(t)
	disk := t.TempDir()
	srv := nfstest.Start(t, disk)
	uid, gid, _ := LocalIDs()
	in := domain.StorageInput{Kind: domain.KindNFS, Name: "Tank", Host: srv.Host,
		Port: strconv.Itoa(srv.Port), Export: "/", UID: strconv.Itoa(uid), GID: strconv.Itoa(gid), Secret: "ignored"}
	if err := s.TestConnection(ctx, "", in); err != nil {
		t.Fatalf("test connection = %v", err)
	}
	st, err := s.CreateStorage(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if st.Export != "/" || st.Port != srv.Port || st.UID != uid || st.GID != gid {
		t.Fatalf("storage = %+v", st)
	}
	if len(secrets.m) != 0 {
		t.Fatalf("an NFS storage kept secrets: %v", secrets.m)
	}

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "To NFS", Mode: domain.ModeSync,
		Source: domain.Location{Path: src}, Destination: domain.Location{StorageID: st.ID, Path: "backup"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, done); run.Status != domain.StatusSuccess || run.Stats.Transfers != 1 {
		t.Fatalf("run = %+v", run)
	}
	if b, err := os.ReadFile(filepath.Join(disk, "backup", "a.txt")); err != nil || string(b) != "a" {
		t.Fatalf("on the export: %q, %v", b, err)
	}
	folders, err := s.Folders(ctx, st.ID, "")
	if err != nil || len(folders) != 1 || folders[0] != "backup" {
		t.Fatalf("folders = %q, %v", folders, err)
	}
	// go-nfs keeps its export list to itself.
	if _, err := s.NFSExports(ctx, in); !errors.Is(err, domain.ErrExportsHidden) {
		t.Fatalf("exports = %v, want hidden", err)
	}
}

func TestAnNFSServerThatIsNotThereIsUnreachable(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	in := domain.StorageInput{Kind: domain.KindNFS, Name: "Gone", Host: "127.0.0.1", Port: "1", Export: "/x", UID: "1", GID: "1"}
	if err := s.TestConnection(ctx, "", in); !errors.Is(err, domain.ErrUnreachable) {
		t.Fatalf("test connection = %v, want unreachable", err)
	}
}

// Changing a storage from NFS to S3 keeps the access key it just saved: the
// kind that had no secret has none to delete.
func TestChangingAnNFSStorageToS3KeepsItsSecret(t *testing.T) {
	ctx := context.Background()
	s, secrets, _ := newService(t)
	uid, gid, _ := LocalIDs()
	st, err := s.CreateStorage(ctx, domain.StorageInput{Kind: domain.KindNFS, Name: "Tank",
		Host: "nas.local", Export: "/backup", UID: strconv.Itoa(uid), GID: strconv.Itoa(gid)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateStorage(ctx, st.ID, domain.StorageInput{Kind: domain.KindS3, Name: "Tank",
		Provider: domain.ProviderAWS, Bucket: "b", AccessKeyID: "key", Secret: "the key"}); err != nil {
		t.Fatal(err)
	}
	if got, err := secrets.Get("storage/" + st.ID + "/secret-access-key"); err != nil || got != "the key" {
		t.Fatalf("the access key after the kind change = %q, %v", got, err)
	}
}

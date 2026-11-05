package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// trashScene is a source and a destination that a sync turns into one
// another: keep.txt is the same on both, changed.txt is newer in the
// source, and gone.txt is in the destination alone.
func trashScene(t *testing.T) (src, dst string) {
	t.Helper()
	src, dst = t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "keep.txt"), "same")
	write(t, filepath.Join(dst, "keep.txt"), "same")
	write(t, filepath.Join(src, "dir", "changed.txt"), "version two")
	write(t, filepath.Join(dst, "dir", "changed.txt"), "one")
	write(t, filepath.Join(dst, "gone.txt"), "deleted at the source")
	return src, dst
}

func TestSyncMovesWhatItDeletesOrReplacesIntoTheTrash(t *testing.T) {
	ctx := context.Background()
	src, dst := trashScene(t)
	folder := TrashFolder(time.Date(2026, 11, 5, 14, 30, 12, 0, time.Local))
	if folder != ".cloudshuttle-trash/2026-11-05 14.30.12" {
		t.Fatalf("folder = %q", folder)
	}
	job := Job{ID: "trash", Source: Location{Path: src}, Destination: Location{Path: dst}, Sync: true,
		Trash: folder, TrashDays: 30}
	p, err := Run(ctx, job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dst, "gone.txt")) || read(t, filepath.Join(dst, "dir", "changed.txt")) != "version two" {
		t.Fatal("the destination is not the source")
	}
	trash := filepath.Join(dst, filepath.FromSlash(folder))
	if read(t, filepath.Join(trash, "gone.txt")) != "deleted at the source" ||
		read(t, filepath.Join(trash, "dir", "changed.txt")) != "one" {
		t.Fatal("the trash lacks the deleted or the replaced file")
	}
	if exists(filepath.Join(trash, "keep.txt")) {
		t.Fatal("the trash holds a file the sync left alone")
	}
	if p.Deletes != 1 || p.Trashed != 2 || p.Transfers != 1 {
		t.Errorf("progress = %+v, want 1 delete, 2 trashed, 1 transfer", p)
	}

	// The checks count the files compared, as without the trash.
	src2, dst2 := trashScene(t)
	plain, err := Run(ctx, Job{ID: "plain", Source: Location{Path: src2}, Destination: Location{Path: dst2}, Sync: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Checks != plain.Checks {
		t.Errorf("checks = %d with the trash, %d without", p.Checks, plain.Checks)
	}

	// The next sync leaves the trash alone, though the source has none.
	if _, err := Run(ctx, job, nil); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(trash, "gone.txt")) {
		t.Fatal("a sync deleted the trash")
	}

	// A task whose source is that destination does not carry the trash.
	further := t.TempDir()
	if _, err := Run(ctx, Job{ID: "further", Source: Location{Path: dst}, Destination: Location{Path: further}}, nil); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(further, TrashRoot)) || !exists(filepath.Join(further, "keep.txt")) {
		t.Fatal("a copy from the destination carried the trash, or nothing")
	}
}

func TestSyncEmptiesTheTrashOfOldRuns(t *testing.T) {
	ctx := context.Background()
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a.txt"), "a")
	old := filepath.Join(dst, TrashRoot, time.Now().AddDate(0, 0, -31).Format(trashStamp))
	recent := filepath.Join(dst, TrashRoot, time.Now().AddDate(0, 0, -29).Format(trashStamp))
	mine := filepath.Join(dst, TrashRoot, "kept by hand")
	for _, dir := range []string{old, recent, mine} {
		write(t, filepath.Join(dir, "nested", "file.txt"), "x")
	}

	job := Job{ID: "prune", Source: Location{Path: src}, Destination: Location{Path: dst}, Sync: true,
		Trash: TrashFolder(time.Now()), TrashDays: 30}
	p, err := Run(ctx, job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if exists(old) {
		t.Error("the trash kept a run's folder past its days")
	}
	if !exists(recent) || !exists(mine) {
		t.Error("the trash lost a recent folder, or one the app did not name")
	}
	if p.Deletes != 0 {
		t.Errorf("deletes = %d: emptying the trash counted as the sync's", p.Deletes)
	}

	// A run without the trash empties none; a copy with it does.
	write(t, filepath.Join(old, "file.txt"), "x")
	job.Sync, job.Trash = false, ""
	if _, err := Run(ctx, job, nil); err != nil {
		t.Fatal(err)
	}
	if !exists(old) {
		t.Error("a run without the trash emptied it")
	}
	job.Trash = TrashFolder(time.Now())
	if _, err := Run(ctx, job, nil); err != nil {
		t.Fatal(err)
	}
	if exists(old) {
		t.Error("a copy with the trash kept a run's folder past its days")
	}
}

func TestCopyMovesWhatItReplacesIntoTheTrash(t *testing.T) {
	ctx := context.Background()
	src, dst := trashScene(t)
	folder := TrashFolder(time.Now())
	p, err := Run(ctx, Job{ID: "copy-trash", Source: Location{Path: src}, Destination: Location{Path: dst},
		Trash: folder, TrashDays: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(dst, filepath.FromSlash(folder))
	if read(t, filepath.Join(dst, "dir", "changed.txt")) != "version two" ||
		read(t, filepath.Join(trash, "dir", "changed.txt")) != "one" {
		t.Fatal("the copy did not keep the replaced file in the trash")
	}
	// A copy deletes nothing: the destination's own file stays where it is.
	if !exists(filepath.Join(dst, "gone.txt")) || exists(filepath.Join(trash, "gone.txt")) {
		t.Fatal("a copy moved a file it does not replace")
	}
	if p.Deletes != 0 || p.Trashed != 1 || p.Transfers != 1 {
		t.Errorf("progress = %+v, want no delete, 1 trashed, 1 transfer", p)
	}
}

func TestStoppedSyncListsWhatItWouldDelete(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	write(t, filepath.Join(src, "kept.txt"), "kept")
	write(t, filepath.Join(dst, "kept.txt"), "kept")
	for i := range maxListedDeletes + 5 {
		write(t, filepath.Join(dst, "old", fmt.Sprintf("f%03d.txt", i)), "old")
	}
	_, err := Run(ctx, Job{ID: "list", Source: Location{Path: src}, Destination: Location{Path: dst}, Sync: true}, nil)
	var stopped *StoppedSync
	if !errors.As(err, &stopped) || !errors.Is(err, ErrLargeDelete) {
		t.Fatalf("run = %v, want a StoppedSync of ErrLargeDelete", err)
	}
	if stopped.Deletes != maxListedDeletes+5 || len(stopped.Paths) != maxListedDeletes {
		t.Fatalf("deletes = %d, %d listed", stopped.Deletes, len(stopped.Paths))
	}
	if !slices.Contains(stopped.Paths, "old/f000.txt") || slices.Contains(stopped.Paths, "kept.txt") {
		t.Errorf("paths = %q", stopped.Paths[:3])
	}
	want := fmt.Sprintf("the sync would delete most of the destination: %d of %d files", maxListedDeletes+5, maxListedDeletes+6)
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestS3SyncMovesIntoTheTrash(t *testing.T) {
	ctx := context.Background()
	remote := fakeS3(t)
	seed, src := t.TempDir(), t.TempDir()
	for i := range 3 {
		write(t, filepath.Join(seed, fmt.Sprintf("a%d.txt", i)), "old")
		write(t, filepath.Join(src, fmt.Sprintf("a%d.txt", i)), "old")
	}
	write(t, filepath.Join(seed, "gone.txt"), "deleted at the source")
	write(t, filepath.Join(src, "a0.txt"), "replaced")
	dst := Location{S3: remote, Path: "backup"}
	if _, err := Run(ctx, Job{ID: "seed", Source: Location{Path: seed}, Destination: dst}, nil); err != nil {
		t.Fatal(err)
	}

	// S3 has no move: the trash copies and deletes, without counting a
	// transfer or another delete, so the preview's cap holds.
	folder := TrashFolder(time.Now())
	p, err := Run(ctx, Job{ID: "s3-trash", Source: Location{Path: src}, Destination: dst, Sync: true,
		Trash: folder, TrashDays: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Deletes != 1 || p.Trashed != 2 || p.Transfers != 1 {
		t.Errorf("progress = %+v, want 1 delete, 2 trashed, 1 transfer", p)
	}

	back := t.TempDir()
	trash := Location{S3: remote, Path: "backup/" + folder}
	if _, err := Run(ctx, Job{ID: "down", Source: trash, Destination: Location{Path: back}}, nil); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(back, "gone.txt")) != "deleted at the source" || read(t, filepath.Join(back, "a0.txt")) != "old" {
		t.Fatal("the trash lacks the deleted or the replaced file")
	}
}

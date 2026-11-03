package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestCopyAndSyncBetweenLocalFolders(t *testing.T) {
	ctx := context.Background()
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a.txt"), "alpha")
	write(t, filepath.Join(src, "nested", "b.txt"), "bravo")
	write(t, filepath.Join(dst, "stale.txt"), "old")

	job := Job{ID: "copy", Source: Location{Path: src}, Destination: Location{Path: dst}}

	// A dry run changes nothing.
	job.DryRun = true
	if _, err := Run(ctx, job, nil); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dst, "a.txt")) {
		t.Fatal("dry run copied a file")
	}

	// A copy adds files and keeps the destination's own.
	job.DryRun = false
	got, err := Run(ctx, job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Transfers != 2 || got.Bytes != int64(len("alpha")+len("bravo")) {
		t.Errorf("copy progress = %+v, want 2 transfers of 10 bytes", got)
	}
	if !exists(filepath.Join(dst, "nested", "b.txt")) || !exists(filepath.Join(dst, "stale.txt")) {
		t.Fatal("copy did not add the files or removed the destination's own")
	}

	// A sync deletes what the source does not have.
	job.ID, job.Sync = "sync", true
	got, err = Run(ctx, job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dst, "stale.txt")) {
		t.Error("sync kept a file the source does not have")
	}
	if got.Deletes != 1 {
		t.Errorf("sync deletes = %d, want 1", got.Deletes)
	}
}

func TestSyncStopsBeforeEmptyingTheDestination(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		write(t, filepath.Join(dst, "photos", fmt.Sprintf("a%d.jpg", i)), "precious")
	}

	// The source holds nothing: a sync would delete every file of the
	// destination, which may be the only copy left.
	job := Job{ID: "empty", Source: Location{Path: src}, Destination: Location{Path: dst}, Sync: true}
	if _, err := Run(ctx, job, nil); !errors.Is(err, ErrEmptySource) {
		t.Fatalf("run = %v, want ErrEmptySource", err)
	}
	if n := countFiles(t, dst); n != 12 {
		t.Fatalf("%d files left, want all 12", n)
	}

	// A dry run changes nothing anyway, and still tells what would go.
	job.DryRun = true
	p, err := Run(ctx, job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Deletes != 12 || countFiles(t, dst) != 12 {
		t.Fatalf("dry run = %+v, %d files left; want 12 deletes and nothing gone", p, countFiles(t, dst))
	}

	// Forced, the sync does what was asked.
	job.DryRun, job.Force = false, true
	if _, err := Run(ctx, job, nil); err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, dst); n != 0 {
		t.Fatalf("%d files left after a forced sync, want none", n)
	}
}

func TestSyncStopsBeforeDeletingMostOfTheDestination(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	write(t, filepath.Join(src, "new.txt"), "new")
	for i := range 20 {
		write(t, filepath.Join(dst, fmt.Sprintf("old%d.txt", i)), "old")
	}

	job := Job{ID: "most", Source: Location{Path: src}, Destination: Location{Path: dst}, Sync: true}
	if _, err := Run(ctx, job, nil); !errors.Is(err, ErrLargeDelete) {
		t.Fatalf("run = %v, want ErrLargeDelete", err)
	}
	if n := countFiles(t, dst); n != 20 {
		t.Fatalf("%d files left, want all 20", n)
	}

	// A few deletes go through: the source has 20 of the destination's
	// files, and the destination one more.
	root2 := t.TempDir()
	src2, dst2 := filepath.Join(root2, "src"), filepath.Join(root2, "dst")
	for i := range 20 {
		write(t, filepath.Join(src2, fmt.Sprintf("keep%d.txt", i)), "keep")
	}
	if _, err := Run(ctx, Job{ID: "seed", Source: Location{Path: src2}, Destination: Location{Path: dst2}}, nil); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dst2, "stale.txt"), "stale")
	p, err := Run(ctx, Job{ID: "few", Source: Location{Path: src2}, Destination: Location{Path: dst2}, Sync: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Deletes != 1 {
		t.Fatalf("run = %+v, want 1 delete", p)
	}
	if n := countFiles(t, dst2); n != 20 {
		t.Fatalf("%d files left, want 20", n)
	}
}

// countFiles counts the files under root.
func countFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCheck(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := Check(ctx, Location{Path: dir}); err != nil {
		t.Fatalf("check of a folder: %v", err)
	}
	if err := Check(ctx, Location{Path: filepath.Join(dir, "missing")}); err != nil {
		t.Fatalf("check of a folder still to create: %v", err)
	}
	file := filepath.Join(dir, "file.txt")
	write(t, file, "x")
	if err := Check(ctx, Location{Path: file}); !errors.Is(err, ErrIsFile) {
		t.Fatalf("check of a file = %v, want ErrIsFile", err)
	}
}

func TestRunCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := t.TempDir()
	write(t, filepath.Join(src, "a.txt"), "alpha")
	_, err := Run(ctx, Job{ID: "canceled", Source: Location{Path: src}, Destination: Location{Path: t.TempDir()}}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestFolders(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write(t, filepath.Join(root, "photos", "2024", "a.jpg"), "a")
	write(t, filepath.Join(root, "photos", "2025", "b.jpg"), "b")
	write(t, filepath.Join(root, "photos", "notes.txt"), "n")

	got, err := Folders(ctx, Location{Path: filepath.Join(root, "photos")})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"2024", "2025"}) {
		t.Errorf("folders = %q", got)
	}
	if _, err := Folders(ctx, Location{Path: filepath.Join(root, "nope")}); !errors.Is(err, ErrFolderNotFound) {
		t.Errorf("missing folder: %v", err)
	}
}

func TestRunLogs(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a.txt"), "alpha")
	write(t, filepath.Join(src, "nested", "b.txt"), "bravo")

	var mu sync.Mutex
	var lines []Line
	job := Job{ID: "logged", Source: Location{Path: src}, Destination: Location{Path: dst},
		Log: func(l Line) { mu.Lock(); lines = append(lines, l); mu.Unlock() }}
	if _, err := Run(context.Background(), job, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var app, copied int
	for _, l := range lines {
		switch {
		case l.Source == "app":
			app++
		case l.Source == "rclone" && strings.Contains(l.Text, "Copied ("):
			copied++
		}
		if l.Time.IsZero() || l.Level == "" {
			t.Errorf("line without a time or a level: %+v", l)
		}
	}
	if app < 4 || copied != 2 {
		t.Errorf("%d app lines, %d copies logged; lines:\n%v", app, copied, lines)
	}
	if strings.Contains(fmt.Sprint(lines), "DEBUG") {
		t.Error("DEBUG lines kept")
	}

	// A job no more running hears nothing.
	n := len(lines)
	mu.Unlock()
	if _, err := Run(context.Background(), Job{ID: "quiet", Source: Location{Path: src}, Destination: Location{Path: t.TempDir()}}, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(lines) != n {
		t.Errorf("a finished job got %d more lines", len(lines)-n)
	}
}

func TestOpenSharesOutOfTime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := openShares(ctx, SMB{Host: "127.0.0.1", Port: 1, Username: "guest"}, []string{"a", "b"})
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("shares = %q, want the unprobed ones kept", got)
	}
}

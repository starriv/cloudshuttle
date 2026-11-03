package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
)

func TestProtectedDestinationThroughSymlink(t *testing.T) {
	s, _, done := newService(t)
	base := t.TempDir()
	s.dataDir = filepath.Join(base, "data")
	if err := os.Mkdir(s.dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(s.dataDir, alias); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	in := domain.TaskInput{Name: "alias", Source: domain.Location{Path: src}, Destination: domain.Location{Path: alias}, Mode: domain.ModeSync}
	for _, dest := range []string{alias, filepath.Join(alias, "not-created")} {
		in.Destination.Path = dest
		if _, err := s.CreateTask(context.Background(), in); domain.AsProblems(err).For(domain.FieldDestination) != domain.CodeForbiddenDestination {
			t.Fatalf("destination %s: %v", dest, err)
		}
	}
	// A legacy or externally changed task must also be checked at execution.
	task := taskFrom(domain.Task{ID: domain.NewID()}, in, time.Now())
	task.Destination.Path = alias
	if err := s.store.PutTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(s.dataDir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), task.ID, RunOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, done); run.Status != domain.StatusFailed {
		t.Fatalf("run=%+v", run)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatal(err)
	}
}

func TestRealPathAndAliasOverlap(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	base := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(base, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(ctx, domain.TaskInput{Name: "same", Mode: domain.ModeCopy, Source: domain.Location{Path: base}, Destination: domain.Location{Path: alias}}); domain.AsProblems(err).For(domain.FieldDestination) != domain.CodeSameLocation {
		t.Fatalf("same location: %v", err)
	}
	if _, err := s.CreateTask(ctx, domain.TaskInput{Name: "first", Mode: domain.ModeSync, Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: base}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(ctx, domain.TaskInput{Name: "second", Mode: domain.ModeCopy, Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: filepath.Join(alias, "new")}}); domain.AsProblems(err).For(domain.FieldDestination) != domain.CodeSharedDestination {
		t.Fatalf("alias overlap: %v", err)
	}
	dangling := filepath.Join(t.TempDir(), "dangling")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	if _, err := realPath(dangling); err == nil {
		t.Fatal("accepted dangling symlink")
	}
}

func TestOrphanCleanupKeepsOwnActiveRun(t *testing.T) {
	s, _, done := newService(t)
	ctx := context.Background()
	gate := make(chan struct{})
	entered := make(chan struct{})
	old := beforeRun
	beforeRun = func() { close(entered); <-gate }
	defer func() { beforeRun = old }()
	task, err := s.CreateTask(ctx, domain.TaskInput{Name: "live", Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: t.TempDir()}, Mode: domain.ModeCopy})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Run(ctx, task.ID, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	s.cancelOrphans(ctx, false)
	stored, err := s.store.Run(ctx, run.ID)
	close(gate)
	wait(t, done)
	if err != nil || stored.Status != domain.StatusRunning {
		t.Fatalf("live run changed: %+v, %v", stored, err)
	}
}

func TestRunDueCancellationReachesQueuedRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file locks")
	}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	p := openProcess(t, t.TempDir(), Options{Owner: OwnerScheduler, Now: func() time.Time { return now }})
	ctx := context.Background()
	task, err := p.CreateTask(ctx, domain.TaskInput{Name: "due", Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: t.TempDir()}, Mode: domain.ModeCopy, Schedule: domain.Schedule{Repeat: domain.RepeatHourly, Every: 1}})
	if err != nil {
		t.Fatal(err)
	}
	task.ScheduleSince = now.Add(-2 * time.Hour)
	if err := p.store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	lock, ok, err := tryLock(p.lockPath("transfer"))
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer lock.unlock()
	runctx, cancel := context.WithCancel(ctx)
	defer cancel()
	exited := make(chan error, 1)
	go func() { exited <- p.RunDue(runctx) }()
	eventually(t, "queued run", func() bool { return len(p.Active()) > 0 })
	cancel()
	select {
	case err := <-exited:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not stop")
	}
	if run := wait(t, p.finished); run.Status != domain.StatusCanceled {
		t.Fatalf("run=%+v", run)
	}
}

func TestRestoreChecksUnpolledForeignRun(t *testing.T) {
	p := openProcess(t, t.TempDir(), Options{})
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := p.store.BackupConfig(ctx, path); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: domain.NewID(), TaskID: domain.NewID(), TaskName: "foreign", Status: domain.StatusRunning, StartedAt: time.Now()}
	if err := p.store.StartRun(ctx, run, OwnerScheduler, true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := p.RestoreBackup(ctx, path, ""); !errors.Is(err, domain.ErrRunsActive) {
		t.Fatalf("restore during foreign run: %v", err)
	}
}

func TestMaintenanceLockSerializesProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file locks")
	}
	dir := t.TempDir()
	app := openProcess(t, dir, Options{})
	scheduler := openProcess(t, dir, Options{Owner: OwnerScheduler})
	ctx := context.Background()
	task, err := app.CreateTask(ctx, domain.TaskInput{Name: "test", Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: t.TempDir()}, Mode: domain.ModeCopy})
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := app.lockMaintenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	localContext, localCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer localCancel()
	if _, err := app.lockMaintenance(localContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("local maintenance wait ignored cancellation: %v", err)
	}
	deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := scheduler.Run(deadline, task.ID, RunOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run bypassed maintenance: %v", err)
	}
	live, err := app.store.LiveRuns(ctx)
	if err != nil || len(live) != 0 {
		t.Fatalf("admitted run: %+v %v", live, err)
	}
}

func TestLockErrorsFailClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file locks")
	}
	p := openProcess(t, t.TempDir(), Options{})
	for _, name := range []string{"app", "scheduler", "transfer", "maintenance"} {
		if err := os.Mkdir(p.lockPath(name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if p.lockOwn(context.Background()) {
		t.Fatal("process lock error accepted")
	}
	if !p.alive(OwnerScheduler) {
		t.Fatal("unknown ownership considered dead")
	}
	if err := p.takeSlot(context.Background()); err == nil {
		p.releaseSlot()
		t.Fatal("transfer lock error accepted")
	}
	if len(p.slot) != 0 {
		t.Fatal("failed lock leaked in-process slot")
	}
	if _, err := p.lockMaintenance(context.Background()); err == nil {
		t.Fatal("maintenance lock error accepted")
	}
	if err := p.Start(context.Background()); err == nil {
		t.Fatal("Start accepted missing ownership")
	}
}

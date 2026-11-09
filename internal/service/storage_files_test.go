package service

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
)

func TestStorageBrowserWritesRespectTaskAndTransferLocks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires cross-process file locks")
	}
	data := t.TempDir()
	app := openProcess(t, data, Options{})
	scheduler := openProcess(t, data, Options{Owner: OwnerScheduler})
	ctx := context.Background()
	st, err := app.CreateStorage(ctx, domain.StorageInput{
		Name: "NAS", Kind: domain.KindSMB, Host: "127.0.0.1", Port: "1", Username: "guest",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := app.CreateTask(ctx, domain.TaskInput{
		Name: "Backup", Mode: domain.ModeCopy, AllowRoot: true,
		Source:      domain.Location{Path: t.TempDir()},
		Destination: domain.Location{StorageID: st.ID, Path: "Files/backup"},
	})
	if err != nil {
		t.Fatal(err)
	}
	change := StorageChange{Source: "Files/BACKUP/a.txt", Delete: true, ExpectedSource: &domain.StorageEntry{Name: "a.txt"}}
	var conflict *StorageTaskConflict
	if err := app.ChangeStorage(ctx, st, change); !errors.As(err, &conflict) ||
		len(conflict.Destinations) != 1 || conflict.Destinations[0].ID != task.ID || len(conflict.Sources) != 0 {
		t.Fatalf("destination warning: %v", err)
	}
	if err := scheduler.takeSlot(ctx); err != nil {
		t.Fatal(err)
	}
	defer scheduler.releaseSlot()
	change.ConfirmedTasks = conflict.Tasks()
	deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := app.ChangeStorage(deadline, st, change); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("write did not wait for the scheduler: %v", err)
	}
	for _, change := range []StorageChange{
		{Source: "Files", Directory: true, Delete: true},
		{Source: "Files/.CLOUDSHUTTLE-TRASH/a.txt", Delete: true, ExpectedSource: &domain.StorageEntry{Name: "a.txt"}},
		{Source: "Files/a.txt", Target: "OtherShare/a.txt"},
	} {
		if err := app.ChangeStorage(ctx, st, change); !errors.Is(err, domain.ErrStorageProtected) {
			t.Fatalf("accepted protected path %+v: %v", change, err)
		}
	}
}

// Changing a task's source is confirmed too: its next run carries the
// change to the destination, and a sync removes there what was removed here.
func TestStorageBrowserWarnsAboutTaskSources(t *testing.T) {
	data := t.TempDir()
	app := openProcess(t, data, Options{})
	ctx := context.Background()
	st, err := app.CreateStorage(ctx, domain.StorageInput{
		Name: "NAS", Kind: domain.KindSMB, Host: "127.0.0.1", Port: "1", Username: "guest",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := app.CreateTask(ctx, domain.TaskInput{
		Name: "Photos", Mode: domain.ModeSync,
		Source:      domain.Location{StorageID: st.ID, Path: "Files/photos"},
		Destination: domain.Location{Path: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	change := StorageChange{Source: "Files/photos/a.jpg", Target: "Files/a.jpg"}
	var conflict *StorageTaskConflict
	if err := app.ChangeStorage(ctx, st, change); !errors.As(err, &conflict) ||
		len(conflict.Sources) != 1 || conflict.Sources[0].ID != task.ID || len(conflict.Destinations) != 0 {
		t.Fatalf("source warning: %v", err)
	}
	change.ConfirmedTasks = conflict.Tasks()
	if err := app.checkBrowserTasks(ctx, st.ID, change); err != nil {
		t.Fatalf("confirmed change still warned: %v", err)
	}
	other := StorageChange{Source: "Files/other/a.jpg", Target: "Files/a.jpg"}
	if err := app.checkBrowserTasks(ctx, st.ID, other); err != nil {
		t.Fatalf("warned about a change outside the task: %v", err)
	}
}

// A change that waits for a run says so, and says when its turn comes.
func TestStorageChangeReportsWhileQueued(t *testing.T) {
	data := t.TempDir()
	app := openProcess(t, data, Options{})
	ctx := context.Background()
	st, err := app.CreateStorage(ctx, domain.StorageInput{
		Name: "NAS", Kind: domain.KindSMB, Host: "127.0.0.1", Port: "1", Username: "guest",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A stale copy stops the change as soon as it holds the slot, before
	// it reaches the network.
	stale := st
	stale.UpdatedAt = st.UpdatedAt.Add(-time.Hour)
	queued := make(chan bool, 4)
	change := StorageChange{Target: "Files/new", Directory: true, Queued: func(q bool) { queued <- q }}

	if err := app.takeSlot(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.ChangeStorage(ctx, stale, change) }()
	select {
	case q := <-queued:
		if !q {
			t.Fatal("first report was not queued")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no report while the slot was taken")
	}
	app.releaseSlot()
	if err := <-done; !errors.Is(err, domain.ErrStorageChanged) {
		t.Fatalf("change = %v, want ErrStorageChanged", err)
	}
	if q := <-queued; q {
		t.Fatal("second report was still queued")
	}

	// A free slot is taken without a report.
	if err := app.ChangeStorage(ctx, stale, change); !errors.Is(err, domain.ErrStorageChanged) {
		t.Fatalf("change = %v, want ErrStorageChanged", err)
	}
	if len(queued) != 0 {
		t.Fatalf("reported %v with a free slot", <-queued)
	}
}

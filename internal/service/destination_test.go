package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/starriv/cloudshuttle/internal/domain"
)

func TestFolderDestinationStaysFixedAcrossRunsAndEdits(t *testing.T) {
	for _, mode := range []domain.Mode{domain.ModeCopy, domain.ModeSync} {
		t.Run(string(mode), func(t *testing.T) {
			s, _, done := newService(t)
			ctx := context.Background()
			src := filepath.Join(t.TempDir(), "devops")
			if err := os.Mkdir(src, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0600); err != nil {
				t.Fatal(err)
			}
			parent := t.TempDir()
			in := domain.TaskInput{Name: "Folder", Source: domain.Location{Path: src}, Destination: domain.Location{Path: parent}, Mode: mode}
			task, err := s.CreateTask(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if task.AllowRoot || task.DestinationFolder != "devops" || filepath.Base(task.Destination.Path) != "devops" {
				t.Fatalf("task = %+v", task)
			}
			for range 2 {
				if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
					t.Fatal(err)
				}
				if r := wait(t, done); r.Status != domain.StatusSuccess {
					t.Fatalf("run = %+v", r)
				}
			}
			if _, err := os.Stat(filepath.Join(parent, "a.txt")); !os.IsNotExist(err) {
				t.Fatalf("root was written: %v", err)
			}
			if _, err := os.Stat(filepath.Join(parent, "devops", "a.txt")); err != nil {
				t.Fatal(err)
			}
			in.Name = "Renamed"
			updated, err := s.UpdateTask(ctx, task.ID, in)
			if err != nil || updated.Destination != task.Destination {
				t.Fatalf("edited = %+v, %v", updated, err)
			}
			got, err := s.store.Task(ctx, task.ID)
			if err != nil || got.DestinationFolder != "devops" || got.AllowRoot || got.DestinationPending {
				t.Fatalf("stored = %+v, %v", got, err)
			}
			updated, err = s.UpdateTask(ctx, task.ID, taskInput(got))
			if err != nil || updated.Destination != task.Destination {
				t.Fatalf("final input = %+v, %v", updated, err)
			}
		})
	}
}

func TestDestinationCollisionRequiresChoiceAndNumbersPastFiles(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "devops"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "devops01"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	in := domain.TaskInput{Name: "Unique", Source: domain.Location{Path: filepath.Join(t.TempDir(), "devops")}, Destination: domain.Location{Path: parent}, Mode: domain.ModeCopy}
	_, err := s.CreateTask(ctx, in)
	var collision *DestinationExists
	if !errors.As(err, &collision) || !collision.Directory {
		t.Fatalf("collision = %v", err)
	}
	if tasks, _ := s.Tasks(ctx); len(tasks) != 0 {
		t.Fatal("saved without choice")
	}
	in.DestinationChoice = "unique"
	task, err := s.CreateTask(ctx, in)
	if err != nil || task.DestinationFolder != "devops02" {
		t.Fatalf("unique = %+v, %v", task, err)
	}
	in.Name = "Reserved"
	task, err = s.CreateTask(ctx, in)
	if err != nil || task.DestinationFolder != "devops03" {
		t.Fatalf("reserved = %+v, %v", task, err)
	}
	in.Name, in.DestinationChoice = "Reuse", "reuse"
	task, err = s.CreateTask(ctx, in)
	if err != nil || task.DestinationFolder != "devops" {
		t.Fatalf("reuse = %+v, %v", task, err)
	}
	// A file can never be approved as a destination directory.
	in.Name, in.Source.Path = "File", filepath.Join(t.TempDir(), "devops01")
	_, err = s.CreateTask(ctx, in)
	if !errors.As(err, &collision) || collision.Directory {
		t.Fatalf("file = %v", err)
	}
	// Explicit direct transfer retains the selected path.
	in.Name, in.AllowRoot = "Direct", true
	task, err = s.CreateTask(ctx, in)
	if err != nil || task.DestinationFolder != "" || !task.AllowRoot || filepath.Base(task.Destination.Path) != filepath.Base(parent) {
		t.Fatalf("direct = %+v, %v", task, err)
	}
}

func TestDestinationSymlinksCannotEscapeSelectedParent(t *testing.T) {
	for _, dangling := range []bool{false, true} {
		t.Run(fmt.Sprint("dangling=", dangling), func(t *testing.T) {
			s, _, _ := newService(t)
			parent, outside := t.TempDir(), t.TempDir()
			if dangling {
				outside = filepath.Join(outside, "missing")
			}
			if err := os.Symlink(outside, filepath.Join(parent, "devops")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(parent, "devops01")); err != nil {
				t.Fatal(err)
			}
			in := domain.TaskInput{Name: "Links", Source: domain.Location{Path: filepath.Join(t.TempDir(), "devops")}, Destination: domain.Location{Path: parent}, Mode: domain.ModeCopy}
			for _, choice := range []string{"", "reuse"} {
				in.DestinationChoice = choice
				_, err := s.CreateTask(context.Background(), in)
				var collision *DestinationExists
				if !errors.As(err, &collision) || collision.Directory {
					t.Fatalf("choice %q: %v", choice, err)
				}
			}
			in.DestinationChoice = "unique"
			task, err := s.CreateTask(context.Background(), in)
			if err != nil || task.DestinationFolder != "devops02" || !task.DestinationPending {
				t.Fatalf("unique=%+v,%v", task, err)
			}
		})
	}
}

func TestFirstTransferStopsIfDestinationAppearsAndCanBeReapproved(t *testing.T) {
	for _, mode := range []domain.Mode{domain.ModeCopy, domain.ModeSync} {
		t.Run(string(mode), func(t *testing.T) {
			s, _, done := newService(t)
			ctx := context.Background()
			src := filepath.Join(t.TempDir(), "devops")
			if err := os.Mkdir(src, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("new contents"), 0600); err != nil {
				t.Fatal(err)
			}
			in := domain.TaskInput{Name: "Late", Source: domain.Location{Path: src}, Destination: domain.Location{Path: t.TempDir()}, Mode: mode}
			task, err := s.CreateTask(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if !task.DestinationPending {
				t.Fatal("new target already initialized")
			}
			if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual, DryRun: true}); err != nil {
				t.Fatal(err)
			}
			if r := wait(t, done); r.Status != domain.StatusSuccess {
				t.Fatalf("preview=%+v", r)
			}
			if _, err := os.Lstat(task.Destination.Path); !os.IsNotExist(err) {
				t.Fatalf("preview made destination: %v", err)
			}
			if err := os.MkdirAll(task.Destination.Path, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(task.Destination.Path, "a.txt")
			if err := os.WriteFile(file, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual, Force: true}); err != nil {
				t.Fatal(err)
			}
			if r := wait(t, done); r.Status != domain.StatusFailed || r.Problem != domain.RunProblemDestinationChanged {
				t.Fatalf("late collision=%+v", r)
			}
			if b, err := os.ReadFile(file); err != nil || string(b) != "old" {
				t.Fatalf("changed existing contents: %q,%v", b, err)
			}
			_, err = s.UpdateTask(ctx, task.ID, in)
			var collision *DestinationExists
			if !errors.As(err, &collision) || !collision.Directory {
				t.Fatalf("editing did not ask: %v", err)
			}
			in.DestinationChoice = "reuse"
			task, err = s.UpdateTask(ctx, task.ID, in)
			if err != nil || task.DestinationPending {
				t.Fatalf("reapproved=%+v,%v", task, err)
			}
			if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
				t.Fatal(err)
			}
			if r := wait(t, done); r.Status != domain.StatusSuccess {
				t.Fatalf("approved run=%+v", r)
			}
			if b, err := os.ReadFile(file); err != nil || string(b) != "new contents" {
				t.Fatalf("approved did not update: %q,%v", b, err)
			}
		})
	}
}

func TestInitializedChildCannotBeReplacedWithSymlink(t *testing.T) {
	s, _, done := newService(t)
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "devops")
	os.Mkdir(src, 0700)
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("data"), 0600)
	task, err := s.CreateTask(ctx, domain.TaskInput{Name: "Replace", Source: domain.Location{Path: src}, Destination: domain.Location{Path: t.TempDir()}, Mode: domain.ModeCopy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	if r := wait(t, done); r.Status != domain.StatusSuccess {
		t.Fatalf("first=%+v", r)
	}
	if err := os.Rename(task.Destination.Path, task.Destination.Path+"-old"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, task.Destination.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); domain.AsProblems(err).For(domain.FieldDestination) != domain.CodeForbiddenDestination {
		t.Fatalf("replacement symlink was not refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("wrote outside: %v", err)
	}
}

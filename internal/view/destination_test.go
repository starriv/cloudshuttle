package view

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/locales"
)

func TestTaskFormChoosesNumberedDestination(t *testing.T) {
	h := newHarness(t)
	h.newTask("Folder")
	if h.app.taskForm.allowRoot {
		t.Fatal("direct transfer defaults to on")
	}
	src := filepath.Join(t.TempDir(), "devops")
	if err := os.Mkdir(src, 0700); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "devops"), 0700); err != nil {
		t.Fatal(err)
	}
	h.app.taskForm.source.path, h.app.taskForm.dest.path = src, parent
	h.click(locales.TaskSave())
	h.expect(locales.TaskDestinationExists())
	if len(h.app.tasks) != 0 {
		t.Fatal("collision saved prematurely")
	}
	h.click(locales.TaskDestinationUnique())
	h.waitFor("numbered task saved", func() bool { return !h.app.taskForm.open })
	h.settle()
	if len(h.app.tasks) != 1 || h.app.tasks[0].DestinationFolder != "devops01" {
		t.Fatalf("tasks = %+v", h.app.tasks)
	}
	task := h.app.tasks[0]
	h.app.openTaskForm(&task)
	if filepath.Base(h.app.taskForm.dest.path) != filepath.Base(parent) {
		t.Fatal("editing appends the child twice")
	}
	h.settle()
	h.click(locales.TaskSave())
	if h.app.taskForm.open {
		t.Fatal("editing asked about its own folder")
	}
}

func TestOldSaveCallbackCannotChangeNewTaskForm(t *testing.T) {
	for _, kind := range []string{"success", "validation", "collision"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			h.newTask("First")
			updates := 2
			if kind == "validation" {
				h.app.taskForm.name = ""
				updates = 1
			}
			if kind == "collision" {
				src := filepath.Join(t.TempDir(), "devops")
				os.Mkdir(src, 0700)
				parent := t.TempDir()
				os.Mkdir(filepath.Join(parent, "devops"), 0700)
				h.app.taskForm.source.path, h.app.taskForm.dest.path = src, parent
				updates = 1
			}
			h.app.saveTask()
			// Hold all queued UI work until a new form replaces the old instance.
			callbacks := make([]func(), 0, updates)
			for range updates {
				select {
				case fn := <-h.host.updates:
					callbacks = append(callbacks, fn)
				case <-time.After(5 * time.Second):
					t.Fatal("save did not return")
				}
			}
			h.app.taskForm.open = false
			h.app.openTaskForm(nil)
			h.app.taskForm.name = "Second"
			for _, fn := range callbacks {
				fn()
			}
			if !h.app.taskForm.open || h.app.taskForm.name != "Second" || h.app.taskForm.collision != nil || len(h.app.taskForm.problems) > 0 || h.app.taskForm.saving {
				t.Fatalf("stale callback changed new form: %+v", h.app.taskForm)
			}
		})
	}
}

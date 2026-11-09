package view

import (
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/locales"
)

// segmentsSpan returns how wide the segments from first to last are in the
// form, and how wide the fields' column is, which the mode's hint fills.
func segmentsSpan(t *testing.T, h *harness, first, last string) (width, column float32) {
	t.Helper()
	a, ok := h.tt.Find(first)
	if !ok {
		t.Fatalf("no %q", first)
	}
	b, ok := h.tt.Find(last)
	if !ok {
		t.Fatalf("no %q", last)
	}
	hint, ok := h.tt.Find(locales.ModeCopyHint())
	if !ok {
		t.Fatal("no mode hint")
	}
	return (b.X + b.W) - a.X, hint.W
}

func TestTheDirectTransferOptionIsACheckBoxWithItsOwnLabel(t *testing.T) {
	h := newHarness(t)
	h.newTask("Devops")
	h.settle()
	// The option is a check box whose text is its label, so a click on the
	// text toggles it; the hint is below, in the controls' column.
	before := h.app.taskForm.allowRoot
	h.click(locales.TaskAllowRoot())
	if h.app.taskForm.allowRoot == before {
		t.Fatal("clicking the label did not toggle the option")
	}
	label, ok := h.tt.Find(locales.TaskAllowRoot())
	hint, hintOK := h.tt.Find(locales.TaskAllowRootHint())
	if !ok || !hintOK {
		t.Fatalf("label at %v (%v), hint at %v (%v)", label, ok, hint, hintOK)
	}
	if hint.Y <= label.Y {
		t.Errorf("the hint is at %v, not below the label at %v", hint, label)
	}
	if hint.X > label.X {
		t.Errorf("the hint starts at %v, inside the label at %v", hint.X, label.X)
	}
}

// TestTheSegmentsKeepTheirLabelWidth keeps the segmented controls, which
// choose the mode and the schedule, as wide as their labels: a field would
// otherwise stretch one across the whole column, like a text input.
func TestTheSegmentsKeepTheirLabelWidth(t *testing.T) {
	cases := []struct {
		name, first, last string
	}{
		{"mode", locales.ModeCopy(), locales.ModeSync()},
		{"schedule", locales.RepeatOff(), locales.RepeatMonthly()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.newTask("Devops")
			h.settle()
			width, column := segmentsSpan(t, h, tc.first, tc.last)
			if width > column*0.8 {
				t.Errorf("the segments span %v of the column's %v", width, column)
			}
		})
	}
}

func TestTaskSaveShowsValidationBelowScrolledForm(t *testing.T) {
	h := newHarness(t)
	h.tt.SetSize(940, 660)
	h.newTask("Incomplete")
	h.app.taskForm.name = ""
	h.tt.Frame()
	h.tt.Scroll(470, 360, 0, 1800)
	h.tt.Advance(time.Second)
	h.tt.Frame()
	h.click(locales.TaskSave())
	if !h.app.taskForm.open {
		t.Fatal("invalid task closed the form")
	}
	summary, ok := h.tt.Find(locales.TaskFixFields())
	if !ok || summary.W <= 0 || summary.H <= 0 || summary.Y+summary.H > 660 {
		t.Fatalf("validation summary is not visible: %v (%t)", summary, ok)
	}
}

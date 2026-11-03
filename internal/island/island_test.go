package island

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/locales"
)

var notched = Geometry{NotchWidth: 180, BarHeight: 32}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newIsland(t *testing.T) (*Island, *ui.Tester, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)}
	i := New(notched)
	i.now = clk.now
	w, h := notched.Window()
	return i, ui.NewTester(i.View, w, h), clk
}

func running(id string, done, total int64) service.Active {
	return service.Active{
		Run:      domain.Run{ID: id, TaskID: "t-" + id, TaskName: "Photos", Status: domain.StatusRunning},
		Progress: engine.Progress{Bytes: done, TotalBytes: total},
	}
}

func TestHiddenWithNothingToShow(t *testing.T) {
	i, tt, _ := newIsland(t)
	tt.Frame()
	if i.Visible() || len(tt.Texts()) != 0 {
		t.Fatalf("visible %v, texts %q", i.Visible(), tt.Texts())
	}
}

func TestOpensAsARunStartsThenCompacts(t *testing.T) {
	i, tt, clk := newIsland(t)
	i.SetRuns([]service.Active{running("a", 25, 100)})
	tt.Frame()
	if i.mode() != expanded || !tt.HasText("Photos") || !tt.HasText("25%") {
		t.Fatalf("mode %d, texts %q", i.mode(), tt.Texts())
	}

	clk.t = clk.t.Add(startedFor)
	i.SetRuns([]service.Active{running("a", 50, 100)}) // the same run: no reopening
	tt.Frame()
	if i.mode() != compact || tt.HasText("Photos") || !tt.HasText("50%") {
		t.Fatalf("mode %d, texts %q", i.mode(), tt.Texts())
	}

	i.SetRuns([]service.Active{running("a", 60, 100), running("b", 0, 0)})
	clk.t = clk.t.Add(startedFor)
	tt.Frame()
	if !tt.HasText(locales.IslandMore(1)) {
		t.Fatalf("two runs: texts %q", tt.Texts())
	}
}

func TestOpensToTellHowARunEnded(t *testing.T) {
	i, tt, clk := newIsland(t)
	i.SetRuns([]service.Active{running("a", 0, 0)})
	clk.t = clk.t.Add(startedFor)
	i.SetRuns(nil)
	i.Finished(domain.Run{ID: "a", TaskName: "Photos", Status: domain.StatusFailed, Message: "403 Forbidden"})
	tt.Frame()
	if i.mode() != expanded || !tt.HasText(locales.NotifyFailedTitle("Photos")) || !tt.HasText("403 Forbidden") {
		t.Fatalf("mode %d, texts %q", i.mode(), tt.Texts())
	}

	clk.t = clk.t.Add(finishedFor)
	tt.Frame()
	if i.Visible() {
		t.Fatal("still visible after telling")
	}
}

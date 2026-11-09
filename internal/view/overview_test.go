package view

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/locales"
)

func TestWeekStartIsMidnightSixDaysBeforeToday(t *testing.T) {
	now := time.Date(2026, 11, 6, 15, 30, 0, 0, time.UTC)
	if got, want := weekStart(now), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("weekStart = %v, want %v", got, want)
	}
	// It goes by the calendar, not by 168 hours.
	midnight := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	if got, want := weekStart(midnight), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("weekStart at midnight = %v, want %v", got, want)
	}
}

func TestLastDaysCountTheSevenCalendarDays(t *testing.T) {
	now := time.Date(2026, 11, 6, 15, 0, 0, 0, time.UTC)
	start := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	nov4 := time.Date(2026, 11, 4, 23, 59, 59, 0, time.UTC)
	a := &App{now: func() time.Time { return now }, week: []domain.Run{
		{StartedAt: start, Status: domain.StatusSuccess},
		{StartedAt: start.Add(-time.Second), Status: domain.StatusFailed}, // before the first bar
		{StartedAt: nov4, Status: domain.StatusFailed},
		{StartedAt: now, Status: domain.StatusSuccess},
	}}
	days := a.lastDays()
	if len(days) != 7 {
		t.Fatalf("days = %d, want 7", len(days))
	}
	if !days[0].start.Equal(start) || !days[6].start.Equal(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("days start %v, want %v through today", days[0].start, start)
	}
	if days[0].runs != 1 || days[0].failed != 0 {
		t.Errorf("first day = %+v, want one success", days[0])
	}
	if days[4].runs != 1 || days[4].failed != 1 {
		t.Errorf("Nov 4 = %+v, want one failure", days[4])
	}
	if days[6].runs != 1 || days[6].failed != 0 {
		t.Errorf("today = %+v, want one success", days[6])
	}
	var runs, failed int
	for _, d := range days {
		runs, failed = runs+d.runs, failed+d.failed
	}
	if runs != 3 || failed != 1 {
		t.Errorf("week = %d runs, %d failed; want 3 and 1", runs, failed)
	}
}

func TestRecentGroupsFoldOnlyConsecutiveSuccesses(t *testing.T) {
	a := &App{recent: []domain.Run{
		{ID: "a3", TaskID: "A", Status: domain.StatusSuccess},
		{ID: "a2", TaskID: "A", Status: domain.StatusSuccess},
		{ID: "a1", TaskID: "A", Status: domain.StatusSuccess},
		{ID: "af", TaskID: "A", Status: domain.StatusFailed},
		{ID: "b2", TaskID: "B", Status: domain.StatusSuccess},
		{ID: "b1", TaskID: "B", Status: domain.StatusSuccess},
	}}
	groups := a.recentGroups()
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	if got := []int{len(groups[0]), len(groups[1]), len(groups[2])}; fmt.Sprint(got) != "[3 1 2]" {
		t.Fatalf("group sizes = %v, want [3 1 2]", got)
	}
	if groups[0][0].ID != "a3" || groups[0][2].ID != "a1" {
		t.Errorf("first group = %+v, want newest first", groups[0])
	}
	if groups[2][0].ID != "b2" {
		t.Errorf("last group = %+v, want B's newest", groups[2])
	}
}

func TestRecentGroupsStopAtTheRowsTheOverviewShows(t *testing.T) {
	runs := make([]domain.Run, 0, recentRuns+4)
	for i := range recentRuns + 4 {
		runs = append(runs, domain.Run{ID: fmt.Sprint(i), TaskID: fmt.Sprint(i), Status: domain.StatusFailed})
	}
	if got := (&App{recent: runs}).recentGroups(); len(got) != recentRuns {
		t.Fatalf("groups = %d, want %d", len(got), recentRuns)
	}
}

func TestOverviewHealthUsesLatestRealRun(t *testing.T) {
	for _, tc := range []struct {
		name    string
		real    domain.Status
		preview bool
	}{
		{name: "never run"},
		{name: "preview only", preview: true},
		{name: "canceled then preview", real: domain.StatusCanceled, preview: true},
		{name: "failure then preview", real: domain.StatusFailed, preview: true},
		{name: "real success", real: domain.StatusSuccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			task, err := h.svc.CreateTask(ctx, domain.TaskInput{
				Name: "Backup", Mode: domain.ModeCopy, AllowRoot: true,
				Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: t.TempDir()},
			})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			addRun := func(id string, status domain.Status, dry bool, started time.Time) {
				run := domain.Run{ID: id, TaskID: task.ID, TaskName: task.Name, Mode: task.Mode, Trigger: domain.TriggerManual,
					Status: status, DryRun: dry, StartedAt: started, FinishedAt: started.Add(time.Second)}
				if err := h.db.InsertRun(ctx, run); err != nil {
					t.Fatal(err)
				}
				if err := h.db.FinishRun(ctx, run); err != nil {
					t.Fatal(err)
				}
			}
			if tc.real != "" {
				addRun("real", tc.real, false, now.Add(-time.Minute))
			}
			if tc.preview {
				addRun("preview", domain.StatusSuccess, true, now.Add(-time.Second))
			}
			h.app.Reload()
			h.settle()
			switch tc.real {
			case domain.StatusSuccess:
				h.expect(locales.OverviewSubtitleAll())
			case domain.StatusFailed:
				h.expect(locales.OverviewSubtitleFailing(1))
			default:
				h.expect(locales.OverviewSubtitleNeutral())
			}
			if tc.real != domain.StatusSuccess && h.tt.HasText(locales.OverviewAllSucceeded()) {
				t.Fatal("overview claimed all backups succeeded")
			}
		})
	}
}

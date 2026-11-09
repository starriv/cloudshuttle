package view

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine"
	"github.com/starriv/cloudshuttle/internal/service"
)

// TestScreenshots renders each page and dialog of the window, and writes
// them as PNGs, for looking at the design: set CLOUDSHUTTLE_SHOTS to the
// folder to write them into, and CLOUDSHUTTLE_SHOTS_DARK=1 for the dark
// appearance, CLOUDSHUTTLE_SHOTS_ZH=1 for Chinese.
func TestScreenshots(t *testing.T) {
	dir := os.Getenv("CLOUDSHUTTLE_SHOTS")
	if dir == "" {
		t.Skip("set CLOUDSHUTTLE_SHOTS=<folder> to write screenshots")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dark := os.Getenv("CLOUDSHUTTLE_SHOTS_DARK") != ""
	zh := os.Getenv("CLOUDSHUTTLE_SHOTS_ZH") != ""
	if zh {
		// The catalog's accessors read the app's language through i18n's
		// locale function, which a Tester's SetLocale does not set.
		i18n.SetLocaleFunc(func() string { return "zh-Hans" })
		t.Cleanup(func() { i18n.SetLocaleFunc(nil) })
	}

	h := newHarness(t)
	if dark {
		h.tt.SetDark(true)
	}
	if zh {
		h.tt.SetLocale("zh-Hans")
	}
	seedShowcase(t, h)

	shot := func(name string) {
		// A page change starts a transition: draw its first frame, let the
		// clock run past it, then draw the page at rest.
		h.tt.Frame()
		h.tt.Advance(600 * time.Millisecond)
		h.tt.Frame()
		img := h.tt.Image()
		path := filepath.Join(dir, name+".png")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
	}

	// Pages.
	for _, p := range []string{pageOverview, pageTasks, pageStorages, pageHistory, pageSettings} {
		h.app.page = p
		if p == pageHistory {
			h.app.reloadHistory()
			h.app.historyChosen.Clear()
			for i, r := range h.app.history {
				if i == 0 || i == 2 {
					h.app.historyChosen.Add(r.ID)
				}
			}
			h.app.historyRow = 0
		}
		shot("page-" + p)
	}

	// The settings further down, under the Updates and Backup cards.
	h.app.page = pageSettings
	h.tt.Frame()
	h.tt.Advance(600 * time.Millisecond)
	h.tt.Frame()
	h.tt.Scroll(660, 430, 0, 2400)
	h.tt.Advance(300 * time.Millisecond)
	h.tt.Frame()
	{
		img := h.tt.Image()
		f, err := os.Create(filepath.Join(dir, "page-settings-bottom.png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()
	}

	// Overview with a run in progress.
	h.app.page = pageOverview
	live := aLiveRun(t, h)
	h.app.running = []service.Active{live}
	h.app.active = map[string]service.Active{live.Run.TaskID: live}
	shot("page-overview-running")
	h.app.running, h.app.active = nil, map[string]service.Active{}

	// Empty states.
	empty := newHarness(t)
	if dark {
		empty.tt.SetDark(true)
	}
	if zh {
		empty.tt.SetLocale("zh-Hans")
	}
	for _, p := range []string{pageOverview, pageTasks, pageStorages, pageHistory} {
		empty.app.page = p
		empty.tt.Frame()
		empty.tt.Advance(600 * time.Millisecond)
		empty.tt.Frame()
		img := empty.tt.Image()
		f, err := os.Create(filepath.Join(dir, "empty-"+p+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()
	}

	// Dialogs.
	h.app.page = pageTasks
	h.app.openTaskForm(nil)
	shot("dialog-task-new")
	h.app.taskForm.exclude.open = true
	shot("dialog-task-exclude")
	h.app.openTaskForm(&h.app.tasks[0])
	shot("dialog-task-edit")
	h.app.taskForm.open = false
	h.app.openStorageForm(&h.app.storages[0])
	shot("dialog-storage")
	h.app.storageForm.open = false
	h.app.restoreForm = restoreForm{open: true}
	shot("dialog-restore")
	h.app.restoreForm.open = false
	h.app.exclude = settingsExcludeForm{open: true, text: strings.Join(h.app.settings.GlobalExcludes, "\n")}
	shot("dialog-exclude")
	h.app.exclude.open = false
	h.app.confirm = confirmation{
		open:    true,
		title:   "Delete \u201cPhotos\u201d?",
		message: "Its runs and logs go too. The files it copied stay where they are.",
	}
	shot("dialog-confirm")
	h.app.confirm.open = false

	// The history with one run in detail, and its log.
	h.app.page = pageHistory
	h.app.reloadHistory()
	h.app.historyChosen.Clear()
	if len(h.app.history) > 0 {
		r := h.app.history[0]
		h.app.historyChosen.Add(r.ID)
		h.app.historyRow = 0
	}
	h.settle()
	shot("page-history-detail")
}

// seedShowcase fills the app with the data a person's app might hold after a
// while: storages, tasks, runs over the last week and a run in progress.
func seedShowcase(t *testing.T, h *harness) {
	t.Helper()
	now := time.Now()
	ctx := context.Background()
	storages := []domain.Storage{
		{
			ID: "st-aws", Name: "AWS Backup", Kind: domain.KindS3, Provider: domain.ProviderAWS,
			Region: "us-east-1", Bucket: "home-backup", AccessKeyID: "AKIAIOSFODNN7EXAMPLE",
			Check: domain.Check{At: now.Add(-2 * time.Hour), OK: true},
		},
		{
			ID: "st-r2", Name: "Cloudflare R2", Kind: domain.KindS3, Provider: domain.ProviderCloudflare,
			Endpoint: "https://8f2a.region.r2.cloudflarestorage.com", Region: "auto",
			Bucket: "media", AccessKeyID: "R2ACCESSKEY",
			Check: domain.Check{At: now.Add(-26 * time.Hour), OK: false, Problem: domain.ProblemUnreachable},
		},
		{
			ID: "st-nas", Name: "Synology NAS", Kind: domain.KindSMB,
			Host: "synology.local", Port: 445, Username: "backup",
			Check: domain.Check{At: now.Add(-5 * time.Hour), OK: true},
		},
	}
	for _, s := range storages {
		s.CreatedAt, s.UpdatedAt = now.Add(-30*24*time.Hour), now
		if err := h.db.PutStorage(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	scheduled := func(s domain.Schedule) domain.Schedule { return s.Normalize() }
	tasks := []domain.Task{
		{
			ID: "tk-docs", Name: "Documents", AllowRoot: true,
			Source:      domain.Location{Path: "/Users/li/Documents"},
			Destination: domain.Location{StorageID: "st-aws", Path: "documents"},
			Mode:        domain.ModeSync,
			Schedule:    scheduled(domain.Schedule{Repeat: domain.RepeatDaily, Hour: 21, Minute: 0}),
			TrashDays:   domain.TrashFollow,
			Exclude:     domain.NewExclude(),
		},
		{
			ID: "tk-photos", Name: "Photos", AllowRoot: true,
			Source:      domain.Location{Path: "/Users/li/Pictures"},
			Destination: domain.Location{StorageID: "st-r2", Path: "photos"},
			Mode:        domain.ModeCopy,
			Schedule:    scheduled(domain.Schedule{Repeat: domain.RepeatWeekly, Days: domain.WeekdaysOf(time.Monday, time.Thursday), Hour: 3, Minute: 30}),
			TrashDays:   domain.TrashFollow,
			Exclude:     domain.NewExclude(),
		},
		{
			ID: "tk-projects", Name: "Project files", AllowRoot: true,
			Source:      domain.Location{Path: "/Users/li/Projects"},
			Destination: domain.Location{StorageID: "st-nas", Path: "home/backup/projects"},
			Mode:        domain.ModeSync,
			Schedule:    scheduled(domain.Schedule{Repeat: domain.RepeatHourly, Every: 4}),
			TrashDays:   30,
			Exclude:     domain.NewExclude(),
		},
		{
			ID: "tk-music", Name: "Music library", AllowRoot: true,
			Source:      domain.Location{Path: "/Users/li/Music"},
			Destination: domain.Location{StorageID: "st-aws", Path: "music"},
			Mode:        domain.ModeCopy,
			Schedule:    scheduled(domain.Schedule{Repeat: domain.RepeatOff}),
			TrashDays:   domain.TrashOff,
			Exclude:     domain.NewExclude(),
		},
	}
	for i := range tasks {
		tasks[i].ScheduleSince = now.Add(-30 * 24 * time.Hour)
		tasks[i].LastSuccessAt = now.Add(-20 * time.Hour)
		tasks[i].CreatedAt, tasks[i].UpdatedAt = now.Add(-40*24*time.Hour), now
		if err := h.db.PutTask(ctx, tasks[i]); err != nil {
			t.Fatal(err)
		}
	}

	// Runs over the last seven days: successes mostly, some failures and a
	// canceled one, so the chart and the lists have something to show.
	type runSpec struct {
		task      string
		status    domain.Status
		at        time.Time
		dur       time.Duration
		transfers int64
		size      int64
		deletes   int64
		dry       bool
	}
	var specs []runSpec
	day := func(daysAgo int, hour, minute int) time.Time {
		d := now.AddDate(0, 0, -daysAgo)
		return time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, d.Location())
	}
	for i := 0; i < 7; i++ {
		specs = append(specs, runSpec{"tk-docs", domain.StatusSuccess, day(i, 21, 0), 92 * time.Second, 1240, 8_400_000_000, 3, false})
	}
	specs = append(specs,
		runSpec{"tk-projects", domain.StatusSuccess, now.Add(-26 * time.Hour), 34 * time.Second, 86, 640_000_000, 0, false},
		runSpec{"tk-projects", domain.StatusSuccess, now.Add(-22 * time.Hour), 28 * time.Second, 41, 320_000_000, 1, false},
		runSpec{"tk-projects", domain.StatusSuccess, now.Add(-18 * time.Hour), 31 * time.Second, 12, 140_000_000, 0, false},
		runSpec{"tk-photos", domain.StatusSuccess, now.Add(-3 * 24 * time.Hour), 640 * time.Second, 4210, 32_000_000_000, 0, false},
		runSpec{"tk-photos", domain.StatusFailed, day(1, 3, 30), 12 * time.Second, 0, 0, 0, false},
		runSpec{"tk-music", domain.StatusCanceled, now.Add(-52 * time.Hour), 8 * time.Second, 22, 900_000_000, 0, false},
		runSpec{"tk-docs", domain.StatusSuccess, now.Add(-2 * time.Hour), 84 * time.Second, 1180, 7_900_000_000, 2, true},
	)
	for i, s := range specs {
		run := domain.Run{
			ID:         fmt.Sprintf("run-%02d", i),
			TaskID:     s.task,
			TaskName:   taskName(tasks, s.task),
			Mode:       domain.ModeCopy,
			Trigger:    domain.TriggerScheduled,
			DryRun:     s.dry,
			Status:     s.status,
			StartedAt:  s.at,
			FinishedAt: s.at.Add(s.dur),
			Stats: domain.Stats{
				Bytes: s.size, TotalBytes: s.size, Transfers: s.transfers, TotalTransfers: s.transfers,
				Checks: s.transfers * 2, Deletes: s.deletes,
				Speed: float64(s.size) / s.dur.Seconds(),
			},
		}
		if s.status == domain.StatusFailed {
			run.Message = "rclone: copy: connection reset by peer: synology.local:445"
		}
		if err := h.db.InsertRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := h.db.FinishRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.db.PutRunLog(ctx, "run-00", showcaseLog); err != nil {
		t.Fatal(err)
	}
	h.app.Reload()
	h.app.page = pageOverview
}

// showcaseLog is what a run's log looks like.
const showcaseLog = `03:03:05.837 INFO   app        task=Documents trigger=scheduled sync=false
03:03:05.837 INFO   app        source=/Users/li/Documents destination=AWS Backup: documents
03:03:05.839 INFO   rclone     rclone v1.71.2
03:03:05.840 INFO   rclone     documents/a.jpg: Copied (new) size=204800
03:03:05.841 INFO   rclone     documents/b.jpg: Copied (new) size=1048576
03:03:05.842 INFO   rclone     documents/notes.md: Copied (new) size=5120
03:03:06.020 INFO   rclone     documents/old/c.txt: Deleted
03:03:06.021 INFO   rclone     There was nothing to transfer
03:03:06.500 INFO   app        transferred 1240 files, 8.4 GB in 1m32s, 3 deleted
03:03:06.501 INFO   app        finished: success
`

func taskName(tasks []domain.Task, id string) string {
	for _, t := range tasks {
		if t.ID == id {
			return t.Name
		}
	}
	return ""
}

// aLiveRun is a run in progress, a third of the way through a transfer.
func aLiveRun(t *testing.T, h *harness) service.Active {
	t.Helper()
	run := domain.Run{
		ID: "run-live", TaskID: "tk-photos", TaskName: "Photos", Mode: domain.ModeCopy,
		Trigger: domain.TriggerManual, Status: domain.StatusRunning,
		StartedAt: time.Now().Add(-3 * time.Minute),
		Stats:     domain.Stats{TotalTransfers: 4210},
	}
	return service.Active{Run: run, Progress: engine.Progress{
		Bytes: 12_400_000_000, TotalBytes: 32_000_000_000,
		Transfers: 1420, TotalTransfers: 4210, Speed: 42_000_000,
	}}
}

var _ = ui.Toast{}

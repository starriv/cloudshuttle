package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine"
	"github.com/starriv/cloudshuttle/internal/format"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/internal/updates"
	"github.com/starriv/cloudshuttle/internal/view"
	"github.com/starriv/cloudshuttle/locales"
)

// fakeTray records the menus the tray is given, and whether it was destroyed.
type fakeTray struct {
	menus     []*mygo.Menu
	destroyed bool
}

func (f *fakeTray) SetMenu(m *mygo.Menu) { f.menus = append(f.menus, m) }
func (f *fakeTray) Destroy()             { f.destroyed = true }

// fakeTrayService stands in for the service, which needs a database.
type fakeTrayService struct{ active []service.Active }

func (f *fakeTrayService) Active() []service.Active { return f.active }

// stubTrayDate takes the app out of the menu's dates: tests run without it,
// and only a running app knows the user's regional formats.
func stubTrayDate(t *testing.T) {
	t.Helper()
	old := trayLocale
	trayLocale = func() string { return "en-US" }
	t.Cleanup(func() { trayLocale = old })
}

// trayItem finds the menu item of the row with key.
func trayItem(t *taskTray, key string) *mygo.MenuItem {
	for i, row := range t.rows {
		if row.key == key && i < len(t.items) {
			return t.items[i]
		}
	}
	return nil
}

func TestTrayStatus(t *testing.T) {
	for _, tt := range []struct {
		name string
		runs []service.Active
		want string
	}{
		{"idle", nil, locales.TrayIdle()},
		{"transferring", []service.Active{{}}, locales.TrayRunning(1)},
		{"waiting for another process", []service.Active{{Queued: true}}, locales.TrayQueued(1)},
		{"transfer and queue", []service.Active{{}, {Queued: true}, {Queued: true}}, locales.TrayActivity(1, 2)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := trayStatusText(tt.runs); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTrayTaskDetails(t *testing.T) {
	active := []service.Active{
		{Run: domain.Run{ID: "queued", TaskID: "q", TaskName: "Queue"}, Queued: true},
		{Run: domain.Run{ID: "live", TaskID: "a", TaskName: "Photos"}, Progress: engine.Progress{Bytes: 50, TotalBytes: 100, Speed: 20}},
	}
	recent := []domain.Run{
		{ID: "old-active", TaskID: "a", TaskName: "Photos", Status: domain.StatusSuccess},
		{ID: "failed", TaskID: "b", TaskName: "Backup", Status: domain.StatusFailed, DryRun: true},
		{ID: "older", TaskID: "b", TaskName: "Backup", Status: domain.StatusSuccess},
	}
	rows := taskTrayRows(active, recent, false, "en-US")
	if rows[1].run.ID != "live" {
		t.Fatal("transfer must precede the queue")
	}
	byKey := map[string]trayRow{}
	for _, r := range rows {
		byKey[r.key] = r
	}
	if text := byKey["progress/live"].label; !strings.Contains(text, "50%") || !strings.Contains(text, locales.Speed("20 B")) {
		t.Fatalf("missing progress: %q", text)
	}
	if got := byKey["active/queued"].label; !strings.Contains(got, locales.TaskWaiting()) {
		t.Fatalf("missing queue state: %q", got)
	}
	if got := byKey["recent/failed"]; got.run.ID != "failed" || !strings.Contains(got.label, locales.StatusFailed()) || !strings.Contains(got.label, locales.DryRunBadge()) {
		t.Fatalf("missing run identity/result: %+v", got)
	}
	for _, key := range []string{"recent/old-active", "recent/older"} {
		if _, ok := byKey[key]; ok {
			t.Fatalf("duplicate task %s", key)
		}
	}
}

func TestTrayUnknownProgressAndLoadError(t *testing.T) {
	rows := taskTrayRows([]service.Active{{Run: domain.Run{ID: "scan", TaskID: "a"}}}, nil, true, "en-US")
	var preparing, failed, empty bool
	for _, r := range rows {
		preparing = preparing || r.label == locales.TrayPreparing()
		failed = failed || r.key == "error"
		empty = empty || r.key == "empty"
	}
	if !preparing || !failed || empty {
		t.Fatalf("wrong scanning/error state: %+v", rows)
	}
}

func TestTrayUnknownTotalShowsWhatMoved(t *testing.T) {
	active := []service.Active{{
		Run:      domain.Run{ID: "unknown", TaskID: "a", TaskName: "Photos"},
		Progress: engine.Progress{Bytes: 4096, Speed: 1024},
	}}
	byKey := map[string]trayRow{}
	for _, r := range taskTrayRows(active, nil, false, "en-US") {
		byKey[r.key] = r
	}
	detail := byKey["progress/unknown"].label
	if detail == locales.TrayPreparing() {
		t.Fatal("progress without a total must not say it is checking files")
	}
	if !strings.Contains(detail, format.Bytes(4096)) || !strings.Contains(detail, locales.Speed(format.Bytes(1024))) {
		t.Fatalf("missing transferred bytes or speed: %q", detail)
	}
}

func TestTrayLimitsLists(t *testing.T) {
	var active []service.Active
	for i := 0; i < trayActiveRows+1; i++ {
		active = append(active, service.Active{
			Run:    domain.Run{ID: fmt.Sprintf("r%d", i), TaskID: fmt.Sprintf("t%d", i), TaskName: fmt.Sprintf("Task %d", i)},
			Queued: i == trayActiveRows,
		})
	}
	var recent []domain.Run
	for i := 0; i < trayRecentRows+2; i++ {
		recent = append(recent, domain.Run{
			ID: fmt.Sprintf("old%d", i), TaskID: fmt.Sprintf("u%d", i), TaskName: "Old",
			Status: domain.StatusSuccess, StartedAt: time.Now(), FinishedAt: time.Now(),
		})
	}
	rows := taskTrayRows(active, recent, false, "en-US")
	actives, recents, more := 0, 0, 0
	for _, r := range rows {
		switch {
		case strings.HasPrefix(r.key, "active/"):
			actives++
		case strings.HasPrefix(r.key, "recent/"):
			recents++
		case r.key == "more":
			more++
			if r.label != locales.TrayMore(1) || !r.opensWindow {
				t.Fatalf("more row = %+v, want a clickable %q", r, locales.TrayMore(1))
			}
		}
	}
	if actives != trayActiveRows {
		t.Fatalf("showed %d active runs, want %d", actives, trayActiveRows)
	}
	if recents != trayRecentRows {
		t.Fatalf("showed %d recent runs, want %d", recents, trayRecentRows)
	}
	if more != 1 {
		t.Fatalf("showed %d more rows, want 1", more)
	}
}

// TestTraySummaryIconIsNeutral guards the idle row, which must not look
// like a success when the last runs failed.
func TestTraySummaryIconIsNeutral(t *testing.T) {
	if got := taskTrayRows(nil, nil, false, "en-US")[0].icon; got != icons.Idle {
		t.Fatal("an idle menu must show the neutral icon")
	}
	if got := taskTrayRows([]service.Active{{Run: domain.Run{ID: "r"}}}, nil, false, "en-US")[0].icon; got != icons.Running {
		t.Fatal("a running menu must show the running icon")
	}
}

func TestTrayPercentFollowsTheLocale(t *testing.T) {
	active := []service.Active{{Run: domain.Run{ID: "r"}, Progress: engine.Progress{Bytes: 1, TotalBytes: 4}}}
	for locale, want := range map[string]string{"en-US": "25%", "de-DE": "25\u00a0%", "tr-TR": "%25"} {
		for _, row := range taskTrayRows(active, nil, false, locale) {
			if row.key == "progress/r" && !strings.HasPrefix(row.label, want) {
				t.Errorf("%s: progress = %q, want it to start with %q", locale, row.label, want)
			}
		}
	}
}

func TestTrayRunName(t *testing.T) {
	long := strings.Repeat("字", 40)
	if got := trayRunName(domain.Run{TaskName: long}); got != strings.Repeat("字", 17)+"…" {
		t.Fatalf("long CJK name = %q", got)
	}
	if got := trayRunName(domain.Run{TaskName: strings.Repeat("a", 40)}); got != strings.Repeat("a", 35)+"…" {
		t.Fatalf("long Latin name = %q", got)
	}
	if got := trayRunName(domain.Run{TaskName: strings.Repeat("a", 36)}); got != strings.Repeat("a", 36) {
		t.Fatalf("a name that fits = %q", got)
	}
	if got := trayRunName(domain.Run{TaskName: "  Photos \n NAS  "}); got != "Photos NAS" {
		t.Fatalf("name with space = %q", got)
	}
	if got, want := trayRunName(domain.Run{TaskName: "Photos", DryRun: true}), locales.TrayDetail("Photos", locales.DryRunBadge()); got != want {
		t.Fatalf("dry run = %q, want %q", got, want)
	}
	if got := trayRunName(domain.Run{TaskName: long, DryRun: true}); !strings.HasSuffix(got, locales.TrayDetail("…", locales.DryRunBadge())) {
		t.Fatalf("dry run of a long name = %q", got)
	}
}

func TestTrayMenuReusesItemsAsProgressMoves(t *testing.T) {
	stubTrayDate(t)
	fake := &fakeTray{}
	svc := &fakeTrayService{active: []service.Active{{
		Run:      domain.Run{ID: "r1", TaskID: "t1", TaskName: "Photos"},
		Progress: engine.Progress{Bytes: 1, TotalBytes: 10, Speed: 1},
	}}}
	tray := &taskTray{tray: fake, svc: svc, show: func() {}, openRun: func(domain.Run) {}}
	tray.set(view.Snapshot{Tasks: []domain.Task{{ID: "t1"}}, Storages: []domain.Storage{{ID: "s1"}}})
	if len(fake.menus) != 1 {
		t.Fatalf("set made %d menus, want 1", len(fake.menus))
	}
	if tray.stats != locales.TrayMiniStats(1, 1) {
		t.Fatalf("stats = %q, want one task and one storage", tray.stats)
	}
	if got := trayItem(tray, "stats"); got == nil || got.Label != tray.stats {
		t.Fatalf("stats row = %+v", got)
	}
	if got := trayItem(tray, "progress/r1"); got == nil || !strings.Contains(got.Label, "10%") {
		t.Fatalf("first progress = %+v", got)
	}

	svc.active[0].Progress = engine.Progress{Bytes: 5, TotalBytes: 10, Speed: 2}
	tray.update()
	if len(fake.menus) != 1 {
		t.Fatal("progress replaced the whole menu")
	}
	if got := trayItem(tray, "progress/r1"); got == nil || !strings.Contains(got.Label, "50%") {
		t.Fatalf("updated progress = %+v", got)
	}
}

func TestTrayMenuIsRebuiltWhenRunsChange(t *testing.T) {
	stubTrayDate(t)
	fake := &fakeTray{}
	svc := &fakeTrayService{active: []service.Active{{Run: domain.Run{ID: "r1", TaskID: "t1", TaskName: "Photos"}}}}
	tray := &taskTray{tray: fake, svc: svc, show: func() {}, openRun: func(domain.Run) {}}
	tray.set(view.Snapshot{})

	svc.active = append(svc.active, service.Active{Run: domain.Run{ID: "r2", TaskID: "t2", TaskName: "Backup"}})
	tray.update()
	if len(fake.menus) != 2 {
		t.Fatalf("a new run made %d menus in all, want 2", len(fake.menus))
	}
	if trayItem(tray, "active/r2") == nil {
		t.Fatal("the new run is not in the menu")
	}
}

func TestTrayMenuOpensRunsAndTheWindow(t *testing.T) {
	stubTrayDate(t)
	fake := &fakeTray{}
	svc := &fakeTrayService{active: []service.Active{{Run: domain.Run{ID: "r1", TaskID: "t1", TaskName: "Photos"}}}}
	var opened string
	shown := 0
	tray := &taskTray{tray: fake, svc: svc, show: func() { shown++ }, openRun: func(r domain.Run) { opened = r.ID }}
	tray.set(view.Snapshot{Recent: []domain.Run{{
		ID: "f1", TaskID: "t2", TaskName: "Backup", Status: domain.StatusFailed,
		StartedAt: time.Now(), FinishedAt: time.Now(),
	}}})

	run := trayItem(tray, "recent/f1")
	if run == nil || run.Click == nil || !run.IsEnabled() {
		t.Fatalf("recent row = %+v, want a clickable item", run)
	}
	run.Click(run, nil)
	if opened != "f1" {
		t.Fatalf("opened %q, want f1", opened)
	}
	if detail := trayItem(tray, "result/f1"); detail == nil || detail.IsEnabled() {
		t.Fatalf("detail row = %+v, want informational and disabled", detail)
	}

	for i := 0; i < trayActiveRows+1; i++ {
		svc.active = append(svc.active, service.Active{Run: domain.Run{ID: fmt.Sprintf("x%d", i), TaskID: fmt.Sprintf("tx%d", i), TaskName: "More"}})
	}
	tray.update()
	more := trayItem(tray, "more")
	if more == nil || more.Click == nil || !more.IsEnabled() {
		t.Fatalf("more row = %+v, want a clickable item", more)
	}
	more.Click(more, nil)
	if shown != 1 {
		t.Fatalf("opened the window %d times, want 1", shown)
	}
}

func TestTrayStatsUnavailable(t *testing.T) {
	stubTrayDate(t)
	fake := &fakeTray{}
	tray := &taskTray{tray: fake, svc: &fakeTrayService{}, show: func() {}, openRun: func(domain.Run) {}}
	tray.set(view.Snapshot{TaskErr: errors.New("no tasks"), RecentErr: errors.New("no runs")})

	if tray.stats != locales.TrayStatsUnavailable() {
		t.Fatalf("stats = %q, want the unavailable message", tray.stats)
	}
	if got := trayItem(tray, "stats"); got == nil || got.Label != locales.TrayStatsUnavailable() {
		t.Fatalf("stats row = %+v", got)
	}
	if trayItem(tray, "error") == nil {
		t.Fatal("a failed read of recent runs must say so")
	}
	if trayItem(tray, "empty") != nil {
		t.Fatal("a failed read must not also say there are no runs")
	}
}

// TestTrayPlurals guards the English counts, which say "1 more task".
func TestTrayPlurals(t *testing.T) {
	if got := locales.TrayMore(1); strings.Contains(got, "tasks") {
		t.Errorf("TrayMore(1) = %q", got)
	}
	if got := trayStatusText([]service.Active{{}}); strings.Contains(got, "queued") {
		t.Errorf("one transfer and no queue = %q, want no queue", got)
	}
}

// TestTrayUpdateItem names the next step of the update above Open, follows
// the update without rebuilding the menu, and hands its step to onUpdate.
func TestTrayUpdateItem(t *testing.T) {
	stubTrayDate(t)
	fake := &fakeTray{}
	var clicked []updates.Phase
	tray := &taskTray{tray: fake, svc: &fakeTrayService{}, show: func() {}, openRun: func(domain.Run) {},
		onUpdate: func(s updates.State) { clicked = append(clicked, s.Phase) }}
	tray.set(view.Snapshot{})
	if tray.updateItem == nil || tray.updateItem.Label != locales.MenuCheckUpdates() {
		t.Fatalf("update item = %+v", tray.updateItem)
	}
	menus := len(fake.menus)
	tray.setUpdate(updates.State{Phase: updates.PhaseAvailable, Version: "1.3.0"})
	if tray.updateItem.Label != locales.TrayInstallUpdate("1.3.0") {
		t.Fatalf("label = %q", tray.updateItem.Label)
	}
	tray.updateItem.Click(tray.updateItem, nil)
	tray.setUpdate(updates.State{Phase: updates.PhaseReady, Version: "1.3.0"})
	if tray.updateItem.Label != locales.TrayRestartUpdate() {
		t.Fatalf("label = %q", tray.updateItem.Label)
	}
	if len(fake.menus) != menus {
		t.Fatal("the menu was rebuilt for the update")
	}
	if len(clicked) != 1 || clicked[0] != updates.PhaseAvailable {
		t.Fatalf("clicked %v", clicked)
	}

	plain := &taskTray{tray: &fakeTray{}, svc: &fakeTrayService{}, show: func() {}, openRun: func(domain.Run) {}}
	plain.set(view.Snapshot{})
	if plain.updateItem != nil {
		t.Fatal("an update item without onUpdate")
	}
}

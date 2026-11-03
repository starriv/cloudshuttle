package main

import (
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/format"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/internal/view"
	"github.com/starriv/cloudshuttle/locales"
	"golang.org/x/text/width"
)

// trayActiveRows and trayRecentRows bound the menu: each run's name and
// detail are two rows, so a long list would not fit on the screen.
const (
	trayActiveRows = 5
	trayRecentRows = 5
)

// trayLocale writes the menu's dates and percents in the language the app
// shows and the user's regional formats. It is a variable because tests run
// without the app, whose region only the running app reports.
var trayLocale = func() string {
	return mygo.App.Region().FormatLocale(i18n.Locale())
}

// trayStatusText includes runs owned by the scheduler as well as this process.
func trayStatusText(runs []service.Active) string {
	if len(runs) == 0 {
		return locales.TrayIdle()
	}
	running, queued := 0, 0
	for _, run := range runs {
		if run.Queued {
			queued++
		} else {
			running++
		}
	}
	switch {
	case queued == 0:
		return locales.TrayRunning(running)
	case running == 0:
		return locales.TrayQueued(queued)
	}
	return locales.TrayActivity(running, queued)
}

// trayMenu is the part of MyGo's tray the menu uses. Tests replace it.
type trayMenu interface{ SetMenu(*mygo.Menu) }

// trayService is the part of the service the menu reads. Tests replace it.
type trayService interface{ Active() []service.Active }

// taskTray keeps menu items stable while progress changes, so an open menu
// is not replaced on every engine update. All methods run on the main thread.
type taskTray struct {
	tray       trayMenu
	svc        trayService
	show       func()
	openRun    func(domain.Run)
	stats      string // refreshed with configuration, not on each progress tick
	recent     []domain.Run
	loadFailed bool
	rows       []trayRow
	items      []*mygo.MenuItem
}

type trayRow struct {
	key, label  string
	icon        *ui.SVG
	run         domain.Run
	opensWindow bool // opens the main window
}

// set takes what the window just read, so a change is read from the store
// once for the window and the status menu alike.
func (t *taskTray) set(s view.Snapshot) {
	t.stats = locales.TrayMiniStats(len(s.Tasks), len(s.Storages))
	if s.TaskErr != nil || s.StorageErr != nil {
		t.stats = locales.TrayStatsUnavailable()
	}
	t.loadFailed = s.RecentErr != nil
	t.recent = s.Recent
	t.update()
}

func (t *taskTray) update() {
	rows := []trayRow{{key: "stats", label: t.stats, icon: icons.Tasks}}
	rows = append(rows, taskTrayRows(t.svc.Active(), t.recent, t.loadFailed, trayLocale())...)
	same := len(rows) == len(t.rows)
	if same {
		for i := range rows {
			if rows[i].key != t.rows[i].key {
				same = false
				break
			}
		}
	}
	if same {
		for i, row := range rows {
			if row.icon != t.rows[i].icon {
				t.items[i].SetIcon(menuIcons()[row.icon])
			}
			if row.label != t.rows[i].label {
				t.items[i].SetLabel(row.label)
			}
		}
		t.rows = rows
		return
	}
	t.rows = rows
	t.items = nil
	for _, row := range rows {
		item := &mygo.MenuItem{Label: row.label, Disabled: row.run.ID == "" && !row.opensWindow, Icon: menuIcons()[row.icon]}
		if row.label == "" {
			item = mygo.Separator()
		}
		switch {
		case row.run.ID != "":
			item.Click = func(*mygo.MenuItem, *mygo.Window) { t.openRun(row.run) }
		case row.opensWindow:
			item.Click = func(*mygo.MenuItem, *mygo.Window) { t.show() }
		}
		t.items = append(t.items, item)
	}
	items := append([]*mygo.MenuItem(nil), t.items...)
	items = append(items, mygo.Separator(),
		&mygo.MenuItem{Label: locales.TrayOpen(), Icon: menuIcons()[icons.Overview], Click: func(*mygo.MenuItem, *mygo.Window) { t.show() }},
		mygo.Separator(), &mygo.MenuItem{Role: mygo.RoleQuit, Label: locales.TrayQuit()})
	t.tray.SetMenu(mygo.NewMenu(items))
}

func taskTrayRows(active []service.Active, recent []domain.Run, loadFailed bool, locale string) []trayRow {
	// Ready says nothing of how the last runs went, which the rows below show.
	summaryIcon := icons.Idle
	if len(active) > 0 {
		summaryIcon = icons.Running
	}
	rows := []trayRow{{key: "summary", label: trayStatusText(active), icon: summaryIcon}}
	activeTasks := map[string]bool{}
	for _, a := range active {
		activeTasks[a.Run.TaskID] = true
	}
	// Service.Active already puts the transfer first, but order here too so
	// the menu does not depend on the caller.
	ordered := append([]service.Active(nil), active...)
	slices.SortStableFunc(ordered, func(a, b service.Active) int {
		if a.Queued == b.Queued {
			return 0
		}
		if a.Queued {
			return 1
		}
		return -1
	})
	for i, a := range ordered {
		if i == trayActiveRows {
			rows = append(rows, trayRow{key: "more", label: locales.TrayMore(len(active) - trayActiveRows), opensWindow: true})
			break
		}
		state := view.StatusLabel(domain.StatusRunning)
		statusIcon := view.StatusIcon(domain.StatusRunning)
		detail := locales.TrayPreparing()
		if a.Queued {
			state = locales.TaskWaiting()
			statusIcon = icons.Untested
			detail = ""
		} else if a.Progress.TotalBytes > 0 {
			ratio := min(1, max(0, float64(a.Progress.Bytes)/float64(a.Progress.TotalBytes)))
			detail = trayJoin(
				i18n.FormatNumber(locale, ratio, i18n.NumberOptions{Percent: true}),
				locales.Progress(format.Bytes(a.Progress.Bytes), format.Bytes(a.Progress.TotalBytes)),
				locales.Speed(format.Bytes(int64(a.Progress.Speed))))
		} else if a.Progress.Bytes > 0 || a.Progress.Speed > 0 {
			// The total is not known yet: say what has been transferred and
			// how fast, rather than that the files are still being checked.
			detail = format.Bytes(a.Progress.Bytes)
			if a.Progress.Speed > 0 {
				detail = trayJoin(detail, locales.Speed(format.Bytes(int64(a.Progress.Speed))))
			}
		}
		rows = append(rows, trayRow{key: "active/" + a.Run.ID, label: locales.TrayRunLine(trayRunName(a.Run), state), run: a.Run, icon: statusIcon})
		if detail != "" {
			rows = append(rows, trayRow{key: "progress/" + a.Run.ID, label: detail})
		}
	}
	rows = append(rows, trayRow{key: "divider"}, trayRow{key: "recent", label: locales.TrayRecent(), icon: icons.History})
	count := 0
	seenTasks := map[string]bool{}
	for _, r := range recent {
		if r.Status == domain.StatusRunning || activeTasks[r.TaskID] || seenTasks[r.TaskID] {
			continue
		}
		if count == trayRecentRows {
			break
		}
		count++
		seenTasks[r.TaskID] = true
		state := view.StatusLabel(r.Status)
		statusIcon := view.StatusIcon(r.Status)
		rows = append(rows, trayRow{key: "recent/" + r.ID, label: locales.TrayRunLine(trayRunName(r), state), run: r, icon: statusIcon})
		var parts []string
		if !r.StartedAt.IsZero() {
			parts = append(parts, i18n.FormatDate(locale, r.StartedAt.Local(), i18n.DateOptions{Skeleton: "Mdjm"}))
		}
		parts = append(parts, format.Bytes(r.Stats.Bytes))
		if !r.FinishedAt.IsZero() {
			parts = append(parts, format.Duration(r.FinishedAt.Sub(r.StartedAt)))
		}
		detail := trayJoin(parts...)
		rows = append(rows, trayRow{key: "result/" + r.ID, label: detail})
	}
	if loadFailed {
		rows = append(rows, trayRow{key: "error", label: locales.TrayLoadFailed()})
	} else if count == 0 {
		rows = append(rows, trayRow{key: "empty", label: locales.TrayNoRecent()})
	}
	return rows
}

// trayNameWidth bounds a task's name in the menu, in columns of a Latin
// letter: a CJK character takes two, so names in either script are as wide.
const trayNameWidth = 36

func trayRunName(r domain.Run) string {
	name := strings.Join(strings.Fields(r.TaskName), " ")
	if columns(name) > trayNameWidth {
		cut, used := 0, 0
		for i, c := range name {
			if used+columns(string(c)) > trayNameWidth-1 { // room for the ellipsis
				break
			}
			used += columns(string(c))
			cut = i + len(string(c))
		}
		name = name[:cut] + "…"
	}
	if r.DryRun {
		name = trayJoin(name, locales.DryRunBadge())
	}
	return name
}

// columns is how wide s is, in columns of a Latin letter.
func columns(s string) int {
	n := 0
	for _, c := range s {
		switch width.LookupRune(c).Kind() {
		case width.EastAsianWide, width.EastAsianFullwidth:
			n += 2
		default:
			n++
		}
	}
	return n
}

// trayJoin joins the parts of a menu row the way the user's language does.
func trayJoin(parts ...string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out = locales.TrayDetail(out, p)
	}
	return out
}

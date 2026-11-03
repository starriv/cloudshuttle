// Package view is CloudShuttle's native interface, drawn by MyGo: a sidebar
// of pages, each a function of the App's state. The view runs on the main
// thread; the service's callbacks reach it through Host.Update.
package view

import (
	"context"
	"runtime"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/internal/store"
	"github.com/starriv/cloudshuttle/locales"
)

// Host is what the view needs from the window it is in.
type Host interface {
	// Update runs fn on the main thread, then draws a new frame.
	Update(fn func())
	// ChooseFolder asks the user for a folder, "" when canceled. It blocks:
	// call it off the main thread.
	ChooseFolder() (string, error)
	// OpenBackgroundSettings opens the system's settings where the person
	// lets the app run in the background.
	OpenBackgroundSettings()
	// ChooseFile asks the user for a file, starting in dir, "" when
	// canceled. It blocks: call it off the main thread.
	ChooseFile(dir string) (string, error)
	// SetAppearance gives the window, and the app's other windows, the
	// appearance the person chose.
	SetAppearance(domain.Appearance)
}

const (
	pageOverview = "overview"
	pageTasks    = "tasks"
	pageStorages = "storages"
	pageHistory  = "history"
	pageSettings = "settings"
)

// recentRuns is how many runs the overview lists.
const recentRuns = 8

// Snapshot is what the window last read of the configuration and of the
// recent runs, which the status menu shows too: a change is read once for
// both. Its slices are not changed after Reload.
type Snapshot struct {
	Tasks      []domain.Task
	Storages   []domain.Storage
	Recent     []domain.Run
	TaskErr    error
	StorageErr error
	RecentErr  error
}

// App is the state of the window.
type App struct {
	svc  *service.Service
	host Host
	now  func() time.Time

	page string

	snapshot Snapshot
	storages []domain.Storage
	tasks    []domain.Task
	settings domain.Settings
	shown    domain.Appearance // the appearance given to the host last
	shownSet bool
	lastRuns map[string]domain.Run
	recent   []domain.Run
	week     []domain.Run              // the runs of the last seven days
	running  []service.Active          // the oldest first
	active   map[string]service.Active // the same, by task ID
	loadErr  error

	taskList    ui.ListState
	storageList ui.ListState

	history       []domain.Run
	historyList   ui.ListState
	historyRow    int                  // the row chosen last, where the keys move from
	historyChosen ui.Selection[string] // the runs chosen, by ID
	historyTask   string               // the label chosen in the task filter
	historyStatus string               // the label chosen in the status filter
	runLog        runLogView

	taskForm       taskForm
	storageForm    storageForm
	settingsForm   settingsForm
	restoreForm    restoreForm
	storageTests   int             // counts the forms' connection tests, to keep the latest
	checking       map[string]bool // the storages being tested, by ID
	serverSearches int             // counts the forms' searches of the network, likewise
	folderLoads    int             // counts the folder browser's reads, likewise
	confirm        confirmation

	toasts []ui.Toast // shown in the next frame
}

// confirmation asks before an action that cannot be taken back.
type confirmation struct {
	open    bool
	title   string
	message string
	ok      string // the button that does it: Delete when empty
	action  func()
}

// New makes the window's state and reads the service's data.
func New(svc *service.Service, host Host) *App {
	a := &App{svc: svc, host: host, now: time.Now, page: pageOverview, historyRow: -1, checking: map[string]bool{}}
	a.taskList.Key = func(i int) any { return a.tasks[i].ID }
	a.storageList.Key = func(i int) any { return a.storages[i].ID }
	a.historyList.Key = func(i int) any { return a.history[i].ID }
	a.historyList.Selected = &a.historyRow
	a.historyList.Selection = &a.historyChosen
	a.Reload()
	return a
}

// Reload reads storages, tasks and runs again. Call it on the main thread.
func (a *App) Reload() {
	ctx := context.Background()
	a.loadErr = nil
	var err error
	if a.storages, err = a.svc.Storages(ctx); err != nil {
		a.loadErr = err
	}
	storageErr := err
	if a.tasks, err = a.svc.Tasks(ctx); err != nil {
		a.loadErr = err
	}
	taskErr := err
	if a.lastRuns, err = a.svc.LastRuns(ctx); err != nil {
		a.loadErr = err
	}
	// Read enough for the status menu too, which dedupes by task and lists
	// the newest 5; the overview shows the first recentRuns of them.
	runs, recentErr := a.svc.Runs(ctx, store.RunFilter{Limit: store.DefaultRunLimit})
	if recentErr != nil {
		a.loadErr = recentErr
	}
	a.recent = runs[:min(len(runs), recentRuns)]
	if a.week, err = a.svc.Runs(ctx, store.RunFilter{Since: a.now().AddDate(0, 0, -7), Limit: 10000}); err != nil {
		a.loadErr = err
	}
	a.snapshot = Snapshot{
		Tasks: a.tasks, Storages: a.storages, Recent: runs,
		TaskErr: taskErr, StorageErr: storageErr, RecentErr: recentErr,
	}
	a.settings = a.svc.Settings()
	a.showAppearance()
	a.reloadHistory()
	a.ReloadProgress()
}

// Snapshot returns what the window last read, for the status menu.
func (a *App) Snapshot() Snapshot { return a.snapshot }

// showAppearance gives the host the appearance of the settings, once per
// change: a restored backup brings its own.
func (a *App) showAppearance() {
	if a.shownSet && a.shown == a.settings.Appearance {
		return
	}
	a.shown, a.shownSet = a.settings.Appearance, true
	a.host.SetAppearance(a.shown)
}

// ReloadProgress reads the runs in progress again. Call it on the main thread.
func (a *App) ReloadProgress() {
	a.running = a.svc.Active()
	a.active = map[string]service.Active{}
	for _, run := range a.running {
		a.active[run.Run.TaskID] = run
	}
	a.refreshRunLog()
}

func (a *App) reloadHistory() {
	filter := store.RunFilter{}
	if i := indexOf(a.historyTaskOptions(), a.historyTask); i > 0 {
		filter.TaskID = a.tasks[i-1].ID
	}
	if i := indexOf(a.historyStatusOptions(), a.historyStatus); i > 0 {
		filter.Status = historyStatuses[i-1]
	}
	selected := ""
	if a.historyRow >= 0 && a.historyRow < len(a.history) {
		selected = a.history[a.historyRow].ID
	}
	runs, err := a.svc.Runs(context.Background(), filter)
	if err != nil {
		a.loadErr = err
	}
	a.history, a.historyRow = runs, -1
	shown := map[string]bool{}
	for i, r := range runs {
		shown[r.ID] = true
		if r.ID == selected {
			a.historyRow = i
		}
	}
	// Runs deleted, or filtered out, are chosen no more: what the history
	// deletes is what it shows chosen.
	for id := range a.historyChosen.All() {
		if !shown[id] {
			a.historyChosen.Remove(id)
		}
	}
}

// toast shows a message in the next frame, for results that arrive between
// frames.
func (a *App) toast(message string) { a.toasts = append(a.toasts, ui.Toast{Title: message}) }

func (a *App) toastError(err error) {
	a.toasts = append(a.toasts, ui.Toast{Title: errorText(err), Type: "error"})
}

// View builds the window.
func (a *App) View(c *ui.Context) {
	for _, t := range a.toasts {
		c.AddToast(t)
	}
	a.toasts = nil

	t := c.Theme()
	bar := c.TitleBar()
	vibrant := c.Vibrancy()
	if vibrant {
		// The sidebar shows the window's material; the content is opaque.
		c.Root().Background(ui.Transparent)
	}
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		side := ui.Column(c).Width(sidebarWidth).BorderWidth(0, 1, 0, 0).BorderColor(t.Border)
		if !vibrant {
			side.Background(t.Surface)
		}
		side.Children(func() {
			// Room for the window controls, which drags the window.
			ui.Box(c).Height(max(bar.Height, 12)).FillWidth().DragWindow()
			sidebar := ui.Sidebar(c, &a.page, func() {
				ui.SidebarItem(c, pageOverview, icons.Overview, locales.NavOverview())
				ui.SidebarItem(c, pageTasks, icons.Tasks, locales.NavTasks()).Children(func() {
					if n := len(a.active); n > 0 {
						ui.Badge(c, formatCount(n))
					}
				})
				ui.SidebarItem(c, pageStorages, icons.Storages, locales.NavStorages())
				ui.SidebarItem(c, pageHistory, icons.History, locales.NavHistory())
				ui.SidebarItem(c, pageSettings, icons.Settings, locales.NavSettings())
			}).Grow(1)
			if vibrant {
				sidebar.Background(ui.Transparent)
			}
			// The About panel says the rclone a run uses where the system
			// has one; elsewhere it stays under the overview.
			if runtime.GOOS != "darwin" {
				ui.Text(c, locales.RcloneVersion(engine.Version())).
					TextColor(t.TextMuted).FontSize(11).Padding(t.Space(3)).SingleLine()
			}
		})
		ui.Column(c).Grow(1).MinWidth(0).Background(t.Background).Children(func() {
			// Each page comes in from a little below as it fades in, and
			// the page it replaces fades out: one motion, on navigation.
			ui.Column(c.Key(a.page)).Grow(1).MinHeight(0).Padding(0, t.Space(6), t.Space(5), t.Space(6)).
				Gap(t.Space(4)).Transition(pageTransition).Children(func() {
				switch a.page {
				case pageTasks:
					a.tasksPage(c)
				case pageStorages:
					a.storagesPage(c)
				case pageHistory:
					a.historyPage(c)
				case pageSettings:
					a.settingsPage(c)
				default:
					a.overviewPage(c)
				}
				if a.loadErr != nil {
					ui.Row(c).Gap(8).AlignItems(ui.Center).TextColor(t.Danger).Children(func() {
						ui.Icon(c, icons.Failed)
						ui.Text(c, locales.ErrorLoad(a.loadErr.Error())).Grow(1).MinWidth(0)
					})
				}
			})
		})
	})

	a.taskDialog(c)
	a.storageDialog(c)
	a.restoreDialog(c)
	ok := a.confirm.ok
	if ok == "" {
		ok = locales.ButtonDelete()
	}
	if ui.AlertDialog(c, &a.confirm.open, a.confirm.title, a.confirm.message,
		locales.ButtonCancel(), ok) == 1 {
		a.confirm.action()
	}
}

const sidebarWidth = 210

// headerHeight is the height of a page's title bar, which is the window's.
const headerHeight = 56

var pageTransition = ui.ElementTransition{
	Duration: 220 * time.Millisecond,
	Position: true,
	Enter:    &ui.Motion{Y: 8},
	Exit:     &ui.Motion{},
}

// header builds a page's title and its main action, in the window's title
// bar: dragging it moves the window.
func header(c *ui.Context, title string, action func()) {
	ui.Row(c).Height(max(c.TitleBar().Height, headerHeight)).AlignItems(ui.Center).Gap(12).DragWindow().Children(func() {
		ui.Text(c, title).FontSize(22).FontWeight(500).Grow(1).MinWidth(0).SingleLine()
		if action != nil {
			action()
		}
	})
}

// empty builds the state of a page with nothing to show: its icon, what to
// do next, and the button that does it.
func empty(c *ui.Context, svg *ui.SVG, message string, action func()) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
		ui.Column(c).Size(56, 56).Center().Radius(16).Background(t.Surface).TextColor(t.TextMuted).Children(func() {
			ui.Icon(c, svg).FontSize(26)
		})
		ui.Text(c, message).TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(420)
		if action != nil {
			action()
		}
	})
}

func iconButton(c *ui.Context, svg *ui.SVG, label string, primary bool) ui.Element {
	var b ui.Element
	if primary {
		b = ui.PrimaryButton(c, "")
	} else {
		b = ui.Button(c, "")
	}
	return b.Children(func() {
		ui.Icon(c, svg)
		ui.Text(c, label).SingleLine()
	})
}

func formatCount(n int) string {
	if n > 99 {
		return "99+"
	}
	return strconv.Itoa(n)
}

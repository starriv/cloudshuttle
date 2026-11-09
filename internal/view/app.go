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
	"github.com/starriv/cloudshuttle/internal/updates"
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
	// SetAppIcon shows the app's icon where the person chose: the Dock,
	// the menu bar, or both (macOS; elsewhere the app keeps both).
	SetAppIcon(domain.AppIcon)
}

const (
	pageOverview = "overview"
	pageTasks    = "tasks"
	pageStorages = "storages"
	pageHistory  = "history"
	pageSettings = "settings"
)

// recentRuns is how many rows the overview lists of the latest runs.
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
	svc     *service.Service
	host    Host
	updater *updates.Updater // nil without one
	now     func() time.Time

	page string

	snapshot Snapshot
	storages []domain.Storage
	tasks    []domain.Task
	settings domain.Settings
	shown    domain.Appearance // the appearance given to the host last
	shownSet bool
	// shownIcon is the icon placement given to the host last, and
	// shownIconSet whether it was given at all.
	shownIcon    domain.AppIcon
	shownIconSet bool
	lastRuns     map[string]domain.Run
	lastRealRuns map[string]domain.Run
	recent       []domain.Run
	week         []domain.Run              // the runs of the last seven days
	running      []service.Active          // the oldest first
	active       map[string]service.Active // the same, by task ID
	loadErr      error

	taskList     ui.ListState
	storageList  ui.ListState
	storageFiles *storageFiles

	history       []domain.Run
	historyList   ui.ListState
	historyPanel  float32              // the width of the details panel beside the runs
	historyRow    int                  // the row chosen last, where the keys move from
	historyChosen ui.Selection[string] // the runs chosen, by ID
	historyTask   string               // the label chosen in the task filter
	historyStatus string               // the label chosen in the status filter
	runLog        runLogView
	runDeletes    runDeletesView

	taskForm       taskForm
	storageForm    storageForm
	settingsForm   settingsForm
	exclude        settingsExcludeForm
	restoreForm    restoreForm
	storageTests   int             // counts the forms' connection tests, to keep the latest
	checking       map[string]bool // the storages being tested, by ID
	serverSearches int             // counts the forms' searches of the network, likewise
	exportLists    int             // counts the forms' listings of NFS exports, likewise
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
	a := &App{svc: svc, host: host, now: time.Now, page: pageOverview, historyRow: -1,
		checking: map[string]bool{}, historyPanel: 300}
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
	if a.lastRealRuns, err = a.svc.LastRealRuns(ctx); err != nil {
		a.loadErr = err
	}
	// Read enough for the status menu too, which dedupes by task and lists
	// the newest 5; the overview folds them into recentRuns rows.
	runs, recentErr := a.svc.Runs(ctx, store.RunFilter{Limit: store.DefaultRunLimit})
	if recentErr != nil {
		a.loadErr = recentErr
	}
	a.recent = runs
	if a.week, err = a.svc.Runs(ctx, store.RunFilter{Since: weekStart(a.now()), Limit: 10000}); err != nil {
		a.loadErr = err
	}
	a.snapshot = Snapshot{
		Tasks: a.tasks, Storages: a.storages, Recent: runs,
		TaskErr: taskErr, StorageErr: storageErr, RecentErr: recentErr,
	}
	a.settings = a.svc.Settings()
	a.showAppearance()
	a.showIcon()
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

// showIcon gives the host where the settings say the app shows its icon,
// once per change: a restored backup brings its own.
func (a *App) showIcon() {
	if a.shownIconSet && a.shownIcon == a.settings.AppIcon {
		return
	}
	a.shownIcon, a.shownIconSet = a.settings.AppIcon, true
	a.host.SetAppIcon(a.shownIcon)
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

	p := appTheme(c)
	bar := c.TitleBar()
	vibrant := c.Vibrancy()
	if vibrant {
		// The sidebar shows the window's material; the content is opaque.
		c.Root().Background(ui.Transparent)
	}
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		a.sidebar(c, p, bar, vibrant)
		ui.Column(c).Grow(1).MinWidth(0).Background(p.bg).Children(func() {
			// Each page comes in from a little below as it fades in, and
			// the page it replaces fades out: one motion, on navigation.
			ui.Column(c.Key(a.page)).Grow(1).MinHeight(0).
				Padding(0, pagePad, 0, pagePad).Gap(gapMedium).
				Transition(pageTransition).Children(func() {
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
					ui.Row(c).Gap(8).AlignItems(ui.Center).TextColor(p.danger).Children(func() {
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
	a.globalExcludeDialog(c)
	a.storageFileEditDialog(c)
	a.confirmDialog(c)
}

// sidebar is the window's left edge: the app's mark, the pages, and what
// the app is doing now. It shows the desktop's material where it can.
func (a *App) sidebar(c *ui.Context, p palette, bar ui.TitleBar, vibrant bool) {
	items := []struct {
		page, label string
		icon        *ui.SVG
		badge       int
	}{
		{pageOverview, locales.NavOverview(), icons.Overview, 0},
		{pageTasks, locales.NavTasks(), icons.Tasks, len(a.active)},
		{pageStorages, locales.NavStorages(), icons.Storages, 0},
		{pageHistory, locales.NavHistory(), icons.History, 0},
		{pageSettings, locales.NavSettings(), icons.Settings, 0},
	}
	buttons := make([]ui.Element, len(items))
	draw := func(i int) {
		it := items[i]
		buttons[i] = a.navItem(c, p, it.page, it.label, it.icon, it.badge)
	}
	side := ui.Column(c).Width(sidebarWidth).Role(ui.RoleTree)
	if !vibrant {
		side.Background(p.sidebar)
	}
	side.Children(func() {
		// Room for the window controls, which drags the window.
		ui.Box(c).Height(max(bar.Height, 18)).FillWidth().DragWindow()
		a.brand(c, p)
		ui.Column(c).Padding(0, 10, 0, 10).Gap(2).Children(func() {
			for i := range len(items) - 1 {
				draw(i)
			}
		})
		ui.Spacer(c)
		// Settings sits at the foot of the list, clear of the window's
		// bottom edge and its rounded corner: nothing else goes under it.
		ui.Column(c).Padding(0, 10, 16, 10).Gap(2).Children(func() {
			ui.Box(c).Height(1).FillWidth().Background(p.hairline).Margin(0, 6, 10, 6)
			draw(len(items) - 1)
			if runtime.GOOS != "darwin" {
				ui.Text(c, locales.RcloneVersion(engine.Version())).
					TextColor(p.faint).FontSize(sizeMicro).Padding(6, 6, 0, 6).SingleLine()
			}
		})
	})
	// Move selection and keyboard focus together while keeping the layout.
	for i, button := range buttons {
		next := i
		switch {
		case button.Shortcut(0, ui.KeyUp):
			next = max(i-1, 0)
		case button.Shortcut(0, ui.KeyDown):
			next = min(i+1, len(items)-1)
		case button.Shortcut(0, ui.KeyHome):
			next = 0
		case button.Shortcut(0, ui.KeyEnd):
			next = len(items) - 1
		default:
			continue
		}
		a.selectPage(items[next].page)
		buttons[next].Focus()
		break
	}
}

// navItem is one page in the sidebar: its icon, its name, and how many
// runs are on it, when any.
func (a *App) navItem(c *ui.Context, p palette, page, label string, icon *ui.SVG, badge int) ui.Element {
	selected := a.page == page
	b := ui.ButtonBase(c.Key(page)).Height(34).PaddingX(10).Gap(10).Radius(8).
		Justify(ui.Start).AlignItems(ui.Center).Role(ui.RoleTreeItem).Label(label).Selected(selected)
	switch {
	case selected:
		b.Background(p.accentSoft)
	case b.Hovered():
		b.Background(p.hover)
	}
	b.Transition(quickTransition)
	b.Children(func() {
		color := p.muted
		if selected {
			color = p.accent
		}
		ui.Icon(c, icon).Size(16, 16).TextColor(color)
		ui.Text(c, label).FontSize(sizeBody).FontWeight(540).TextColor(p.text).
			Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
		if badge > 0 {
			pill(c, page, nil, formatCount(badge), p.accent, p.accentSoft)
		}
	})
	if b.Clicked() {
		a.selectPage(page)
	}
	return b
}

// brand is the app's mark and name at the top of the sidebar.
func (a *App) brand(c *ui.Context, p palette) {
	ui.Row(c).Height(52).Padding(2, 16).Gap(12).AlignItems(ui.Center).DragWindow().Children(func() {
		ui.Box(c).Size(30, 30).Radius(9).Center().Background(p.accent).TextColor(p.accentText).
			Children(func() {
				ui.Icon(c, icons.Mark).Size(20, 20)
			})
		ui.Column(c).Grow(1).MinWidth(0).Gap(0).Children(func() {
			ui.Text(c, locales.AppName()).FontSize(13.5).FontWeight(640).LetterSpacing(-0.1).
				TextColor(p.text).SingleLine().Ellipsis("…")
			if a.updater != nil {
				ui.Text(c, a.updater.State().Current).FontSize(sizeMicro).TextColor(p.faint).
					SingleLine().Ellipsis("…")
			}
		})
	})
}

const sidebarWidth = 232

// pagePadding is the room each page leaves between the content area's sides
// and its content.
const pagePadding = pagePad

// pageTransition moves a page in from a little below as it fades in.
var pageTransition = ui.ElementTransition{
	Duration: 220 * time.Millisecond,
	Position: true,
	Enter:    &ui.Motion{Y: 8},
	Exit:     &ui.Motion{},
}

// pageScroll scrolls a page's content. A scroll bar floats over the content
// it scrolls and takes no room, so the container carries the page's side
// padding, bleeding into that of the page: the bar shows beside the content,
// in the margin, rather than over it.
func pageScroll(c *ui.Context) ui.Element {
	return ui.Scroll(c).Grow(1).MinHeight(0).MarginX(-pagePadding).PaddingX(pagePadding)
}

// iconButton is the button of a page's header: a symbol and a word, or the
// page's main action.
func iconButton(c *ui.Context, svg *ui.SVG, label string, primary bool) ui.Element {
	kind := buttonSoft
	if primary {
		kind = buttonPrimary
	}
	return accentButton(c, label, svg, label, kind)
}

func formatCount(n int) string {
	if n > 99 {
		return "99+"
	}
	return strconv.Itoa(n)
}

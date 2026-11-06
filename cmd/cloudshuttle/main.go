// Command cloudshuttle is the CloudShuttle desktop app: it copies and syncs
// folders to S3-compatible storages and SMB servers with rclone, compiled
// in, by hand or on schedules. It lives in the menu bar or the
// notification area while its window is closed.
//
// The same executable is the scheduler: started by the system with
// schedulerArg, it runs the tasks that are due, with no window, and quits.
package main

import (
	"context"
	_ "embed"
	"errors"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"syscall"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/internal/store"
	"github.com/starriv/cloudshuttle/internal/updates"
	"github.com/starriv/cloudshuttle/internal/view"
	"github.com/starriv/cloudshuttle/locales"
)

//go:embed tray.png
var trayIcon []byte

// The app's argument, which only the system passes: schedulerArg starts the
// scheduler instead of the app.
const schedulerArg = "--scheduler"

func main() {
	if slices.Contains(os.Args[1:], schedulerArg) {
		runScheduler()
		return
	}
	if !mygo.App.RequestSingleInstanceLock() {
		return // the running instance comes to the front
	}

	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)

	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		log.Fatal(err)
	}
	out, closeLog := openLog(dir)
	defer closeLog()
	slog.SetDefault(slog.New(slog.NewTextHandler(out, nil)))
	db, err := store.Open(filepath.Join(dir, "cloudshuttle.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	host := &windowHost{}
	var svc *service.Service
	var tray *taskTray // accessed only on the main thread
	var isl *notchIsland
	var show func()             // shows the main window, once it exists
	var windowShown atomic.Bool // the main window is on screen
	openUpdates := func() {     // the settings' Updates card, on the main thread
		host.update(func(a *view.App) { a.OpenUpdates() })
		if show != nil {
			show()
		}
	}
	var upd *updates.Updater
	upd = updates.New(updates.Options{
		Source:  mygoUpdates{},
		Current: mygo.App.Version(),
		// Every run counts, the scheduler's too: the person asked to
		// wait for the tasks, and a restart would stop the window's.
		Runs:     func() int { return len(svc.Active()) },
		Quiet:    func() bool { return !windowShown.Load() },
		Relaunch: func() { relaunchIntoUpdate(dir, windowShown.Load()) },
		OnChange: func() {
			host.update(func(*view.App) {
				if tray != nil {
					tray.setUpdate(upd.State())
				}
			})
		},
		OnAvailable: func(version string) {
			notifyUpdate(version, openUpdates)
		},
	})

	runsChanged := func() {
		if isl != nil {
			isl.SetRuns(svc.Active())
		}
	}
	svc = service.New(service.Options{
		Store:     db,
		Secrets:   keychain{},
		DataDir:   dir,
		Registrar: newRegistrar(),
		OnChange: func() {
			host.update(func(a *view.App) {
				a.Reload()
				if tray != nil {
					tray.set(a.Snapshot())
				}
			})
			runsChanged()
			// A restored backup brings its own choice; a run that started
			// or ended may be the one a restart waits for. Off this
			// goroutine, which may hold the service's lock.
			go func() {
				st := svc.Settings()
				upd.SetAutomatic(st.CheckUpdates, st.InstallUpdates)
				upd.RunsChanged()
			}()
		},
		OnProgress: func() {
			host.update(func(a *view.App) {
				a.ReloadProgress()
				if tray != nil {
					tray.update()
				}
			})
			runsChanged()
		},
		OnRunFinished: func(r domain.Run) {
			if isl != nil {
				isl.Finished(r)
			}
			go upd.RunsChanged()
		},
		OnNotify: notifyFailure,
		OnRefused: func(t domain.Task, err error) {
			notifyRefused(svc.Settings(), t)
		},
	})
	app := view.New(svc, host)
	app.SetUpdater(upd)
	host.app = app

	var quitting bool // OnBeforeQuit and OnClose run on the main thread
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { quitting = true })
	mygo.App.OnQuit(svc.Close)

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:  locales.AppName(),
			Width:  1080,
			Height: 700,
			// Low enough for a small laptop, high enough for the task
			// dialog and the global exclusion editor, whose buttons are
			// below the fields.
			MinWidth:  900,
			MinHeight: 640,
			StateKey:  "main",
			// The view draws the title bar; the sidebar shows the
			// desktop's material through it, as Finder's and Mail's do.
			TitleBarStyle: mygo.TitleBarHidden,
			Vibrancy:      mygo.VibrancySidebar,
			Hidden:        true,
			Content:       ui.View(app.View),
		})
		host.win = win

		// Closing the window hides it, and the island goes on showing the
		// runs. The schedules do not need it: the system runs them.
		trayAvailable := true
		win.OnClose(func(e *mygo.CloseEvent) {
			if !quitting {
				e.PreventDefault()
				if trayAvailable {
					win.Hide()
				} else {
					mygo.App.Quit()
				}
			}
		})
		win.OnShow(func() { windowShown.Store(true) })
		win.OnHide(func() {
			windowShown.Store(false)
			go upd.RunsChanged() // an update installed by itself waits for this
		})
		show = func() {
			// Restore, so a window the user minimized comes back too: with no
			// Dock icon, the status menu is the only way to it.
			win.Restore()
			win.Show()
			win.Focus()
		}
		mygo.App.OnSecondInstance(func([]string, string) { show() })
		// The island is a visible panel even when the main window is hidden,
		// so hasVisibleWindows says nothing about the window: reopening the
		// app always brings the window back.
		mygo.App.OnActivate(func(bool) { show() })

		statusIcon, err := mygo.NewTray(mygo.TrayOptions{
			ID: "main", Icon: trayIcon, IconIsTemplate: true, ToolTip: locales.AppName(),
		})
		if err != nil {
			slog.Warn("tray", "err", err)
			mygo.App.SetActivationPolicy(mygo.ActivationPolicyRegular)
			show()
			trayAvailable = false
		} else {
			tray = &taskTray{tray: statusIcon, svc: svc, show: show, openRun: func(r domain.Run) {
				host.update(func(a *view.App) { a.OpenRun(r.TaskID, r.ID) })
				show()
			}, onUpdate: func(s updates.State) {
				openUpdates()
				updateStep(upd, s)
			}, updateState: upd.State()}
			tray.set(app.Snapshot())
		}

		isl = newIsland()

		// The About panel says what app this is; it says the rclone a run
		// uses too, where the window no longer does.
		if runtime.GOOS == "darwin" {
			mygo.App.SetMenu(appMenu(func() {
				openUpdates()
				upd.Check()
			}))
		}

		// A first run has nothing in the status menu yet: open the window so
		// the person sees where to add a storage and a task. Later launches,
		// and a wake to tell a failure, leave the window hidden.
		if firstRun(app.Snapshot()) || shownBeforeUpdate(dir) {
			show()
		}

		if err := svc.Start(context.Background()); err != nil {
			slog.Error("start the service", "err", err)
		}
		st := svc.Settings()
		upd.SetAutomatic(st.CheckUpdates, st.InstallUpdates)
		upd.Start(context.Background())
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// firstRun reports whether the app is not set up at all, with no storage and
// no task: the window then opens by itself, so a new person finds where to
// start. Someone who set up a storage, or removed every task, has been here
// and is left in the menu bar, at login too.
func firstRun(s view.Snapshot) bool {
	return s.TaskErr == nil && s.StorageErr == nil && len(s.Tasks) == 0 && len(s.Storages) == 0
}

// appMenu is the system's menu bar: the default one, whose About shows the
// standard panel, with the rclone compiled into the app as its credits.
func appMenu(checkForUpdates func()) *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu, Submenu: []*mygo.MenuItem{
			{Label: locales.MenuAbout(), Click: func(*mygo.MenuItem, *mygo.Window) { showAbout() }},
			{Label: locales.MenuCheckUpdates(), Click: func(*mygo.MenuItem, *mygo.Window) { checkForUpdates() }},
			mygo.Separator(),
			{Role: mygo.RoleServices},
			mygo.Separator(),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Role: mygo.RoleFileMenu},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleViewMenu},
		{Role: mygo.RoleWindowMenu},
	})
}

// showAbout shows the system's About panel, as the system's own About does,
// and says there the rclone every run uses.
func showAbout() {
	mygo.App.ShowAboutPanel(mygo.AboutPanelOptions{
		ApplicationName:    locales.AppName(),
		ApplicationVersion: mygo.App.Version(),
		Credits:            locales.RcloneVersion(engine.Version()),
	})
}

// openLog keeps the app's own log beside the database, so that what
// happened to a run can be read after it, even when no window showed it. A
// log grown past 5 MB gives way to a fresh one.
func openLog(dir string) (io.Writer, func()) {
	path := filepath.Join(dir, "cloudshuttle.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > 5<<20 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return os.Stderr, func() {}
	}
	return io.MultiWriter(os.Stderr, f), func() { _ = f.Close() }
}

// windowHost connects the view to its window.
type windowHost struct {
	app *view.App
	win *mygo.Window
}

func (h *windowHost) Update(fn func()) {
	if h.win != nil {
		h.win.Update(fn)
	}
}

func (h *windowHost) update(fn func(*view.App)) {
	h.Update(func() { fn(h.app) })
}

func (h *windowHost) SetAppearance(a domain.Appearance) {
	source := mygo.ThemeSystem
	switch a {
	case domain.AppearanceLight:
		source = mygo.ThemeLight
	case domain.AppearanceDark:
		source = mygo.ThemeDark
	}
	mygo.Theme.SetSource(source)
}

func (h *windowHost) OpenBackgroundSettings() {
	go func() {
		if err := openBackgroundSettings(); err != nil {
			slog.Warn("open the background settings", "err", err)
		}
	}()
}

func (h *windowHost) ChooseFile(dir string) (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: h.win, DefaultPath: dir})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

func (h *windowHost) ChooseFolder() (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: h.win, Directory: true})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

// keychain keeps the storages' secrets in the system's credential store.
type keychain struct{}

func (keychain) Get(key string) (string, error) {
	b, err := mygo.Secrets.Get(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return "", domain.ErrMissingSecret
	}
	return string(b), err
}

func (keychain) Set(key, value string) error { return mygo.Secrets.Set(key, []byte(value)) }

func (keychain) Delete(key string) error { return mygo.Secrets.Delete(key) }

// problemText says why a run stopped before it deleted anything, in the
// person's language.
func problemText(problem string) string {
	switch problem {
	case domain.RunProblemEmptySource:
		return locales.RunProblemEmptySource()
	case domain.RunProblemLargeDelete:
		return locales.RunProblemLargeDelete()
	}
	return ""
}

// notifyFailure tells the user about a run that failed, which they may not
// be looking at: most runs are scheduled. The service calls it only when
// the user asked to be told.
func notifyFailure(run domain.Run) {
	if !mygo.NotificationsSupported() {
		return
	}
	body := run.Message
	if text := problemText(run.Problem); text != "" {
		body = text
	}
	n := mygo.NewNotification(mygo.NotificationOptions{
		ID:    "run/" + run.ID,
		Title: locales.NotifyFailedTitle(run.TaskName),
		Body:  body,
	})
	go func() {
		if err := n.Show(); err != nil {
			slog.Warn("notify", "err", err)
		}
	}()
}

// notifyRefused tells the user a scheduled run did not start, because
// another task is writing to its destination: the schedule keeps going, so
// silence would look like the backup happened.
func notifyRefused(settings domain.Settings, t domain.Task) {
	if !settings.NotifySkipped || !mygo.NotificationsSupported() {
		return
	}
	n := mygo.NewNotification(mygo.NotificationOptions{
		ID:    "refused/" + t.ID,
		Title: locales.NotifySkippedTitle(t.Name),
		Body:  locales.NotifyDestinationBusy(),
	})
	go func() {
		if err := n.Show(); err != nil {
			slog.Warn("notify", "err", err)
		}
	}()
}

// runScheduler is the scheduler's process: the system starts it at the
// times of the schedules and as the user logs in; it runs the tasks that
// are due, with no window, and quits. It shares the window's data folder,
// database, log and keychain items. launchd stops it with SIGTERM, as when
// the window changes the schedules: the run in progress is canceled and
// recorded.
func runScheduler() {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		log.Fatal(err)
	}
	out, closeLog := openLog(dir)
	defer closeLog()
	slog.SetDefault(slog.New(slog.NewTextHandler(out, nil)).With("process", "scheduler"))
	db, err := store.Open(filepath.Join(dir, "cloudshuttle.db"))
	if err != nil {
		slog.Error("open the database", "err", err)
		return
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	svc := service.New(service.Options{
		Owner:   service.OwnerScheduler,
		Store:   db,
		Secrets: keychain{},
		DataDir: dir,
		WakeApp: wakeApp,
	})
	if err := svc.RunDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("run the tasks that are due", "err", err)
	}
	svc.Close()
}

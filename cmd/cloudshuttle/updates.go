package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/starriv/cloudshuttle/internal/updates"
	"github.com/starriv/cloudshuttle/locales"
)

// mygoUpdates finds and installs updates with MyGo: signed releases of
// mygo.json's updates, on GitHub.
type mygoUpdates struct{}

func (mygoUpdates) Enabled() bool { return mygo.Updater.Enabled() }

func (mygoUpdates) Check(ctx context.Context) (updates.Release, error) {
	up, err := mygo.Updater.Check(ctx)
	if err != nil || up == nil {
		return nil, err
	}
	return mygoRelease{up}, nil
}

type mygoRelease struct{ up *mygo.Update }

func (r mygoRelease) Version() string { return r.up.Version }

func (r mygoRelease) Install(ctx context.Context, progress func(downloaded, total int64)) error {
	return r.up.Install(ctx, progress)
}

// updateStep takes the next step of the update that s is at, from the
// status menu: install the version found, restart into the one installed
// (once no task runs), or check.
func updateStep(u *updates.Updater, s updates.State) {
	switch s.Phase {
	case updates.PhaseAvailable, updates.PhaseInstallFailed:
		u.Install()
	case updates.PhaseReady:
		u.Restart()
	default:
		u.Check()
	}
}

// showAfterUpdate is the file, in the data folder, that tells the app
// restarted into an update to show its window again: the person was using
// it when the app restarted. relaunchMarker marks the restart itself, which
// is not the same as the person opening the app: a Dock app shows its window
// when it is opened, but not when an update installed by itself restarts
// it.
const (
	showAfterUpdate = "show-after-update"
	relaunchMarker  = "relaunched-into-update"
)

// relaunchIntoUpdate quits the app and opens it again, into the update
// installed, with its window shown if it was.
func relaunchIntoUpdate(dir string, windowShown bool) {
	if err := os.WriteFile(filepath.Join(dir, relaunchMarker), nil, 0o600); err != nil {
		slog.Warn("remember the restart into the update", "err", err)
	}
	if windowShown {
		if err := os.WriteFile(filepath.Join(dir, showAfterUpdate), nil, 0o600); err != nil {
			slog.Warn("remember the window across the restart", "err", err)
		}
	}
	mygo.App.Relaunch() // it goes to the main thread itself
}

// restartedIntoUpdate reports, once, whether the app restarted into an
// update at all.
func restartedIntoUpdate(dir string) bool {
	return os.Remove(filepath.Join(dir, relaunchMarker)) == nil
}

// shownBeforeUpdate reports, once, whether the app restarted into an update
// while its window showed.
func shownBeforeUpdate(dir string) bool {
	return os.Remove(filepath.Join(dir, showAfterUpdate)) == nil
}

// notifyUpdate tells the person about a newer version an automatic check
// found; open shows where to install it.
func notifyUpdate(version string, open func()) {
	if !mygo.NotificationsSupported() {
		return
	}
	n := mygo.NewNotification(mygo.NotificationOptions{
		ID:    "update/" + version,
		Title: locales.NotifyUpdateTitle(version),
		Body:  locales.NotifyUpdateBody(),
	})
	n.OnClick(func() { mygo.RunOnMain(open) })
	go func() {
		if err := n.Show(); err != nil {
			slog.Warn("notify", "err", err)
		}
	}()
}

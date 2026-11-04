package view

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/starriv/cloudshuttle/internal/updates"
	"github.com/starriv/cloudshuttle/locales"
)

type fakeRelease struct{}

func (fakeRelease) Version() string { return "1.3.0" }

func (fakeRelease) Install(_ context.Context, progress func(int64, int64)) error {
	progress(100, 100)
	return nil
}

type fakeUpdates struct{ newer bool }

func (fakeUpdates) Enabled() bool { return true }

func (f fakeUpdates) Check(context.Context) (updates.Release, error) {
	if !f.newer {
		return nil, nil
	}
	return fakeRelease{}, nil
}

// TestUpdatesCard checks for an update from the settings page, installs it
// while a task runs, and sees the restart wait for the task.
func TestUpdatesCard(t *testing.T) {
	h := newHarness(t)
	var runs atomic.Int32
	var relaunched atomic.Int32
	u := updates.New(updates.Options{
		Source:   fakeUpdates{newer: true},
		Current:  "1.2.0",
		Runs:     func() int { return int(runs.Load()) },
		Relaunch: func() { relaunched.Add(1) },
		OnChange: func() { h.host.Update(func() {}) },
	})
	h.app.SetUpdater(u)
	h.click(locales.NavSettings())
	h.expect(locales.SettingsUpdatesTitle())
	h.expect(locales.UpdatesVersion("1.2.0"))
	h.expect(locales.UpdatesIdle())

	h.click(locales.UpdatesCheck())
	h.waitFor("the check", func() bool { return u.State().Phase == updates.PhaseAvailable })
	h.settle()
	h.expect(locales.UpdatesAvailable("1.3.0"))

	runs.Store(1)
	h.click(locales.UpdatesInstall())
	h.waitFor("the install", func() bool { return u.State().Phase == updates.PhaseReady })
	h.settle()
	h.expect(locales.UpdatesWaiting(1))
	h.expect(locales.UpdatesCancelRestart())
	if relaunched.Load() != 0 {
		t.Fatal("restarted while a task runs")
	}
	runs.Store(0)
	u.RunsChanged()
	if relaunched.Load() != 1 {
		t.Fatal("did not restart once the task ended")
	}
}

// TestUpdatesChoicesAreKeptAtOnce turns automatic checks off from the card:
// kept without the page's Save, and installing by itself goes with them.
func TestUpdatesChoicesAreKeptAtOnce(t *testing.T) {
	h := newHarness(t)
	h.app.SetUpdater(updates.New(updates.Options{Source: fakeUpdates{}, Current: "1.2.0"}))
	h.click(locales.NavSettings())
	if !h.svc.Settings().CheckUpdates {
		t.Fatal("automatic checks are off by default")
	}
	h.app.chooseUpdates(true, true)
	h.waitFor("the save", func() bool { return h.svc.Settings().InstallUpdates })
	h.app.chooseUpdates(false, true)
	h.waitFor("the save", func() bool { return !h.svc.Settings().CheckUpdates })
	if h.svc.Settings().InstallUpdates {
		t.Fatal("installs by itself with automatic checks off")
	}
}

// TestUpdatesCardWithoutUpdates is a copy that cannot update itself: the
// card says so and links to the downloads, with no choices to make.
func TestUpdatesCardWithoutUpdates(t *testing.T) {
	h := newHarness(t)
	h.app.SetUpdater(updates.New(updates.Options{Source: disabledUpdates{}, Current: "1.2.0"}))
	h.click(locales.NavSettings())
	h.expect(locales.UpdatesUnavailable())
	h.expect(locales.UpdatesDownloadPage())
	if h.tt.HasText(locales.SettingsCheckUpdates()) {
		t.Fatal("offers automatic checks to a copy that cannot update")
	}
}

type disabledUpdates struct{ fakeUpdates }

func (disabledUpdates) Enabled() bool { return false }

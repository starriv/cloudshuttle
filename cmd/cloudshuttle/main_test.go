package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/egoist/mygo"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/view"
	"github.com/starriv/cloudshuttle/locales"
)

// TestFirstRunOpensTheWindow guards the one launch that shows the window by
// itself, so a new person finds where to add a storage and a task.
func TestFirstRunOpensTheWindow(t *testing.T) {
	if !firstRun(view.Snapshot{}) {
		t.Fatal("an empty app must open the window")
	}
	if firstRun(view.Snapshot{Tasks: []domain.Task{{ID: "t"}}}) {
		t.Fatal("an app with a task must stay in the menu bar")
	}
	if firstRun(view.Snapshot{Storages: []domain.Storage{{ID: "s"}}}) {
		t.Fatal("an app with a storage has been set up, and must stay in the menu bar")
	}
	if firstRun(view.Snapshot{TaskErr: errors.New("unreadable")}) || firstRun(view.Snapshot{StorageErr: errors.New("unreadable")}) {
		t.Fatal("a failed read must not be taken for an empty app")
	}
}

// TestTheAppIconDecidesWhereTheAppShows guards the mapping from the setting
// to the activation policy and the two icons.
func TestTheAppIconDecidesWhereTheAppShows(t *testing.T) {
	for _, c := range []struct {
		icon       domain.AppIcon
		policy     mygo.ActivationPolicy
		dock, menu bool
	}{
		{domain.AppIconDockAndMenuBar, mygo.ActivationPolicyRegular, true, true},
		{domain.AppIconDock, mygo.ActivationPolicyRegular, true, false},
		{domain.AppIconMenuBar, mygo.ActivationPolicyAccessory, false, true},
	} {
		if got := activationPolicyFor(c.icon); got != c.policy {
			t.Errorf("icon %v: policy = %q, want %q", c.icon, got, c.policy)
		}
		if got := appShowsDock(c.icon); got != c.dock {
			t.Errorf("icon %v: Dock = %v, want %v", c.icon, got, c.dock)
		}
		if got := appShowsMenuBar(c.icon); got != c.menu {
			t.Errorf("icon %v: menu bar = %v, want %v", c.icon, got, c.menu)
		}
	}
}

// A background update restart must not bring back the Dock icon the user
// dismissed by closing the window. Showing the window restores it; Dock-only
// mode (also the tray failure fallback) always keeps a way back to the window.
func TestTheDockStaysHiddenAcrossABackgroundUpdateRestart(t *testing.T) {
	for _, c := range []struct {
		name       string
		icon       domain.AppIcon
		shown      bool
		background bool
		want       mygo.ActivationPolicy
	}{
		{"closed window after update", domain.AppIconDockAndMenuBar, false, true, mygo.ActivationPolicyAccessory},
		{"visible window after update", domain.AppIconDockAndMenuBar, true, true, mygo.ActivationPolicyRegular},
		{"ordinary launch", domain.AppIconDockAndMenuBar, false, false, mygo.ActivationPolicyRegular},
		{"Dock only after update", domain.AppIconDock, false, true, mygo.ActivationPolicyRegular},
		{"menu bar only after update", domain.AppIconMenuBar, false, true, mygo.ActivationPolicyAccessory},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Startup first applies the background policy, then show restores
			// the Dock when the restart's marker says the window was visible.
			policy := activationPolicyForWindow(c.icon, !c.background)
			if showOnLaunch(false, c.shown, appShowsDock(c.icon), c.background) {
				policy = activationPolicyForWindow(c.icon, true)
			}
			if policy != c.want {
				t.Fatalf("policy = %q, want %q", policy, c.want)
			}
		})
	}
}

// TestOnlyMacOSLeavesTheDockOut guards that the other systems keep the
// notification-area icon whatever a restored backup says.
func TestOnlyMacOSLeavesTheDockOut(t *testing.T) {
	want := domain.AppIconDock
	if runtime.GOOS != "darwin" {
		want = domain.AppIconDockAndMenuBar
	}
	if got := iconOnThisSystem(domain.AppIconDock); got != want {
		t.Errorf("Dock only on %s = %v, want %v", runtime.GOOS, got, want)
	}
}

// TestTheWindowOpensWhenTheAppDoes guards the launches that show the window:
// a first run, an update the person was using, and opening a Dock app. A
// launch the system makes in the background is not opening the app.
func TestTheWindowOpensWhenTheAppDoes(t *testing.T) {
	for _, c := range []struct {
		name                                     string
		firstRun, windowShowed, dock, background bool
		want                                     bool
	}{
		{"first run", true, false, false, false, true},
		{"update the person was using", false, true, false, true, true},
		{"update installed in the background", false, false, true, true, false},
		{"a Dock app opening", false, false, true, false, true},
		{"a menu bar app opening", false, false, false, false, false},
		{"a wake in the background", false, false, true, true, false},
	} {
		if got := showOnLaunch(c.firstRun, c.windowShowed, c.dock, c.background); got != c.want {
			t.Errorf("%s: show = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestAnUpdateRestartIsToldApartFromThePersonOpeningTheApp guards the two
// markers the restart leaves: one says it restarted at all, the other that
// its window showed, each read once.
func TestAnUpdateRestartIsToldApartFromThePersonOpeningTheApp(t *testing.T) {
	dir := t.TempDir()
	if restartedIntoUpdate(dir) || shownBeforeUpdate(dir) {
		t.Fatal("an ordinary launch looks like a restart into an update")
	}
	for _, name := range []string{relaunchMarker, showAfterUpdate} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !restartedIntoUpdate(dir) {
		t.Fatal("the restart into the update was not seen")
	}
	if !shownBeforeUpdate(dir) {
		t.Fatal("the window that showed was not seen")
	}
	if restartedIntoUpdate(dir) || shownBeforeUpdate(dir) {
		t.Fatal("a marker was reported twice")
	}
}

// TestTheHostMovesTheIconAtOnce guards that a choice from the settings
// reaches the window at once, and that nothing happens before it is ready.
func TestTheHostMovesTheIconAtOnce(t *testing.T) {
	host := &windowHost{}
	host.SetAppIcon(domain.AppIconDock) // before the window is ready
	var got []domain.AppIcon
	host.icon = func(icon domain.AppIcon) { got = append(got, icon) }
	host.SetAppIcon(domain.AppIconMenuBar)
	if len(got) != 1 || got[0] != domain.AppIconMenuBar {
		t.Fatalf("the host was shown %v, want the menu bar once", got)
	}
}

// TestAppMenuStartsWithTheAboutThePanelShows guards the About the window
// no longer says: the app menu's first item opens the system's panel.
func TestAppMenuStartsWithTheAboutThePanelShows(t *testing.T) {
	checked := 0
	items := appMenu(func() { checked++ }).Items()
	if len(items) == 0 || items[0].Role != mygo.RoleAppMenu {
		t.Fatal("the menu bar does not start with the app menu")
	}
	sub := items[0].Submenu
	if len(sub) == 0 {
		t.Fatal("the app menu is empty")
	}
	if about := sub[0]; about.Label != locales.MenuAbout() || about.Click == nil {
		t.Fatalf("first item = %q (click %t), want %q", about.Label, about.Click != nil, locales.MenuAbout())
	}
	// Check for Updates comes next, as in other Mac apps.
	if len(sub) < 2 || sub[1].Label != locales.MenuCheckUpdates() {
		t.Fatal("the app menu has no Check for Updates after About")
	}
	sub[1].Click(sub[1], nil)
	if checked != 1 {
		t.Fatal("Check for Updates did not check")
	}
}

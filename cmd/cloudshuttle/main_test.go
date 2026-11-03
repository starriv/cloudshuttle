package main

import (
	"testing"

	"github.com/egoist/mygo"
	"github.com/starriv/cloudshuttle/locales"
)

// TestAppMenuStartsWithTheAboutThePanelShows guards the About the window
// no longer says: the app menu's first item opens the system's panel.
func TestAppMenuStartsWithTheAboutThePanelShows(t *testing.T) {
	items := appMenu().Items()
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
}

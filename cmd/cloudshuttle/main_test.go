package main

import (
	"errors"
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

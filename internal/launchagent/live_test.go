package launchagent

import (
	"os"
	"testing"

	"github.com/starriv/cloudshuttle/internal/domain"
)

// TestLiveRegister loads a throwaway job into launchd and removes it; set
// CLOUDSHUTTLE_TEST_LAUNCHD to a folder for its definition.
func TestLiveRegister(t *testing.T) {
	dir := os.Getenv("CLOUDSHUTTLE_TEST_LAUNCHD")
	if dir == "" {
		t.Skip("set CLOUDSHUTTLE_TEST_LAUNCHD to load a job into launchd")
	}
	a := Agent{Label: "com.starriv.cloudshuttle.test-scheduler", Program: "/usr/bin/true", Dir: dir}
	wakes := domain.Schedule{Repeat: domain.RepeatDaily, Hour: 4, Minute: 17}.Wakes()
	if err := a.Register(wakes); err != nil {
		t.Fatal(err)
	}
	if !a.Loaded() {
		t.Fatal("not loaded")
	}
	// The same schedule again leaves it as it is.
	if err := a.Register(wakes); err != nil {
		t.Fatal(err)
	}
	if err := a.Register(nil); err != nil {
		t.Fatal(err)
	}
	if a.Loaded() {
		t.Fatal("still loaded after removing it")
	}
	if _, err := os.Stat(dir + "/" + a.Label + ".plist"); !os.IsNotExist(err) {
		t.Fatalf("the definition is left: %v", err)
	}
}

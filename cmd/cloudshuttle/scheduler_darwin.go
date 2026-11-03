package main

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/starriv/cloudshuttle/internal/launchagent"
	"github.com/starriv/cloudshuttle/internal/service"
)

// appBundle returns the app bundle the executable runs from, and its
// identifier; ok is false outside one, as under go run.
func appBundle() (bundle, exe, id string, ok bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", "", false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	macOS := filepath.Dir(exe)
	contents := filepath.Dir(macOS)
	bundle = filepath.Dir(contents)
	if filepath.Base(macOS) != "MacOS" || filepath.Base(contents) != "Contents" || !strings.HasSuffix(bundle, ".app") {
		return "", "", "", false
	}
	info, err := os.ReadFile(filepath.Join(contents, "Info.plist"))
	if err != nil {
		return "", "", "", false
	}
	m := bundleIDPattern.FindSubmatch(info)
	if m == nil {
		return "", "", "", false
	}
	return bundle, exe, string(m[1]), true
}

var bundleIDPattern = regexp.MustCompile(`<key>CFBundleIdentifier</key>\s*<string>([^<]+)</string>`)

// newRegistrar is launchd, which starts the scheduler, this executable
// with schedulerArg, at the schedules' times and as the user logs in. An
// app run outside its bundle has none: a launch agent would outlive the
// build it names.
func newRegistrar() service.Registrar {
	_, exe, id, ok := appBundle()
	if !ok {
		return nil
	}
	return launchagent.Agent{Label: id + ".scheduler", Program: exe, Args: []string{schedulerArg}, BundleID: id}
}

// wakeApp opens the app in the background, with no window showing, for it
// to tell a failure of the scheduler's.
func wakeApp() {
	bundle, _, _, ok := appBundle()
	if !ok {
		return
	}
	if out, err := exec.Command("open", "-g", "-j", "-a", bundle, "--args", backgroundArg).CombinedOutput(); err != nil {
		slog.Warn("open the app to tell a failure", "err", err, "out", string(out))
	}
}

// openBackgroundSettings opens System Settings where the person lets apps
// run in the background.
func openBackgroundSettings() error {
	return exec.Command("open", "x-apple.systempreferences:com.apple.LoginItems-Settings.extension").Run()
}

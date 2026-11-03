// Package icloud locates CloudShuttle's own folder in the person's iCloud
// Drive, where the app keeps a copy of its configuration.
package icloud

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// FolderName is the folder the app keeps in iCloud Drive, apart from the
// folders of the person's other apps.
const FolderName = "CloudShuttle"

// ErrUnavailable is iCloud Drive not being set up on this computer.
var ErrUnavailable = errors.New("iCloud Drive is not available")

// Supported reports whether this system has iCloud Drive at all.
func Supported() bool { return runtime.GOOS == "darwin" }

// Folder returns the app's own folder in iCloud Drive, creating it when it
// is not there yet. It returns ErrUnavailable when iCloud Drive is off.
func Folder() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	drive := filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs")
	return folder(drive)
}

// folder returns the app's folder inside drive. The app is not sandboxed, so
// iCloud Drive is an ordinary folder in the home folder; a ubiquity
// container would need an entitlement the app does not carry.
func folder(drive string) (string, error) {
	if !Supported() {
		return "", ErrUnavailable
	}
	if fi, err := os.Stat(drive); err != nil || !fi.IsDir() {
		return "", ErrUnavailable
	}
	dir := filepath.Join(drive, FolderName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

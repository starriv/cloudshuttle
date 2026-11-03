package icloud

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFolder(t *testing.T) {
	if !Supported() {
		t.Skip("iCloud Drive is macOS only")
	}
	drive := t.TempDir()
	dir, err := folder(drive)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(drive, FolderName); dir != want {
		t.Fatalf("folder = %q, want %q", dir, want)
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("the app's folder was not made: %v", err)
	}
	// The configuration carries the names of servers and the folders tasks
	// use: only the person may read it.
	if mode := fi.Mode().Perm(); mode != 0o700 {
		t.Fatalf("folder mode = %o, want 700", mode)
	}

	// A folder there already is the same folder, not an error.
	if _, err := folder(drive); err != nil {
		t.Fatalf("a second call: %v", err)
	}
}

func TestFolderWithoutICloudDrive(t *testing.T) {
	if !Supported() {
		t.Skip("iCloud Drive is macOS only")
	}
	if _, err := folder(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("folder = %v, want ErrUnavailable", err)
	}
}

func TestFolderTakenByAFile(t *testing.T) {
	if !Supported() {
		t.Skip("iCloud Drive is macOS only")
	}
	drive := t.TempDir()
	if err := os.WriteFile(filepath.Join(drive, FolderName), []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := folder(drive); err == nil {
		t.Fatal("folder made itself over a file")
	}
}

package view

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/locales"
)

func TestSettingsPageOffersICloudBackup(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("iCloud Drive is macOS only")
	}
	h := newHarness(t)
	h.click(locales.NavSettings())
	h.expect(locales.SettingsBackupTitle())
	h.expect(locales.SettingsBackupICloud())
	h.expect(locales.SettingsBackupICloudHint())
}

// TestBackupPasswordAndRestore turns backups on with a password, then
// restores the backup they wrote, as a person would on a new computer.
func TestBackupPasswordAndRestore(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("iCloud Drive is macOS only")
	}
	folder := t.TempDir()
	h := newHarnessWith(t, service.Options{DataDir: t.TempDir(), BackupFolder: func() (string, error) { return folder, nil }})
	h.click(locales.NavSettings())
	h.app.settingsForm.backupICloud = true
	h.settle()
	h.expect(locales.SettingsBackupPassword())

	// No password: the backups cannot be turned on.
	h.app.saveSettings()
	h.waitFor("the save", func() bool { return !h.app.settingsForm.saving })
	h.expect(locales.ErrorPasswordRequired())

	h.app.settingsForm.password, h.app.settingsForm.confirm = "long enough", "long enougj"
	h.app.saveSettings()
	h.waitFor("the save", func() bool { return !h.app.settingsForm.saving })
	h.expect(locales.ErrorPasswordMismatch())

	h.app.settingsForm.confirm = "long enough"
	h.app.saveSettings()
	h.waitFor("the save", func() bool { return !h.app.settingsForm.saving })
	h.expect(locales.SettingsBackupPasswordSet())
	h.expect(locales.SettingsBackupPasswordChange())
	backup := filepath.Join(folder, service.BackupName)
	h.waitFor("the backup", func() bool { _, err := os.Stat(backup); return err == nil })

	// Restore it, with a wrong password first.
	h.app.restoreForm = restoreForm{open: true} // the button is below the test window's fold
	h.app.restoreForm.path, h.app.restoreForm.password = backup, "wrong password"
	h.settle()
	h.click(locales.RestoreButton())
	h.waitFor("the restore", func() bool { return !h.app.restoreForm.busy })
	h.expect(locales.RestoreWrongPassword())
	h.app.restoreForm.password = "long enough"
	h.settle()
	h.click(locales.RestoreButton())
	h.waitFor("the restore", func() bool { return !h.app.restoreForm.busy })
	h.settle()
	if h.app.restoreForm.open {
		t.Fatalf("the dialog stayed open: %q", h.app.restoreForm.problem)
	}
	h.expect(locales.RestoreDone())
	if got := h.svc.Settings(); !got.BackupICloud {
		t.Fatalf("settings = %+v, want those of the backup", got)
	}
}

// The storage and restore sheets scroll their body at the window's smallest
// size, so their foot, where Cancel and Save are, stays at hand.
func TestTheStorageSheetsFitTheSmallestWindow(t *testing.T) {
	h := newHarness(t)
	h.tt.SetSize(900, 640)
	h.tt.Advance(500 * time.Millisecond)

	h.click(locales.NavStorages())
	h.click(locales.StoragesAdd())
	h.settle()
	if !h.app.storageForm.open {
		t.Fatal("the storage sheet is not open")
	}
	h.click(locales.StorageSave()) // a problem, not a scroll, is what answers this
	h.settle()
	if !h.app.storageForm.open {
		t.Fatal("the storage sheet closed without saving")
	}
	h.click(locales.ButtonCancel())
	h.settle()
	if h.app.storageForm.open {
		t.Fatal("the storage sheet did not close at the smallest window")
	}

	h.app.restoreForm = restoreForm{open: true, path: filepath.Join(t.TempDir(), "backup")}
	h.settle()
	if !h.app.restoreForm.open {
		t.Fatal("the restore sheet is not open")
	}
	h.click(locales.ButtonCancel())
	h.settle()
	if h.app.restoreForm.open {
		t.Fatal("the restore sheet did not close at the smallest window")
	}
}

package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icloud"
	"github.com/starriv/cloudshuttle/internal/seal"
	"github.com/starriv/cloudshuttle/internal/store"
)

// password is the backups' password in these tests.
const password = "correct horse battery"

// newBackupService makes a service that copies its configuration into
// folder, a stand-in for iCloud Drive.
func newBackupService(t *testing.T, folder func() (string, error)) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: st, Secrets: &memSecrets{m: map[string]string{}}, BackupFolder: folder, DataDir: t.TempDir()})
	t.Cleanup(func() {
		s.Close()
		st.Close()
	})
	return s, st
}

// waitForConfig waits until the backup in folder, opened with password,
// holds what check asks of it.
func waitForConfig(t *testing.T, folder string, check func(domain.Settings, []domain.Storage, []domain.Task) bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opened.db")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if sealed, err := os.ReadFile(filepath.Join(folder, BackupName)); err == nil {
			plain, err := seal.Open(password, sealed)
			if err != nil {
				t.Fatalf("the backup does not open with its password: %v", err)
			}
			if bytes.Contains(sealed, []byte("SQLite format")) || bytes.Contains(sealed, []byte("cloudflarestorage")) {
				t.Fatal("the backup is not encrypted")
			}
			os.Remove(path)
			os.WriteFile(path, plain, 0o600)
			if copy, err := store.Open(path); err == nil {
				ctx := context.Background()
				settings, settingsErr := copy.Settings(ctx)
				storages, storagesErr := copy.Storages(ctx)
				tasks, tasksErr := copy.Tasks(ctx)
				runs, runsErr := copy.Runs(ctx, store.RunFilter{})
				copy.Close()
				if settingsErr == nil && storagesErr == nil && tasksErr == nil && runsErr == nil &&
					len(runs) == 0 && check(settings, storages, tasks) {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no backup in %s holds the configuration", folder)
}

func TestConfigurationIsCopiedToICloud(t *testing.T) {
	ctx := context.Background()
	folder := t.TempDir()
	s, _ := newBackupService(t, func() (string, error) { return folder, nil })

	settings := domain.DefaultSettings()
	settings.BackupICloud = true
	if _, err := s.SaveSettings(ctx, settings); domain.AsProblems(err).For(domain.FieldBackupPassword) != domain.CodeRequired {
		t.Fatalf("backups on without a password: %v, want the password required", err)
	}
	if err := s.SetBackupPassword(ctx, password, password); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateStorage(ctx, domain.StorageInput{Name: "R2", Provider: domain.ProviderCloudflare,
		Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "b", AccessKeyID: "k", Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	// The copy holds the configuration, and the settings say it is kept.
	waitForConfig(t, folder, func(got domain.Settings, storages []domain.Storage, tasks []domain.Task) bool {
		return got.BackupICloud && len(storages) == 1 && storages[0].Name == "R2" && len(tasks) == 0
	})

	// A change after the first copy reaches it too.
	settings.RetentionDays = 30
	if _, err := s.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	waitForConfig(t, folder, func(got domain.Settings, _ []domain.Storage, _ []domain.Task) bool {
		return got.RetentionDays == 30
	})
}

func TestConfigurationIsCopiedWhenTheAppStarts(t *testing.T) {
	ctx := context.Background()
	folder := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	settings := domain.DefaultSettings()
	settings.BackupICloud = true
	if err := st.PutSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	k, err := seal.NewKey(password)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: st, Secrets: &memSecrets{m: map[string]string{backupKeySecret: k.Encode()}},
		BackupFolder: func() (string, error) { return folder, nil }, DataDir: t.TempDir()})
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitForConfig(t, folder, func(got domain.Settings, _ []domain.Storage, _ []domain.Task) bool {
		return got.BackupICloud
	})
}

func TestICloudBackupIsRefusedWithoutICloudDrive(t *testing.T) {
	ctx := context.Background()
	s, _ := newBackupService(t, func() (string, error) { return "", icloud.ErrUnavailable })
	if err := s.SetBackupPassword(ctx, password, password); err != nil {
		t.Fatal(err)
	}

	settings := domain.DefaultSettings()
	settings.BackupICloud = true
	_, err := s.SaveSettings(ctx, settings)
	if problems := domain.AsProblems(err); problems.For(domain.FieldSettings) != domain.CodeICloudUnavailable {
		t.Fatalf("saving %+v = %v, want an iCloud problem", settings, err)
	}
	if got := s.Settings(); got.BackupICloud {
		t.Fatalf("settings = %+v after a refused save, want the backup off", got)
	}
}

func TestNoConfigurationCopyWithoutPermission(t *testing.T) {
	ctx := context.Background()
	folder := t.TempDir()
	s, _ := newBackupService(t, func() (string, error) { return folder, nil })
	if _, err := s.CreateStorage(ctx, domain.StorageInput{Name: "R2", Provider: domain.ProviderCloudflare,
		Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "b", AccessKeyID: "k", Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	// Let any copy a bug scheduled run and write.
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(folder, BackupName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a copy was made without permission: %v", err)
	}
}

func TestPasswordRules(t *testing.T) {
	ctx := context.Background()
	s, _ := newBackupService(t, func() (string, error) { return t.TempDir(), nil })
	cases := []struct {
		password, confirm string
		field             domain.Field
		code              domain.Code
	}{
		{"", "", domain.FieldBackupPassword, domain.CodeRequired},
		{"long enough", "long enougj", domain.FieldBackupConfirm, domain.CodeMismatch},
	}
	for _, c := range cases {
		if got := domain.AsProblems(s.SetBackupPassword(ctx, c.password, c.confirm)).For(c.field); got != c.code {
			t.Errorf("%q, %q: %s = %q, want %q", c.password, c.confirm, c.field, got, c.code)
		}
	}
	if s.HasBackupPassword() {
		t.Fatal("a refused password was kept")
	}
	// Any password the person confirms is theirs to choose.
	if err := s.SetBackupPassword(ctx, "猫", "猫"); err != nil {
		t.Fatal(err)
	}
	if !s.HasBackupPassword() {
		t.Fatal("the password was not kept")
	}
}

func TestRestoreBackup(t *testing.T) {
	ctx := context.Background()
	folder := t.TempDir()
	s, _ := newBackupService(t, func() (string, error) { return folder, nil })
	if err := s.SetBackupPassword(ctx, password, password); err != nil {
		t.Fatal(err)
	}
	settings := domain.DefaultSettings()
	settings.BackupICloud, settings.RetentionDays = true, 30
	if _, err := s.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateStorage(ctx, domain.StorageInput{Name: "R2", Provider: domain.ProviderCloudflare,
		Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "b", AccessKeyID: "k", Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	waitForConfig(t, folder, func(_ domain.Settings, storages []domain.Storage, _ []domain.Task) bool {
		return len(storages) == 1
	})
	backup := filepath.Join(t.TempDir(), BackupName)
	data, _ := os.ReadFile(filepath.Join(folder, BackupName))
	os.WriteFile(backup, data, 0o600)

	// On another computer: nothing set up yet.
	other, _ := newBackupService(t, func() (string, error) { return t.TempDir(), nil })
	if err := other.RestoreBackup(ctx, backup, "wrong password"); !errors.Is(err, domain.ErrWrongPassword) {
		t.Fatalf("a wrong password: %v", err)
	}
	notBackup := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(notBackup, []byte("hello"), 0o600)
	if err := other.RestoreBackup(ctx, notBackup, password); !errors.Is(err, domain.ErrNotBackup) {
		t.Fatalf("a file that is no backup: %v", err)
	}
	if err := other.RestoreBackup(ctx, backup, password); err != nil {
		t.Fatal(err)
	}
	storages, _ := other.Storages(ctx)
	if len(storages) != 1 || storages[0].Name != "R2" {
		t.Fatalf("storages = %+v", storages)
	}
	if got := other.Settings(); got.RetentionDays != 30 {
		t.Fatalf("settings = %+v", got)
	}

	// A backup of an earlier version, unencrypted, restores too.
	plain := filepath.Join(t.TempDir(), "cloudshuttle.db")
	if err := s.store.BackupConfig(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if err := other.RestoreBackup(ctx, plain, ""); err != nil {
		t.Fatalf("an unencrypted backup: %v", err)
	}
}

func TestTheUnencryptedBackupGoes(t *testing.T) {
	ctx := context.Background()
	folder := t.TempDir()
	old := filepath.Join(folder, "cloudshuttle.db")
	os.WriteFile(old, []byte("SQLite format 3\x00"), 0o600)
	s, _ := newBackupService(t, func() (string, error) { return folder, nil })
	if err := s.SetBackupPassword(ctx, password, password); err != nil {
		t.Fatal(err)
	}
	settings := domain.DefaultSettings()
	settings.BackupICloud = true
	if _, err := s.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	waitForConfig(t, folder, func(got domain.Settings, _ []domain.Storage, _ []domain.Task) bool { return got.BackupICloud })
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the unencrypted copy is still in iCloud Drive: %v", err)
	}
}

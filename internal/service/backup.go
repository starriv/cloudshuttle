package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine"
	"github.com/starriv/cloudshuttle/internal/seal"
	"github.com/starriv/cloudshuttle/internal/store"
)

// The backup of the configuration
//
// The storages, tasks and settings are copied into iCloud Drive as one
// file, encrypted with a password the person chooses (package seal:
// Argon2id, XChaCha20-Poly1305). The credential store keeps the key made
// from it, not the password, so that each change is backed up without
// asking; the password alone opens the file again, here or on another
// computer. The plain copy exists only for a moment, in the app's own
// data folder, never in iCloud Drive.

const (
	// BackupName is the encrypted backup in the iCloud folder.
	BackupName = "cloudshuttle.backup"
	// plainBackupName is the unencrypted copy earlier versions wrote,
	// removed once an encrypted one is in its place.
	plainBackupName = "cloudshuttle.db"
	// backupKeySecret is the credential store's key of the backup key.
	backupKeySecret = "backup/key"
)

// HasBackupPassword reports whether a password for the backups is set.
func (s *Service) HasBackupPassword() bool {
	_, err := s.backupKeyOf()
	return err == nil
}

// backupKeyOf returns the key the backups are encrypted with, read from the
// credential store the first time.
func (s *Service) backupKeyOf() (*seal.Key, error) {
	s.backupMu.Lock()
	k := s.backupKey
	s.backupMu.Unlock()
	if k != nil {
		return k, nil
	}
	text, err := s.secrets.Get(backupKeySecret)
	if err != nil {
		return nil, err
	}
	k, err = seal.DecodeKey(text)
	if err != nil {
		return nil, err
	}
	s.backupMu.Lock()
	s.backupKey = k
	s.backupMu.Unlock()
	return k, nil
}

// SetBackupPassword sets the password the backups are encrypted with, from
// the next one on, which it writes at once when backups are on. Making the
// key takes a fraction of a second on purpose: call it off the main thread.
func (s *Service) SetBackupPassword(ctx context.Context, password, confirm string) error {
	var p domain.Problems
	switch {
	case password == "":
		p = append(p, domain.Problem{Field: domain.FieldBackupPassword, Code: domain.CodeRequired})
	case confirm != password:
		p = append(p, domain.Problem{Field: domain.FieldBackupConfirm, Code: domain.CodeMismatch})
	}
	if len(p) > 0 {
		return p
	}
	k, err := seal.NewKey(password)
	if err != nil {
		return err
	}
	if err := s.secrets.Set(backupKeySecret, k.Encode()); err != nil {
		return fmt.Errorf("keep the backup key: %w", err)
	}
	s.backupMu.Lock()
	s.backupKey = k
	s.backupMu.Unlock()
	s.onChange()
	s.backupIfEnabled()
	return nil
}

// BackupFolder returns where the backups are kept, for a person to look
// for one there.
func (s *Service) BackupFolder() (string, error) { return s.backupDir() }

// copyConfig writes the configuration, encrypted, into the iCloud folder,
// replacing the last backup in one step: a half-written one is never what
// iCloud keeps.
func (s *Service) copyConfig(ctx context.Context) error {
	k, err := s.backupKeyOf()
	if err != nil {
		return fmt.Errorf("no backup password: %w", err)
	}
	dir, err := s.backupDir()
	if err != nil {
		return err
	}
	plain, err := s.plainConfig(ctx)
	if err != nil {
		return err
	}
	sealed, err := k.Seal(plain)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+BackupName+".tmp")
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, BackupName)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// The unencrypted copy of earlier versions goes, now that an
	// encrypted one has its place.
	if err := os.Remove(filepath.Join(dir, plainBackupName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("remove the unencrypted backup", "err", err)
	}
	return nil
}

// plainConfig returns the configuration as a database file's bytes, made
// in a folder of the app's own that only the person can read.
func (s *Service) plainConfig(ctx context.Context) ([]byte, error) {
	tmpDir, err := os.MkdirTemp(s.dataDir, ".backup-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	path := filepath.Join(tmpDir, "config.db")
	if err := s.store.BackupConfig(ctx, path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// RestoreBackup replaces the storages, tasks and settings with those of the
// backup at path, opened with password; the history stays. It refuses while
// tasks run, in the window or the scheduler. A backup of an earlier version
// that was not encrypted opens without a password. The secrets of the
// storages are not in a backup: those this computer does not have already
// are to be entered again.
func (s *Service) RestoreBackup(ctx context.Context, path, password string) error {
	s.mu.Lock()
	busy := len(s.active) > 0 || len(s.foreign) > 0
	s.mu.Unlock()
	if busy {
		return domain.ErrRunsActive
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	switch {
	case seal.Sealed(data):
		data, err = seal.Open(password, data)
		switch {
		case errors.Is(err, seal.ErrWrongPassword):
			return domain.ErrWrongPassword
		case err != nil:
			return fmt.Errorf("%w: %w", domain.ErrNotBackup, err)
		}
	case !bytes.HasPrefix(data, []byte("SQLite format 3\x00")):
		return domain.ErrNotBackup
	}
	tmpDir, err := os.MkdirTemp(s.dataDir, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	plain := filepath.Join(tmpDir, "config.db")
	if err := os.WriteFile(plain, data, 0o600); err != nil {
		return err
	}
	unlock, err := s.lockMaintenance(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	s.mu.Lock()
	busy = len(s.active) > 0
	s.mu.Unlock()
	if busy {
		return domain.ErrRunsActive
	}
	if err := s.store.RestoreConfig(ctx, plain, func(backup *store.Store) error {
		storages, err := backup.Storages(ctx)
		if err != nil {
			return err
		}
		tasks, err := backup.Tasks(ctx)
		if err != nil {
			return err
		}
		resolved := make([]domain.TaskInput, 0, len(tasks))
		for _, t := range tasks {
			if t.Exclude.Unreadable {
				return domain.ErrNotBackup
			}
			for _, p := range t.Exclude.Patterns {
				if !engine.ValidExclude(p) {
					return domain.ErrNotBackup
				}
			}
			in, err := resolveTask(taskInput(t))
			if err != nil {
				return domain.ErrNotBackup
			}
			problems := domain.AsProblems(in.Validate(storages))
			if len(s.protectDestination(problems, in)) != 0 {
				return domain.ErrNotBackup
			}
			for _, other := range resolved {
				if (in.Mode == domain.ModeSync || other.Mode == domain.ModeSync) && domain.Overlap(in.Destination, other.Destination, storages) {
					return domain.ErrNotBackup
				}
			}
			resolved = append(resolved, in)
		}
		st, err := backup.Settings(ctx)
		if err != nil {
			return domain.ErrNotBackup
		}
		for _, p := range st.GlobalExcludes {
			if !engine.ValidExclude(p) {
				return domain.ErrNotBackup
			}
		}
		return nil
	}); err != nil {
		if strings.Contains(err.Error(), "no such table") || strings.Contains(err.Error(), "not a database") {
			return fmt.Errorf("%w: %w", domain.ErrNotBackup, err)
		}
		return err
	}
	if st, err := s.store.Settings(ctx); err == nil {
		s.settingsMu.Lock()
		s.settings = st
		s.settingsMu.Unlock()
	}
	slog.Info("restored the configuration from a backup", "path", path)
	s.requestSync()
	s.onChange()
	s.backupIfEnabled()
	return nil
}

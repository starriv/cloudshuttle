package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine"
)

func (s *Service) browserRemote(ctx context.Context, expected domain.Storage, path string) (engine.Location, error) {
	if !validBrowserPath(expected, path) ||
		expected.Kind == domain.KindS3 && (expected.Bucket == "" || !domain.ValidStoragePath(expected.Bucket) || strings.Contains(expected.Bucket, "/")) {
		return engine.Location{}, domain.ErrStoragePath
	}
	st, err := s.store.Storage(ctx, expected.ID)
	if err != nil {
		return engine.Location{}, err
	}
	if !st.SameConnection(expected) || st.UpdatedAt.UnixMilli() != expected.UpdatedAt.UnixMilli() {
		return engine.Location{}, domain.ErrStorageChanged
	}
	secret, err := s.secret(st)
	if err != nil {
		return engine.Location{}, err
	}
	return remote(st, secret, path), nil
}

func validBrowserPath(st domain.Storage, path string) bool {
	if !domain.ValidStoragePath(path) {
		return false
	}
	if st.Kind == domain.KindSMB {
		for _, part := range strings.Split(path, "/") {
			if strings.TrimRight(part, ". ") != part || strings.ContainsAny(part, `\:<>"|?*`) {
				return false
			}
		}
	}
	return true
}

// StorageEntries browses a saved bucket, share or directory.
func (s *Service) StorageEntries(ctx context.Context, st domain.Storage, path string) ([]domain.StorageEntry, bool, error) {
	loc, err := s.browserRemote(ctx, st, path)
	if err != nil {
		return nil, false, err
	}
	if st.Kind == domain.KindSMB && path == "" {
		shares, err := engine.Folders(ctx, loc)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("list storage shares", "storage", st.Name, "err", err)
			}
			return nil, false, connectionError(err)
		}
		entries := make([]domain.StorageEntry, 0, len(shares))
		for _, name := range shares {
			if validBrowserPath(st, name) && !strings.Contains(name, "/") {
				entries = append(entries, domain.StorageEntry{Name: name, Directory: true})
			}
		}
		return entries, false, nil
	}
	entries, limited, err := engine.StorageEntries(ctx, loc)
	if err != nil && ctx.Err() == nil {
		slog.Warn("list storage directory", "storage", st.Name, "err", err)
	}
	return entries, limited, connectionError(err)
}

// StorageWork reports browser transfers that should finish before restarting.
func (s *Service) StorageWork() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.browserWork
}

func (s *Service) beginStorageWork(ctx context.Context) (context.Context, func()) {
	s.mu.Lock()
	ctx, cancel := context.WithCancel(ctx)
	if s.closed {
		s.mu.Unlock()
		cancel()
		return ctx, func() {}
	}
	s.browserWork++
	s.bg.Add(1)
	s.mu.Unlock()
	stop := context.AfterFunc(s.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
		s.mu.Lock()
		s.browserWork--
		idle := s.browserWork == 0 && !s.closed
		s.mu.Unlock()
		s.bg.Done()
		if idle {
			s.onChange()
		}
	}
}

func (s *Service) DownloadStorageFile(ctx context.Context, st domain.Storage, dir, name, folder string) error {
	ctx, done := s.beginStorageWork(ctx)
	defer done()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validBrowserPath(st, name) || name == "" || strings.Contains(name, "/") ||
		!filepath.IsLocal(name) || st.Kind == domain.KindSMB && dir == "" || !filepath.IsAbs(folder) {
		return domain.ErrStoragePath
	}
	loc, err := s.browserRemote(ctx, st, dir)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(folder, ".cloudshuttle-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	readErr := engine.DownloadStorageFile(ctx, loc, name, tmp)
	// CreateTemp makes the file private; a download is an ordinary file.
	if err := errors.Join(readErr, tmp.Chmod(downloadMode), tmp.Close(), ctx.Err()); err != nil {
		return err
	}
	err = publishDownload(tmp.Name(), filepath.Join(folder, name))
	if errors.Is(err, os.ErrExist) {
		return domain.ErrStorageExists
	}
	return err
}

const downloadMode = 0o644

// linkFile is os.Link; a test replaces it to act as a volume without links.
var linkFile = os.Link

// publishDownload puts the completed download at target without replacing a
// file created there while it ran. A hard link publishes it whole at once;
// exFAT, FAT and network volumes have no hard links, so there it is copied
// into a file created exclusively, which only this download can have made.
func publishDownload(tmp, target string) error {
	err := linkFile(tmp, target)
	if err == nil || errors.Is(err, os.ErrExist) {
		return err
	}
	in, err := os.Open(tmp)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, downloadMode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err = errors.Join(err, out.Sync(), out.Close()); err != nil {
		_ = os.Remove(target)
		return err
	}
	return nil
}

func (s *Service) PrepareStorageDelete(ctx context.Context, st domain.Storage, dir string, entry domain.StorageEntry) (domain.StorageEntry, error) {
	if entry.Name == "" || strings.Contains(entry.Name, "/") || !validBrowserPath(st, entry.Name) {
		return entry, domain.ErrStoragePath
	}
	loc, err := s.browserRemote(ctx, st, dir)
	if err != nil {
		return entry, err
	}
	return engine.PrepareStorageDelete(ctx, loc, entry)
}

type StorageChange struct {
	Source, Target, LocalFile string
	Directory, Delete         bool
	Replace                   *domain.StorageReplacement
	ExpectedSource            *domain.StorageEntry
	ConfirmedTasks            []domain.Task
	// Queued, when set, hears true when the change has to wait for a run to
	// finish before it may write, and false when its turn comes. It is
	// called from the goroutine that runs the change.
	Queued func(queued bool)
}

// StorageTaskConflict names the tasks a change touches: those whose
// destination it changes, which a later sync may overwrite, and those whose
// source it changes, which their next run carries to the destination.
type StorageTaskConflict struct{ Destinations, Sources []domain.Task }

func (e *StorageTaskConflict) Error() string { return "storage change overlaps task folders" }

// Tasks is every task the change touches, as ConfirmedTasks takes them.
func (e *StorageTaskConflict) Tasks() []domain.Task {
	return append(slices.Clip(e.Destinations), e.Sources...)
}

func (s *Service) ChangeStorage(ctx context.Context, st domain.Storage, change StorageChange) error {
	c := change
	switch {
	case c.Delete && (c.Source == "" || c.Target != "" || c.LocalFile != ""),
		c.Delete && !c.Directory && (c.ExpectedSource == nil || c.ExpectedSource.Directory),
		c.Directory && !c.Delete && (c.Source != "" || c.LocalFile != ""),
		!c.Delete && c.Target == "",
		!c.Directory && !c.Delete && (c.Source == "") == (c.LocalFile == ""),
		c.Replace != nil && c.LocalFile == "":
		return domain.ErrStoragePath
	}

	root := ""
	source, target := c.Source, c.Target
	for _, p := range []string{source, target} {
		if p == "" {
			continue
		}
		if !validBrowserPath(st, p) {
			return domain.ErrStoragePath
		}
		for _, part := range strings.Split(p, "/") {
			if strings.EqualFold(part, ".cloudshuttle-trash") {
				return domain.ErrStorageProtected
			}
		}
		if st.Kind == domain.KindSMB {
			share, _, ok := strings.Cut(p, "/")
			if !ok || root != "" && root != share {
				return domain.ErrStorageProtected
			}
			root = share
		}
	}
	if root != "" {
		source = strings.TrimPrefix(source, root+"/")
		target = strings.TrimPrefix(target, root+"/")
	}
	if source != "" && source == target {
		return domain.ErrStorageExists
	}
	ctx, done := s.beginStorageWork(ctx)
	defer done()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.takeSlotQueued(ctx, c.Queued); err != nil {
		return err
	}
	defer s.releaseSlot()
	loc, err := s.prepareStorageChange(ctx, st, root, c)
	if err != nil {
		return err
	}
	job := engine.StorageChange{
		Source: source, Target: target, Delete: c.Delete, Directory: c.Directory, Replace: c.Replace, ExpectedSource: c.ExpectedSource,
	}
	if c.LocalFile != "" {
		file, err := os.Open(c.LocalFile)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return domain.ErrStoragePath
		}
		job.Input, job.Size, job.Modified = file, info.Size(), info.ModTime()
	}
	return engine.ChangeStorage(ctx, loc, job)
}

// Take a consistent configuration snapshot while the transfer slot is
// held. Release maintenance before network I/O so task admission can continue.
func (s *Service) prepareStorageChange(ctx context.Context, st domain.Storage, root string, c StorageChange) (engine.Location, error) {
	unlock, err := s.lockMaintenance(ctx)
	if err != nil {
		return engine.Location{}, err
	}
	defer unlock()
	loc, err := s.browserRemote(ctx, st, root)
	if err != nil {
		return engine.Location{}, err
	}
	return loc, s.checkBrowserTasks(ctx, st.ID, c)
}

func (s *Service) checkBrowserTasks(ctx context.Context, id string, c StorageChange) error {
	tasks, err := s.store.Tasks(ctx)
	if err != nil {
		return err
	}
	storages, err := s.store.Storages(ctx)
	if err != nil {
		return err
	}
	touches := func(l domain.Location) bool {
		for _, p := range []string{c.Source, c.Target} {
			if p != "" && domain.Overlap(domain.Location{StorageID: id, Path: p}, l, storages) {
				return true
			}
		}
		return false
	}
	conflict := &StorageTaskConflict{}
	for _, t := range tasks {
		switch {
		case touches(t.Destination):
			conflict.Destinations = append(conflict.Destinations, t)
		case touches(t.Source):
			conflict.Sources = append(conflict.Sources, t)
		}
	}
	affected := conflict.Tasks()
	if len(affected) != 0 && !slices.EqualFunc(affected, c.ConfirmedTasks, func(a, b domain.Task) bool {
		return a.ID == b.ID && a.Name == b.Name && a.Source == b.Source &&
			a.Destination == b.Destination && a.Mode == b.Mode
	}) {
		return conflict
	}
	return nil
}

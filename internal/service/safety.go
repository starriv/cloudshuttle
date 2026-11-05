package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
)

// lockMaintenance serializes run admission and restore across both processes.
// Transfers are represented by running database rows before this lock is released.
func (s *Service) lockMaintenance(ctx context.Context) (func(), error) {
	select {
	case s.maintenance <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-s.maintenance }
	if path := s.lockPath("maintenance"); path != "" {
		for {
			if err := ctx.Err(); err != nil {
				release()
				return nil, err
			}
			l, ok, err := tryLock(path)
			if err != nil {
				release()
				return nil, fmt.Errorf("acquire maintenance lock: %w", err)
			}
			if ok {
				return func() { l.unlock(); release() }, nil
			}
			select {
			case <-ctx.Done():
				release()
				return nil, ctx.Err()
			case <-time.After(lockPoll):
			}
		}
	}
	return release, nil
}

// realPath resolves existing ancestors as well as a destination not created yet.
// Dangling links and inaccessible ancestors are rejected instead of guessed.
func realPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("local path is not absolute on this system")
	}
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, statErr := os.Lstat(path); statErr == nil {
		return "", err
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = realPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

func resolveTask(in domain.TaskInput) (domain.TaskInput, error) {
	for _, l := range []*domain.Location{&in.Source, &in.Destination} {
		if !l.Local() {
			continue
		}
		path, err := realPath(l.Path)
		if err != nil {
			return in, err
		}
		l.Path = path
	}
	return in, nil
}

func taskInput(t domain.Task) domain.TaskInput {
	return domain.TaskInput{Name: t.Name, Source: t.Source, Destination: t.Destination, Mode: t.Mode,
		Schedule: t.Schedule, TrashDays: t.TrashDays}
}

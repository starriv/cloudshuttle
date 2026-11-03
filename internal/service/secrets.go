package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/starriv/cloudshuttle/internal/domain"
)

// cachedSecrets keeps in memory, for as long as the app runs, each secret
// read from the credential store. Reading one may block while the system
// asks the user: on macOS, the keychain asks before an app whose signature
// changed reads what it stored. Asked once, the secret serves every later
// run without asking again, scheduled ones included.
type cachedSecrets struct {
	store Secrets
	mu    sync.Mutex
	known map[string]string
}

func newCachedSecrets(store Secrets) *cachedSecrets {
	return &cachedSecrets{store: store, known: map[string]string{}}
}

func (c *cachedSecrets) Get(key string) (string, error) {
	// The lock is held while the store is read: two goroutines asking for
	// one secret at once would ask the person to allow both reads, which
	// is what this cache is here to spare them.
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.known[key]; ok {
		return v, nil
	}
	v, err := c.store.Get(key)
	if err != nil {
		return "", err
	}
	c.known[key] = v
	return v, nil
}

func (c *cachedSecrets) Set(key, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.store.Set(key, value); err != nil {
		return err
	}
	c.known[key] = value
	return nil
}

func (c *cachedSecrets) Delete(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.store.Delete(key); err != nil {
		return err
	}
	delete(c.known, key)
	return nil
}

// readScheduledSecrets reads the secrets of the storages that scheduled
// tasks use, as the app starts: should the system ask the user to let the
// app read them, it asks now, as they open the app, and not as a task runs
// on its schedule with no one there to answer, which would leave the run
// waiting.
func (s *Service) readScheduledSecrets(ctx context.Context, tasks []domain.Task) {
	used := map[string]bool{}
	for _, t := range tasks {
		if !t.Schedule.On() {
			continue
		}
		for _, l := range []domain.Location{t.Source, t.Destination} {
			if !l.Local() {
				used[l.StorageID] = true
			}
		}
	}
	for id := range used {
		st, err := s.store.Storage(ctx, id)
		if err != nil {
			continue
		}
		if _, err := s.secret(st); err != nil && !errors.Is(err, domain.ErrMissingSecret) {
			slog.Warn("read secret", "storage", st.Name, "err", err)
		}
	}
}

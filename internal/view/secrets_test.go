package view

import (
	"sync"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/locales"
)

type waitingSecrets struct {
	memSecrets
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (s *waitingSecrets) pause()                         { s.once.Do(func() { close(s.entered) }); <-s.gate }
func (s *waitingSecrets) Get(key string) (string, error) { s.pause(); return s.memSecrets.Get(key) }
func (s *waitingSecrets) Set(key, value string) error    { s.pause(); return s.memSecrets.Set(key, value) }
func (s *waitingSecrets) Delete(key string) error        { s.pause(); return s.memSecrets.Delete(key) }

func TestCredentialOperationsLeaveMainThreadResponsive(t *testing.T) {
	for _, operation := range []string{"load-settings", "save-storage", "delete-storage"} {
		t.Run(operation, func(t *testing.T) {
			secrets := &waitingSecrets{memSecrets: memSecrets{m: map[string]string{}}, gate: make(chan struct{}), entered: make(chan struct{})}
			var release sync.Once
			// Ensure a regression fails with a bounded delay instead of hanging the suite.
			timer := time.AfterFunc(2*time.Second, func() { release.Do(func() { close(secrets.gate) }) })
			defer timer.Stop()
			defer release.Do(func() { close(secrets.gate) })
			h := newHarnessWith(t, service.Options{Secrets: secrets})
			start := time.Now()
			switch operation {
			case "load-settings":
				h.click(locales.NavSettings())
			case "save-storage":
				h.app.openStorageForm(nil)
				h.app.storageForm.in.Name = "bucket"
				h.app.storageForm.in.Bucket = "b"
				h.app.storageForm.in.Endpoint = "http://127.0.0.1:1"
				h.app.storageForm.in.AccessKeyID = "key"
				h.app.storageForm.in.Secret = "secret"
				h.app.saveStorage()
			case "delete-storage":
				st := domain.Storage{ID: "s", Name: "NAS", Kind: domain.KindSMB, Host: "nas.local", Username: "guest"}
				if err := h.db.PutStorage(t.Context(), st); err != nil {
					t.Fatal(err)
				}
				h.app.confirmDeleteStorage(st)
				h.app.confirm.action()
			}
			if time.Since(start) > time.Second {
				t.Fatal("credential store blocked the main thread")
			}
			select {
			case <-secrets.entered:
			case <-time.After(time.Second):
				t.Fatal("credential operation not started")
			}
			// The renderer and updates must remain usable while the credential store waits.
			h.settle()
			release.Do(func() { close(secrets.gate) })
			if operation == "delete-storage" {
				h.waitFor("delete", func() bool { return len(h.app.toasts) > 0 })
			}
			if operation == "save-storage" {
				h.waitFor("save", func() bool { return !h.app.storageForm.saving })
			}
		})
	}
}

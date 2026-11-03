package view

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rclone/gofakes3"
	"github.com/rclone/gofakes3/s3mem"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/locales"
)

// fakeBucket serves a bucket from memory holding keys, and adds a storage
// of it.
func (h *harness) fakeBucket(keys ...string) domain.Storage {
	h.t.Helper()
	ctx := context.Background()
	backend := s3mem.New()
	if err := backend.CreateBucket(ctx, "b"); err != nil {
		h.t.Fatal(err)
	}
	for _, k := range keys {
		if _, err := backend.PutObject(ctx, "b", k, nil, strings.NewReader("x"), 1); err != nil {
			h.t.Fatal(err)
		}
	}
	server := httptest.NewServer(gofakes3.New(backend,
		gofakes3.WithV4Auth(map[string]string{"AKIDTEST": "secret-test"})).Server())
	h.t.Cleanup(server.Close)
	st, err := h.svc.CreateStorage(ctx, domain.StorageInput{
		Name: "Bucket", Kind: domain.KindS3, Provider: domain.ProviderMinio,
		Endpoint: server.URL, Region: "us-east-1", Bucket: "b", AccessKeyID: "AKIDTEST", Secret: "secret-test",
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.settle()
	return st
}

func TestBrowseAStorageForAFolder(t *testing.T) {
	h := newHarness(t)
	h.fakeBucket("photos/2024/a.jpg", "photos/2025/b.jpg", "docs/c.txt")
	h.click(locales.NavTasks())
	h.click(locales.TasksAdd())              // its destination is the storage
	h.app.taskForm.dest.path = "photos/2026" // not there yet: S3 shows it empty
	h.click(locales.TaskBrowse())
	b := h.app.taskForm.browser
	h.waitFor("the folders", func() bool { return !b.loading })
	if strings.Join(b.path, "/") != "photos/2026" || len(b.folders) != 0 || b.failed != "" {
		t.Fatalf("browser at %q shows %q; failed %q", b.path, b.folders, b.failed)
	}
	h.expect(locales.BrowseEmpty())
	h.click("photos") // back up through the path
	h.waitFor("photos", func() bool { return !b.loading })
	if strings.Join(b.path, "/") != "photos" || strings.Join(b.folders, ",") != "2024,2025" {
		t.Fatalf("browser at %q shows %q; failed %q", b.path, b.folders, b.failed)
	}

	// Up to the top through the path, then into docs.
	h.click("b")
	h.waitFor("the top", func() bool { return !b.loading })
	if strings.Join(b.folders, ",") != "docs,photos" {
		t.Fatalf("top shows %q", b.folders)
	}
	h.click("photos")
	h.click(locales.BrowseChoose("photos"))
	if h.app.taskForm.browser != nil || h.app.taskForm.dest.path != "photos" {
		t.Fatalf("chose %q; browser open %v", h.app.taskForm.dest.path, h.app.taskForm.browser != nil)
	}
}

// TestOpenAShareByName types the name of a share the server does not list,
// as a NAS may not for a while after renaming one.
func TestOpenAShareByName(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.CreateStorage(context.Background(), domain.StorageInput{
		Name: "NAS", Kind: domain.KindSMB, Host: "127.0.0.1", Port: "1", Username: "guest", // nothing listens there
	}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.click(locales.NavTasks())
	h.click(locales.TasksAdd()) // its destination is the server
	h.click(locales.TaskBrowse())
	b := h.app.taskForm.browser
	h.waitFor("the shares", func() bool { return !b.loading })
	h.expect(locales.BrowseShareHint())

	h.fill(locales.BrowseShareName(), "SMB")
	h.click(locales.BrowseOpen())
	if strings.Join(b.path, "/") != "SMB" {
		t.Fatalf("browser at %q", b.path)
	}
}

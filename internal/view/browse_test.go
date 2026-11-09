package view

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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
		if _, err := backend.PutObject(ctx, "b", k, map[string]string{"Last-Modified": time.Now().UTC().Format(http.TimeFormat)}, strings.NewReader("x"), 1); err != nil {
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

func TestBrowseStorageFiles(t *testing.T) {
	h := newHarness(t)
	h.fakeBucket("photos/a.jpg", "report.txt")
	h.click(locales.NavStorages())
	h.click(locales.FilesBrowse())
	b := h.app.storageFiles
	if b == nil {
		t.Fatal("browser did not open")
	}
	h.waitFor("storage entries", func() bool { return !b.loading })
	h.expect("report.txt")
	h.fill(locales.FilesFilter(), "photos")
	h.click("photos")
	h.click(locales.FilesOpenFolder())
	h.waitFor("nested entries", func() bool { return !b.loading })
	h.expect("a.jpg")
	h.click("a.jpg")
	h.tt.Key(0, ui.KeyF2)
	h.fill(locales.FilesNewName(), "b.jpg")
	h.tt.Key(0, ui.KeyEnter)
	h.waitFor("renamed entry", func() bool {
		return !b.busy && !b.loading
	})
	if len(b.entries) != 1 || b.entries[0].Name != "b.jpg" {
		t.Fatalf("after rename: %+v, operation %q, load %q", b.entries, b.operationError, b.loadError)
	}
	h.expect("b.jpg")
	h.click("b.jpg")
	h.click(locales.FilesActions())
	if err := h.tt.ChooseMenuItem(locales.FilesMove()); err != nil {
		t.Fatalf("move menu: %v", err)
	}
	h.click(locales.FilesParent())
	h.waitFor("bucket root", func() bool { return !b.loading })
	h.click(locales.FilesMoveHere())
	h.waitFor("move to root", func() bool { return !b.busy && !b.loading })
	h.expect("b.jpg")
	h.click(locales.NavOverview())
	if h.app.storageFiles != nil {
		t.Fatal("browser remained open after leaving storage")
	}
}

func TestStorageBrowserCanReturnAfterListFailure(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.CreateStorage(context.Background(), domain.StorageInput{
		Name: "NAS", Kind: domain.KindSMB, Host: "127.0.0.1", Port: "1", Username: "guest",
	}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.click(locales.NavStorages())
	h.click(locales.FilesBrowse())
	b := h.app.storageFiles
	h.waitFor("connection failure", func() bool { return !b.loading })
	h.expect(locales.ErrorUnreachable())
	h.click(locales.NavStorages())
	if h.app.storageFiles != nil {
		t.Fatal("clicking the selected Storages sidebar item did not return to the cards")
	}
	h.click(locales.FilesBrowse())
	h.click(locales.FilesBack())
	if h.app.storageFiles != nil {
		t.Fatal("back did not return to the cards during a list request")
	}
	h.click(locales.FilesBrowse())
	h.click(locales.FilesParent())
	if h.app.storageFiles != nil {
		t.Fatal("Up at the storage root did not return to the cards")
	}
}

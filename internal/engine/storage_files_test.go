package engine

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rclone/gofakes3"
	"github.com/rclone/gofakes3/s3mem"
	"github.com/starriv/cloudshuttle/internal/domain"
)

func TestStorageBrowserS3RoundTrip(t *testing.T) {
	ctx := context.Background()
	backend := s3mem.New()
	if err := backend.CreateBucket(ctx, "photos"); err != nil {
		t.Fatal(err)
	}
	inner := gofakes3.New(backend, gofakes3.WithV4Auth(map[string]string{"AKIDTEST": "secret-test"})).Server()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// gofakes3's HTTP router strips trailing slashes from object keys.
		// Keep marker keys intact while using the same backend for lists.
		name := strings.TrimPrefix(r.URL.Path, "/photos/")
		if name == "" || !strings.HasSuffix(name, "/") {
			inner.ServeHTTP(w, r)
			return
		}
		switch r.Method {
		case http.MethodPut:
			if _, err := backend.PutObject(r.Context(), "photos", name, nil, r.Body, 0); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
			}
		case http.MethodHead:
			if _, err := backend.HeadObject(r.Context(), "photos", name); err != nil {
				w.WriteHeader(http.StatusNotFound)
			}
		case http.MethodDelete:
			if _, err := backend.DeleteObject(r.Context(), "photos", name); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
			}
		default:
			inner.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	loc := Location{S3: &S3{Provider: "Minio", Bucket: "photos", Endpoint: server.URL,
		Region: "us-east-1", AccessKeyID: "AKIDTEST", SecretAccessKey: "secret-test"}}
	change := func(c StorageChange) error { return ChangeStorage(ctx, loc, c) }
	mustChange := func(c StorageChange) {
		t.Helper()
		if err := change(c); err != nil {
			t.Fatal(err)
		}
	}
	mustChange(StorageChange{Target: "photos", Directory: true})
	if err := change(StorageChange{Target: "photos", Directory: true}); !errors.Is(err, domain.ErrStorageExists) {
		t.Fatalf("create an existing empty folder: %v, want ErrStorageExists", err)
	}
	entries, limited, err := StorageEntries(ctx, loc)
	if err != nil || limited || len(entries) != 1 || entries[0].Name != "photos" || !entries[0].Directory {
		t.Fatalf("empty directory not listed: %+v, %t, %v", entries, limited, err)
	}
	upload := func(name string, replace *domain.StorageReplacement) error {
		return change(StorageChange{Target: name, Input: strings.NewReader("test"), Size: 4, Replace: replace})
	}
	if err := upload("photos/a.txt", nil); err != nil {
		t.Fatal(err)
	}
	var existing *domain.StorageReplacement
	if err := upload("photos/a.txt", nil); !errors.As(err, &existing) {
		t.Fatalf("duplicate upload: %v", err)
	}
	if err := upload("photos/a.txt", existing); err != nil {
		t.Fatal(err)
	}
	if err := change(StorageChange{Source: "photos", Delete: true, Directory: true}); !errors.Is(err, domain.ErrStorageNotEmpty) {
		t.Fatalf("delete nonempty directory: %v", err)
	}
	mustChange(StorageChange{Source: "photos/a.txt", Target: "b.txt"})
	var output bytes.Buffer
	if err := DownloadStorageFile(ctx, loc, "b.txt", &output); err != nil || output.String() != "test" {
		t.Fatalf("download: %q, %v", output.String(), err)
	}
	mustChange(StorageChange{Source: "b.txt", Delete: true})
	mustChange(StorageChange{Source: "photos", Delete: true, Directory: true})
	entries, _, err = StorageEntries(ctx, loc)
	if err != nil || len(entries) != 0 {
		t.Fatalf("after removal: %+v, %v", entries, err)
	}
}

func TestStorageMoveKeepsSourceWhenDeleteDenied(t *testing.T) {
	ctx := context.Background()
	backend := s3mem.New()
	if err := backend.CreateBucket(ctx, "photos"); err != nil {
		t.Fatal(err)
	}
	inner := gofakes3.New(backend, gofakes3.WithV4Auth(map[string]string{"AKIDTEST": "secret-test"})).Server()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer server.Close()
	loc := Location{S3: &S3{Provider: "Minio", Bucket: "photos", Endpoint: server.URL,
		Region: "us-east-1", AccessKeyID: "AKIDTEST", SecretAccessKey: "secret-test"}}
	if err := ChangeStorage(ctx, loc, StorageChange{Target: "source", Input: strings.NewReader("x"), Size: 1}); err != nil {
		t.Fatal(err)
	}
	if err := ChangeStorage(ctx, loc, StorageChange{Source: "source", Target: "dest"}); !errors.Is(err, domain.ErrStoragePartialMove) {
		t.Fatalf("move: %v", err)
	}
	for _, name := range []string{"source", "dest"} {
		var got bytes.Buffer
		if err := DownloadStorageFile(ctx, loc, name, &got); err != nil || got.String() != "x" {
			t.Fatalf("%s: %q, %v", name, got.String(), err)
		}
	}
}

func TestStorageBrowserWithPrefixReadOnlyPermission(t *testing.T) {
	ctx := context.Background()
	backend := s3mem.New()
	if err := backend.CreateBucket(ctx, "photos"); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.PutObject(ctx, "photos", "shared/a.txt", map[string]string{"Last-Modified": time.Now().UTC().Format(http.TimeFormat)}, strings.NewReader("x"), 1); err != nil {
		t.Fatal(err)
	}
	inner := gofakes3.New(backend, gofakes3.WithV4Auth(map[string]string{"AKIDTEST": "secret-test"})).Server()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listShared := r.Method == http.MethodGet && r.URL.Query().Get("prefix") == "shared/"
		readShared := (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			strings.HasPrefix(r.URL.Path, "/photos/shared/")
		if !listShared && !readShared {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer server.Close()
	remote := &S3{Provider: "Minio", Bucket: "photos", Endpoint: server.URL,
		Region: "us-east-1", AccessKeyID: "AKIDTEST", SecretAccessKey: "secret-test"}
	if _, _, err := StorageEntries(ctx, Location{S3: remote}); !errors.Is(err, domain.ErrStorageDenied) {
		t.Fatalf("bucket root: %v", err)
	}
	shared := Location{S3: remote, Path: "shared"}
	entries, _, err := StorageEntries(ctx, shared)
	if err != nil || len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("prefix list: %+v, %v", entries, err)
	}
	var out bytes.Buffer
	if err := DownloadStorageFile(ctx, shared, "a.txt", &out); err != nil || out.String() != "x" {
		t.Fatalf("prefix download: %q, %v", out.String(), err)
	}
	if err := ChangeStorage(ctx, Location{S3: remote}, StorageChange{Source: "shared/a.txt", Delete: true}); !errors.Is(err, domain.ErrStorageDenied) {
		t.Fatalf("read-only delete: %v", err)
	}
}

func TestStorageMoveDoesNotOverwriteConcurrentS3Writer(t *testing.T) {
	ctx := context.Background()
	backend := s3mem.New()
	if err := backend.CreateBucket(ctx, "photos"); err != nil {
		t.Fatal(err)
	}
	inner := gofakes3.New(backend, gofakes3.WithV4Auth(map[string]string{"AKIDTEST": "secret-test"})).Server()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/photos/target" {
			// Another client publishes the target after our existence check.
			if _, err := backend.PutObject(r.Context(), "photos", "target", nil, strings.NewReader("other"), 5); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if r.Header.Get("If-None-Match") == "*" {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
		}
		inner.ServeHTTP(w, r)
	}))
	defer server.Close()
	loc := Location{S3: &S3{Provider: "Minio", Bucket: "photos", Endpoint: server.URL,
		Region: "us-east-1", AccessKeyID: "AKIDTEST", SecretAccessKey: "secret-test"}}
	if err := ChangeStorage(ctx, loc, StorageChange{Target: "source", Input: strings.NewReader("original"), Size: 8}); err != nil {
		t.Fatal(err)
	}
	if err := ChangeStorage(ctx, loc, StorageChange{Source: "source", Target: "target"}); !errors.Is(err, domain.ErrStorageExists) {
		t.Fatalf("concurrent move: %v", err)
	}
	for name, want := range map[string]string{"source": "original", "target": "other"} {
		var output bytes.Buffer
		if err := DownloadStorageFile(ctx, loc, name, &output); err != nil || output.String() != want {
			t.Fatalf("%s: %q, %v", name, output.String(), err)
		}
	}
}

func TestStorageDeleteRejectsChangedSourceAndSendsETag(t *testing.T) {
	ctx := context.Background()
	backend := s3mem.New()
	if err := backend.CreateBucket(ctx, "photos"); err != nil {
		t.Fatal(err)
	}
	inner := gofakes3.New(backend, gofakes3.WithV4Auth(map[string]string{"AKIDTEST": "secret-test"})).Server()
	var deleteRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/photos/file" {
			deleteRequests.Add(1)
			if r.Header.Get("If-Match") == "" {
				t.Error("DELETE had no If-Match")
			}
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer server.Close()
	loc := Location{S3: &S3{Provider: "Minio", Bucket: "photos", Endpoint: server.URL,
		Region: "us-east-1", AccessKeyID: "AKIDTEST", SecretAccessKey: "secret-test"}}
	put := func(content string, replace *domain.StorageReplacement) error {
		return ChangeStorage(ctx, loc, StorageChange{
			Target: "file", Input: strings.NewReader(content), Size: int64(len(content)), Replace: replace,
		})
	}
	if err := put("first", nil); err != nil {
		t.Fatal(err)
	}
	entries, _, err := StorageEntries(ctx, loc)
	if err != nil || len(entries) != 1 {
		t.Fatalf("list before deletion: %+v, %v", entries, err)
	}
	prepared, err := PrepareStorageDelete(ctx, loc, entries[0])
	if err != nil || prepared.ETag == "" {
		t.Fatalf("prepare confirmation: %+v, %v", prepared, err)
	}
	// Replace the object without changing the metadata used by the browser
	// listing. The ETag captured for confirmation must still reject it.
	metadata := map[string]string{"Last-Modified": prepared.Modified.UTC().Format(http.TimeFormat)}
	if _, err := backend.PutObject(ctx, "photos", "file", metadata, strings.NewReader("other"), 5); err != nil {
		t.Fatal(err)
	}
	change := StorageChange{Source: "file", Delete: true, ExpectedSource: &prepared}
	if err := ChangeStorage(ctx, loc, change); !errors.Is(err, domain.ErrStorageEntryChanged) {
		t.Fatalf("delete of stale selection: %v", err)
	}

	if got := deleteRequests.Load(); got != 0 {
		t.Fatalf("stale confirmation issued %d delete requests", got)
	}
	entries, _, err = StorageEntries(ctx, loc)
	if err != nil || len(entries) != 1 {
		t.Fatalf("list after replacement: %+v, %v", entries, err)
	}
	prepared, err = PrepareStorageDelete(ctx, loc, entries[0])
	if err != nil {
		t.Fatal(err)
	}
	change.ExpectedSource = &prepared
	if err := ChangeStorage(ctx, loc, change); !errors.Is(err, domain.ErrStorageEntryChanged) {
		t.Fatalf("failed delete precondition: %v", err)
	}
	var output bytes.Buffer
	if err := DownloadStorageFile(ctx, loc, "file", &output); err != nil || output.String() != "other" {
		t.Fatalf("replacement: %q, %v", output.String(), err)
	}
}

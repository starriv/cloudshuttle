package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rclone/gofakes3"
	"github.com/rclone/gofakes3/s3mem"
)

// fakeS3 serves an S3 API from memory, with one bucket and one key pair.
func fakeS3(t *testing.T) *S3 {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket(context.Background(), "photos"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(gofakes3.New(backend,
		gofakes3.WithV4Auth(map[string]string{"AKIDTEST": "secret-test"})).Server())
	t.Cleanup(server.Close)
	return &S3{
		Provider: "Minio", Endpoint: server.URL, Region: "us-east-1", Bucket: "photos",
		AccessKeyID: "AKIDTEST", SecretAccessKey: "secret-test",
	}
}

func TestRoundTripThroughS3(t *testing.T) {
	ctx := context.Background()
	remote := fakeS3(t)
	if err := Check(ctx, Location{S3: remote}); err != nil {
		t.Fatalf("check: %v", err)
	}

	src, back := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a.txt"), "alpha")
	write(t, filepath.Join(src, "nested", "b.txt"), "bravo")

	up := Job{ID: "up", Source: Location{Path: src}, Destination: Location{S3: remote, Path: "backup"}}
	if p, err := Run(ctx, up, nil); err != nil || p.Transfers != 2 {
		t.Fatalf("upload: %+v, %v", p, err)
	}
	down := Job{ID: "down", Source: Location{S3: remote, Path: "backup"}, Destination: Location{Path: back}}
	if _, err := Run(ctx, down, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(back, "nested", "b.txt"))
	if err != nil || string(got) != "bravo" {
		t.Fatalf("downloaded %q, %v", got, err)
	}
}

func TestCheckRejectsWrongSecret(t *testing.T) {
	remote := fakeS3(t)
	remote.SecretAccessKey = "wrong"
	if err := Check(context.Background(), Location{S3: remote}); err == nil {
		t.Fatal("check passed with a wrong secret")
	}
}

func TestFoldersInS3(t *testing.T) {
	ctx := context.Background()
	remote := fakeS3(t)
	src := t.TempDir()
	write(t, filepath.Join(src, "2024", "a.jpg"), "a")
	write(t, filepath.Join(src, "2025", "b.jpg"), "b")
	if _, err := Run(ctx, Job{ID: "up", Source: Location{Path: src}, Destination: Location{S3: remote, Path: "photos"}}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := Folders(ctx, Location{S3: remote, Path: "photos"})
	if err != nil || len(got) != 2 || got[0] != "2024" {
		t.Errorf("folders = %q, %v", got, err)
	}
}

func TestS3SyncStopsOnAnEmptySource(t *testing.T) {
	ctx := context.Background()
	remote := fakeS3(t)
	src := t.TempDir()
	for i := range 3 {
		write(t, filepath.Join(src, fmt.Sprintf("a%d.txt", i)), "precious")
	}
	up := Job{ID: "seed", Source: Location{Path: src}, Destination: Location{S3: remote, Path: "photos"}}
	if _, err := Run(ctx, up, nil); err != nil {
		t.Fatal(err)
	}

	// The source is empty: the bucket's prefix must stay as it is.
	empty := t.TempDir()
	syncDown := Job{ID: "empty-s3", Source: Location{Path: empty}, Destination: Location{S3: remote, Path: "photos"}, Sync: true}
	if _, err := Run(ctx, syncDown, nil); !errors.Is(err, ErrEmptySource) {
		t.Fatalf("run = %v, want ErrEmptySource", err)
	}

	back := t.TempDir()
	down := Job{ID: "check", Source: Location{S3: remote, Path: "photos"}, Destination: Location{Path: back}}
	if _, err := Run(ctx, down, nil); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := os.Stat(filepath.Join(back, fmt.Sprintf("a%d.txt", i))); err != nil {
			t.Fatalf("file %d lost: %v", i, err)
		}
	}
}

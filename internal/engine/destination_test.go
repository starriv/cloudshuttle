package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInitializeDestinationDoesNotReuseLocalNames(t *testing.T) {
	ctx := context.Background()
	loc := Location{Path: filepath.Join(t.TempDir(), "devops")}
	if err := InitializeDestination(ctx, loc, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(loc.Path); !os.IsNotExist(err) {
		t.Fatalf("preview created target: %v", err)
	}
	if err := InitializeDestination(ctx, loc, false); err != nil {
		t.Fatal(err)
	}
	if err := InitializeDestination(ctx, loc, false); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("reused directory: %v", err)
	}
	if err := os.Remove(loc.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), loc.Path); err != nil {
		t.Fatal(err)
	}
	if err := InitializeDestination(ctx, loc, false); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("reused symlink: %v", err)
	}
}

func TestInitializeS3DestinationChecksPrefixAndFiles(t *testing.T) {
	ctx := context.Background()
	remote := fakeS3(t)
	loc := Location{S3: remote, Path: "backup/devops"}
	if err := InitializeDestination(ctx, loc, true); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	write(t, filepath.Join(src, "devops", "a.txt"), "keep")
	write(t, filepath.Join(src, "devops01"), "file")
	if _, err := Run(ctx, Job{ID: "destination-check", Source: Location{Path: src}, Destination: Location{S3: remote, Path: "backup"}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"devops", "devops01"} {
		loc.Path = "backup/" + name
		for _, dryRun := range []bool{false, true} {
			if err := InitializeDestination(ctx, loc, dryRun); !errors.Is(err, ErrDestinationExists) {
				t.Fatalf("reused %s (preview %v): %v", name, dryRun, err)
			}
		}
	}
	loc.Path = "backup/devops02"
	if err := InitializeDestination(ctx, loc, false); err != nil {
		t.Fatalf("new prefix: %v", err)
	}
}

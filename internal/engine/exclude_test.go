package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestARunLeavesOutWhatItExcludes(t *testing.T) {
	ctx := context.Background()
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "photo.jpg"), "photo")
	write(t, filepath.Join(src, ".DS_Store"), "x")
	write(t, filepath.Join(src, "deep", ".DS_Store"), "x")
	write(t, filepath.Join(src, "app", "node_modules", "lib", "index.js"), "x")
	write(t, filepath.Join(src, "app", "main.go"), "x")
	write(t, filepath.Join(src, "cache", "a.bin"), "x")
	write(t, filepath.Join(src, "nested", "cache", "kept.bin"), "x")
	write(t, filepath.Join(src, "big.iso"), strings.Repeat("x", 2048))
	// The destination's own excluded files: a sync leaves them alone.
	write(t, filepath.Join(dst, "old", ".DS_Store"), "theirs")
	write(t, filepath.Join(dst, "huge.iso"), strings.Repeat("x", 4096))

	job := Job{ID: "exclude", Source: Location{Path: src}, Destination: Location{Path: dst}, Sync: true, Force: true,
		Exclude: []string{".DS_Store", "node_modules/", "/cache/"}, MaxSize: 1024}
	p, err := Run(ctx, job, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"photo.jpg", "app/main.go", "nested/cache/kept.bin"} {
		if !exists(filepath.Join(dst, filepath.FromSlash(kept))) {
			t.Errorf("%s was not copied", kept)
		}
	}
	for _, left := range []string{".DS_Store", "deep/.DS_Store", "app/node_modules", "cache", "big.iso"} {
		if exists(filepath.Join(dst, filepath.FromSlash(left))) {
			t.Errorf("%s was copied", left)
		}
	}
	if !exists(filepath.Join(dst, "old", ".DS_Store")) || !exists(filepath.Join(dst, "huge.iso")) {
		t.Error("the sync deleted an excluded file of the destination")
	}
	if p.Deletes != 0 {
		t.Errorf("deletes = %d, want none", p.Deletes)
	}
}

func TestValidExclude(t *testing.T) {
	for pattern, want := range map[string]bool{
		"*.tmp": true, "node_modules/": true, "/Projects/build/": true, "photo-{2024,2025}/**": true,
		"[abc": false, "{a,b": false, "a***": false,
	} {
		if got := ValidExclude(pattern); got != want {
			t.Errorf("ValidExclude(%q) = %v, want %v", pattern, got, want)
		}
	}
}

// The delete preview counts with the run's exclusions too: a sync leaves the
// excluded files of the destination alone, so they are not among the files
// it would delete.
func TestTheDeletePreviewHonoursTheExclusions(t *testing.T) {
	ctx := context.Background()
	src, dst := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "keep.txt"), "keep")
	write(t, filepath.Join(dst, "node_modules", "lib", "index.js"), "x")
	write(t, filepath.Join(dst, "target", "debug", "app"), "x")
	write(t, filepath.Join(dst, "old.txt"), "old")

	job := Job{ID: "preview-exclude", Source: Location{Path: src}, Destination: Location{Path: dst},
		Sync: true, Exclude: []string{"node_modules/", "target/"}}
	p, err := Run(ctx, job, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Only old.txt is a delete; the excluded folders are not even counted.
	if p.Deletes != 1 {
		t.Fatalf("deletes = %d, want only old.txt", p.Deletes)
	}
	if !exists(filepath.Join(dst, "node_modules", "lib", "index.js")) ||
		!exists(filepath.Join(dst, "target", "debug", "app")) {
		t.Error("the sync deleted an excluded folder of the destination")
	}
	if exists(filepath.Join(dst, "old.txt")) {
		t.Error("the sync did not delete what the source lacks")
	}
}

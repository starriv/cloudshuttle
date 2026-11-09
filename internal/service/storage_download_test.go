package service

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPublishDownload(t *testing.T) {
	for _, tc := range []struct {
		name string
		link func(string, string) error
	}{
		{"hard link", os.Link},
		// exFAT, FAT and network volumes refuse hard links.
		{"no hard links", func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.ENOTSUP} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func(old func(string, string) error) { linkFile = old }(linkFile)
			linkFile = tc.link
			dir := t.TempDir()
			tmp := filepath.Join(dir, ".tmp")
			if err := os.WriteFile(tmp, []byte("photo"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(tmp, downloadMode); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, "a.jpg")
			if err := publishDownload(tmp, target); err != nil {
				t.Fatalf("publish: %v", err)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != "photo" {
				t.Fatalf("published %q, %v", got, err)
			}
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm&0o044 == 0 {
				t.Errorf("mode = %v, want readable by others", perm)
			}

			// A file that appeared meanwhile is kept.
			if err := os.WriteFile(target, []byte("theirs"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := publishDownload(tmp, target); !errors.Is(err, os.ErrExist) {
				t.Fatalf("over an existing file: %v, want ErrExist", err)
			}
			if got, _ := os.ReadFile(target); string(got) != "theirs" {
				t.Errorf("existing file became %q", got)
			}
		})
	}
}

package engine

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A case-only rename finds its own source when it looks for the target; it
// must still rename instead of reporting that the name is taken.
func TestStorageRenameChangesOnlyCaseOnSMB(t *testing.T) {
	ctx := context.Background()
	server, share := smbServer(t)
	folder := "cloudshuttle-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	loc := Location{SMB: server, Path: share}
	change := func(c StorageChange) error { return ChangeStorage(ctx, loc, c) }
	if err := change(StorageChange{Target: folder, Directory: true}); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() {
		for _, name := range []string{"a.txt", "A.txt"} {
			_ = change(StorageChange{Source: folder + "/" + name, Delete: true})
		}
		_ = change(StorageChange{Source: folder, Delete: true, Directory: true})
	})
	if err := change(StorageChange{Target: folder + "/a.txt", Input: strings.NewReader("test"), Size: 4}); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if err := change(StorageChange{Source: folder + "/a.txt", Target: folder + "/A.txt"}); err != nil {
		t.Fatalf("case-only rename: %v", err)
	}
	entries, _, err := StorageEntries(ctx, Location{SMB: server, Path: share + "/" + folder})
	if err != nil || len(entries) != 1 || entries[0].Name != "A.txt" {
		t.Fatalf("after rename: %+v, %v", entries, err)
	}
}

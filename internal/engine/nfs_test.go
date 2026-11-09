package engine

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine/nfs3"
	"github.com/starriv/cloudshuttle/internal/engine/nfs3/nfstest"
)

// nfsExport serves a temporary folder over NFS and returns it with how the
// engine reaches it.
func nfsExport(t *testing.T) (string, *NFS) {
	t.Helper()
	dir := t.TempDir()
	srv := nfstest.Start(t, dir)
	return dir, &NFS{Host: srv.Host, Port: srv.Port, Export: "/", UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
}

func TestACopyGoesToAnNFSExportAndBack(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	write(t, filepath.Join(src, "photo.jpg"), "photo")
	write(t, filepath.Join(src, "albums", "2024", "trip.jpg"), strings.Repeat("x", 3<<20)) // several writes
	write(t, filepath.Join(src, ".DS_Store"), "x")
	when := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(src, "photo.jpg"), when, when); err != nil {
		t.Fatal(err)
	}
	disk, server := nfsExport(t)

	job := Job{ID: "nfs-copy", Source: Location{Path: src}, Destination: Location{NFS: server, Path: "backup"},
		Exclude: []string{".DS_Store"}}
	p, err := Run(ctx, job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Transfers != 2 {
		t.Errorf("transfers = %d, want 2", p.Transfers)
	}
	if read(t, filepath.Join(disk, "backup", "photo.jpg")) != "photo" ||
		len(read(t, filepath.Join(disk, "backup", "albums", "2024", "trip.jpg"))) != 3<<20 {
		t.Fatal("the export does not hold the files")
	}
	if exists(filepath.Join(disk, "backup", ".DS_Store")) {
		t.Error("an excluded file was copied")
	}
	if info, err := os.Stat(filepath.Join(disk, "backup", "photo.jpg")); err != nil || !info.ModTime().Equal(when) {
		t.Errorf("modification time = %v, %v; want %v", info.ModTime(), err, when)
	}
	if entries, _ := filepath.Glob(filepath.Join(disk, "backup", ".cloudshuttle-partial-*")); len(entries) > 0 {
		t.Errorf("partial files left: %v", entries)
	}

	// Nothing changed: a second run compares and copies nothing.
	p, err = Run(ctx, job, nil)
	if err != nil || p.Transfers != 0 {
		t.Fatalf("second run = %+v, %v; want nothing copied", p, err)
	}

	// And back to this computer.
	back := t.TempDir()
	p, err = Run(ctx, Job{ID: "nfs-back", Source: Location{NFS: server, Path: "backup"}, Destination: Location{Path: back}}, nil)
	if err != nil || p.Transfers != 2 {
		t.Fatalf("copy back = %+v, %v", p, err)
	}
	if read(t, filepath.Join(back, "albums", "2024", "trip.jpg")) != strings.Repeat("x", 3<<20) {
		t.Fatal("the file came back different")
	}
}

func TestNFSTransferBetweenServersWithTheSamePath(t *testing.T) {
	for _, tc := range []struct {
		name string
		sync bool
	}{{"copy", false}, {"sync", true}} {
		t.Run(tc.name, func(t *testing.T) {
			a, src := nfsExport(t)
			b, dst := nfsExport(t)
			write(t, filepath.Join(a, "data", "item"), "content")
			job := Job{ID: tc.name, Sync: tc.sync, Source: Location{NFS: src, Path: "data"}, Destination: Location{NFS: dst, Path: "data"}}
			if _, err := Run(context.Background(), job, nil); err != nil {
				t.Fatal(err)
			}
			if got := read(t, filepath.Join(b, "data", "item")); got != "content" {
				t.Fatalf("copied file = %q", got)
			}
		})
	}
}

func TestASyncToNFSDeletesIntoTheTrash(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	disk, server := nfsExport(t)
	write(t, filepath.Join(src, "keep.txt"), "same")
	write(t, filepath.Join(src, "dir", "changed.txt"), "version two")
	write(t, filepath.Join(disk, "dst", "keep.txt"), "same")
	write(t, filepath.Join(disk, "dst", "dir", "changed.txt"), "one")
	write(t, filepath.Join(disk, "dst", "gone.txt"), "deleted at the source")
	old := time.Now().Add(-time.Hour)
	for _, name := range []string{"keep.txt"} {
		for _, root := range []string{src, filepath.Join(disk, "dst")} {
			if err := os.Chtimes(filepath.Join(root, name), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	folder := TrashFolder(time.Now())
	job := Job{ID: "nfs-sync", Source: Location{Path: src}, Destination: Location{NFS: server, Path: "dst"},
		Sync: true, Force: true, Trash: folder, TrashDays: 30}
	p, err := Run(ctx, job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(disk, "dst", "gone.txt")) || read(t, filepath.Join(disk, "dst", "dir", "changed.txt")) != "version two" {
		t.Fatal("the destination is not the source")
	}
	trash := filepath.Join(disk, "dst", filepath.FromSlash(folder))
	if read(t, filepath.Join(trash, "gone.txt")) != "deleted at the source" ||
		read(t, filepath.Join(trash, "dir", "changed.txt")) != "one" {
		t.Fatal("the trash lacks the deleted or the replaced file")
	}
	if p.Trashed != 2 {
		t.Errorf("trashed = %d, want 2", p.Trashed)
	}
}

func TestTheFoldersOfAnNFSExport(t *testing.T) {
	ctx := context.Background()
	disk, server := nfsExport(t)
	write(t, filepath.Join(disk, "Photos", "a.jpg"), "x")
	write(t, filepath.Join(disk, "Music", "b.mp3"), "x")
	write(t, filepath.Join(disk, "notes.txt"), "x")
	if err := os.Symlink(filepath.Join(disk, "Photos"), filepath.Join(disk, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(disk, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := Check(ctx, Location{NFS: server}); err != nil {
		t.Fatalf("check = %v", err)
	}
	names, err := Folders(ctx, Location{NFS: server})
	slices.Sort(names)
	if err != nil || !slices.Equal(names, []string{"Music", "Photos"}) {
		t.Fatalf("folders = %q, %v", names, err)
	}
	if _, err := Folders(ctx, Location{NFS: server, Path: "missing"}); !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("folders of a missing folder = %v, want not found", err)
	}
	entries, err := DestinationEntries(ctx, Location{NFS: server})
	if err != nil || len(entries) != 5 || !entries["photos"] || entries["notes.txt"] || entries["link"] || entries["dangling"] {
		t.Fatalf("destination entries = %v, %v", entries, err)
	}
	for _, name := range []string{"link", "dangling"} {
		if err := InitializeDestination(ctx, Location{NFS: server, Path: name}, true); !errors.Is(err, ErrDestinationExists) {
			t.Errorf("dry-run initialization over %s = %v", name, err)
		}
	}
	if err := InitializeDestination(ctx, Location{NFS: server, Path: "Photos"}, false); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("initialize a taken folder = %v", err)
	}
	if err := InitializeDestination(ctx, Location{NFS: server, Path: "New"}, false); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(disk, "New")); err != nil || !info.IsDir() {
		t.Fatalf("the new folder: %v", err)
	}
	f, err := newDirFs(ctx, Location{NFS: server})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown(f)
	// The name appears after the occupancy check, before the final MKDIR.
	if err := f.(*nfsFs).claimDestination(ctx, "New"); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("claim after a concurrent create = %v", err)
	}
}

func TestNFSUploadRecoversWhenTheCreateReplyIsLost(t *testing.T) {
	disk, server := nfsExport(t)
	proxy, dropped := dropNFSReply(t, *server, 8) // CREATE
	in := StorageChange{Target: "new.txt", Input: strings.NewReader("mine"), Size: 4, Modified: time.Now()}
	if err := ChangeStorage(context.Background(), Location{NFS: &proxy}, in); err != nil {
		t.Fatal(err)
	}
	if !dropped.Load() || read(t, filepath.Join(disk, "new.txt")) != "mine" {
		t.Fatal("the upload did not recover from the lost create reply")
	}
}

// dropNFSReply forwards test RPCs, dropping the first successful reply to
// proc after the server has already done what it asked.
func dropNFSReply(t *testing.T, server NFS, proc uint32) (NFS, *atomic.Bool) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	addr := net.JoinHostPort(server.Host, strconv.Itoa(server.Port))
	var dropped atomic.Bool
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer client.Close()
				upstream, err := net.Dial("tcp", addr)
				if err != nil {
					return
				}
				defer upstream.Close()
				for {
					request, err := readTestRPCRecord(client)
					if err != nil {
						return
					}
					if _, err := upstream.Write(request); err != nil {
						return
					}
					reply, err := readTestRPCRecord(upstream)
					if err != nil {
						return
					}
					// Both RPC replies and their verifiers are AUTH_NONE in this fixture.
					if len(request) >= 28 && len(reply) >= 32 &&
						binary.BigEndian.Uint32(request[16:20]) == 100003 &&
						binary.BigEndian.Uint32(request[24:28]) == proc &&
						binary.BigEndian.Uint32(reply[24:28]) == 0 &&
						binary.BigEndian.Uint32(reply[28:32]) == 0 &&
						dropped.CompareAndSwap(false, true) {
						return
					}
					if _, err := client.Write(reply); err != nil {
						return
					}
				}
			}()
		}
	}()
	server.Port = listener.Addr().(*net.TCPAddr).Port
	return server, &dropped
}

func readTestRPCRecord(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size>>31 == 0 || size&0x7fffffff > 4<<20 {
		return nil, errors.New("unexpected test RPC record")
	}
	record := make([]byte, 4+int(size&0x7fffffff))
	copy(record, header[:])
	_, err := io.ReadFull(r, record[4:])
	return record, err
}

// The file browser never replaces a file it was not asked to.
func TestTheBrowserUploadsToNFSWithoutReplacing(t *testing.T) {
	ctx := context.Background()
	disk, server := nfsExport(t)
	write(t, filepath.Join(disk, "taken.txt"), "theirs")
	loc := Location{NFS: server}
	err := ChangeStorage(ctx, loc, StorageChange{Target: "taken.txt", Input: strings.NewReader("mine"), Size: 4, Modified: time.Now()})
	var replacement *domain.StorageReplacement
	if !errors.As(err, &replacement) {
		t.Fatalf("upload onto a taken name = %v, want it to ask", err)
	}
	if read(t, filepath.Join(disk, "taken.txt")) != "theirs" {
		t.Fatal("the file was replaced")
	}
	if err := ChangeStorage(ctx, loc, StorageChange{Target: "new.txt", Input: strings.NewReader("mine"), Size: 4, Modified: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(disk, "new.txt")) != "mine" {
		t.Fatal("the new file is not there")
	}
	// A move publishes a new name and removes the old one.
	if err := ChangeStorage(ctx, loc, StorageChange{Source: "new.txt", Target: "moved/new.txt"}); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(disk, "new.txt")) || read(t, filepath.Join(disk, "moved", "new.txt")) != "mine" {
		t.Fatal("the file did not move")
	}
	if err := ChangeStorage(ctx, loc, StorageChange{Source: "moved/new.txt", Target: "taken.txt"}); !errors.Is(err, domain.ErrStorageExists) {
		var r *domain.StorageReplacement
		if !errors.As(err, &r) {
			t.Fatalf("a move onto a taken name = %v, want it refused", err)
		}
	}
	if read(t, filepath.Join(disk, "taken.txt")) != "theirs" {
		t.Fatal("a move replaced a file")
	}
}

func TestAnNFSServerThatIsNotThere(t *testing.T) {
	ctx := context.Background()
	err := Check(ctx, Location{NFS: &NFS{Host: "127.0.0.1", Port: 1, Export: "/x"}})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("check = %v, want unreachable", err)
	}
}

// A write that was cut off leaves a hidden sibling behind; no run may take
// that for a file of the backup.
func TestALeftoverPartialIsNotCopied(t *testing.T) {
	disk, server := nfsExport(t)
	write(t, filepath.Join(disk, "backup", partialPrefix+"deadbeef"), "half a file")
	write(t, filepath.Join(disk, "backup", "real.txt"), "real")

	back := t.TempDir()
	p, err := Run(context.Background(), Job{ID: "partial", Source: Location{NFS: server, Path: "backup"},
		Destination: Location{Path: back}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Transfers != 1 {
		t.Errorf("transfers = %d, want only the real file", p.Transfers)
	}
	if exists(filepath.Join(back, partialPrefix+"deadbeef")) {
		t.Error("the leftover partial was copied into the backup")
	}
	if !exists(filepath.Join(back, "real.txt")) {
		t.Error("the real file was not copied")
	}
}

// The errors of the two RPC programs an export needs stay distinct: a port
// that serves no MOUNT is not a server without NFS version 3.
func TestAPortWithoutMountIsNotAServerWithoutNFSv3(t *testing.T) {
	// accept_stat 1 is PROC_UNAVAIL, which MOUNT answers on a port that
	// serves NFS alone; 2 is a version mismatch of the NFS program itself.
	noMount := &nfs3.RPCError{Prog: 100005, Vers: 3, Accept: 1}
	if err := classifyNFS(noMount); !errors.Is(err, ErrNoMount) {
		t.Errorf("a port without MOUNT = %v, want ErrNoMount", err)
	}
	noV3 := &nfs3.RPCError{Prog: 100003, Vers: 3, Accept: 2}
	if err := classifyNFS(noV3); !errors.Is(err, ErrNoNFSv3) {
		t.Errorf("a server without NFSv3 = %v, want ErrNoNFSv3", err)
	}
}

// A delete whose reply was lost is not a failure: the retry answers that
// the file is gone, which is what the delete came to do.
func TestALostRemoveReplyIsNotAFailure(t *testing.T) {
	disk, server := nfsExport(t)
	write(t, filepath.Join(disk, "gone.txt"), "x")
	proxy, dropped := dropNFSReply(t, *server, 12) // REMOVE
	ctx := context.Background()
	f, err := newDirFs(ctx, Location{NFS: &proxy})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown(f)
	obj, err := f.NewObject(ctx, "gone.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := obj.Remove(ctx); err != nil {
		t.Fatalf("remove with a lost reply = %v, want it counted as done", err)
	}
	if !dropped.Load() || exists(filepath.Join(disk, "gone.txt")) {
		t.Fatal("the file was not removed")
	}
}

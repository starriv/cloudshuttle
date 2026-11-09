// Package nfstest serves a folder over NFSv3 for tests, in process: the
// pure-Go server of github.com/willscott/go-nfs, which serves MOUNT and NFS
// on one port, as user-space servers do. Only tests import it.
package nfstest

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	nfs "github.com/willscott/go-nfs"
	"github.com/willscott/go-nfs/helpers"
)

// Server is a folder served over NFS on the loopback address.
type Server struct {
	Host string
	Port int
	Dir  string // the folder on disk; every export path mounts it
}

// Start serves dir until the test ends.
func Start(t testing.TB, dir string) Server {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := helpers.NewCachingHandler(helpers.NewNullAuthHandler(changeFS{osfs.New(dir)}), 4096)
	go func() { _ = nfs.Serve(l, handler) }()
	t.Cleanup(func() { l.Close() })
	_, port, _ := net.SplitHostPort(l.Addr().String())
	n, _ := strconv.Atoi(port)
	return Server{Host: "127.0.0.1", Port: n, Dir: dir}
}

// changeFS lets the server set modes and times, which osfs alone does not.
type changeFS struct{ billy.Filesystem }

var _ billy.Change = changeFS{}

func (f changeFS) path(name string) string { return filepath.Join(f.Root(), name) }

func (f changeFS) Chmod(name string, mode os.FileMode) error { return os.Chmod(f.path(name), mode) }
func (f changeFS) Lchown(name string, uid, gid int) error    { return os.Lchown(f.path(name), uid, gid) }
func (f changeFS) Chown(name string, uid, gid int) error     { return os.Chown(f.path(name), uid, gid) }
func (f changeFS) Chtimes(name string, atime, mtime time.Time) error {
	return os.Chtimes(f.path(name), atime, mtime)
}

package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want error
	}{
		{&smb2.ResponseError{Code: statusLogonFailure}, ErrAuthFailed},
		{&smb2.ResponseError{Code: statusAccessDenied}, ErrAuthFailed},
		{&smb2.ResponseError{Code: statusBadNetworkName}, ErrShareNotFound},
		{&net.OpError{Op: "dial", Err: errors.New("connection refused")}, ErrUnreachable},
		{&net.DNSError{Err: "no such host", Name: "nas.local"}, ErrUnreachable},
		{context.DeadlineExceeded, ErrUnreachable},
	}
	for _, tc := range cases {
		got := classify(fmt.Errorf("couldn't connect SMB: %w", tc.err))
		if !errors.Is(got, tc.want) {
			t.Errorf("%v: got %v, want %v", tc.err, got, tc.want)
		}
		if !errors.Is(got, tc.err) && !errors.As(got, new(*smb2.ResponseError)) {
			t.Errorf("%v: lost the cause in %v", tc.err, got)
		}
	}
	if err := classify(errors.New("disk full")); errors.Is(err, ErrUnreachable) || errors.Is(err, ErrAuthFailed) {
		t.Errorf("other errors stay as they are: %v", err)
	}
}

// TestCheckUnreachableSMB connects to a port nothing listens on.
func TestCheckUnreachableSMB(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = Check(ctx, Location{SMB: &SMB{Host: "127.0.0.1", Port: port, Username: "me"}, Path: "photos"})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("check = %v, want ErrUnreachable", err)
	}
}

// smbServer reads a real server from CLOUDSHUTTLE_TEST_SMB, such as
// smb://user:pass@nas.local/share, and the share to write in; the tests
// that need one skip without it. They write into a folder of their own in
// the share and delete it after.
func smbServer(t *testing.T) (*SMB, string) {
	t.Helper()
	raw := os.Getenv("CLOUDSHUTTLE_TEST_SMB")
	if raw == "" {
		t.Skip("CLOUDSHUTTLE_TEST_SMB not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := &SMB{Host: u.Hostname(), Username: u.User.Username()}
	s.Password, _ = u.User.Password()
	if p := u.Port(); p != "" {
		s.Port, _ = strconv.Atoi(p)
	}
	return s, strings.Trim(u.Path, "/")
}

func TestRoundTripThroughSMB(t *testing.T) {
	ctx := context.Background()
	server, share := smbServer(t)
	if err := Check(ctx, Location{SMB: server}); err != nil {
		t.Fatalf("check: %v", err)
	}
	shares, err := Folders(ctx, Location{SMB: server})
	if err != nil || !slices.ContainsFunc(shares, func(s string) bool { return strings.EqualFold(s, share) }) {
		t.Fatalf("shares = %q, %v; want %q among them", shares, err, share)
	}

	folder := share + "/cloudshuttle-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	src, back := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a.txt"), "alpha")
	write(t, filepath.Join(src, "nested", "b.txt"), "bravo")

	remote := Location{SMB: server, Path: folder}
	t.Cleanup(func() {
		empty := t.TempDir()
		_, _ = Run(ctx, Job{ID: "clean", Source: Location{Path: empty}, Destination: remote, Sync: true}, nil)
	})
	if p, err := Run(ctx, Job{ID: "up", Source: Location{Path: src}, Destination: remote}, nil); err != nil || p.Transfers != 2 {
		t.Fatalf("upload: %+v, %v", p, err)
	}
	if folders, err := Folders(ctx, remote); err != nil || len(folders) != 1 || folders[0] != "nested" {
		t.Errorf("folders = %q, %v", folders, err)
	}
	if _, err := Run(ctx, Job{ID: "down", Source: remote, Destination: Location{Path: back}}, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(back, "nested", "b.txt"))
	if err != nil || string(got) != "bravo" {
		t.Fatalf("downloaded %q, %v", got, err)
	}

	wrong := *server
	wrong.Password += "-wrong"
	if err := Check(ctx, Location{SMB: &wrong}); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("wrong password: %v, want ErrAuthFailed", err)
	}
	if err := Check(ctx, Location{SMB: server, Path: "cloudshuttle-no-such-share"}); !errors.Is(err, ErrShareNotFound) {
		t.Errorf("missing share: %v, want ErrShareNotFound", err)
	}
}

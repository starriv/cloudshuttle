package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/starriv/cloudshuttle/internal/domain"
)

// putNewSMB uses SMB's exclusive create. rclone's SMB Put truncates existing
// files and its Move asks the server to replace the target.
func putNewSMB(ctx context.Context, loc Location, name string, in io.Reader, modified time.Time) error {
	backend, err := fs.Find("smb")
	if err != nil {
		return err
	}
	enc := backend.Options.Get("encoding").Default.(encoder.MultiEncoder)
	name = strings.ReplaceAll(enc.FromStandardPath(name), "/", "\\")
	shareName := loc.Path
	port := loc.SMB.Port
	if port == 0 {
		port = 445
	}
	addr := net.JoinHostPort(strings.Trim(loc.SMB.Host, "[]"), strconv.Itoa(port))
	conn, err := fshttp.NewDialer(ctx).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	dialer := smb2.Dialer{Initiator: &smb2.NTLMInitiator{
		User: loc.SMB.Username, Password: loc.SMB.Password,
		Domain: backend.Options.Get("domain").Default.(string),
	}}
	session, err := dialer.DialConn(ctx, conn, addr)
	if err != nil {
		return err
	}
	share, err := session.WithContext(ctx).Mount(shareName)
	if err != nil {
		return err
	}
	file, err := share.WithContext(ctx).OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		var response *smb2.ResponseError
		const statusObjectNameCollision = 0xC0000035
		if errors.Is(err, os.ErrExist) ||
			errors.As(err, &response) && response.Code == statusObjectNameCollision {
			return domain.ErrStorageExists
		}
		return err
	}

	_, writeErr := io.Copy(file, in)
	if writeErr == nil && !modified.IsZero() {
		// The open handle denies deletion and replacement by name.
		_ = share.WithContext(ctx).Chtimes(name, modified, modified)
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		// There is no handle-based removal in the client. Removing by
		// name could delete a replacement from another client.
		return errors.Join(domain.ErrStorageIncompleteWrite, err)
	}
	return nil
}

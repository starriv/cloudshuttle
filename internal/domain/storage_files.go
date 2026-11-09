package domain

import (
	"errors"
	"strings"
	"time"
)

// StorageEntry describes a direct child of a remote directory.
type StorageEntry struct {
	Name      string
	Directory bool
	Size      int64
	Modified  time.Time
	ETag      string // populated when a supported backend prepares a delete
	Checksum  string // used when a conditional delete is unavailable
}

// StorageReplacement identifies the existing entry shown for an upload
// confirmation. An updated entry requires a new confirmation.
type StorageReplacement struct {
	Size     int64
	Modified time.Time
}

func (e *StorageReplacement) Error() string { return ErrStorageExists.Error() }
func (e *StorageReplacement) Unwrap() error { return ErrStorageExists }

var (
	ErrStoragePath            = errors.New("invalid storage path")
	ErrStorageDenied          = errors.New("storage operation denied")
	ErrStorageExists          = errors.New("storage entry already exists")
	ErrStorageMissing         = errors.New("storage entry not found")
	ErrStorageProtected       = errors.New("protected storage path")
	ErrStorageMoveUnsupported = errors.New("no safe conditional move for this provider")
	ErrStorageMoveTooLarge    = errors.New("object exceeds safe single-request move size")
	ErrStorageIncompleteWrite = errors.New("storage write may be incomplete")
	ErrStorageEntryChanged    = errors.New("storage entry changed")
	ErrStorageChanged         = errors.New("storage connection changed")
	ErrStoragePartialMove     = errors.New("storage move partially completed")
	ErrStorageNotEmpty        = errors.New("storage directory is not empty")
)

// ValidStoragePath accepts only paths the backend can use without cleaning
// away segments. Object keys may hold ':' and '\', as in a timestamped log;
// the names an SMB server refuses are checked for SMB alone.
func ValidStoragePath(path string) bool {
	if path == "" {
		return true
	}
	for _, name := range strings.Split(path, "/") {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "\x00\r\n") {
			return false
		}
	}
	return true
}

package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/object"
	"github.com/starriv/cloudshuttle/internal/domain"
)

const StorageListLimit = 5000

var errListLimit = errors.New("storage listing limit")

func StorageEntries(ctx context.Context, loc Location) ([]domain.StorageEntry, bool, error) {
	ctx = probing(ctx)
	fs.GetConfig(ctx).UseServerModTime = true
	f, err := makeConfiguredFs(ctx, loc, true, true, true)
	if err != nil {
		return nil, false, storageError(err)
	}
	defer shutdown(f)
	var entries []domain.StorageEntry
	collect := func(batch fs.DirEntries) error {
		for _, entry := range batch {
			name := entry.Remote()
			if strings.Contains(name, "/") || !domain.ValidStoragePath(name) || name == "" {
				continue
			}
			if len(entries) == StorageListLimit {
				return errListLimit
			}
			_, dir := entry.(fs.Directory)
			entries = append(entries, domain.StorageEntry{
				Name: name, Directory: dir, Size: entry.Size(), Modified: entry.ModTime(ctx),
			})
		}
		return ctx.Err()
	}
	if list := f.Features().ListP; list != nil {
		err = list(ctx, "", collect)
	} else {
		var batch fs.DirEntries
		batch, err = f.List(ctx, "")
		if err == nil {
			err = collect(batch)
		}
	}
	if errors.Is(err, errListLimit) {
		return entries, true, nil
	}
	return entries, false, storageError(err)
}

func storageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, domain.ErrStoragePartialMove) || errors.Is(err, domain.ErrStorageIncompleteWrite) {
		return err
	}
	err = classify(err)
	var status interface{ HTTPStatusCode() int }
	if errors.As(err, &status) && (status.HTTPStatusCode() == 401 || status.HTTPStatusCode() == 403) ||
		errors.Is(err, ErrAuthFailed) || errors.Is(err, fs.ErrorPermissionDenied) {
		return domain.ErrStorageDenied
	}
	if errors.Is(err, fs.ErrorObjectNotFound) || errors.Is(err, fs.ErrorDirNotFound) ||
		errors.Is(err, ErrShareNotFound) || errors.As(err, &status) && status.HTTPStatusCode() == 404 {
		return domain.ErrStorageMissing
	}
	if status != nil && (status.HTTPStatusCode() == 412 || status.HTTPStatusCode() == 409) {
		return domain.ErrStorageExists
	}
	if errors.Is(err, fs.ErrorDirectoryNotEmpty) {
		return domain.ErrStorageNotEmpty
	}
	return err
}

func DownloadStorageFile(ctx context.Context, loc Location, name string, out io.Writer) error {
	f, err := makeConfiguredFs(ctx, loc, true, true, false)
	if err != nil {
		return storageError(err)
	}
	defer shutdown(f)
	obj, err := f.NewObject(ctx, name)
	if err != nil {
		return storageError(err)
	}
	reader, err := obj.Open(ctx)
	if err != nil {
		return storageError(err)
	}
	defer reader.Close()
	_, err = io.Copy(out, reader)
	return storageError(err)
}

// PrepareStorageDelete rechecks the listing and pins the ETag before the
// user sees the confirmation, when the backend supports conditional deletion.
func PrepareStorageDelete(ctx context.Context, loc Location, entry domain.StorageEntry) (domain.StorageEntry, error) {
	if loc.S3 == nil || entry.Directory {
		return entry, nil
	}
	ctx = probing(ctx)
	fs.GetConfig(ctx).UseServerModTime = true
	f, err := makeConfiguredFs(ctx, loc, true, true, false)
	if err != nil {
		return entry, storageError(err)
	}
	defer shutdown(f)
	obj, err := f.NewObject(ctx, entry.Name)
	if err != nil {
		return entry, storageError(err)
	}
	if !matchesStorageEntry(loc, obj, entry, ctx) {
		return entry, domain.ErrStorageEntryChanged
	}
	if !conditionalStorageDelete(loc) {
		entry.Checksum, err = obj.Hash(ctx, hash.MD5)
		return entry, storageError(err)
	}
	guard, err := guardS3Object(ctx, loc, obj)
	if err != nil {
		return entry, storageError(err)
	}
	entry.ETag = guard.etag
	return entry, nil
}

func conditionalStorageDelete(loc Location) bool {
	return loc.S3 != nil && (loc.S3.Provider == string(domain.ProviderAWS) ||
		loc.S3.Provider == string(domain.ProviderMinio))
}

func matchesStorageEntry(loc Location, obj fs.Object, expected domain.StorageEntry, ctx context.Context) bool {
	modified, listed := obj.ModTime(ctx), expected.Modified
	if loc.S3 != nil {
		// ListObjects includes fractions of a second, while HeadObject's
		// HTTP date only has second precision.
		modified, listed = modified.Truncate(time.Second), listed.Truncate(time.Second)
	}
	if expected.Size != obj.Size() || !listed.Equal(modified) {
		return false
	}
	if expected.Checksum != "" {
		sum, err := obj.Hash(ctx, hash.MD5)
		return err == nil && sum == expected.Checksum
	}
	return true
}

type StorageChange struct {
	Source, Target    string
	Directory, Delete bool
	Replace           *domain.StorageReplacement
	ExpectedSource    *domain.StorageEntry
	Input             io.Reader
	Size              int64
	Modified          time.Time
}

func ChangeStorage(ctx context.Context, loc Location, change StorageChange) error {
	ctx, config := fs.AddConfig(ctx)
	config.UseServerModTime = true
	singleUpload := false
	if loc.S3 != nil && change.Source != "" && !change.Delete {
		switch domain.Provider(loc.S3.Provider) {
		case domain.ProviderAWS:
		case domain.ProviderCloudflare, domain.ProviderMinio:
			// These providers document the PutObject condition, but not
			// the same guarantee at multipart completion.
			singleUpload = true
		default:
			return domain.ErrStorageMoveUnsupported
		}
	}
	f, err := makeFsWithOptions(ctx, loc, true, true, change.Directory, singleUpload)
	if err != nil {
		return storageError(err)
	}
	defer shutdown(f)
	return storageError(changeStorage(ctx, f, loc, change))
}

func changeStorage(ctx context.Context, f fs.Fs, loc Location, c StorageChange) error {
	if c.Delete && c.Source == "" {
		return domain.ErrStorageProtected
	}
	if !c.Delete && c.Target == "" {
		return domain.ErrStorageProtected
	}
	if c.Delete && c.Directory {
		entries, err := f.List(ctx, c.Source)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return domain.ErrStorageNotEmpty
		}
		return f.Rmdir(ctx, c.Source)
	}
	var source fs.Object
	var guard *s3ObjectGuard
	if c.Source != "" {
		var err error
		source, err = f.NewObject(ctx, c.Source)
		if err != nil {
			return err
		}
		if expected := c.ExpectedSource; expected != nil && !matchesStorageEntry(loc, source, *expected, ctx) {
			return domain.ErrStorageEntryChanged
		}
		if conditionalStorageDelete(loc) {
			guard, err = guardS3Object(ctx, loc, source)
			if err != nil {
				return err
			}
			if expected := c.ExpectedSource; expected != nil && expected.ETag != "" && expected.ETag != guard.etag {
				return domain.ErrStorageEntryChanged
			}
		}
		if c.Delete {
			if guard != nil {
				return guard.remove(ctx)
			}
			return source.Remove(ctx)
		}
		if loc.SMB != nil && c.Source != c.Target && strings.EqualFold(c.Source, c.Target) {
			return renameSMBCase(ctx, f, source, c.Target)
		}
	}
	existing, err := f.NewObject(ctx, c.Target)
	switch {
	case errors.Is(err, fs.ErrorIsDir):
		return domain.ErrStorageExists
	case err != nil && !errors.Is(err, fs.ErrorObjectNotFound):
		return err
	case existing != nil:
		if c.Input == nil {
			return domain.ErrStorageExists
		}
		current := &domain.StorageReplacement{Size: existing.Size(), Modified: existing.ModTime(ctx)}
		if c.Replace == nil || c.Replace.Size != current.Size || !c.Replace.Modified.Equal(current.Modified) {
			return current
		}
	case c.Replace != nil:
		return domain.ErrStorageMissing
	}
	if c.Directory {
		// A folder that lists, even empty, is already there: Mkdir would
		// succeed on it and the person would think it was new.
		_, err := f.List(ctx, c.Target)
		if err == nil {
			return domain.ErrStorageExists
		}
		if !errors.Is(err, fs.ErrorDirNotFound) {
			return err
		}
		return f.Mkdir(ctx, c.Target)
	}
	if source == nil {
		info := object.NewStaticObjectInfo(c.Target, c.Modified, c.Size, true, nil, f)
		if loc.S3 != nil {
			var options []fs.OpenOption
			if existing == nil {
				options = append(options, &fs.HTTPOption{Key: "If-None-Match", Value: "*"})
			}
			_, err := f.Put(ctx, c.Input, info, options...)
			return err
		}
		if existing == nil {
			return putNew(ctx, f, loc, c.Target, c.Input, c.Modified)
		}
		// SMB and NFS stream directly into the destination and removes partial
		// uploads on failure. Stage a sibling so a failed upload cannot
		// remove the file the user agreed to replace.
		parent, _ := path.Split(c.Target)
		temp := parent + uploadPrefix + domain.NewID()
		staged, err := f.Put(ctx, c.Input, object.NewStaticObjectInfo(temp, c.Modified, c.Size, true, nil, f))
		if err != nil {
			return err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			_ = staged.Remove(cleanup)
		}()
		latest, err := f.NewObject(ctx, c.Target)
		switch {
		case err != nil:
			return err
		case c.Replace == nil || latest.Size() != c.Replace.Size || !latest.ModTime(ctx).Equal(c.Replace.Modified):
			return &domain.StorageReplacement{Size: latest.Size(), Modified: latest.ModTime(ctx)}
		}
		_, err = f.Features().Move(ctx, staged, c.Target)
		return err
	}
	if loc.S3 != nil && loc.S3.Provider != string(domain.ProviderAWS) &&
		(source.Size() < 0 || source.Size() > 5_000_000_000) {
		// Keep the no-overwrite condition on PutObject itself.
		return domain.ErrStorageMoveTooLarge
	}

	// Publish the target without replacing a competing writer, then
	// verify the source again before removing it.
	snapshot := domain.StorageEntry{Size: source.Size(), Modified: source.ModTime(ctx)}
	if loc.S3 != nil && guard == nil {
		snapshot.Checksum, err = source.Hash(ctx, hash.MD5)
		if err != nil {
			return err
		}
	}
	var readOptions []fs.OpenOption
	if guard != nil {
		readOptions = append(readOptions, &fs.HTTPOption{Key: "If-Match", Value: guard.etag})
	}
	reader, err := source.Open(ctx, readOptions...)
	if err != nil {
		return err
	}
	if loc.SMB != nil || loc.NFS != nil {
		err = putNew(ctx, f, loc, c.Target, reader, snapshot.Modified)
	} else {
		copyCtx, config := fs.AddConfig(ctx)
		config.Metadata = true
		info := storageMoveInfo{Object: source, target: c.Target}
		_, err = f.Put(copyCtx, reader, info, &fs.HTTPOption{Key: "If-None-Match", Value: "*"})
	}
	closeErr := reader.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return fmt.Errorf("%w: %w", domain.ErrStoragePartialMove, closeErr)
	}
	if guard != nil {
		if err := guard.remove(ctx); err != nil {
			return errors.Join(domain.ErrStoragePartialMove, err)
		}
		return nil
	}
	current, err := f.NewObject(ctx, c.Source)
	if err != nil || !matchesStorageEntry(loc, current, snapshot, ctx) {
		return domain.ErrStoragePartialMove
	}
	if err := current.Remove(ctx); err != nil {
		return fmt.Errorf("%w: %w", domain.ErrStoragePartialMove, err)
	}
	return nil
}

// putNew writes a new file on an SMB share or NFS export without replacing
// one that is there: domain.ErrStorageExists when the name is taken.
func putNew(ctx context.Context, f fs.Fs, loc Location, name string, in io.Reader, modified time.Time) error {
	if loc.NFS == nil {
		return putNewSMB(ctx, loc, name, in, modified)
	}
	nf, ok := f.(*nfsFs)
	if !ok {
		return errors.New("nfs: unexpected backend")
	}
	_, err := nf.putNew(ctx, in, name, modified)
	if errors.Is(err, errNFSExists) {
		return domain.ErrStorageExists
	}
	return err
}

// renameSMBCase changes only the case of a name. The server folds case, so
// the target lookup and an exclusive create would both find the source
// itself; a native rename is safe once the parent lists exactly one entry
// that folds to the name, and that entry is the source.
func renameSMBCase(ctx context.Context, f fs.Fs, source fs.Object, target string) error {
	parent, _ := path.Split(source.Remote())
	targetParent, targetName := path.Split(target)
	if parent != targetParent {
		return domain.ErrStorageExists
	}
	entries, err := f.List(ctx, strings.TrimSuffix(parent, "/"))
	if err != nil {
		return err
	}
	found := false
	for _, entry := range entries {
		if !strings.EqualFold(path.Base(entry.Remote()), targetName) {
			continue
		}
		if found || entry.Remote() != source.Remote() {
			return domain.ErrStorageExists
		}
		found = true
	}
	if !found {
		return domain.ErrStorageMissing
	}
	_, err = f.Features().Move(ctx, source, target)
	return err
}

type storageMoveInfo struct {
	fs.Object
	target string
}

func (i storageMoveInfo) Remote() string { return i.target }

func (i storageMoveInfo) Metadata(ctx context.Context) (fs.Metadata, error) {
	return fs.GetMetadata(ctx, i.Object)
}

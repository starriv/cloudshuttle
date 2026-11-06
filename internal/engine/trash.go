package engine

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/filter"
	"github.com/rclone/rclone/fs/operations"
)

// TrashRoot is the folder at the top of a destination that a run moves the
// files it deletes or replaces into, one folder a run, rather than lose
// them: a sync's deletes, and the earlier versions of what a copy or a sync
// replaces. Runs leave it out on both sides: a sync never deletes it, and a
// task whose source is another's destination does not carry it along.
const TrashRoot = ".cloudshuttle-trash"

// trashStamp names a run's folder in the trash by when it started, in a
// form every backend takes: no colons, which SMB refuses.
const trashStamp = "2006-01-02 15.04.05"

// TrashFolder is the folder, relative to the destination, of a run that
// started at t.
func TrashFolder(t time.Time) string {
	return TrashRoot + "/" + t.Local().Format(trashStamp)
}

// trashFs is a run's folder in the trash, as rclone's backup dir. It moves
// a file in on the server, by copying it and deleting the original where the
// backend has no move of its own (S3): rclone would otherwise count the copy
// as a transfer and the original as one more delete, so a run's numbers
// would not tell what it did, and its cap on deletes would stop it.
type trashFs struct {
	fs.Fs
	moves atomic.Int64 // files rclone asked to move in, each counted as a check
	moved atomic.Int64 // files that are in
}

func (t *trashFs) Features() *fs.Features {
	ft := *t.Fs.Features()
	ft.Move = t.move
	return &ft
}

func (t *trashFs) move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	t.moves.Add(1)
	inner := t.Fs.Features()
	if move := inner.Move; move != nil {
		o, err := move(ctx, src, remote)
		if err == nil {
			t.moved.Add(1)
		}
		if !errors.Is(err, fs.ErrorCantMove) {
			return o, err
		}
	}
	if inner.Copy == nil {
		return nil, fs.ErrorCantMove
	}
	o, err := inner.Copy(ctx, src, remote)
	if errors.Is(err, fs.ErrorCantCopy) {
		return nil, fs.ErrorCantMove
	}
	if err != nil {
		return nil, err
	}
	if err := src.Remove(ctx); err != nil {
		return o, fmt.Errorf("remove after copying into the trash: %w", err)
	}
	t.moved.Add(1)
	return o, nil
}

// useTrash makes the job's folder in the trash rclone's backup dir. rclone
// looks a backup dir up by name in its cache of remotes, so the folder goes
// in under a name of the job's: it is built in memory, as every remote is.
func useTrash(ctx context.Context, ci *fs.ConfigInfo, job Job) (*trashFs, error) {
	loc := job.Destination
	loc.Path = path.Join(loc.Path, job.Trash)
	f, err := newDirFs(ctx, loc)
	if err != nil {
		return nil, fmt.Errorf("the trash: %w", classify(err))
	}
	// A listing tells S3 the bucket is there, as the destination's own
	// listing does: else the first copy into the trash would ask to create
	// the bucket, which a key without that right, or a provider answering
	// that it exists, fails.
	if _, err := f.List(ctx, ""); err != nil && !errors.Is(err, fs.ErrorDirNotFound) {
		shutdown(f)
		return nil, fmt.Errorf("the trash: %w", classify(err))
	}
	t := &trashFs{Fs: f}
	name := "cloudshuttle-trash-" + job.ID
	cache.Put(name, t)
	ci.BackupDir = name
	return t, nil
}

// deleteList collects what a sync would delete, as its preview finds it.
type deleteList struct {
	mu    stdsync.Mutex
	paths []string
}

// maxListedDeletes is how many of the files a preview would delete it keeps
// by name, for a person to look over: past it, they are only counted.
const maxListedDeletes = 100

func (l *deleteList) logger(_ context.Context, sigil operations.Sigil, _, dst fs.DirEntry, err error) {
	if sigil != operations.MissingOnSrc || err != nil || dst == nil {
		return // a folder, or what is not a delete
	}
	l.mu.Lock()
	if len(l.paths) < maxListedDeletes {
		l.paths = append(l.paths, dst.Remote())
	}
	l.mu.Unlock()
}

// StoppedSync is a sync the delete check stopped before it changed a file:
// what it would have deleted. It wraps ErrEmptySource or ErrLargeDelete.
type StoppedSync struct {
	Err         error
	Deletes     int64 // the files it would delete
	Destination int64 // the files in the destination
	// Paths are some of the files it would delete, relative to the
	// destination: the first maxListedDeletes it found.
	Paths []string
}

func (e *StoppedSync) Error() string {
	if errors.Is(e.Err, ErrEmptySource) {
		return fmt.Sprintf("%v: it would delete %d files", e.Err, e.Deletes)
	}
	return fmt.Sprintf("%v: %d of %d files", e.Err, e.Deletes, e.Destination)
}

func (e *StoppedSync) Unwrap() error { return e.Err }

// pruneTrash deletes the trash's folders of runs that started more than the
// job's days ago. It goes by the folders' names, which the runs gave them,
// and leaves alone what it did not name. It counts apart from the run, whose
// numbers tell what the sync did; a failure only goes into the log.
func pruneTrash(ctx context.Context, job Job, group string, log func(Line)) {
	if job.TrashDays <= 0 {
		return
	}
	defer muteRclone()()
	pctx, ci := fs.AddConfig(ctx)
	ci.DryRun = false
	ci.MaxDelete = -1
	ci.BackupDir = ""
	ci.LogLevel = fs.LogLevelError
	fi, err := filter.NewFilter(nil)
	if err != nil {
		log(appLine("WARN", "emptying the trash: %v", err))
		return
	}
	pctx = filter.ReplaceConfig(pctx, fi)
	name := group + "/trash"
	pctx = accounting.WithStatsGroup(pctx, name)
	defer deleteStats(name)

	loc := job.Destination
	loc.Path = path.Join(loc.Path, TrashRoot)
	f, err := newDirFs(pctx, loc)
	if err != nil {
		log(appLine("WARN", "emptying the trash: %v", classify(err)))
		return
	}
	defer shutdown(f)
	entries, err := f.List(pctx, "")
	if errors.Is(err, fs.ErrorDirNotFound) {
		return
	}
	if err != nil {
		log(appLine("WARN", "emptying the trash: %v", classify(err)))
		return
	}
	cutoff := time.Now().AddDate(0, 0, -job.TrashDays)
	var purged []string
	for _, e := range entries {
		if _, ok := e.(fs.Directory); !ok {
			continue
		}
		folder := path.Base(e.Remote())
		started, err := time.ParseInLocation(trashStamp, folder, time.Local)
		if err != nil || !started.Before(cutoff) {
			continue
		}
		if err := operations.Purge(pctx, f, e.Remote()); err != nil {
			log(appLine("WARN", "emptying the trash: %s: %v", folder, classify(err)))
			continue
		}
		purged = append(purged, folder)
	}
	if len(purged) > 0 {
		log(appLine("INFO", "emptied from the trash, older than %d days: %s", job.TrashDays, strings.Join(purged, ", ")))
	}
}

package engine

import (
	"context"
	"errors"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"
	"github.com/rclone/rclone/fs/fserrors"
)

// A run that touches an SMB server tries again when the server drops it.
//
// Some SMB servers, a small NAS or a computer that shares a folder, reset a
// connection that arrives while others are being opened, or one that has
// been quiet for a while. rclone reports a directory it could not list as an
// error of the run and copies nothing below it, so a copy that moved
// thousands of files can still end as failed over a handful of folders. The
// run tries again from where it stopped, with fewer folders and files at a
// time: what is already at the destination is compared, not copied again.
const (
	// smbRunTries is how many times an SMB run is tried: the first at the
	// settings, then twice more, more gently.
	smbRunTries = 3
	// A server that just reset a connection needs a moment before it takes
	// another; the second pause is longer because the first did not help.
	smbFirstPause  = 3 * time.Second
	smbSecondPause = 15 * time.Second
	// The quiet settings keep the number of sessions a small server sees at
	// once small: listings and files at two each, and one of each on the
	// last try. A healthy server never runs this path.
	smbQuietTransfers = 2
	smbQuietCheckers  = 2
	// A retry uses rclone's own default of low-level retries, whatever the
	// setting says: the setting was not enough for this server.
	smbQuietRetries = 10
)

// attemptPolicy is how hard one attempt works: zero leaves rclone's default.
type attemptPolicy struct {
	transfers int
	checkers  int
	retries   int
}

// attemptsFor says how many times a job runs. A job that stays on this
// computer, or on a service that answers every request itself, runs once: a
// refused request there is a real failure. An SMB server may reset a
// connection and then work again, so a run with one tries again.
func attemptsFor(job Job) int {
	if job.Source.SMB == nil && job.Destination.SMB == nil {
		return 1
	}
	return smbRunTries
}

// policyFor gives the run's settings on the first attempt, and quieter ones
// afterwards: a server that could not take the run at its full pace gets
// fewer sessions at once, and one file at a time on the last try.
func policyFor(job Job, attempt int) attemptPolicy {
	p := attemptPolicy{transfers: job.Transfers, checkers: job.Checkers, retries: job.Retries}
	if attempt <= 1 {
		return p
	}
	p.transfers = minCount(p.transfers, smbQuietTransfers)
	p.checkers = minCount(p.checkers, smbQuietCheckers)
	p.retries = max(p.retries, smbQuietRetries)
	if attempt >= smbRunTries {
		p.transfers, p.checkers = 1, 1
	}
	return p
}

// minCount folds a count into a limit, treating zero as the library's
// default, which the limit replaces.
func minCount(v, limit int) int {
	if v <= 0 || v > limit {
		return limit
	}
	return v
}

// pauseFor is how long the run waits before attempt, so that the server can
// close what it reset and whatever else it was doing.
func pauseFor(attempt int) time.Duration {
	if attempt <= 2 {
		return smbFirstPause
	}
	return smbSecondPause
}

// retryable reports whether another attempt could mend the failure. What
// another try cannot mend is a wrong password, a share that is not there, a
// source that is a file, the run's own guards, and the person stopping it;
// everything else that looks like the network giving way under the run is
// worth trying again.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrAuthFailed) || errors.Is(err, ErrShareNotFound) ||
		errors.Is(err, ErrIsFile) || errors.Is(err, ErrEmptySource) ||
		errors.Is(err, ErrLargeDelete) {
		return false
	}
	var stopped *StoppedSync
	if errors.As(err, &stopped) {
		return false
	}
	if fserrors.ShouldRetry(err) {
		return true
	}
	// rclone's check walks into the fields of the errors it is given, but
	// not into this library's own: it keeps the network error in a field
	// instead of unwrapping it.
	var transport *smb2.TransportError
	if errors.As(err, &transport) {
		return retryable(transport.Err)
	}
	return false
}

// combine adds an attempt's progress to the run's. Files copied by an earlier
// attempt are not copied again, so the sums are what the run moved. A later
// attempt plans only what is left, which may include folders an earlier one
// could not list: the run's total is what was already moved plus that plan,
// and never less than an earlier total. Every attempt compares all the files
// again, so the checks are the most any attempt made, not a sum.
func combine(total, p Progress) Progress {
	total.TotalBytes = max(total.TotalBytes, total.Bytes+p.TotalBytes)
	total.TotalTransfers = max(total.TotalTransfers, total.Transfers+p.TotalTransfers)
	total.Bytes += p.Bytes
	total.Checks = max(total.Checks, p.Checks)
	total.Deletes += p.Deletes
	total.Transfers += p.Transfers
	total.Trashed += p.Trashed
	total.Errors = p.Errors
	total.Speed = p.Speed
	if total.TotalBytes < total.Bytes {
		total.TotalBytes = total.Bytes
	}
	if total.TotalTransfers < total.Transfers {
		total.TotalTransfers = total.Transfers
	}
	return total
}

// attemptResult is what an attempt did: its progress, why it stopped, and
// whether it opened the trash, whose older folders the run empties once, when
// it is done.
type attemptResult struct {
	progress Progress
	err      error
	trash    bool
	attempts int // how many attempts ran, which tells a retried failure
}

// runAttempts calls attempt until it succeeds, fails for a reason another
// attempt cannot mend, or runs out of tries, and adds up what each did. The
// attempt is told what earlier attempts already did, so that the progress the
// person sees keeps counting from there instead of starting again.
func runAttempts(ctx context.Context, tries int, pause func(attempt int) time.Duration,
	attempt func(number int, already Progress) attemptResult,
	onRetry func(number int, err error, wait time.Duration)) attemptResult {
	var total Progress
	var lastErr error
	var trash bool
	for number := 1; number <= tries; number++ {
		if number > 1 {
			wait := pause(number)
			onRetry(number, lastErr, wait)
			if !sleepContext(ctx, wait) {
				return attemptResult{progress: total, err: ctx.Err(), trash: trash, attempts: number - 1}
			}
		}
		result := attempt(number, total)
		total = combine(total, result.progress)
		trash = result.trash
		if result.err == nil {
			return attemptResult{progress: total, trash: trash, attempts: number}
		}
		lastErr = result.err
		if number == tries || !retryable(result.err) {
			return attemptResult{progress: total, err: result.err, trash: trash, attempts: number}
		}
		if ctx.Err() != nil {
			return attemptResult{progress: total, err: ctx.Err(), trash: trash, attempts: number}
		}
	}
	return attemptResult{progress: total, err: lastErr, trash: trash, attempts: tries}
}

// sleepContext waits for d, or until ctx ends, and reports whether it waited
// the whole time.
func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// remainingDeletes preserves the first successful preview's limit across
// attempts. Count attempted deletions too, since a lost reply can hide a
// successful delete on the server.
func remainingDeletes(limit *int64, deleted, preview int64) int64 {
	if *limit < 0 {
		*limit = preview
	}
	return min(preview, max(0, *limit-deleted))
}

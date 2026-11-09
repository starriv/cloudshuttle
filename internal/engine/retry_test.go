package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"
)

// resetError is what a server dropping a connection looks like through
// rclone and the SMB library: a transport error holding a TCP reset.
func resetError() error {
	return &smb2.TransportError{Err: &net.OpError{
		Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET),
	}}
}

func TestAttemptsFor(t *testing.T) {
	local := Location{Path: "/tmp/a"}
	smb := Location{SMB: &SMB{Host: "nas.local", Username: "me"}, Path: "share/a"}
	if got := attemptsFor(Job{Source: local, Destination: local}); got != 1 {
		t.Errorf("local copy tries %d times, want 1", got)
	}
	if got := attemptsFor(Job{Source: local, Destination: smb}); got != smbRunTries {
		t.Errorf("copy to SMB tries %d times, want %d", got, smbRunTries)
	}
	if got := attemptsFor(Job{Source: smb, Destination: smb}); got != smbRunTries {
		t.Errorf("SMB to SMB tries %d times, want %d", got, smbRunTries)
	}
}

func TestPolicyFor(t *testing.T) {
	job := Job{Transfers: 8, Checkers: 32, Retries: 2}
	if got := policyFor(job, 1); got != (attemptPolicy{8, 32, 2}) {
		t.Errorf("first attempt = %+v, want the settings untouched", got)
	}
	if got := policyFor(job, 2); got != (attemptPolicy{smbQuietTransfers, smbQuietCheckers, smbQuietRetries}) {
		t.Errorf("second attempt = %+v, want the quiet settings", got)
	}
	if got := policyFor(job, 3); got != (attemptPolicy{1, 1, smbQuietRetries}) {
		t.Errorf("last attempt = %+v, want one file and one check", got)
	}
	// Zero means the library's default, which the quiet settings replace.
	blank := Job{}
	if got := policyFor(blank, 2); got.transfers != smbQuietTransfers || got.checkers != smbQuietCheckers {
		t.Errorf("second attempt of a job without counts = %+v", got)
	}
	// A job already quieter than the retry stays as it is.
	quiet := Job{Transfers: 1, Checkers: 1, Retries: 10}
	if got := policyFor(quiet, 2); got != (attemptPolicy{1, 1, 10}) {
		t.Errorf("already quiet job = %+v", got)
	}
}

func TestPauseFor(t *testing.T) {
	if pauseFor(2) >= pauseFor(3) {
		t.Errorf("the second pause %v is not longer than the first %v", pauseFor(3), pauseFor(2))
	}
}

func TestRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nothing", nil, false},
		{"a dropped connection", resetError(), true},
		{"a dropped connection during a listing", fmt.Errorf("march failed with 2 error(s): first error: %w", resetError()), true},
		{"a server that is not there", fmt.Errorf("%w: %w", ErrUnreachable, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}), true},
		{"a canceled run", context.Canceled, false},
		{"a deadline of the run", context.DeadlineExceeded, false},
		{"a wrong password", fmt.Errorf("%w: %w", ErrAuthFailed, errors.New("logon failure")), false},
		{"a share that is not there", ErrShareNotFound, false},
		{"a location that is a file", ErrIsFile, false},
		{"an empty source", ErrEmptySource, false},
		{"most of the destination", ErrLargeDelete, false},
		{"a sync the guards stopped", &StoppedSync{Err: ErrEmptySource}, false},
		{"a disk that is full", errors.New("no space left on device"), false},
		{"a bad filter", fmt.Errorf("filter: %w", errors.New("bad pattern")), false},
	}
	for _, tc := range cases {
		if got := retryable(tc.err); got != tc.want {
			t.Errorf("%s: retryable(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

func TestRunAttemptsRetriesAndAddsUp(t *testing.T) {
	var pauses []time.Duration
	var retried []int
	calls := 0
	result := runAttempts(context.Background(), 3,
		func(attempt int) time.Duration {
			pauses = append(pauses, time.Duration(attempt)*time.Second)
			return time.Duration(attempt) * time.Second
		},
		func(number int, already Progress) attemptResult {
			calls++
			if already.Transfers != int64(10*(number-1)) {
				t.Errorf("attempt %d started from %d transfers, want %d", number, already.Transfers, 10*(number-1))
			}
			// Each attempt plans only what is left: 30 files, then 20, then 10.
			left := int64(10 * (4 - number))
			if number < 3 {
				return attemptResult{progress: Progress{Transfers: 10, Bytes: 100, TotalTransfers: left, TotalBytes: 10 * left}, err: resetError()}
			}
			return attemptResult{progress: Progress{Transfers: 10, Bytes: 100, TotalTransfers: left, TotalBytes: 10 * left}, trash: true}
		},
		func(number int, err error, wait time.Duration) {
			retried = append(retried, number)
			if !retryable(err) {
				t.Errorf("retried a failure no attempt can mend: %v", err)
			}
			if wait != time.Duration(number)*time.Second {
				t.Errorf("waited %v before attempt %d", wait, number)
			}
		})
	if calls != 3 {
		t.Errorf("ran %d attempts, want 3", calls)
	}
	if len(retried) != 2 || retried[0] != 2 || retried[1] != 3 {
		t.Errorf("retried before attempts %v, want [2 3]", retried)
	}
	if len(pauses) != 2 {
		t.Errorf("paused %v, want twice", pauses)
	}
	if result.err != nil {
		t.Errorf("err = %v, want none", result.err)
	}
	if result.attempts != 3 {
		t.Errorf("attempts = %d, want 3", result.attempts)
	}
	if !result.trash {
		t.Error("the trash the last attempt opened is not reported")
	}
	want := Progress{Transfers: 30, Bytes: 300, Checks: 0, Deletes: 0, TotalTransfers: 30, TotalBytes: 300}
	if result.progress.Transfers != want.Transfers || result.progress.Bytes != want.Bytes ||
		result.progress.TotalTransfers != want.TotalTransfers || result.progress.TotalBytes != want.TotalBytes {
		t.Errorf("progress = %+v, want %+v", result.progress, want)
	}
}

func TestRunAttemptsStopsOnWhatAnotherTryCannotMend(t *testing.T) {
	calls := 0
	result := runAttempts(context.Background(), 3,
		func(int) time.Duration { return 0 },
		func(number int, _ Progress) attemptResult {
			calls++
			return attemptResult{err: fmt.Errorf("%w: logon failure", ErrAuthFailed)}
		},
		func(int, error, time.Duration) { t.Error("retried a wrong password") })
	if calls != 1 {
		t.Errorf("ran %d attempts, want 1", calls)
	}
	if !errors.Is(result.err, ErrAuthFailed) {
		t.Errorf("err = %v, want the authentication failure", result.err)
	}
}

func TestRunAttemptsStopsWhenThePersonCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	result := runAttempts(ctx, 3,
		func(int) time.Duration { return time.Hour },
		func(int, Progress) attemptResult {
			calls++
			return attemptResult{err: resetError()}
		},
		func(int, error, time.Duration) {})
	if calls != 1 {
		t.Errorf("ran %d attempts after the run was canceled, want 1", calls)
	}
	if !errors.Is(result.err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", result.err)
	}
}

func TestCombineKeepsTheFirstPlannedTotals(t *testing.T) {
	first := Progress{Transfers: 5, Bytes: 50, TotalTransfers: 20, TotalBytes: 200, Speed: 10}
	second := Progress{Transfers: 3, Bytes: 30, Checks: 7, TotalTransfers: 4, TotalBytes: 40, Errors: 1, Speed: 5}
	got := combine(first, second)
	if got.Transfers != 8 || got.Bytes != 80 || got.Checks != 7 || got.Errors != 1 {
		t.Errorf("combined = %+v", got)
	}
	if got.TotalTransfers != 20 || got.TotalBytes != 200 {
		t.Errorf("totals = %d/%d, want the first attempt's 20/200", got.TotalTransfers, got.TotalBytes)
	}
	if got.Speed != 5 {
		t.Errorf("speed = %v, want the last attempt's 5", got.Speed)
	}
}

// An attempt that could not list some folders planned too little; the next
// one finds them, and the run's total grows to hold them, instead of the bar
// filling while files still move.
func TestCombineGrowsToWhatALaterAttemptFinds(t *testing.T) {
	first := Progress{Transfers: 5, Bytes: 50, Checks: 9, TotalTransfers: 5, TotalBytes: 50}
	second := Progress{Transfers: 2, Bytes: 20, Checks: 4, TotalTransfers: 15, TotalBytes: 150}
	got := combine(combine(Progress{}, first), second)
	if got.TotalTransfers != 20 || got.TotalBytes != 200 {
		t.Errorf("totals = %d/%d, want 20/200", got.TotalTransfers, got.TotalBytes)
	}
	if got.Transfers != 7 || got.Bytes != 70 {
		t.Errorf("moved = %d/%d, want 7/70", got.Transfers, got.Bytes)
	}
	if got.Checks != 9 {
		t.Errorf("checks = %d, want the most one attempt made, 9", got.Checks)
	}
}

func TestRetrySharesInitialDeleteLimit(t *testing.T) {
	limit := int64(-1)
	if got := remainingDeletes(&limit, 0, 40); got != 40 {
		t.Fatalf("initial allowance: %d", got)
	}
	if got := remainingDeletes(&limit, 35, 25); got != 5 {
		t.Fatalf("retry allowed %d deletes after spending 35 of the original 40", got)
	}
	if got := remainingDeletes(&limit, 40, 20); got != 0 {
		t.Fatalf("retry allowed %d deletes after spending the original allowance", got)
	}
}

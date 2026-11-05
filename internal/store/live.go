package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
)

// Two processes of the app run tasks and share the database: the window,
// and the scheduler the system starts on the tasks' schedules. A run in
// progress records which of them runs it, and the scheduler keeps its
// progress in the row, for the window to show.

// ErrSlotTaken is a scheduled run that does not start because the task
// already ran since the time it was due: another process ran it.
var ErrSlotTaken = errors.New("the task already ran since then")

// StartRun records r, a run that starts in the process owner, unless the
// task has a run in progress (domain.ErrAlreadyActive), or, for a run due
// at slot (not zero), a run that started then or since (ErrSlotTaken),
// whichever process runs it. Two processes starting one task at once
// start it once.
func (s *Store) StartRun(ctx context.Context, r domain.Run, owner string, queued bool, slot time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO runs (id, task_id, task_name, mode, trigger_kind, dry_run, status, message, problem,
			started_at, owner, queued)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM runs WHERE task_id = ? AND status = ?)
		  AND (? = 0 OR NOT EXISTS (SELECT 1 FROM schedule_starts WHERE task_id = ? AND started_at >= ?))`,
		r.ID, r.TaskID, r.TaskName, r.Mode, r.Trigger, r.DryRun, r.Status, r.Message, r.Problem,
		millis(r.StartedAt), owner, queued,
		r.TaskID, domain.StatusRunning,
		millis(slot), r.TaskID, millis(slot))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	var running int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE task_id = ? AND status = ?`,
		r.TaskID, domain.StatusRunning).Scan(&running); err != nil {
		return err
	}
	if running > 0 {
		return domain.ErrAlreadyActive
	}
	return ErrSlotTaken
}

// LiveRun is a run in progress, as the database has it.
type LiveRun struct {
	Run    domain.Run
	Owner  string
	Queued bool
}

// LiveRuns returns the runs in progress, of every process, the oldest first.
func (s *Store) LiveRuns(ctx context.Context) ([]LiveRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+`, owner, queued FROM runs
		WHERE status = ? ORDER BY started_at, id`, domain.StatusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiveRun
	for rows.Next() {
		var l LiveRun
		var started, finished int64
		r := &l.Run
		if err := rows.Scan(&r.ID, &r.TaskID, &r.TaskName, &r.Mode, &r.Trigger, &r.DryRun, &r.Status,
			&r.Message, &r.Problem, &r.Stats.Bytes, &r.Stats.TotalBytes, &r.Stats.Transfers, &r.Stats.TotalTransfers,
			&r.Stats.Checks, &r.Stats.Deletes, &r.Stats.Errors, &r.Stats.Speed, &r.Stats.Trashed, &r.Trash,
			&started, &finished, &l.Owner, &l.Queued); err != nil {
			return nil, err
		}
		r.StartedAt, r.FinishedAt = fromMillis(started), fromMillis(finished)
		out = append(out, l)
	}
	return out, rows.Err()
}

// Run returns the run with id.
func (s *Store) Run(ctx context.Context, id string) (domain.Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, domain.ErrNotFound
	}
	return r, err
}

// UpdateProgress records how far the run with id has gone, and whether it
// still waits for its turn, and reports whether the window asked it to
// stop.
func (s *Store) UpdateProgress(ctx context.Context, id string, st domain.Stats, queued bool) (bool, error) {
	var cancel bool
	err := s.db.QueryRowContext(ctx, `
		UPDATE runs SET bytes = ?, total_bytes = ?, transfers = ?, total_transfers = ?, checks = ?,
			deletes = ?, errors = ?, speed = ?, queued = ?
		WHERE id = ? AND status = ?
		RETURNING cancel_requested`,
		st.Bytes, st.TotalBytes, st.Transfers, st.TotalTransfers, st.Checks, st.Deletes, st.Errors, st.Speed,
		queued, id, domain.StatusRunning).Scan(&cancel)
	if errors.Is(err, sql.ErrNoRows) {
		return false, domain.ErrNotFound
	}
	return cancel, err
}

// RequestCancel asks the process running the run with id to stop it.
func (s *Store) RequestCancel(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE runs SET cancel_requested = 1 WHERE id = ? AND status = ?`,
		id, domain.StatusRunning)
	if err != nil {
		return err
	}
	return requireOne(res)
}

// CancelOrphanRuns ends the runs in progress of the owners, processes
// that are no more: they quit or crashed before the runs finished. Runs
// recorded before runs had owners have the owner "".
func (s *Store) CancelOrphanRuns(ctx context.Context, owners ...string) (int64, error) {
	if len(owners) == 0 {
		return 0, nil
	}
	args := []any{domain.StatusCanceled, domain.StatusRunning}
	for _, o := range owners {
		args = append(args, o)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE runs SET status = ?, finished_at = started_at, queued = 0
		WHERE status = ? AND owner IN (?`+strings.Repeat(", ?", len(owners)-1)+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LastStarts returns when each task last started a run that was not a dry
// run, finished or not, by task ID: when the task was last due, it ran.
func (s *Store) LastStarts(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task_id, started_at FROM schedule_starts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var started int64
		if err := rows.Scan(&id, &started); err != nil {
			return nil, err
		}
		out[id] = fromMillis(started)
	}
	return out, rows.Err()
}

// MarkNotify marks the run with id as a failure to tell the person about,
// which TakeNotifications then returns once.
func (s *Store) MarkNotify(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET notify = 1 WHERE id = ?`, id)
	return err
}

// TakeNotifications returns the finished runs marked with MarkNotify, and
// unmarks them: each is told once.
func (s *Store) TakeNotifications(ctx context.Context) ([]domain.Run, error) {
	rows, err := s.db.QueryContext(ctx, `UPDATE runs SET notify = 0 WHERE notify = 1 AND status != ?
		RETURNING `+runColumns, domain.StatusRunning)
	if err != nil {
		return nil, err
	}
	return scanRuns(rows)
}

// DataVersion changes whenever another process commits a change to the
// database, and only then.
func (s *Store) DataVersion(ctx context.Context) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&v)
	return v, err
}

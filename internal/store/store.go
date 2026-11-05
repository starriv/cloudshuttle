// Package store keeps storages, tasks and runs in a SQLite database.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
	sqlite "modernc.org/sqlite" // registers the "sqlite" driver too
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed schema.sql
var schema string

// migration is one step of migrations, in the transaction that records it.
type migration func(tx *sql.Tx) error

// sqlStep is a migration that runs statements.
func sqlStep(statements string) migration {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec(statements)
		return err
	}
}

// migrations bring a database made by an earlier version up to schema.sql,
// one step per version; PRAGMA user_version counts the steps applied. A new
// database gets schema.sql whole and starts at len(migrations). Add a step
// here whenever schema.sql changes a table that already exists.
var migrations = []migration{
	// 1: storages can be SMB shares.
	sqlStep(`ALTER TABLE storages ADD COLUMN kind TEXT NOT NULL DEFAULT 's3';
	 ALTER TABLE storages ADD COLUMN host TEXT NOT NULL DEFAULT '';
	 ALTER TABLE storages ADD COLUMN port INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE storages ADD COLUMN username TEXT NOT NULL DEFAULT '';
	 ALTER TABLE storages ADD COLUMN share TEXT NOT NULL DEFAULT '';`),
	// 2: an SMB storage is its server; tasks choose the share, as the first
	// name of their paths.
	sqlStep(`UPDATE tasks SET source_path = (SELECT share FROM storages WHERE id = source_storage_id) ||
		CASE source_path WHEN '' THEN '' ELSE '/' || source_path END
	 WHERE source_storage_id IN (SELECT id FROM storages WHERE kind = 'smb' AND share != '');
	 UPDATE tasks SET dest_path = (SELECT share FROM storages WHERE id = dest_storage_id) ||
		CASE dest_path WHEN '' THEN '' ELSE '/' || dest_path END
	 WHERE dest_storage_id IN (SELECT id FROM storages WHERE kind = 'smb' AND share != '');
	 ALTER TABLE storages DROP COLUMN share;`),
	// 3: storages keep how their connection last tested.
	sqlStep(`ALTER TABLE storages ADD COLUMN checked_at INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE storages ADD COLUMN check_ok INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE storages ADD COLUMN check_problem TEXT NOT NULL DEFAULT '';
	 ALTER TABLE storages ADD COLUMN check_message TEXT NOT NULL DEFAULT '';`),
	// 4: a run says when it stopped before deleting anything.
	sqlStep(`ALTER TABLE runs ADD COLUMN problem TEXT NOT NULL DEFAULT '';`),
	// 5: the configuration can be kept in iCloud Drive. The settings table
	// itself came after the schemas that knew it, so a database without one
	// gets it here, before the column.
	sqlStep(`CREATE TABLE IF NOT EXISTS settings (
		id             INTEGER PRIMARY KEY CHECK (id = 1),
		transfers      INTEGER NOT NULL,
		checkers       INTEGER NOT NULL,
		retries        INTEGER NOT NULL,
		bandwidth_mbps INTEGER NOT NULL,
		notify_failed  INTEGER NOT NULL,
		notify_skipped INTEGER NOT NULL,
		retention_days INTEGER NOT NULL
	);
	ALTER TABLE settings ADD COLUMN backup_icloud INTEGER NOT NULL DEFAULT 0;`),
	// 6: a scheduler the system starts runs tasks beside the window, and
	// records which of the two runs each.
	sqlStep(`ALTER TABLE tasks ADD COLUMN schedule_since INTEGER NOT NULL DEFAULT 0;
	 UPDATE tasks SET schedule_since = updated_at;
	 ALTER TABLE runs ADD COLUMN owner TEXT NOT NULL DEFAULT '';
	 ALTER TABLE runs ADD COLUMN queued INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE runs ADD COLUMN speed REAL NOT NULL DEFAULT 0;
	 ALTER TABLE runs ADD COLUMN cancel_requested INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE runs ADD COLUMN notify INTEGER NOT NULL DEFAULT 0;`),
	// 7: schedules are said as a person says them, no more as cron
	// expressions.
	convertCronSchedules,
	// 8: schedule deduplication survives history deletion and pruning.
	sqlStep(`
CREATE TABLE IF NOT EXISTS schedule_starts (
 task_id TEXT PRIMARY KEY,
 started_at INTEGER NOT NULL
);
CREATE TRIGGER IF NOT EXISTS remember_run_start AFTER INSERT ON runs
WHEN NEW.dry_run = 0
BEGIN
 INSERT INTO schedule_starts(task_id, started_at) VALUES(NEW.task_id, NEW.started_at)
 ON CONFLICT(task_id) DO UPDATE SET started_at = MAX(started_at, excluded.started_at);
END;
INSERT INTO schedule_starts(task_id, started_at)
 SELECT task_id, MAX(started_at) FROM runs WHERE dry_run = 0 GROUP BY task_id
 ON CONFLICT(task_id) DO UPDATE SET started_at = MAX(started_at, excluded.started_at);`),
	// 9: the system starts the app for the schedules only once the person
	// allowed it. A database whose schedules already ran that way keeps
	// them so: the system told the person when it added the app.
	sqlStep(`ALTER TABLE settings ADD COLUMN background INTEGER NOT NULL DEFAULT 0;
	 UPDATE settings SET background = 1 WHERE EXISTS (SELECT 1 FROM tasks WHERE schedule != '');`),
	// 10: the window's appearance, the system's until the person picks one.
	sqlStep(`ALTER TABLE settings ADD COLUMN appearance INTEGER NOT NULL DEFAULT 0;`),
	// 11: the app updates itself: it checks on its own unless the person
	// turns it off, and installs by itself only when they allow it.
	sqlStep(`ALTER TABLE settings ADD COLUMN check_updates INTEGER NOT NULL DEFAULT 1;
	 ALTER TABLE settings ADD COLUMN install_updates INTEGER NOT NULL DEFAULT 0;`),
	// 12: a run moves what it deletes or replaces into a trash folder of
	// its destination, for a month unless the person chooses otherwise, in
	// the settings or for a task; a sync the delete check stopped keeps
	// what it would have deleted.
	addTrash,
}

// addTrash is step 12. A database older than the logs of runs has no
// run_logs yet: schema.sql creates it whole after the migrations.
func addTrash(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE settings ADD COLUMN trash_days INTEGER NOT NULL DEFAULT 30;
	 ALTER TABLE tasks ADD COLUMN trash_days INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE runs ADD COLUMN trashed INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE runs ADD COLUMN trash TEXT NOT NULL DEFAULT '';`); err != nil {
		return err
	}
	var logs int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'run_logs'`).Scan(&logs); err != nil || logs == 0 {
		return err
	}
	_, err := tx.Exec(`ALTER TABLE run_logs ADD COLUMN deletes TEXT NOT NULL DEFAULT '';
	 ALTER TABLE run_logs ADD COLUMN deletes_total INTEGER NOT NULL DEFAULT 0;`)
	return err
}

// Store is the app's database. Its methods are safe from any goroutine.
type Store struct {
	db *sql.DB
}

// ErrNewerDatabase is a database made by a newer version of the app, which
// this one must not write to: its tables may hold columns this version does
// not know.
var ErrNewerDatabase = errors.New("the database was made by a newer version of the app")

// Open opens or creates the database at path. ":memory:" opens one in memory.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path != ":memory:" {
		dsn += "&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite writes one at a time, and an in-memory
	// database lives in its connection.
	db.SetMaxOpenConns(1)
	if err := migrate(db, path); err != nil {
		db.Close()
		return nil, err
	}
	// The database holds where the person's files are, and the names of
	// the servers they use: only they should read it.
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "." && dir != "" {
			_ = os.Chmod(dir, 0o700)
		}
		_ = os.Chmod(path, 0o600)
	}
	// The settings row is there before the first read, and keeps the
	// values a person chose as the app goes on.
	if err := seedSettings(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// seedSettings writes the defaults into a database that has no settings row
// yet, and leaves the row a person already changed as it is.
func seedSettings(db *sql.DB) error {
	d := domain.DefaultSettings()
	_, err := db.Exec(`
		INSERT INTO settings (id, transfers, checkers, retries, bandwidth_mbps,
			notify_failed, notify_skipped, retention_days, backup_icloud, check_updates, install_updates,
			trash_days)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		d.Transfers, d.Checkers, d.Retries, d.BandwidthMBps, d.NotifyFailed, d.NotifySkipped,
		d.RetentionDays, d.BackupICloud, d.CheckUpdates, d.InstallUpdates, d.TrashDays)
	return err
}

// Settings returns the app's settings, the defaults when the database has
// none yet.
func (s *Store) Settings(ctx context.Context) (domain.Settings, error) {
	var (
		st                                        domain.Settings
		notifyFailed, notifySkipped, backupICloud int
		background, appearance                    int
		checkUpdates, installUpdates              int
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT transfers, checkers, retries, bandwidth_mbps, notify_failed, notify_skipped,
			retention_days, backup_icloud, background, appearance, check_updates, install_updates,
			trash_days
		FROM settings WHERE id = 1`).
		Scan(&st.Transfers, &st.Checkers, &st.Retries, &st.BandwidthMBps,
			&notifyFailed, &notifySkipped, &st.RetentionDays, &backupICloud, &background, &appearance,
			&checkUpdates, &installUpdates, &st.TrashDays)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DefaultSettings(), nil
	}
	if err != nil {
		return st, err
	}
	st.NotifyFailed, st.NotifySkipped = notifyFailed != 0, notifySkipped != 0
	st.BackupICloud = backupICloud != 0
	st.Background = domain.Consent(background)
	st.Appearance = domain.Appearance(appearance)
	st.CheckUpdates, st.InstallUpdates = checkUpdates != 0, installUpdates != 0
	return st, nil
}

// PutSettings keeps st as the app's settings.
func (s *Store) PutSettings(ctx context.Context, st domain.Settings) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (id, transfers, checkers, retries, bandwidth_mbps,
			notify_failed, notify_skipped, retention_days, backup_icloud, background, appearance,
			check_updates, install_updates, trash_days)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			transfers = excluded.transfers, checkers = excluded.checkers, retries = excluded.retries,
			bandwidth_mbps = excluded.bandwidth_mbps, notify_failed = excluded.notify_failed,
			notify_skipped = excluded.notify_skipped, retention_days = excluded.retention_days,
			backup_icloud = excluded.backup_icloud, background = excluded.background,
			appearance = excluded.appearance, check_updates = excluded.check_updates,
			install_updates = excluded.install_updates, trash_days = excluded.trash_days`,
		st.Transfers, st.Checkers, st.Retries, st.BandwidthMBps,
		st.NotifyFailed, st.NotifySkipped, st.RetentionDays, st.BackupICloud, int(st.Background),
		int(st.Appearance), st.CheckUpdates, st.InstallUpdates, st.TrashDays)
	return err
}

// BackupConfig writes the configuration - storages, tasks and settings - to
// path as a database a person could put back. The history and its logs are
// left out: a copy kept elsewhere should hold what was set up, not what the
// app ran. A file already at path is replaced.
func (s *Store) BackupConfig(ctx context.Context, path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// VACUUM INTO copies the database as one consistent file, even while a
	// run writes to it, and leaves no WAL beside the copy.
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("copy the database: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS config`, path); err != nil {
		return fmt.Errorf("open the copy: %w", err)
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `DETACH DATABASE config`)
	if _, err := conn.ExecContext(ctx, `DELETE FROM config.run_logs; DELETE FROM config.runs`); err != nil {
		return fmt.Errorf("clear the history of the copy: %w", err)
	}
	// Deleting rows leaves their pages in the file; iCloud should not carry
	// the history's size for a copy that has none of it.
	if _, err := conn.ExecContext(ctx, `VACUUM config`); err != nil {
		return fmt.Errorf("shrink the copy: %w", err)
	}
	return nil
}

func migrate(db *sql.DB, path string) error {
	var existing int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'storages'`).Scan(&existing); err != nil {
		return err
	}
	if existing == 0 {
		if _, err := db.Exec(schema); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
		_, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations)))
		return err
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("%w: its version is %d, this app knows %d", ErrNewerDatabase, version, len(migrations))
	}
	if version < len(migrations) {
		if err := backup(db, path, version); err != nil {
			return err
		}
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if err := migrations[i](tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migrate to version %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	// Tables and indexes added since are created as they are.
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	return nil
}

// backup keeps a copy of a database made by an earlier version, beside it,
// before the migrations change it: a failed upgrade leaves the person's
// storages and tasks as they were.
func backup(db *sql.DB, path string, version int) error {
	if path == ":memory:" {
		return nil
	}
	dst := fmt.Sprintf("%s.v%d.bak", path, version)
	if _, err := os.Stat(dst); err == nil {
		return nil // kept by an earlier attempt
	}
	if _, err := db.Exec(`VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("back up the database before migrating: %w", err)
	}
	_ = os.Chmod(dst, 0o600)
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

const storageColumns = `id, name, kind, provider, endpoint, region, bucket, access_key_id,
	host, port, username, checked_at, check_ok, check_problem, check_message, created_at, updated_at`

// Storages returns every storage, by name.
func (s *Store) Storages(ctx context.Context) ([]domain.Storage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+storageColumns+` FROM storages ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Storage
	for rows.Next() {
		st, err := scanStorage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// Storage returns the storage with id.
func (s *Store) Storage(ctx context.Context, id string) (domain.Storage, error) {
	st, err := scanStorage(s.db.QueryRowContext(ctx, `SELECT `+storageColumns+` FROM storages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Storage{}, domain.ErrNotFound
	}
	return st, err
}

func scanStorage(row scanner) (domain.Storage, error) {
	var st domain.Storage
	var created, updated, checked int64
	if err := row.Scan(&st.ID, &st.Name, &st.Kind, &st.Provider, &st.Endpoint, &st.Region, &st.Bucket,
		&st.AccessKeyID, &st.Host, &st.Port, &st.Username,
		&checked, &st.Check.OK, &st.Check.Problem, &st.Check.Message, &created, &updated); err != nil {
		return domain.Storage{}, err
	}
	st.CreatedAt, st.UpdatedAt, st.Check.At = fromMillis(created), fromMillis(updated), fromMillis(checked)
	return st, nil
}

// PutStorage inserts st, or replaces the storage with its ID. A storage
// with no kind is an S3 one.
func (s *Store) PutStorage(ctx context.Context, st domain.Storage) error {
	if st.Kind == "" {
		st.Kind = domain.KindS3
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO storages (id, name, kind, provider, endpoint, region, bucket, access_key_id,
			host, port, username, checked_at, check_ok, check_problem, check_message, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			name = excluded.name, kind = excluded.kind, provider = excluded.provider,
			endpoint = excluded.endpoint, region = excluded.region, bucket = excluded.bucket,
			access_key_id = excluded.access_key_id, host = excluded.host, port = excluded.port,
			username = excluded.username, checked_at = excluded.checked_at, check_ok = excluded.check_ok,
			check_problem = excluded.check_problem, check_message = excluded.check_message,
			updated_at = excluded.updated_at`,
		st.ID, st.Name, st.Kind, st.Provider, st.Endpoint, st.Region, st.Bucket, st.AccessKeyID,
		st.Host, st.Port, st.Username, millis(st.Check.At), st.Check.OK, st.Check.Problem, st.Check.Message,
		millis(st.CreatedAt), millis(st.UpdatedAt))
	return mapConstraint(err)
}

// SetStorageCheck records how the connection of the storage with id tested.
func (s *Store) SetStorageCheck(ctx context.Context, id string, c domain.Check) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE storages SET checked_at = ?, check_ok = ?, check_problem = ?, check_message = ? WHERE id = ?`,
		millis(c.At), c.OK, c.Problem, c.Message, id)
	if err != nil {
		return err
	}
	return requireOne(res)
}

// DeleteStorage deletes the storage with id, unless a task uses it.
func (s *Store) DeleteStorage(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM storages WHERE id = ?`, id)
	if err != nil {
		return mapConstraint(err)
	}
	return requireOne(res)
}

const taskColumns = `id, name, source_storage_id, source_path, dest_storage_id, dest_path, mode, schedule,
	schedule_since, trash_days, created_at, updated_at`

// Tasks returns every task, by name.
func (s *Store) Tasks(ctx context.Context) ([]domain.Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Task returns the task with id.
func (s *Store) Task(ctx context.Context, id string) (domain.Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.ErrNotFound
	}
	return t, err
}

// PutTask inserts t, or replaces the task with its ID.
func (s *Store) PutTask(ctx context.Context, t domain.Task) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks (`+taskColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			name = excluded.name, source_storage_id = excluded.source_storage_id,
			source_path = excluded.source_path, dest_storage_id = excluded.dest_storage_id,
			dest_path = excluded.dest_path, mode = excluded.mode, schedule = excluded.schedule,
			schedule_since = excluded.schedule_since, trash_days = excluded.trash_days,
			updated_at = excluded.updated_at`,
		t.ID, t.Name, nullable(t.Source.StorageID), t.Source.Path,
		nullable(t.Destination.StorageID), t.Destination.Path, t.Mode, encodeSchedule(t.Schedule),
		millis(t.ScheduleSince), t.TrashDays, millis(t.CreatedAt), millis(t.UpdatedAt))
	return mapConstraint(err)
}

// DeleteTask deletes the task with id. Its runs stay in the history.
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireOne(res)
}

// InsertRun records a run that started.
func (s *Store) InsertRun(ctx context.Context, r domain.Run) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runs (id, task_id, task_name, mode, trigger_kind, dry_run, status, message, problem, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.TaskID, r.TaskName, r.Mode, r.Trigger, r.DryRun, r.Status, r.Message, r.Problem, millis(r.StartedAt))
	return err
}

// FinishRun records the outcome of a run.
func (s *Store) FinishRun(ctx context.Context, r domain.Run) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE runs SET status = ?, message = ?, problem = ?, bytes = ?, total_bytes = ?, transfers = ?,
			total_transfers = ?, checks = ?, deletes = ?, errors = ?, trashed = ?, trash = ?,
			finished_at = ?, queued = 0
		WHERE id = ?`,
		r.Status, r.Message, r.Problem, r.Stats.Bytes, r.Stats.TotalBytes, r.Stats.Transfers,
		r.Stats.TotalTransfers, r.Stats.Checks, r.Stats.Deletes, r.Stats.Errors, r.Stats.Trashed, r.Trash,
		millis(r.FinishedAt), r.ID)
	if err != nil {
		return err
	}
	return requireOne(res)
}

// DefaultRunLimit is how many runs the history reads at most: the newest
// ones, as older ones are rarely what a person is after.
const DefaultRunLimit = 200

// RunFilter narrows the runs Runs returns. Zero fields match every run.
type RunFilter struct {
	TaskID string
	Status domain.Status
	Since  time.Time // runs started then or after; any when zero
	Limit  int       // 200 when zero
}

// Runs returns runs, the newest first.
func (s *Store) Runs(ctx context.Context, f RunFilter) ([]domain.Run, error) {
	var where []string
	var args []any
	if f.TaskID != "" {
		where, args = append(where, "task_id = ?"), append(args, f.TaskID)
	}
	if f.Status != "" {
		where, args = append(where, "status = ?"), append(args, f.Status)
	}
	if !f.Since.IsZero() {
		where, args = append(where, "started_at >= ?"), append(args, millis(f.Since))
	}
	query := `SELECT ` + runColumns + ` FROM runs`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultRunLimit
	}
	query += " ORDER BY started_at DESC, id LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanRuns(rows)
}

const runColumns = `id, task_id, task_name, mode, trigger_kind, dry_run, status, message, problem, bytes,
	total_bytes, transfers, total_transfers, checks, deletes, errors, speed, trashed, trash, started_at,
	finished_at`

func scanRun(row scanner) (domain.Run, error) {
	var r domain.Run
	var started, finished int64
	if err := row.Scan(&r.ID, &r.TaskID, &r.TaskName, &r.Mode, &r.Trigger, &r.DryRun, &r.Status,
		&r.Message, &r.Problem, &r.Stats.Bytes, &r.Stats.TotalBytes, &r.Stats.Transfers, &r.Stats.TotalTransfers,
		&r.Stats.Checks, &r.Stats.Deletes, &r.Stats.Errors, &r.Stats.Speed, &r.Stats.Trashed, &r.Trash,
		&started, &finished); err != nil {
		return domain.Run{}, err
	}
	r.StartedAt, r.FinishedAt = fromMillis(started), fromMillis(finished)
	return r, nil
}

func scanRuns(rows *sql.Rows) ([]domain.Run, error) {
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRuns deletes the runs with ids, and their logs, but for runs in
// progress, and returns how many it deleted.
func (s *Store) DeleteRuns(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	deleted := 0
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE id = ? AND status != ?`, id, domain.StatusRunning)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue // in progress, or gone already
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM run_logs WHERE run_id = ?`, id); err != nil {
			return 0, err
		}
		deleted++
	}
	return deleted, tx.Commit()
}

// PutRunLog keeps the log of the run with id.
func (s *Store) PutRunLog(ctx context.Context, id, log string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO run_logs (run_id, log) VALUES (?, ?)
		ON CONFLICT (run_id) DO UPDATE SET log = excluded.log`, id, log)
	return err
}

// PutRunDeletes keeps what the run with id, a sync the delete check
// stopped, would have deleted.
func (s *Store) PutRunDeletes(ctx context.Context, id string, d domain.StoppedDeletes) error {
	paths, err := json.Marshal(d.Paths)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO run_logs (run_id, log, deletes, deletes_total) VALUES (?, '', ?, ?)
		ON CONFLICT (run_id) DO UPDATE SET deletes = excluded.deletes, deletes_total = excluded.deletes_total`,
		id, string(paths), d.Total)
	return err
}

// RunDeletes returns what the run with id, a sync the delete check stopped,
// would have deleted: ErrNotFound for any other run.
func (s *Store) RunDeletes(ctx context.Context, id string) (domain.StoppedDeletes, error) {
	var d domain.StoppedDeletes
	var paths string
	err := s.db.QueryRowContext(ctx, `SELECT deletes, deletes_total FROM run_logs WHERE run_id = ? AND deletes != ''`, id).
		Scan(&paths, &d.Total)
	if errors.Is(err, sql.ErrNoRows) {
		return d, domain.ErrNotFound
	}
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal([]byte(paths), &d.Paths); err != nil {
		return d, fmt.Errorf("the deletes of run %s: %w", id, err)
	}
	return d, nil
}

// PruneRuns deletes the runs that started before a time, with their logs,
// but runs in progress, and returns how many it deleted.
func (s *Store) PruneRuns(ctx context.Context, before time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	const predicate = `started_at < ? AND status != ?`
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_logs WHERE run_id IN (SELECT id FROM runs WHERE `+
		predicate+`)`, millis(before), domain.StatusRunning); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE `+predicate, millis(before), domain.StatusRunning)
	if err != nil {
		return 0, err
	}
	// Logs whose run is gone, from a crash or from an older delete.
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_logs WHERE run_id NOT IN (SELECT id FROM runs)`); err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// RunLog returns the log of the run with id, or domain.ErrNotFound for a
// run that has none, as runs before logs were kept.
func (s *Store) RunLog(ctx context.Context, id string) (string, error) {
	var log string
	err := s.db.QueryRowContext(ctx, `SELECT log FROM run_logs WHERE run_id = ?`, id).Scan(&log)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	return log, err
}

// LastRuns returns the latest finished run of each task, by task ID.
func (s *Store) LastRuns(ctx context.Context) (map[string]domain.Run, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_id, status, started_at, finished_at, deletes, bytes, transfers FROM runs AS r
		WHERE status != ? AND started_at = (
			SELECT MAX(started_at) FROM runs WHERE task_id = r.task_id AND status != ?)
		ORDER BY started_at, id`,
		domain.StatusRunning, domain.StatusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]domain.Run{}
	for rows.Next() {
		var r domain.Run
		var started, finished int64
		if err := rows.Scan(&r.TaskID, &r.Status, &started, &finished,
			&r.Stats.Deletes, &r.Stats.Bytes, &r.Stats.Transfers); err != nil {
			return nil, err
		}
		r.StartedAt, r.FinishedAt = fromMillis(started), fromMillis(finished)
		// Ties in the same millisecond keep the last row, which the
		// query's order makes the same one every time.
		out[r.TaskID] = r
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanTask(row scanner) (domain.Task, error) {
	var t domain.Task
	var src, dst sql.NullString
	var schedule string
	var since, created, updated int64
	if err := row.Scan(&t.ID, &t.Name, &src, &t.Source.Path, &dst, &t.Destination.Path, &t.Mode,
		&schedule, &since, &t.TrashDays, &created, &updated); err != nil {
		return domain.Task{}, err
	}
	t.Source.StorageID, t.Destination.StorageID = src.String, dst.String
	t.ScheduleSince, t.CreatedAt, t.UpdatedAt = fromMillis(since), fromMillis(created), fromMillis(updated)
	var err error
	if t.Schedule, err = decodeSchedule(schedule); err != nil {
		// A schedule the app cannot read runs nothing, and says so, rather
		// than hiding the task.
		slog.Warn("read a task's schedule", "task", t.Name, "err", err)
	}
	return t, nil
}

func nullable(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func requireOne(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// mapConstraint turns SQLite's constraint failures into the domain's errors,
// by their result code rather than by the shape of their message.
func mapConstraint(err error) error {
	if err == nil {
		return nil
	}
	var dbErr *sqlite.Error
	if errors.As(err, &dbErr) {
		switch dbErr.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return domain.ErrNameTaken
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY, sqlite3.SQLITE_CONSTRAINT_TRIGGER:
			// SQLite reports a row still referenced through ON DELETE
			// RESTRICT as a trigger constraint when the delete runs.
			return domain.ErrStorageInUse
		}
	}
	return err
}

// settingsColumns are those a backup restores: whether the app may run in
// the background is this computer's, and stays.
const settingsColumns = `id, transfers, checkers, retries, bandwidth_mbps, notify_failed, notify_skipped,
	retention_days, backup_icloud, appearance, check_updates, install_updates, trash_days`

// RestoreConfig replaces the storages, tasks and settings with those of the
// database at path, a backup BackupConfig wrote, in one transaction; the
// history stays. A backup of an earlier version is brought up to this one
// first (path is changed), and one of a newer version is refused with
// ErrNewerDatabase.
func (s *Store) RestoreConfig(ctx context.Context, path string, validate ...func(*Store) error) error {
	if err := inspectBackup(ctx, path); err != nil {
		return err
	}
	backup, err := Open(path)
	if err != nil {
		return err
	}
	if err := validateConfig(ctx, backup); err != nil {
		backup.Close()
		return err
	}
	for _, check := range validate {
		if err := check(backup); err != nil {
			backup.Close()
			return err
		}
	}
	if err := backup.Close(); err != nil {
		return err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS restored`, path); err != nil {
		return fmt.Errorf("open the backup: %w", err)
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `DETACH DATABASE restored`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var running int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE status = ?`, domain.StatusRunning).Scan(&running); err != nil {
		return err
	}
	if running != 0 {
		return domain.ErrRunsActive
	}
	// The columns are named: a database that grew by migrations has them
	// in another order than one made from schema.sql.
	for _, q := range []string{
		`DELETE FROM main.tasks`,
		`DELETE FROM main.storages`,
		`INSERT INTO main.storages (` + storageColumns + `) SELECT ` + storageColumns + ` FROM restored.storages`,
		`INSERT INTO main.tasks (` + taskColumns + `) SELECT ` + taskColumns + ` FROM restored.tasks`,
		`CREATE TEMP TABLE kept AS SELECT background FROM main.settings`,
		`DELETE FROM main.settings`,
		`INSERT INTO main.settings (` + settingsColumns + `, background) SELECT ` + settingsColumns +
			`, coalesce((SELECT background FROM kept), 0) FROM restored.settings`,
		`DROP TABLE temp.kept`,
		`INSERT INTO main.schedule_starts(task_id, started_at) SELECT task_id, started_at FROM restored.schedule_starts WHERE true
 ON CONFLICT(task_id) DO UPDATE SET started_at = MAX(started_at, excluded.started_at)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
	}
	return tx.Commit()
}

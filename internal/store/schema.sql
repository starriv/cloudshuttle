-- The current schema, which a new database gets whole. A database made by an
-- earlier version gets the migrations in store.go instead.
CREATE TABLE IF NOT EXISTS storages (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE COLLATE NOCASE,
    kind          TEXT NOT NULL DEFAULT 's3',
    provider      TEXT NOT NULL,
    endpoint      TEXT NOT NULL,
    region        TEXT NOT NULL,
    bucket        TEXT NOT NULL,
    access_key_id TEXT NOT NULL,
    host          TEXT NOT NULL DEFAULT '',
    port          INTEGER NOT NULL DEFAULT 0,
    username      TEXT NOT NULL DEFAULT '',
    checked_at    INTEGER NOT NULL DEFAULT 0,
    check_ok      INTEGER NOT NULL DEFAULT 0,
    check_problem TEXT NOT NULL DEFAULT '',
    check_message TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE COLLATE NOCASE,
    source_storage_id   TEXT REFERENCES storages (id) ON DELETE RESTRICT,
    source_path         TEXT NOT NULL,
    dest_storage_id     TEXT REFERENCES storages (id) ON DELETE RESTRICT,
    dest_path           TEXT NOT NULL,
    mode                TEXT NOT NULL,
    allow_root          INTEGER NOT NULL DEFAULT 0,
    destination_folder  TEXT NOT NULL DEFAULT '',
    destination_pending INTEGER NOT NULL DEFAULT 0,
    -- When the task runs on its own, as JSON (store/schedule.go); '' for by
    -- hand only. schedule_since is when it was set so.
    schedule            TEXT NOT NULL,
    schedule_since      INTEGER NOT NULL DEFAULT 0,
    -- How long its runs keep what they delete or replace: 0 as the
    -- settings say, -1 not at all, else days (domain.Task.TrashDays).
    trash_days          INTEGER NOT NULL DEFAULT 0,
    -- What its runs leave out (domain.Exclude): a JSON array of globs, and
    -- the largest file in bytes, 0 for any.
    excludes            TEXT NOT NULL DEFAULT '[]',
    max_size            INTEGER NOT NULL DEFAULT 0,
    -- When the task last finished a real run that succeeded.
    last_success_at     INTEGER NOT NULL DEFAULT 0,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);

-- What the app has told about a scheduled task that has not succeeded for
-- a while, so the window and the scheduler agree and a restart does not
-- repeat it. pending marks a notice the scheduler left for the window.
CREATE TABLE IF NOT EXISTS stale_notices (
    task_id  TEXT PRIMARY KEY,
    told_at  INTEGER NOT NULL,
    pending  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS runs (
    id              TEXT PRIMARY KEY,
    task_id         TEXT NOT NULL,
    task_name       TEXT NOT NULL,
    mode            TEXT NOT NULL,
    trigger_kind    TEXT NOT NULL,
    dry_run         INTEGER NOT NULL,
    status          TEXT NOT NULL,
    message         TEXT NOT NULL,
    problem         TEXT NOT NULL DEFAULT '',
    bytes           INTEGER NOT NULL DEFAULT 0,
    total_bytes     INTEGER NOT NULL DEFAULT 0,
    transfers       INTEGER NOT NULL DEFAULT 0,
    total_transfers INTEGER NOT NULL DEFAULT 0,
    checks          INTEGER NOT NULL DEFAULT 0,
    deletes         INTEGER NOT NULL DEFAULT 0,
    errors          INTEGER NOT NULL DEFAULT 0,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER NOT NULL DEFAULT 0,
    -- The process running it: the app's window ('app') or the scheduler the
    -- system starts ('scheduler'). While it runs, the scheduler keeps its
    -- progress here for the window to show, and the window asks it to stop
    -- with cancel_requested.
    owner            TEXT NOT NULL DEFAULT '',
    queued           INTEGER NOT NULL DEFAULT 0,
    speed            REAL NOT NULL DEFAULT 0,
    cancel_requested INTEGER NOT NULL DEFAULT 0,
    -- A failure the window has yet to tell the person about.
    notify           INTEGER NOT NULL DEFAULT 0,
    -- What a sync moved into its destination's trash, and where.
    trashed          INTEGER NOT NULL DEFAULT 0,
    trash            TEXT NOT NULL DEFAULT ''
);

-- The app's own settings, one row: how hard runs work, what the app tells
-- the person, and how long it keeps its history.
CREATE TABLE IF NOT EXISTS settings (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    transfers      INTEGER NOT NULL,
    checkers       INTEGER NOT NULL,
    retries        INTEGER NOT NULL,
    bandwidth_mbps INTEGER NOT NULL,
    notify_failed  INTEGER NOT NULL,
    notify_skipped INTEGER NOT NULL,
    notify_stale   INTEGER NOT NULL DEFAULT 1,
    stale_days     INTEGER NOT NULL DEFAULT 7,
    retention_days INTEGER NOT NULL,
    backup_icloud  INTEGER NOT NULL DEFAULT 0,
    background     INTEGER NOT NULL DEFAULT 0, -- domain.Consent
    appearance     INTEGER NOT NULL DEFAULT 0, -- domain.Appearance
    app_icon       INTEGER NOT NULL DEFAULT 2, -- domain.AppIcon: the menu bar alone, as before the setting
    check_updates  INTEGER NOT NULL DEFAULT 1,
    install_updates INTEGER NOT NULL DEFAULT 0,
    trash_days     INTEGER NOT NULL DEFAULT 30,
    -- Apply the global exclusion list to every run
    -- (domain.Settings.ExcludeEnabled), and the list itself as JSON
    -- (domain.Settings.GlobalExcludes).
    exclude_enabled  INTEGER NOT NULL DEFAULT 1,
    global_excludes  TEXT NOT NULL DEFAULT '[]'
);

-- The log of each run, apart from runs so that listing them stays light;
-- for a sync the delete check stopped, what it would have deleted: how many
-- files, and the first of their paths as a JSON array.
CREATE TABLE IF NOT EXISTS run_logs (
    run_id        TEXT PRIMARY KEY,
    log           TEXT NOT NULL,
    deletes       TEXT NOT NULL DEFAULT '',
    deletes_total INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS runs_started_at ON runs (started_at DESC);
CREATE INDEX IF NOT EXISTS runs_task_started_at ON runs (task_id, started_at DESC);
CREATE INDEX IF NOT EXISTS runs_status ON runs (status);

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

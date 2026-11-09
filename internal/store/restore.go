package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/starriv/cloudshuttle/internal/domain"
)

// inspectBackup must run before Open, which is also allowed to create a new
// database. Only recognized legacy/current app schemas may be migrated.
func inspectBackup(ctx context.Context, path string) error {
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro")
	if err != nil {
		return domain.ErrNotBackup
	}
	defer db.Close()
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return domain.ErrNotBackup
	}
	if version > len(migrations) {
		return ErrNewerDatabase
	}
	tables := map[string]string{
		"storages": "id,name,provider,endpoint,region,bucket,access_key_id,created_at,updated_at",
		"tasks":    "id,name,source_storage_id,source_path,dest_storage_id,dest_path,mode,schedule,created_at,updated_at",
		"runs":     "id,task_id,task_name,mode,trigger_kind,dry_run,status,message,started_at,finished_at",
	}
	if version >= 5 {
		tables["settings"] = settingsBaseColumns
	}
	if version >= 8 {
		tables["schedule_starts"] = "task_id,started_at"
	}
	for table, columns := range tables {
		var kind string
		if err := db.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name = ?`, table).Scan(&kind); err != nil || kind != "table" {
			return domain.ErrNotBackup
		}
		rows, err := db.QueryContext(ctx, "SELECT "+columns+" FROM "+table+" LIMIT 0")
		if err != nil {
			return domain.ErrNotBackup
		}
		rows.Close()
	}
	// A backup is data, never a source of executable triggers or views. The
	// sole app trigger must match the definition shipped with this app.
	rows, err := db.QueryContext(ctx, `SELECT type, name, sql FROM sqlite_master WHERE type IN ('trigger','view')`)
	if err != nil {
		return domain.ErrNotBackup
	}
	defer rows.Close()
	for rows.Next() {
		var kind, name, definition string
		if err := rows.Scan(&kind, &name, &definition); err != nil {
			return domain.ErrNotBackup
		}
		if kind != "trigger" || name != "remember_run_start" {
			return domain.ErrNotBackup
		}
		expected := schema[strings.Index(schema, "CREATE TRIGGER IF NOT EXISTS remember_run_start"):]
		normalize := func(s string) string {
			return strings.Join(strings.Fields(strings.TrimSuffix(strings.TrimSpace(strings.ReplaceAll(s, "IF NOT EXISTS ", "")), ";")), " ")
		}
		if normalize(definition) != normalize(expected) {
			return domain.ErrNotBackup
		}
	}
	return rows.Err()
}

func validateConfig(ctx context.Context, backup *Store) error {
	storages, err := backup.Storages(ctx)
	if err != nil {
		return fmt.Errorf("%w: unreadable storages", domain.ErrNotBackup)
	}
	for _, st := range storages {
		in := st.Input()
		if st.ID == "" || in.Normalize().Validate(false) != nil {
			return domain.ErrNotBackup
		}
	}
	tasks, err := backup.Tasks(ctx)
	if err != nil {
		return domain.ErrNotBackup
	}
	for _, t := range tasks {
		if t.Exclude.Unreadable {
			return domain.ErrNotBackup
		}
		in := domain.TaskInput{Name: t.Name, Source: t.Source, Destination: t.Destination, Mode: t.Mode,
			Schedule: t.Schedule, Exclude: t.Exclude}
		if t.ID == "" || in.Validate(storages) != nil {
			return domain.ErrNotBackup
		}
	}
	settings, err := backup.Settings(ctx)
	if err != nil || settings.Validate() != nil {
		return domain.ErrNotBackup
	}
	return nil
}

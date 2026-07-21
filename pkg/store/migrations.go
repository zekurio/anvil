package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const currentSchemaVersion = 5

const currentSchema = `
CREATE TABLE schema_migrations (
	version INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL
);

CREATE TABLE library_scans (
	library_name TEXT PRIMARY KEY,
	next_token INTEGER NOT NULL DEFAULT 0,
	applied_token INTEGER NOT NULL DEFAULT 0,
	applied_at TEXT
);

CREATE TABLE media_sources (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	library_name TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('file', 'package')),
	relative_path TEXT NOT NULL,
	generation INTEGER NOT NULL CHECK (generation >= 1),
	is_current INTEGER NOT NULL DEFAULT 1 CHECK (is_current IN (0, 1)),
	status TEXT NOT NULL CHECK (status IN ('active', 'processed', 'missing')),
	size_bytes INTEGER NOT NULL DEFAULT 0,
	mod_time TEXT,
	hash_algorithm TEXT NOT NULL DEFAULT '',
	hash_value TEXT NOT NULL DEFAULT '',
	first_seen_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (library_name, relative_path, generation)
);

CREATE UNIQUE INDEX media_sources_current_idx
ON media_sources(library_name, relative_path)
WHERE is_current = 1;

CREATE INDEX media_sources_library_path_idx
ON media_sources(library_name, relative_path, generation DESC);

CREATE TABLE media_assets (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	source_id INTEGER NOT NULL REFERENCES media_sources(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	generation INTEGER NOT NULL CHECK (generation >= 1),
	is_current INTEGER NOT NULL DEFAULT 1 CHECK (is_current IN (0, 1)),
	role TEXT NOT NULL CHECK (role IN ('primary_video', 'video', 'sample', 'subtitle', 'metadata', 'extra', 'unknown')),
	status TEXT NOT NULL CHECK (status IN ('active', 'processed', 'missing')),
	size_bytes INTEGER NOT NULL DEFAULT 0,
	mod_time TEXT,
	hash_algorithm TEXT NOT NULL DEFAULT '',
	hash_value TEXT NOT NULL DEFAULT '',
	first_seen_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (source_id, relative_path, generation)
);

CREATE UNIQUE INDEX media_assets_current_idx
ON media_assets(source_id, relative_path)
WHERE is_current = 1;

CREATE INDEX media_assets_source_path_idx
ON media_assets(source_id, relative_path, generation DESC);

CREATE TABLE jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	slug TEXT NOT NULL UNIQUE,
	source_id INTEGER NOT NULL REFERENCES media_sources(id) ON DELETE CASCADE,
	asset_id INTEGER REFERENCES media_assets(id) ON DELETE CASCADE,
	library_name TEXT NOT NULL,
	priority INTEGER NOT NULL DEFAULT 0,
	state TEXT NOT NULL CHECK (state IN ('pending', 'leased', 'running', 'validating', 'replacing', 'complete', 'failed', 'retrying', 'skipped')),
	lease_owner TEXT NOT NULL DEFAULT '',
	lease_deadline TEXT,
	heartbeat_at TEXT,
	attempt_count INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	input_size_bytes INTEGER NOT NULL DEFAULT 0,
	output_size_bytes INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	completed_at TEXT,
	pipeline_context_json BLOB NOT NULL DEFAULT x''
);

CREATE UNIQUE INDEX jobs_target_idx
ON jobs(source_id, ifnull(asset_id, 0));

CREATE INDEX jobs_state_priority_idx
ON jobs(state, priority DESC, created_at ASC, id ASC);

CREATE INDEX jobs_lease_deadline_idx
ON jobs(lease_deadline)
WHERE lease_deadline IS NOT NULL;

CREATE TABLE attempts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	number INTEGER NOT NULL,
	worker_id TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('running', 'succeeded', 'failed', 'canceled')),
	resolved_library_json BLOB NOT NULL DEFAULT x'',
	resolved_flow_json BLOB NOT NULL DEFAULT x'',
	resolved_profile_json BLOB NOT NULL DEFAULT x'',
	started_at TEXT NOT NULL,
	finished_at TEXT,
	error TEXT NOT NULL DEFAULT '',
	UNIQUE (job_id, number)
);

CREATE TABLE attempt_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	attempt_id INTEGER NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
	type TEXT NOT NULL CHECK (type IN ('block_started', 'block_finished', 'block_failed', 'artifact')),
	name TEXT NOT NULL,
	message TEXT NOT NULL DEFAULT '',
	payload BLOB NOT NULL DEFAULT x'',
	created_at TEXT NOT NULL
);

CREATE INDEX attempt_events_attempt_idx
ON attempt_events(attempt_id, id);

CREATE TABLE publish_operations (
	job_id INTEGER PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
	stage TEXT NOT NULL CHECK (stage IN ('prepared', 'published', 'source_cleaned', 'committed', 'conflict')),
	operation_json BLOB NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
`

func (s *SQLiteStore) configure(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
	}
	for _, pragma := range pragmas {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("configure sqlite %q: %w", pragma, err)
		}
	}
	return nil
}

func (s *SQLiteStore) configureReadOnly(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	}
	for _, pragma := range pragmas {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("configure read-only sqlite %q: %w", pragma, err)
		}
	}
	return nil
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	compatible, exists, err := s.schemaCompatibility(ctx)
	if err != nil {
		return err
	}
	if exists {
		if !compatible {
			return ErrIncompatibleSchema
		}
		return s.verifyForeignKeys(ctx)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema bootstrap: %w", err)
	}
	defer rollback(tx)

	if _, err := tx.ExecContext(ctx, currentSchema); err != nil {
		return fmt.Errorf("bootstrap schema version %d: %w", currentSchemaVersion, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)
`, currentSchemaVersion, encodeTime(time.Now())); err != nil {
		return fmt.Errorf("record schema version %d: %w", currentSchemaVersion, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema bootstrap: %w", err)
	}
	return s.verifyForeignKeys(ctx)
}

func (s *SQLiteStore) schemaCompatibility(ctx context.Context) (compatible bool, exists bool, err error) {
	var tableCount int
	if err := s.db.QueryRowContext(ctx, `
SELECT count(*)
FROM sqlite_master
WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
`).Scan(&tableCount); err != nil {
		return false, false, fmt.Errorf("inspect sqlite schema: %w", err)
	}
	if tableCount == 0 {
		return false, false, nil
	}

	var migrationsTable int
	if err := s.db.QueryRowContext(ctx, `
SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'
`).Scan(&migrationsTable); err != nil {
		return false, true, fmt.Errorf("inspect schema version table: %w", err)
	}
	if migrationsTable == 0 {
		return false, true, nil
	}

	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return false, true, fmt.Errorf("read schema versions: %w", err)
	}
	defer closeRows(rows, &err, "close schema versions")

	var versions []string
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return false, true, fmt.Errorf("scan schema version: %w", err)
		}
		versions = append(versions, fmt.Sprint(version))
	}
	if err := rows.Err(); err != nil {
		return false, true, fmt.Errorf("iterate schema versions: %w", err)
	}
	if len(versions) != 1 || versions[0] != fmt.Sprint(currentSchemaVersion) {
		return false, true, nil
	}
	var publishOperationsTable int
	if err := s.db.QueryRowContext(ctx, `
SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'publish_operations'
`).Scan(&publishOperationsTable); err != nil {
		return false, true, fmt.Errorf("inspect publish journal table: %w", err)
	}
	return publishOperationsTable == 1, true, nil
}

func (s *SQLiteStore) verifyForeignKeys(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("verify foreign keys: %w", err)
	}
	defer closeRows(rows, &err, "close foreign key check")
	if !rows.Next() {
		return rows.Err()
	}
	var table string
	var rowID sql.NullInt64
	var parent string
	var foreignKeyID int
	if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
		return fmt.Errorf("scan foreign key violation: %w", err)
	}
	parts := []string{fmt.Sprintf("table %q", table), fmt.Sprintf("parent %q", parent), fmt.Sprintf("foreign key %d", foreignKeyID)}
	if rowID.Valid {
		parts = append(parts, fmt.Sprintf("row %d", rowID.Int64))
	}
	return fmt.Errorf("verify foreign keys: %s", strings.Join(parts, ", "))
}

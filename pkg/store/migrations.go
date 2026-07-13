package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type migration struct {
	version int
	sql     string
	after   func(context.Context, *sql.Tx) error
}

var migrations = []migration{
	{
		version: 1,
		sql: `
CREATE TABLE media_sources (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	library_name TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('file', 'package')),
	relative_path TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('active', 'missing', 'ignored')),
	size_bytes INTEGER NOT NULL DEFAULT 0,
	mod_time TEXT,
	hash_algorithm TEXT NOT NULL DEFAULT '',
	hash_value TEXT NOT NULL DEFAULT '',
	first_seen_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	updated_at TEXT NOT NULL DEFAULT '',
	UNIQUE (library_name, relative_path)
);

CREATE TABLE media_assets (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	source_id INTEGER NOT NULL REFERENCES media_sources(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	role TEXT NOT NULL CHECK (role IN ('primary_video', 'video', 'sample', 'subtitle', 'metadata', 'extra', 'unknown')),
	status TEXT NOT NULL CHECK (status IN ('active', 'processed', 'missing', 'ignored')),
	size_bytes INTEGER NOT NULL DEFAULT 0,
	mod_time TEXT,
	hash_algorithm TEXT NOT NULL DEFAULT '',
	hash_value TEXT NOT NULL DEFAULT '',
	first_seen_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	updated_at TEXT NOT NULL DEFAULT '',
	UNIQUE (source_id, relative_path)
);

CREATE TABLE jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
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
	completed_at TEXT
);

CREATE UNIQUE INDEX jobs_target_idx
ON jobs(source_id, ifnull(asset_id, 0))
;

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
`,
	},
	{
		version: 2,
		sql: `
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
`,
	},
	{
		version: 3,
		sql: `
ALTER TABLE jobs
ADD COLUMN pipeline_context_json BLOB NOT NULL DEFAULT x'';
`,
	},
	{
		version: 4,
		sql: `
ALTER TABLE jobs
ADD COLUMN slug TEXT NOT NULL DEFAULT '';
`,
		after: backfillJobSlugs,
	},
}

func (s *SQLiteStore) configure(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
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
	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL
)
`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	for _, migration := range migrations {
		applied, err := s.migrationApplied(ctx, migration.version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := s.applyMigration(ctx, migration); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) migrationApplied(ctx context.Context, version int) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `
SELECT 1 FROM schema_migrations WHERE version = ?
`, version).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check migration %d: %w", version, err)
	}
	return true, nil
}

func (s *SQLiteStore) applyMigration(ctx context.Context, migration migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", migration.version, err)
	}
	defer rollback(tx)

	if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
		return fmt.Errorf("apply migration %d: %w", migration.version, err)
	}
	if migration.after != nil {
		if err := migration.after(ctx, tx); err != nil {
			return fmt.Errorf("finalize migration %d: %w", migration.version, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)
`, migration.version, encodeTime(time.Now())); err != nil {
		return fmt.Errorf("record migration %d: %w", migration.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", migration.version, err)
	}
	return nil
}

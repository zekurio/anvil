package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const currentSchemaVersion = 9

// minUpgradableSchemaVersion is the oldest schema version schemaMigrations can
// still upgrade in place. Anything older predates the migration chain and is
// rejected by version instead of by whichever table happens to be missing.
const minUpgradableSchemaVersion = 5

// minReadOnlySchemaVersion is the oldest schema version a read-only handle can
// query. Bump it whenever a migration changes a table or column this package
// reads, so preflight fails with ErrIncompatibleSchema instead of a raw SQL
// error from deep inside a query.
//
// It is 9 because preflight reads the libraries it plans against from the
// libraries table, which version 8 added and version 9 took the kind column
// out of. A database the daemon has not migrated yet has no libraries to plan
// against; starting the daemon once migrates it and imports the libraries of
// anvil.toml.
const minReadOnlySchemaVersion = 9

const currentSchema = `
CREATE TABLE schema_migrations (
	version INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL
);

CREATE TABLE meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE flows (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
	graph_json BLOB NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE arrs (
	name TEXT PRIMARY KEY,
	type TEXT NOT NULL CHECK (type IN ('radarr', 'sonarr')),
	base_url TEXT NOT NULL,
	api_key TEXT NOT NULL DEFAULT '',
	api_key_file TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE libraries (
	name TEXT PRIMARY KEY,
	path TEXT NOT NULL,
	flow_id INTEGER NOT NULL REFERENCES flows(id) ON DELETE RESTRICT,
	arr_name TEXT REFERENCES arrs(name) ON DELETE RESTRICT,
	paused INTEGER NOT NULL DEFAULT 0 CHECK (paused IN (0, 1)),
	settings_json BLOB NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE INDEX libraries_flow_idx ON libraries(flow_id);

CREATE INDEX libraries_arr_idx ON libraries(arr_name) WHERE arr_name IS NOT NULL;

CREATE TABLE library_scans (
	library_name TEXT PRIMARY KEY,
	next_token INTEGER NOT NULL DEFAULT 0,
	applied_token INTEGER NOT NULL DEFAULT 0,
	applied_at TEXT
);

CREATE TABLE source_scans (
 library_name TEXT NOT NULL,
 relative_path TEXT NOT NULL,
 applied_token INTEGER NOT NULL,
 PRIMARY KEY (library_name, relative_path)
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
	state TEXT NOT NULL CHECK (state IN ('pending', 'leased', 'running', 'validating', 'replacing', 'complete', 'failed', 'retrying', 'skipped', 'canceled')),
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
	probe_format TEXT NOT NULL DEFAULT '',
	probe_video_codec TEXT NOT NULL DEFAULT '',
	probe_width INTEGER NOT NULL DEFAULT 0,
	probe_height INTEGER NOT NULL DEFAULT 0,
	probe_audio_codec TEXT NOT NULL DEFAULT '',
	probe_audio_channels INTEGER NOT NULL DEFAULT 0,
	pipeline_context_json BLOB NOT NULL DEFAULT x''
);

CREATE UNIQUE INDEX jobs_target_idx
ON jobs(source_id, ifnull(asset_id, 0));

CREATE INDEX jobs_state_priority_idx
ON jobs(state, priority DESC, created_at ASC, id ASC);

CREATE INDEX jobs_lease_deadline_idx
ON jobs(lease_deadline)
WHERE lease_deadline IS NOT NULL;

CREATE INDEX jobs_library_state_idx
ON jobs(library_name, state, input_size_bytes, output_size_bytes);

CREATE INDEX jobs_updated_idx
ON jobs(updated_at DESC, id DESC);

CREATE INDEX jobs_library_updated_idx
ON jobs(library_name, updated_at DESC, id DESC);

CREATE INDEX jobs_file_idx
ON jobs(source_id, asset_id, state, updated_at);

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

// coreSchemaTables must all exist before an existing database is treated as an
// Anvil schema that can be migrated forward instead of reset.
var coreSchemaTables = []string{
	"library_scans", "media_sources", "media_assets", "jobs",
	"attempts", "attempt_events", "publish_operations",
}

type schemaMigration struct {
	version int
	apply   func(context.Context, *sql.Tx) error
}

// schemaMigrations upgrades an existing database one version at a time. Each
// entry is frozen once released: it must keep working against the schema its
// predecessor produced, so it never references the evolving currentSchema.
var schemaMigrations = []schemaMigration{
	{version: 6, apply: addCanceledJobState},
	{version: 7, apply: addSourceScans},
	{version: 8, apply: upgradeToVersion8},
	{version: 9, apply: dropLibraryKind},
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	version, exists, err := s.schemaVersion(ctx)
	if err != nil {
		return err
	}
	if !exists {
		if err := s.bootstrapSchema(ctx); err != nil {
			return err
		}
		return s.verifyForeignKeys(ctx)
	}
	if version < minUpgradableSchemaVersion || version > currentSchemaVersion {
		return fmt.Errorf("%w: schema version %d is outside the upgradable range %d-%d", ErrIncompatibleSchema, version, minUpgradableSchemaVersion, currentSchemaVersion)
	}
	if err := s.requireCoreTables(ctx); err != nil {
		return err
	}
	if version < currentSchemaVersion {
		if err := s.upgradeSchema(ctx, version); err != nil {
			return err
		}
	}
	return s.verifyForeignKeys(ctx)
}

func (s *SQLiteStore) bootstrapSchema(ctx context.Context) error {
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
	return nil
}

// upgradeSchema applies every pending migration on a single reserved
// connection so the foreign-key pragma it toggles cannot leak to other work.
func (s *SQLiteStore) upgradeSchema(ctx context.Context, from int) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve schema migration connection: %w", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("release schema migration connection: %w", closeErr))
		}
	}()
	for _, migration := range schemaMigrations {
		if migration.version <= from {
			continue
		}
		if err := applySchemaMigration(ctx, conn, migration); err != nil {
			return fmt.Errorf("apply schema migration %d: %w", migration.version, err)
		}
		// A schema rebuild is an operator-visible one-off event that no caller
		// can report, so this is the one place the persistence layer logs.
		slog.Info("store schema migrated", "from_version", from, "version", migration.version)
		from = migration.version
	}
	if from != currentSchemaVersion {
		return fmt.Errorf("%w: migrations stopped at schema version %d, expected %d", ErrIncompatibleSchema, from, currentSchemaVersion)
	}
	return nil
}

func applySchemaMigration(ctx context.Context, conn *sql.Conn, migration schemaMigration) (err error) {
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	defer func() {
		if _, restoreErr := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore foreign keys: %w", restoreErr))
		}
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer rollback(tx)

	if err := migration.apply(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)
`, migration.version, encodeTime(time.Now())); err != nil {
		return fmt.Errorf("record schema version %d: %w", migration.version, err)
	}
	if err := verifyForeignKeysTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration transaction: %w", err)
	}
	return nil
}

// addCanceledJobState rebuilds the jobs table so the state CHECK constraint
// accepts operator cancellation. SQLite cannot alter a CHECK in place, so this
// follows the documented table-rebuild procedure with foreign keys disabled.
func addCanceledJobState(ctx context.Context, tx *sql.Tx) error {
	statements := []string{`
CREATE TABLE jobs_migration_6 (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	slug TEXT NOT NULL UNIQUE,
	source_id INTEGER NOT NULL REFERENCES media_sources(id) ON DELETE CASCADE,
	asset_id INTEGER REFERENCES media_assets(id) ON DELETE CASCADE,
	library_name TEXT NOT NULL,
	priority INTEGER NOT NULL DEFAULT 0,
	state TEXT NOT NULL CHECK (state IN ('pending', 'leased', 'running', 'validating', 'replacing', 'complete', 'failed', 'retrying', 'skipped', 'canceled')),
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
)`, `
INSERT INTO jobs_migration_6 (
	id, slug, source_id, asset_id, library_name, priority, state, lease_owner,
	lease_deadline, heartbeat_at, attempt_count, last_error, input_size_bytes,
	output_size_bytes, created_at, updated_at, completed_at, pipeline_context_json
)
SELECT id, slug, source_id, asset_id, library_name, priority, state, lease_owner,
	lease_deadline, heartbeat_at, attempt_count, last_error, input_size_bytes,
	output_size_bytes, created_at, updated_at, completed_at, pipeline_context_json
FROM jobs`, `
DROP TABLE jobs`, `
ALTER TABLE jobs_migration_6 RENAME TO jobs`, `
CREATE UNIQUE INDEX jobs_target_idx ON jobs(source_id, ifnull(asset_id, 0))`, `
CREATE INDEX jobs_state_priority_idx ON jobs(state, priority DESC, created_at ASC, id ASC)`, `
CREATE INDEX jobs_lease_deadline_idx ON jobs(lease_deadline) WHERE lease_deadline IS NOT NULL`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("rebuild jobs table for canceled state: %w", err)
		}
	}
	return nil
}

// schemaVersion reports the highest recorded schema version. A version of 0 on
// an existing database means the schema is not a recognizable Anvil schema.
func (s *SQLiteStore) schemaVersion(ctx context.Context) (version int, exists bool, err error) {
	var tableCount int
	if err := s.db.QueryRowContext(ctx, `
SELECT count(*)
FROM sqlite_master
WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
`).Scan(&tableCount); err != nil {
		return 0, false, fmt.Errorf("inspect sqlite schema: %w", err)
	}
	if tableCount == 0 {
		return 0, false, nil
	}

	var migrationsTable int
	if err := s.db.QueryRowContext(ctx, `
SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'
`).Scan(&migrationsTable); err != nil {
		return 0, true, fmt.Errorf("inspect schema version table: %w", err)
	}
	if migrationsTable == 0 {
		return 0, true, nil
	}

	var highest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&highest); err != nil {
		return 0, true, fmt.Errorf("read schema versions: %w", err)
	}
	if !highest.Valid {
		return 0, true, nil
	}
	return int(highest.Int64), true, nil
}

func (s *SQLiteStore) requireCoreTables(ctx context.Context) error {
	for _, table := range coreSchemaTables {
		var count int
		if err := s.db.QueryRowContext(ctx, `
SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?
`, table).Scan(&count); err != nil {
			return fmt.Errorf("inspect table %q: %w", table, err)
		}
		if count == 0 {
			return ErrIncompatibleSchema
		}
	}
	return nil
}

func (s *SQLiteStore) verifyForeignKeys(ctx context.Context) (err error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("verify foreign keys: %w", err)
	}
	defer closeRows(rows, &err, "close foreign key check")
	return foreignKeyViolation(rows)
}

func verifyForeignKeysTx(ctx context.Context, tx *sql.Tx) (err error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("verify foreign keys: %w", err)
	}
	defer closeRows(rows, &err, "close foreign key check")
	return foreignKeyViolation(rows)
}

func foreignKeyViolation(rows *sql.Rows) error {
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

func addSourceScans(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE source_scans (
 library_name TEXT NOT NULL,
 relative_path TEXT NOT NULL,
 applied_token INTEGER NOT NULL,
 PRIMARY KEY (library_name, relative_path)
);`)
	return err
}

// addFlowsLibrariesArrs moves flows, libraries, and Arr connections into the
// store. It only creates the tables: the libraries of an existing
// installation still live in anvil.toml at this point and are imported after
// the store is open (see ImportLegacyConfig), and the default flow is seeded
// by Open, so the frozen migration never depends on what the default flow
// currently looks like.
func addFlowsLibrariesArrs(ctx context.Context, tx *sql.Tx) error {
	statements := []string{`
CREATE TABLE meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
)`, `
CREATE TABLE flows (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
	graph_json BLOB NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`, `
CREATE TABLE arrs (
	name TEXT PRIMARY KEY,
	type TEXT NOT NULL CHECK (type IN ('radarr', 'sonarr')),
	base_url TEXT NOT NULL,
	api_key TEXT NOT NULL DEFAULT '',
	api_key_file TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`, `
CREATE TABLE libraries (
	name TEXT PRIMARY KEY,
	kind TEXT NOT NULL CHECK (kind IN ('media', 'download')),
	path TEXT NOT NULL,
	flow_id INTEGER NOT NULL REFERENCES flows(id) ON DELETE RESTRICT,
	arr_name TEXT REFERENCES arrs(name) ON DELETE RESTRICT,
	paused INTEGER NOT NULL DEFAULT 0 CHECK (paused IN (0, 1)),
	settings_json BLOB NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`, `
CREATE INDEX libraries_flow_idx ON libraries(flow_id)`, `
CREATE INDEX libraries_arr_idx ON libraries(arr_name) WHERE arr_name IS NOT NULL`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create flows, libraries, and arrs: %w", err)
		}
	}
	return nil
}

// upgradeToVersion8 is everything version 8 changed. The version was not
// released between its two halves, so they are one migration.
// dropLibraryKind takes the kind out of libraries. A flow's output node now
// decides how a result is published, and what a download library did
// differently at intake became settings every library has: reprocessing
// what a scan finds again, the quiet period, the package mode, and the
// residue globs. A download library keeps its values for those; its handoff
// settings are dropped, as is a media library's replacement mode.
func dropLibraryKind(ctx context.Context, tx *sql.Tx) error {
	statements := []string{`
UPDATE libraries SET settings_json = CASE kind
	WHEN 'download' THEN json_remove(json_set(settings_json,
		'$.reprocess', json('true'),
		'$.stable_for', COALESCE(json_extract(settings_json, '$.download.stable_for'), ''),
		'$.package_mode', COALESCE(json_extract(settings_json, '$.download.package_mode'), 'auto'),
		'$.ignorable_globs', COALESCE(json_extract(settings_json, '$.download.ignorable_globs'), json('[]'))),
		'$.download', '$.replacement_mode')
	ELSE json_remove(json_set(settings_json,
		'$.reprocess', json('false'),
		'$.stable_for', '',
		'$.package_mode', 'file',
		'$.ignorable_globs', json('[]')),
		'$.download', '$.replacement_mode')
END`, `
ALTER TABLE libraries DROP COLUMN kind`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("drop library kind: %w", err)
		}
	}
	return nil
}

func upgradeToVersion8(ctx context.Context, tx *sql.Tx) error {
	if err := addFlowsLibrariesArrs(ctx, tx); err != nil {
		return err
	}
	return addJobProbeFacts(ctx, tx)
}

// addJobProbeFacts gives jobs the few probe facts a file list shows as
// columns of their own, and the indexes the lists and the per-library totals
// read. A list used to take those facts out of pipeline_context_json, which
// meant reading and decoding a checkpoint of several kilobytes for every file
// of a library on every poll.
//
// The table is rebuilt rather than altered because of where SQLite puts an
// added column: at the end of the row, behind the checkpoint. A column behind
// a large value is only reached by following the value's overflow pages, so
// the facts would cost as much to read as the checkpoint they replace. The
// rebuild puts them ahead of it.
//
// Existing jobs get their facts from the checkpoint they already hold, in
// the same statement that copies them. The expressions follow the JSON of
// domain.ProbeResult as version 8 wrote it: the first video stream that is
// not an attached picture, and the first audio stream. A job without a
// readable checkpoint keeps empty facts until its next probe.
func addJobProbeFacts(ctx context.Context, tx *sql.Tx) error {
	const stream = `FROM json_each(p.doc, '$.probe.Streams') e WHERE json_extract(e.value, '$.Type') = `
	const video = stream + `'video' AND COALESCE(json_extract(e.value, '$.Disposition.attached_pic'), 0) = 0 ORDER BY e.key LIMIT 1`
	const audio = stream + `'audio' ORDER BY e.key LIMIT 1`
	statements := []string{`
CREATE TABLE jobs_migration_8 (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	slug TEXT NOT NULL UNIQUE,
	source_id INTEGER NOT NULL REFERENCES media_sources(id) ON DELETE CASCADE,
	asset_id INTEGER REFERENCES media_assets(id) ON DELETE CASCADE,
	library_name TEXT NOT NULL,
	priority INTEGER NOT NULL DEFAULT 0,
	state TEXT NOT NULL CHECK (state IN ('pending', 'leased', 'running', 'validating', 'replacing', 'complete', 'failed', 'retrying', 'skipped', 'canceled')),
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
	probe_format TEXT NOT NULL DEFAULT '',
	probe_video_codec TEXT NOT NULL DEFAULT '',
	probe_width INTEGER NOT NULL DEFAULT 0,
	probe_height INTEGER NOT NULL DEFAULT 0,
	probe_audio_codec TEXT NOT NULL DEFAULT '',
	probe_audio_channels INTEGER NOT NULL DEFAULT 0,
	pipeline_context_json BLOB NOT NULL DEFAULT x''
)`, `
INSERT INTO jobs_migration_8 (
	id, slug, source_id, asset_id, library_name, priority, state, lease_owner,
	lease_deadline, heartbeat_at, attempt_count, last_error, input_size_bytes,
	output_size_bytes, created_at, updated_at, completed_at,
	probe_format, probe_video_codec, probe_width, probe_height,
	probe_audio_codec, probe_audio_channels, pipeline_context_json
)
SELECT j.id, j.slug, j.source_id, j.asset_id, j.library_name, j.priority, j.state, j.lease_owner,
	j.lease_deadline, j.heartbeat_at, j.attempt_count, j.last_error, j.input_size_bytes,
	j.output_size_bytes, j.created_at, j.updated_at, j.completed_at,
	COALESCE(json_extract(p.doc, '$.probe.FormatName'), ''),
	COALESCE((SELECT json_extract(e.value, '$.Codec') ` + video + `), ''),
	COALESCE((SELECT json_extract(e.value, '$.Width') ` + video + `), 0),
	COALESCE((SELECT json_extract(e.value, '$.Height') ` + video + `), 0),
	COALESCE((SELECT json_extract(e.value, '$.Codec') ` + audio + `), ''),
	COALESCE((SELECT json_extract(e.value, '$.Channels') ` + audio + `), 0),
	j.pipeline_context_json
FROM jobs j
JOIN (
	SELECT id, CASE WHEN json_valid(CAST(pipeline_context_json AS TEXT))
		THEN CAST(pipeline_context_json AS TEXT) END AS doc
	FROM jobs
) p ON p.id = j.id`, `
DROP TABLE jobs`, `
ALTER TABLE jobs_migration_8 RENAME TO jobs`, `
CREATE UNIQUE INDEX jobs_target_idx ON jobs(source_id, ifnull(asset_id, 0))`, `
CREATE INDEX jobs_state_priority_idx ON jobs(state, priority DESC, created_at ASC, id ASC)`, `
CREATE INDEX jobs_lease_deadline_idx ON jobs(lease_deadline) WHERE lease_deadline IS NOT NULL`, `
CREATE INDEX jobs_library_state_idx ON jobs(library_name, state, input_size_bytes, output_size_bytes)`, `
CREATE INDEX jobs_updated_idx ON jobs(updated_at DESC, id DESC)`, `
CREATE INDEX jobs_library_updated_idx ON jobs(library_name, updated_at DESC, id DESC)`, `
CREATE INDEX jobs_file_idx ON jobs(source_id, asset_id, state, updated_at)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("rebuild jobs table with probe facts: %w", err)
		}
	}
	return nil
}

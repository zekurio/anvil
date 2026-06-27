package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("store: not found")

type SQLiteStore struct {
	db *sql.DB
}

type EnqueueJobInput struct {
	SourceID    domain.MediaSourceID
	AssetID     domain.MediaAssetID
	LibraryName domain.LibraryName
	Priority    int
	Now         time.Time
}

func Open(ctx context.Context, path string) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("store path is required")
	}
	if err := ensureParentDir(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store: %w", err)
	}
	db.SetMaxOpenConns(1)

	store := &SQLiteStore{db: db}
	if err := store.configure(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) UpsertMediaSource(ctx context.Context, source domain.MediaSource) (domain.MediaSource, error) {
	now := defaultNow(source.LastSeenAt)
	if source.FirstSeenAt.IsZero() {
		source.FirstSeenAt = now
	}
	if source.LastSeenAt.IsZero() {
		source.LastSeenAt = now
	}
	if source.Status == "" {
		source.Status = domain.MediaSourceActive
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO media_sources (
	library_name, kind, relative_path, status, size_bytes, mod_time,
	hash_algorithm, hash_value, first_seen_at, last_seen_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(library_name, relative_path) DO UPDATE SET
	kind = excluded.kind,
	status = excluded.status,
	size_bytes = excluded.size_bytes,
	mod_time = excluded.mod_time,
	hash_algorithm = excluded.hash_algorithm,
	hash_value = excluded.hash_value,
	last_seen_at = excluded.last_seen_at,
	updated_at = excluded.last_seen_at
`, string(source.LibraryName), string(source.Kind), source.RelativePath, string(source.Status),
		source.Fingerprint.SizeBytes, encodeTimePtr(source.Fingerprint.ModTime),
		source.Fingerprint.HashAlgorithm, source.Fingerprint.HashValue,
		encodeTime(source.FirstSeenAt), encodeTime(source.LastSeenAt), encodeTime(source.LastSeenAt))
	if err != nil {
		return domain.MediaSource{}, fmt.Errorf("upsert media source: %w", err)
	}

	return s.GetMediaSourceByPath(ctx, source.LibraryName, source.RelativePath)
}

func (s *SQLiteStore) GetMediaSourceByPath(ctx context.Context, libraryName domain.LibraryName, relativePath string) (domain.MediaSource, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, library_name, kind, relative_path, status, size_bytes, mod_time,
	hash_algorithm, hash_value, first_seen_at, last_seen_at
FROM media_sources
WHERE library_name = ? AND relative_path = ?
`, string(libraryName), relativePath)
	return scanMediaSource(row)
}

func (s *SQLiteStore) UpsertMediaAsset(ctx context.Context, asset domain.MediaAsset) (domain.MediaAsset, error) {
	now := defaultNow(asset.LastSeenAt)
	if asset.FirstSeenAt.IsZero() {
		asset.FirstSeenAt = now
	}
	if asset.LastSeenAt.IsZero() {
		asset.LastSeenAt = now
	}
	if asset.Status == "" {
		asset.Status = domain.MediaAssetActive
	}
	if asset.Role == "" {
		asset.Role = domain.MediaAssetRoleUnknown
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO media_assets (
	source_id, relative_path, role, status, size_bytes, mod_time,
	hash_algorithm, hash_value, first_seen_at, last_seen_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(source_id, relative_path) DO UPDATE SET
	role = excluded.role,
	status = excluded.status,
	size_bytes = excluded.size_bytes,
	mod_time = excluded.mod_time,
	hash_algorithm = excluded.hash_algorithm,
	hash_value = excluded.hash_value,
	last_seen_at = excluded.last_seen_at,
	updated_at = excluded.last_seen_at
`, int64(asset.SourceID), asset.RelativePath, string(asset.Role), string(asset.Status),
		asset.Fingerprint.SizeBytes, encodeTimePtr(asset.Fingerprint.ModTime),
		asset.Fingerprint.HashAlgorithm, asset.Fingerprint.HashValue,
		encodeTime(asset.FirstSeenAt), encodeTime(asset.LastSeenAt), encodeTime(asset.LastSeenAt))
	if err != nil {
		return domain.MediaAsset{}, fmt.Errorf("upsert media asset: %w", err)
	}

	return s.GetMediaAssetByPath(ctx, asset.SourceID, asset.RelativePath)
}

func (s *SQLiteStore) GetMediaAssetByPath(ctx context.Context, sourceID domain.MediaSourceID, relativePath string) (domain.MediaAsset, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, source_id, relative_path, role, status, size_bytes, mod_time,
	hash_algorithm, hash_value, first_seen_at, last_seen_at
FROM media_assets
WHERE source_id = ? AND relative_path = ?
`, int64(sourceID), relativePath)
	return scanMediaAsset(row)
}

func (s *SQLiteStore) EnqueueJob(ctx context.Context, input EnqueueJobInput) (domain.Job, bool, error) {
	now := defaultNow(input.Now)
	assetID := nullableAssetID(input.AssetID)

	result, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO jobs (
	source_id, asset_id, library_name, priority, state, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
`, int64(input.SourceID), assetID, string(input.LibraryName), input.Priority,
		string(domain.JobStatePending), encodeTime(now), encodeTime(now))
	if err != nil {
		return domain.Job{}, false, fmt.Errorf("enqueue job: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return domain.Job{}, false, fmt.Errorf("enqueue job rows affected: %w", err)
	}

	job, err := s.GetJobForTarget(ctx, input.SourceID, input.AssetID)
	return job, rows > 0, err
}

func (s *SQLiteStore) GetJobForTarget(ctx context.Context, sourceID domain.MediaSourceID, assetID domain.MediaAssetID) (domain.Job, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, source_id, asset_id, library_name, priority, state, lease_owner,
	lease_deadline, heartbeat_at, attempt_count, last_error, created_at,
	updated_at, completed_at
FROM jobs
WHERE source_id = ?
	AND ifnull(asset_id, 0) = ?
ORDER BY id DESC
LIMIT 1
`, int64(sourceID), int64(assetID))
	return scanJob(row)
}

func (s *SQLiteStore) GetActiveJobForTarget(ctx context.Context, sourceID domain.MediaSourceID, assetID domain.MediaAssetID) (domain.Job, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, source_id, asset_id, library_name, priority, state, lease_owner,
	lease_deadline, heartbeat_at, attempt_count, last_error, created_at,
	updated_at, completed_at
FROM jobs
WHERE source_id = ?
	AND ifnull(asset_id, 0) = ?
	AND state IN (?, ?, ?, ?, ?, ?)
ORDER BY id DESC
LIMIT 1
`, int64(sourceID), int64(assetID),
		string(domain.JobStatePending), string(domain.JobStateLeased), string(domain.JobStateRunning),
		string(domain.JobStateValidating), string(domain.JobStateReplacing), string(domain.JobStateRetrying))
	return scanJob(row)
}

func (s *SQLiteStore) LeaseNextJob(ctx context.Context, workerID string, leaseDeadline time.Time, now time.Time) (*domain.Job, error) {
	if strings.TrimSpace(workerID) == "" {
		return nil, errors.New("worker id is required")
	}
	now = defaultNow(now)
	if leaseDeadline.IsZero() {
		return nil, errors.New("lease deadline is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin lease transaction: %w", err)
	}
	defer rollback(tx)

	var id int64
	err = tx.QueryRowContext(ctx, `
SELECT id
FROM jobs
WHERE state = ?
ORDER BY priority DESC, created_at ASC, id ASC
LIMIT 1
`, string(domain.JobStatePending)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty lease transaction: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select next job: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, lease_owner = ?, lease_deadline = ?, heartbeat_at = ?, updated_at = ?
WHERE id = ? AND state = ?
`, string(domain.JobStateLeased), workerID, encodeTime(leaseDeadline), encodeTime(now), encodeTime(now),
		id, string(domain.JobStatePending))
	if err != nil {
		return nil, fmt.Errorf("lease job: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("lease rows affected: %w", err)
	}
	if rows == 0 {
		return nil, ErrNotFound
	}

	job, err := getJobTx(ctx, tx, domain.JobID(id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit lease transaction: %w", err)
	}

	return &job, nil
}

func (s *SQLiteStore) HeartbeatJob(ctx context.Context, jobID domain.JobID, workerID string, leaseDeadline time.Time, now time.Time) (domain.Job, error) {
	now = defaultNow(now)
	result, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET lease_deadline = ?, heartbeat_at = ?, updated_at = ?
WHERE id = ?
	AND lease_owner = ?
	AND state IN (?, ?, ?, ?)
`, encodeTime(leaseDeadline), encodeTime(now), encodeTime(now), int64(jobID), workerID,
		string(domain.JobStateLeased), string(domain.JobStateRunning),
		string(domain.JobStateValidating), string(domain.JobStateReplacing))
	if err != nil {
		return domain.Job{}, fmt.Errorf("heartbeat job: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.Job{}, fmt.Errorf("heartbeat rows affected: %w", err)
	}
	if rows == 0 {
		return domain.Job{}, ErrNotFound
	}
	return s.GetJob(ctx, jobID)
}

func (s *SQLiteStore) TransitionJob(ctx context.Context, jobID domain.JobID, to domain.JobState, now time.Time, lastError string) (domain.Job, error) {
	now = defaultNow(now)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Job{}, fmt.Errorf("begin transition transaction: %w", err)
	}
	defer rollback(tx)

	job, err := getJobTx(ctx, tx, jobID)
	if err != nil {
		return domain.Job{}, err
	}
	if !domain.CanTransitionJob(job.State, to) {
		return domain.Job{}, fmt.Errorf("invalid job transition %q -> %q", job.State, to)
	}

	leaseOwner := job.LeaseOwner
	var leaseDeadline any
	var heartbeatAt any
	completedAt := nullableTimePtr(job.CompletedAt)
	if to.Terminal() || to == domain.JobStatePending {
		leaseOwner = ""
		leaseDeadline = nil
		heartbeatAt = nil
	} else {
		leaseDeadline = nullableTimePtr(job.LeaseDeadline)
		heartbeatAt = nullableTimePtr(job.HeartbeatAt)
	}
	if to.Terminal() && job.CompletedAt == nil {
		completedAt = encodeTime(now)
	}

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, lease_owner = ?, lease_deadline = ?, heartbeat_at = ?,
	last_error = ?, updated_at = ?, completed_at = ?
WHERE id = ?
`, string(to), leaseOwner, leaseDeadline, heartbeatAt, lastError, encodeTime(now), completedAt, int64(jobID))
	if err != nil {
		return domain.Job{}, fmt.Errorf("transition job: %w", err)
	}

	job, err = getJobTx(ctx, tx, jobID)
	if err != nil {
		return domain.Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Job{}, fmt.Errorf("commit transition transaction: %w", err)
	}

	return job, nil
}

func (s *SQLiteStore) StartAttempt(ctx context.Context, jobID domain.JobID, workerID string, resolvedLibrary, resolvedFlow, resolvedProfile []byte, now time.Time) (domain.Attempt, error) {
	now = defaultNow(now)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Attempt{}, fmt.Errorf("begin attempt transaction: %w", err)
	}
	defer rollback(tx)

	job, err := getJobTx(ctx, tx, jobID)
	if err != nil {
		return domain.Attempt{}, err
	}
	if strings.TrimSpace(workerID) == "" {
		return domain.Attempt{}, errors.New("worker id is required")
	}
	if job.LeaseOwner != workerID {
		return domain.Attempt{}, ErrNotFound
	}
	if job.LeaseDeadline == nil {
		return domain.Attempt{}, ErrNotFound
	}
	if job.LeaseDeadline.Before(now) {
		return domain.Attempt{}, fmt.Errorf("job lease expired at %s", job.LeaseDeadline.Format(time.RFC3339Nano))
	}
	if !domain.CanTransitionJob(job.State, domain.JobStateRunning) {
		return domain.Attempt{}, fmt.Errorf("cannot start attempt from job state %q", job.State)
	}

	number := job.AttemptCount + 1
	result, err := tx.ExecContext(ctx, `
INSERT INTO attempts (
	job_id, number, worker_id, state, resolved_library_json, resolved_flow_json,
	resolved_profile_json, started_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, int64(jobID), number, workerID, string(domain.AttemptStateRunning),
		emptyBytesIfNil(resolvedLibrary), emptyBytesIfNil(resolvedFlow), emptyBytesIfNil(resolvedProfile), encodeTime(now))
	if err != nil {
		return domain.Attempt{}, fmt.Errorf("start attempt: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return domain.Attempt{}, fmt.Errorf("attempt id: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, attempt_count = ?, updated_at = ?
WHERE id = ?
`, string(domain.JobStateRunning), number, encodeTime(now), int64(jobID))
	if err != nil {
		return domain.Attempt{}, fmt.Errorf("mark job running: %w", err)
	}

	attempt, err := getAttemptTx(ctx, tx, domain.AttemptID(id))
	if err != nil {
		return domain.Attempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Attempt{}, fmt.Errorf("commit attempt transaction: %w", err)
	}

	return attempt, nil
}

func (s *SQLiteStore) FinishAttempt(ctx context.Context, attemptID domain.AttemptID, state domain.AttemptState, message string, finishedAt time.Time) (domain.Attempt, error) {
	finishedAt = defaultNow(finishedAt)
	result, err := s.db.ExecContext(ctx, `
UPDATE attempts
SET state = ?, finished_at = ?, error = ?
WHERE id = ? AND state = ?
`, string(state), encodeTime(finishedAt), message, int64(attemptID), string(domain.AttemptStateRunning))
	if err != nil {
		return domain.Attempt{}, fmt.Errorf("finish attempt: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.Attempt{}, fmt.Errorf("finish attempt rows affected: %w", err)
	}
	if rows == 0 {
		return domain.Attempt{}, ErrNotFound
	}
	return s.GetAttempt(ctx, attemptID)
}

func (s *SQLiteStore) RecoverStaleJobs(ctx context.Context, maxAttempts int, now time.Time) (int64, error) {
	now = defaultNow(now)
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin recovery transaction: %w", err)
	}
	defer rollback(tx)

	staleWhere := `
state IN (?, ?, ?, ?)
	AND lease_deadline IS NOT NULL
	AND lease_deadline < ?
`
	args := []any{
		string(domain.JobStateLeased), string(domain.JobStateRunning),
		string(domain.JobStateValidating), string(domain.JobStateReplacing),
		encodeTime(now),
	}

	_, err = tx.ExecContext(ctx, `
UPDATE attempts
SET state = ?, finished_at = ?, error = ?
WHERE state = ?
	AND job_id IN (
		SELECT id FROM jobs WHERE `+staleWhere+`
	)
`, append([]any{
		string(domain.AttemptStateCanceled),
		encodeTime(now),
		"job lease expired during daemon recovery",
		string(domain.AttemptStateRunning),
	}, args...)...)
	if err != nil {
		return 0, fmt.Errorf("cancel stale attempts: %w", err)
	}

	failed, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, lease_owner = '', lease_deadline = NULL, heartbeat_at = NULL,
	last_error = ?, updated_at = ?, completed_at = ?
WHERE `+staleWhere+`
	AND attempt_count >= ?
`, append([]any{
		string(domain.JobStateFailed),
		"lease expired and retry limit was reached",
		encodeTime(now),
		encodeTime(now),
	}, append(args, maxAttempts)...)...)
	if err != nil {
		return 0, fmt.Errorf("fail stale jobs: %w", err)
	}

	pending, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, lease_owner = '', lease_deadline = NULL, heartbeat_at = NULL,
	last_error = ?, updated_at = ?, completed_at = NULL
WHERE `+staleWhere+`
	AND attempt_count < ?
`, append([]any{
		string(domain.JobStatePending),
		"lease expired; job returned to pending",
		encodeTime(now),
	}, append(args, maxAttempts)...)...)
	if err != nil {
		return 0, fmt.Errorf("requeue stale jobs: %w", err)
	}

	failedRows, err := failed.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed recovery rows affected: %w", err)
	}
	pendingRows, err := pending.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("pending recovery rows affected: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit recovery transaction: %w", err)
	}

	return failedRows + pendingRows, nil
}

func (s *SQLiteStore) GetJob(ctx context.Context, id domain.JobID) (domain.Job, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, source_id, asset_id, library_name, priority, state, lease_owner,
	lease_deadline, heartbeat_at, attempt_count, last_error, created_at,
	updated_at, completed_at
FROM jobs
WHERE id = ?
`, int64(id))
	return scanJob(row)
}

func (s *SQLiteStore) GetAttempt(ctx context.Context, id domain.AttemptID) (domain.Attempt, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, job_id, number, worker_id, state, resolved_library_json,
	resolved_flow_json, resolved_profile_json, started_at, finished_at, error
FROM attempts
WHERE id = ?
`, int64(id))
	return scanAttempt(row)
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

func getJobTx(ctx context.Context, tx *sql.Tx, id domain.JobID) (domain.Job, error) {
	row := tx.QueryRowContext(ctx, `
SELECT id, source_id, asset_id, library_name, priority, state, lease_owner,
	lease_deadline, heartbeat_at, attempt_count, last_error, created_at,
	updated_at, completed_at
FROM jobs
WHERE id = ?
`, int64(id))
	return scanJob(row)
}

func getAttemptTx(ctx context.Context, tx *sql.Tx, id domain.AttemptID) (domain.Attempt, error) {
	row := tx.QueryRowContext(ctx, `
SELECT id, job_id, number, worker_id, state, resolved_library_json,
	resolved_flow_json, resolved_profile_json, started_at, finished_at, error
FROM attempts
WHERE id = ?
`, int64(id))
	return scanAttempt(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMediaSource(row scanner) (domain.MediaSource, error) {
	var source domain.MediaSource
	var modTime sql.NullString
	var firstSeenAt string
	var lastSeenAt string
	err := row.Scan(
		&source.ID,
		&source.LibraryName,
		&source.Kind,
		&source.RelativePath,
		&source.Status,
		&source.Fingerprint.SizeBytes,
		&modTime,
		&source.Fingerprint.HashAlgorithm,
		&source.Fingerprint.HashValue,
		&firstSeenAt,
		&lastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MediaSource{}, ErrNotFound
	}
	if err != nil {
		return domain.MediaSource{}, err
	}
	source.Fingerprint.ModTime = parseNullTime(modTime)
	source.FirstSeenAt = parseTime(firstSeenAt)
	source.LastSeenAt = parseTime(lastSeenAt)
	return source, nil
}

func scanMediaAsset(row scanner) (domain.MediaAsset, error) {
	var asset domain.MediaAsset
	var modTime sql.NullString
	var firstSeenAt string
	var lastSeenAt string
	err := row.Scan(
		&asset.ID,
		&asset.SourceID,
		&asset.RelativePath,
		&asset.Role,
		&asset.Status,
		&asset.Fingerprint.SizeBytes,
		&modTime,
		&asset.Fingerprint.HashAlgorithm,
		&asset.Fingerprint.HashValue,
		&firstSeenAt,
		&lastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MediaAsset{}, ErrNotFound
	}
	if err != nil {
		return domain.MediaAsset{}, err
	}
	asset.Fingerprint.ModTime = parseNullTime(modTime)
	asset.FirstSeenAt = parseTime(firstSeenAt)
	asset.LastSeenAt = parseTime(lastSeenAt)
	return asset, nil
}

func scanJob(row scanner) (domain.Job, error) {
	var job domain.Job
	var assetID sql.NullInt64
	var leaseDeadline sql.NullString
	var heartbeatAt sql.NullString
	var completedAt sql.NullString
	var createdAt string
	var updatedAt string
	err := row.Scan(
		&job.ID,
		&job.SourceID,
		&assetID,
		&job.LibraryName,
		&job.Priority,
		&job.State,
		&job.LeaseOwner,
		&leaseDeadline,
		&heartbeatAt,
		&job.AttemptCount,
		&job.LastError,
		&createdAt,
		&updatedAt,
		&completedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, ErrNotFound
	}
	if err != nil {
		return domain.Job{}, err
	}
	if assetID.Valid {
		job.AssetID = domain.MediaAssetID(assetID.Int64)
	}
	job.LeaseDeadline = parseNullTimePtr(leaseDeadline)
	job.HeartbeatAt = parseNullTimePtr(heartbeatAt)
	job.CreatedAt = parseTime(createdAt)
	job.UpdatedAt = parseTime(updatedAt)
	job.CompletedAt = parseNullTimePtr(completedAt)
	return job, nil
}

func scanAttempt(row scanner) (domain.Attempt, error) {
	var attempt domain.Attempt
	var finishedAt sql.NullString
	var startedAt string
	err := row.Scan(
		&attempt.ID,
		&attempt.JobID,
		&attempt.Number,
		&attempt.WorkerID,
		&attempt.State,
		&attempt.ResolvedLibrary,
		&attempt.ResolvedFlow,
		&attempt.ResolvedProfile,
		&startedAt,
		&finishedAt,
		&attempt.Error,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Attempt{}, ErrNotFound
	}
	if err != nil {
		return domain.Attempt{}, err
	}
	attempt.StartedAt = parseTime(startedAt)
	attempt.FinishedAt = parseNullTimePtr(finishedAt)
	return attempt, nil
}

func encodeTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func encodeTimePtr(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return encodeTime(t)
}

func nullableTimePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return encodeTime(*t)
}

func nullableAssetID(id domain.MediaAssetID) any {
	if id == 0 {
		return nil
	}
	return int64(id)
}

func emptyBytesIfNil(value []byte) []byte {
	if value == nil {
		return []byte{}
	}
	return value
}

func parseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseNullTime(value sql.NullString) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return parseTime(value.String)
}

func parseNullTimePtr(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	t := parseTime(value.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

func defaultNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}

func ensureParentDir(path string) error {
	if path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create store directory %q: %w", dir, err)
	}
	return nil
}

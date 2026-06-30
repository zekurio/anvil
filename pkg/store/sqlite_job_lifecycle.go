package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

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

func (s *SQLiteStore) RetryJob(ctx context.Context, id domain.JobID, now time.Time) (domain.Job, error) {
	now = defaultNow(now)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Job{}, fmt.Errorf("begin retry transaction: %w", err)
	}
	defer rollback(tx)

	job, err := getJobTx(ctx, tx, id)
	if err != nil {
		return domain.Job{}, err
	}
	switch job.State {
	case domain.JobStateFailed, domain.JobStateSkipped, domain.JobStateRetrying:
	default:
		return domain.Job{}, fmt.Errorf("cannot retry job %d from state %q", id, job.State)
	}

	_, err = tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, lease_owner = '', lease_deadline = NULL, heartbeat_at = NULL,
	last_error = '', updated_at = ?, completed_at = NULL
WHERE id = ?
`, string(domain.JobStatePending), encodeTime(now), int64(id))
	if err != nil {
		return domain.Job{}, fmt.Errorf("retry job: %w", err)
	}

	job, err = getJobTx(ctx, tx, id)
	if err != nil {
		return domain.Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Job{}, fmt.Errorf("commit retry transaction: %w", err)
	}
	return job, nil
}

func (s *SQLiteStore) RecordJobFileSizes(ctx context.Context, jobID domain.JobID, inputSizeBytes int64, outputSizeBytes int64, now time.Time) (domain.Job, error) {
	now = defaultNow(now)
	if inputSizeBytes < 0 {
		inputSizeBytes = 0
	}
	if outputSizeBytes < 0 {
		outputSizeBytes = 0
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET input_size_bytes = ?, output_size_bytes = ?, updated_at = ?
WHERE id = ?
`, inputSizeBytes, outputSizeBytes, encodeTime(now), int64(jobID))
	if err != nil {
		return domain.Job{}, fmt.Errorf("record job file sizes: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.Job{}, fmt.Errorf("record job file sizes rows affected: %w", err)
	}
	if rows == 0 {
		return domain.Job{}, ErrNotFound
	}
	return s.GetJob(ctx, jobID)
}

func (s *SQLiteStore) RetryFailedJobs(ctx context.Context, libraryName domain.LibraryName, now time.Time) (int64, error) {
	now = defaultNow(now)
	query := `
UPDATE jobs
SET state = ?, lease_owner = '', lease_deadline = NULL, heartbeat_at = NULL,
	last_error = '', updated_at = ?, completed_at = NULL
WHERE state = ?
`
	args := []any{string(domain.JobStatePending), encodeTime(now), string(domain.JobStateFailed)}
	if libraryName != "" {
		query += " AND library_name = ?"
		args = append(args, string(libraryName))
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("retry failed jobs: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("retry failed rows affected: %w", err)
	}
	return rows, nil
}

func (s *SQLiteStore) LeaseNextJob(ctx context.Context, workerID string, leaseDeadline time.Time, now time.Time) (*domain.Job, error) {
	return s.LeaseNextJobForLibraries(ctx, workerID, leaseDeadline, now, nil)
}

func (s *SQLiteStore) LeaseNextJobForLibraries(ctx context.Context, workerID string, leaseDeadline time.Time, now time.Time, allowedLibraries []domain.LibraryName) (*domain.Job, error) {
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
	query := `
SELECT id
FROM jobs
WHERE state = ?
`
	args := []any{string(domain.JobStatePending)}
	if len(allowedLibraries) > 0 {
		query += " AND library_name IN (" + placeholders(len(allowedLibraries)) + ")\n"
		for _, library := range allowedLibraries {
			args = append(args, string(library))
		}
	}
	query += `
ORDER BY priority DESC, created_at ASC, id ASC
LIMIT 1
`
	err = tx.QueryRowContext(ctx, query, args...).Scan(&id)
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

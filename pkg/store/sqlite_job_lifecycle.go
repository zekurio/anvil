package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
)

const activeOccurrenceTargetSQL = `EXISTS (
	SELECT 1
	FROM media_sources AS s
	LEFT JOIN media_assets AS a ON a.id = jobs.asset_id
	WHERE s.id = jobs.source_id
		AND s.is_current = 1 AND s.status = 'active'
		AND (jobs.asset_id IS NULL OR (
			a.source_id = s.id AND a.is_current = 1 AND a.status = 'active'
		))
)`

const inactiveOccurrenceJobError = "input occurrence is no longer current and active"

const defaultCancelReason = "canceled by operator"

func (s *SQLiteStore) EnqueueJob(ctx context.Context, input EnqueueJobInput) (domain.Job, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Job{}, false, fmt.Errorf("begin enqueue transaction: %w", err)
	}
	defer rollback(tx)
	job, inserted, err := enqueueJobTx(ctx, tx, input)
	if err != nil {
		return domain.Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Job{}, false, fmt.Errorf("commit enqueue transaction: %w", err)
	}
	return job, inserted, nil
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
	case domain.JobStateFailed, domain.JobStateSkipped, domain.JobStateCanceled, domain.JobStateRetrying:
	default:
		return domain.Job{}, fmt.Errorf("cannot retry job %d from state %q", id, job.State)
	}
	active, err := jobTargetActiveTx(ctx, tx, id)
	if err != nil {
		return domain.Job{}, err
	}
	if !active {
		return domain.Job{}, fmt.Errorf("cannot retry job %d: %s", id, inactiveOccurrenceJobError)
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

// CancelJobs terminally cancels every requested job that is still cancelable.
// It is idempotent and never fails the batch for one job: an already terminal
// job, a job whose state no longer matches the requested filter, a job that
// disappeared, and a job with a journaled publish are all reported with
// Canceled=false and a SkipReason. Refusing a journaled publish is the
// data-safety rule that matters most: canceling clears the lease, so a job
// terminated between preparing and committing a publish could never be
// re-leased, recovered, or rescanned, and its destination file, backup, and
// journal row would be stranded. Attempts still marked running are canceled in
// the same transaction, because a canceled job clears its lease and would
// otherwise never be visited by stale-job recovery.
func (s *SQLiteStore) CancelJobs(ctx context.Context, input CancelJobsInput) ([]CancelJobResult, error) {
	now := defaultNow(input.Now)
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = defaultCancelReason
	}
	if len(input.IDs) == 0 {
		return nil, errors.New("at least one job id is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin cancel transaction: %w", err)
	}
	defer rollback(tx)

	seen := make(map[domain.JobID]struct{}, len(input.IDs))
	results := make([]CancelJobResult, 0, len(input.IDs))
	for _, id := range input.IDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		job, err := getJobTx(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			results = append(results, CancelJobResult{JobID: id, SkipReason: CancelSkipMissing})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load job %d for cancel: %w", id, err)
		}
		result := CancelJobResult{
			JobID: job.ID, Slug: job.Slug, LibraryName: job.LibraryName,
			PreviousState: job.State, State: job.State,
		}
		skip, err := cancelSkipReasonTx(ctx, tx, job, input.States)
		if err != nil {
			return nil, err
		}
		if skip != "" {
			result.SkipReason = skip
			results = append(results, result)
			continue
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE attempts
SET state = ?, finished_at = ?, error = ?
WHERE job_id = ? AND state = ?
`, string(domain.AttemptStateCanceled), encodeTime(now), reason, int64(job.ID), string(domain.AttemptStateRunning)); err != nil {
			return nil, fmt.Errorf("cancel attempts for job %d: %w", job.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, lease_owner = '', lease_deadline = NULL, heartbeat_at = NULL,
	last_error = ?, updated_at = ?, completed_at = ?
WHERE id = ? AND state = ?
`, string(domain.JobStateCanceled), reason, encodeTime(now), encodeTime(now), int64(job.ID), string(job.State)); err != nil {
			return nil, fmt.Errorf("cancel job %d: %w", job.ID, err)
		}
		result.State = domain.JobStateCanceled
		result.Canceled = true
		results = append(results, result)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit cancel transaction: %w", err)
	}
	return results, nil
}

// cancelSkipReasonTx reports why a job must not be canceled, or an empty reason
// when canceling it is safe.
func cancelSkipReasonTx(ctx context.Context, tx *sql.Tx, job domain.Job, states []domain.JobState) (CancelSkipReason, error) {
	if !job.State.Cancelable() {
		return CancelSkipAlreadyTerminal, nil
	}
	if len(states) > 0 && !slices.Contains(states, job.State) {
		return CancelSkipStateChanged, nil
	}
	var stage string
	err := tx.QueryRowContext(ctx, `
SELECT stage FROM publish_operations WHERE job_id = ?
`, int64(job.ID)).Scan(&stage)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("check publish operation for job %d: %w", job.ID, err)
	}
	// A conflicted publish has already stopped making progress and needs an
	// operator, so refusing the cancel would only trap the job. Note the
	// destination may already be published and the original may already be a
	// .anvil-backup: a conflict can be raised from the published stage during
	// cleanup. Canceling leaves that residue in place, exactly as the conflict
	// did; `anvild retry` re-queues the job and re-runs publish recovery.
	//
	// Every other stage means the destination is being written or has been
	// written and the job still owes its completion bookkeeping, so only the
	// job itself can finish it.
	if stage == string(replacepkg.PublishStageConflict) {
		return "", nil
	}
	return CancelSkipPublishInFlight, nil
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

func (s *SQLiteStore) CompleteJobOccurrence(ctx context.Context, input CompleteJobOccurrenceInput) (domain.Job, error) {
	now := defaultNow(input.CompletedAt)
	if input.InputSizeBytes < 0 {
		input.InputSizeBytes = 0
	}
	if input.OutputSizeBytes < 0 {
		input.OutputSizeBytes = 0
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Job{}, fmt.Errorf("begin occurrence completion: %w", err)
	}
	defer rollback(tx)

	job, err := getJobTx(ctx, tx, input.JobID)
	if err != nil {
		return domain.Job{}, err
	}
	if !domain.CanTransitionJob(job.State, domain.JobStateComplete) {
		return domain.Job{}, fmt.Errorf("cannot complete occurrence job from state %q", job.State)
	}
	source, err := getMediaSourceTx(ctx, tx, job.SourceID)
	if err != nil {
		return domain.Job{}, err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE attempts
SET state = ?, finished_at = ?, error = ''
WHERE id = ? AND job_id = ? AND state = ?
`, string(domain.AttemptStateSucceeded), encodeTime(now), int64(input.AttemptID), int64(input.JobID), string(domain.AttemptStateRunning))
	if err != nil {
		return domain.Job{}, fmt.Errorf("finish successful occurrence attempt: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.Job{}, fmt.Errorf("finish occurrence attempt rows affected: %w", err)
	}
	if rows == 0 {
		return domain.Job{}, ErrNotFound
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, lease_owner = '', lease_deadline = NULL, heartbeat_at = NULL,
	last_error = '', input_size_bytes = ?, output_size_bytes = ?, updated_at = ?, completed_at = ?
WHERE id = ?
`, string(domain.JobStateComplete), input.InputSizeBytes, input.OutputSizeBytes, encodeTime(now), encodeTime(now), int64(input.JobID)); err != nil {
		return domain.Job{}, fmt.Errorf("complete occurrence job: %w", err)
	}
	if input.FinalInputFingerprint != nil {
		if job.AssetID != 0 {
			if err := updateAssetFingerprintTx(ctx, tx, job.AssetID, *input.FinalInputFingerprint, now); err != nil {
				return domain.Job{}, err
			}
		}
		if source.Kind == domain.SourceKindFile {
			if err := updateSourceFingerprintTx(ctx, tx, job.SourceID, *input.FinalInputFingerprint, now); err != nil {
				return domain.Job{}, err
			}
		}
	}
	if input.SourceMediaRemoved {
		if source.Kind == domain.SourceKindFile {
			if err := retireSourceTx(ctx, tx, job.SourceID, now); err != nil {
				return domain.Job{}, err
			}
		} else {
			if job.AssetID != 0 {
				if err := retireAssetTx(ctx, tx, job.AssetID, now); err != nil {
					return domain.Job{}, err
				}
			}
			if err := refreshSourceLifecycleTx(ctx, tx, job.SourceID, now); err != nil {
				return domain.Job{}, err
			}
		}
	} else {
		if job.AssetID != 0 {
			if _, err := tx.ExecContext(ctx, `
UPDATE media_assets SET status = ?, updated_at = ? WHERE id = ? AND is_current = 1
`, string(domain.MediaAssetProcessed), encodeTime(now), int64(job.AssetID)); err != nil {
				return domain.Job{}, fmt.Errorf("mark asset occurrence processed: %w", err)
			}
		}
		if err := refreshSourceLifecycleTx(ctx, tx, job.SourceID, now); err != nil {
			return domain.Job{}, err
		}
	}
	job, err = getJobTx(ctx, tx, input.JobID)
	if err != nil {
		return domain.Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Job{}, fmt.Errorf("commit occurrence completion: %w", err)
	}
	return job, nil
}

func (s *SQLiteStore) RetryFailedJobs(ctx context.Context, libraryName domain.LibraryName, now time.Time) (int64, error) {
	now = defaultNow(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin bulk retry transaction: %w", err)
	}
	defer rollback(tx)

	query := `
UPDATE jobs
SET state = ?, lease_owner = '', lease_deadline = NULL, heartbeat_at = NULL,
	last_error = '', updated_at = ?, completed_at = NULL
WHERE state = ?
	AND ` + activeOccurrenceTargetSQL + `
`
	args := []any{string(domain.JobStatePending), encodeTime(now), string(domain.JobStateFailed)}
	if libraryName != "" {
		query += " AND library_name = ?"
		args = append(args, string(libraryName))
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("retry failed jobs: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("retry failed rows affected: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit bulk retry transaction: %w", err)
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
SELECT jobs.id
FROM jobs
WHERE jobs.state = ?
	AND ` + activeOccurrenceTargetSQL + `
`
	args := []any{string(domain.JobStatePending)}
	if len(allowedLibraries) > 0 {
		query += " AND jobs.library_name IN (" + placeholders(len(allowedLibraries)) + ")\n"
		for _, library := range allowedLibraries {
			args = append(args, string(library))
		}
	}
	query += `
ORDER BY jobs.priority DESC, jobs.created_at ASC, jobs.id ASC
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

func (s *SQLiteStore) ReleaseLeasedJob(ctx context.Context, jobID domain.JobID, workerID string, now time.Time) (domain.Job, error) {
	now = defaultNow(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Job{}, fmt.Errorf("begin release transaction: %w", err)
	}
	defer rollback(tx)

	job, err := getJobTx(ctx, tx, jobID)
	if err != nil {
		return domain.Job{}, err
	}
	if job.State != domain.JobStateLeased || job.LeaseOwner != workerID {
		return domain.Job{}, ErrNotFound
	}
	active, err := jobTargetActiveTx(ctx, tx, jobID)
	if err != nil {
		return domain.Job{}, err
	}
	state := domain.JobStatePending
	lastError := ""
	var completedAt any
	if !active {
		state = domain.JobStateSkipped
		lastError = inactiveOccurrenceJobError
		completedAt = encodeTime(now)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, lease_owner = '', lease_deadline = NULL, heartbeat_at = NULL,
	last_error = ?, updated_at = ?, completed_at = ?
WHERE id = ?
	AND lease_owner = ?
	AND state = ?
`, string(state), lastError, encodeTime(now), completedAt, int64(jobID), workerID, string(domain.JobStateLeased))
	if err != nil {
		return domain.Job{}, fmt.Errorf("release leased job: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.Job{}, fmt.Errorf("release leased job rows affected: %w", err)
	}
	if rows == 0 {
		return domain.Job{}, ErrNotFound
	}
	job, err = getJobTx(ctx, tx, jobID)
	if err != nil {
		return domain.Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Job{}, fmt.Errorf("commit release transaction: %w", err)
	}
	return job, nil
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
	if job.State == to && to.Terminal() {
		// A terminal state is already final. Re-recording it keeps repeated
		// cancellations idempotent without rewriting the original outcome.
		if err := tx.Commit(); err != nil {
			return domain.Job{}, fmt.Errorf("commit transition transaction: %w", err)
		}
		return job, nil
	}
	if to == domain.JobStatePending {
		active, err := jobTargetActiveTx(ctx, tx, jobID)
		if err != nil {
			return domain.Job{}, err
		}
		if !active {
			to = domain.JobStateSkipped
			lastError = inactiveOccurrenceJobError
		}
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

	skipped, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, lease_owner = '', lease_deadline = NULL, heartbeat_at = NULL,
	last_error = ?, updated_at = ?, completed_at = ?
WHERE `+staleWhere+`
	AND NOT `+activeOccurrenceTargetSQL+`
`, append([]any{
		string(domain.JobStateSkipped),
		inactiveOccurrenceJobError,
		encodeTime(now),
		encodeTime(now),
	}, args...)...)
	if err != nil {
		return 0, fmt.Errorf("skip stale jobs with inactive occurrences: %w", err)
	}

	failed, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, lease_owner = '', lease_deadline = NULL, heartbeat_at = NULL,
	last_error = ?, updated_at = ?, completed_at = ?
WHERE `+staleWhere+`
	AND attempt_count >= ?
	AND `+activeOccurrenceTargetSQL+`
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
	AND `+activeOccurrenceTargetSQL+`
`, append([]any{
		string(domain.JobStatePending),
		"lease expired; job returned to pending",
		encodeTime(now),
	}, append(args, maxAttempts)...)...)
	if err != nil {
		return 0, fmt.Errorf("requeue stale jobs: %w", err)
	}

	skippedRows, err := skipped.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("skipped recovery rows affected: %w", err)
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

	return skippedRows + failedRows + pendingRows, nil
}

func jobTargetActiveTx(ctx context.Context, tx *sql.Tx, jobID domain.JobID) (bool, error) {
	var active bool
	err := tx.QueryRowContext(ctx, `
SELECT `+activeOccurrenceTargetSQL+`
FROM jobs
WHERE id = ?
`, int64(jobID)).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("check job occurrence lifecycle: %w", err)
	}
	return active, nil
}

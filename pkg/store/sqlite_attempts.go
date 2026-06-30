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

func (s *SQLiteStore) RecordAttemptEvent(ctx context.Context, event domain.AttemptEvent) (domain.AttemptEvent, error) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO attempt_events (attempt_id, type, name, message, payload, created_at)
VALUES (?, ?, ?, ?, ?, ?)
`, int64(event.AttemptID), string(event.Type), event.Name, event.Message, emptyBytesIfNil(event.Payload), encodeTime(event.CreatedAt))
	if err != nil {
		return domain.AttemptEvent{}, fmt.Errorf("record attempt event: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return domain.AttemptEvent{}, fmt.Errorf("attempt event id: %w", err)
	}
	return s.GetAttemptEvent(ctx, domain.AttemptEventID(id))
}

func (s *SQLiteStore) GetAttemptEvent(ctx context.Context, id domain.AttemptEventID) (domain.AttemptEvent, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, attempt_id, type, name, message, payload, created_at
FROM attempt_events
WHERE id = ?
`, int64(id))
	return scanAttemptEvent(row)
}

func (s *SQLiteStore) ListAttemptEvents(ctx context.Context, attemptID domain.AttemptID) (events []domain.AttemptEvent, err error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, attempt_id, type, name, message, payload, created_at
FROM attempt_events
WHERE attempt_id = ?
ORDER BY id ASC
`, int64(attemptID))
	if err != nil {
		return nil, fmt.Errorf("list attempt events: %w", err)
	}
	defer closeRows(rows, &err, "close attempt events")

	for rows.Next() {
		event, err := scanAttemptEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attempt events: %w", err)
	}
	return events, nil
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

func (s *SQLiteStore) ListAttemptsForJob(ctx context.Context, jobID domain.JobID) (attempts []domain.Attempt, err error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, job_id, number, worker_id, state, resolved_library_json,
	resolved_flow_json, resolved_profile_json, started_at, finished_at, error
FROM attempts
WHERE job_id = ?
ORDER BY number ASC, id ASC
`, int64(jobID))
	if err != nil {
		return nil, fmt.Errorf("list attempts for job: %w", err)
	}
	defer closeRows(rows, &err, "close attempts for job")

	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attempts for job: %w", err)
	}
	return attempts, nil
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

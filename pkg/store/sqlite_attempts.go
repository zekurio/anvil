package store

import (
	"context"
	"database/sql"
	"encoding/json"
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

// SaveAttemptProfile records the profile an attempt resolved to. The profile
// is only known once the flow has been evaluated against the probe, which is
// after the attempt row exists.
func (s *SQLiteStore) SaveAttemptProfile(ctx context.Context, attemptID domain.AttemptID, resolvedProfile []byte) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE attempts SET resolved_profile_json = ? WHERE id = ?
`, emptyBytesIfNil(resolvedProfile), int64(attemptID))
	if err != nil {
		return fmt.Errorf("save attempt profile: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("save attempt profile rows affected: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
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

func (s *SQLiteStore) ListAttemptsForJob(ctx context.Context, jobID domain.JobID) ([]domain.Attempt, error) {
	return listAttemptsForJob(ctx, s.db, jobID)
}

func listAttemptsForJob(ctx context.Context, q rowsQuerier, jobID domain.JobID) (attempts []domain.Attempt, err error) {
	rows, err := q.QueryContext(ctx, `
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

// maxArtifactJobIDsPerQuery bounds how many job ids are bound in one statement.
// SQLite caps bound parameters, and exceeding that cap fails the whole listing
// rather than degrading, so the query is chunked well below the limit.
const maxArtifactJobIDsPerQuery = 500

// LatestAttemptArtifacts returns the named artifact events belonging to the most
// recent attempt of each job that recorded any. Jobs without a matching event
// are absent from the result, which is what lets a caller tell "nothing was
// recorded" apart from "recorded, and it kept everything".
//
// It reads by name rather than by meaning so the store keeps no knowledge of
// what any particular artifact payload contains.
func (s *SQLiteStore) LatestAttemptArtifacts(ctx context.Context, name string, jobIDs []domain.JobID) (map[domain.JobID][]domain.AttemptEvent, error) {
	// A single IN list is set membership, but chunking is not: a repeated id
	// split across two chunks would append its events twice. Deduplicate so the
	// result never depends on the caller's ordering.
	unique := make([]domain.JobID, 0, len(jobIDs))
	seen := make(map[domain.JobID]struct{}, len(jobIDs))
	for _, jobID := range jobIDs {
		if _, ok := seen[jobID]; ok {
			continue
		}
		seen[jobID] = struct{}{}
		unique = append(unique, jobID)
	}
	result := make(map[domain.JobID][]domain.AttemptEvent, len(unique))
	for start := 0; start < len(unique); start += maxArtifactJobIDsPerQuery {
		end := min(start+maxArtifactJobIDsPerQuery, len(unique))
		if err := s.appendLatestAttemptArtifacts(ctx, name, unique[start:end], result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// appendLatestAttemptArtifacts collects one chunk of job ids into result.
//
// Ordering by attempts.id is ordering by recency: the column is INTEGER PRIMARY
// KEY AUTOINCREMENT, so ids strictly increase and are never reused, and RetryJob
// preserves attempt_count so attempt numbers cannot diverge from it.
func (s *SQLiteStore) appendLatestAttemptArtifacts(ctx context.Context, name string, jobIDs []domain.JobID, result map[domain.JobID][]domain.AttemptEvent) (err error) {
	if len(jobIDs) == 0 {
		return nil
	}
	args := make([]any, 0, len(jobIDs)+2)
	args = append(args, string(domain.AttemptEventArtifact), name)
	for _, jobID := range jobIDs {
		args = append(args, int64(jobID))
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT e.id, e.attempt_id, e.type, e.name, e.message, e.payload, e.created_at, a.job_id
FROM attempt_events e
JOIN attempts a ON a.id = e.attempt_id
WHERE e.type = ? AND e.name = ? AND a.job_id IN (`+placeholders(len(jobIDs))+`)
	AND e.attempt_id = (
		SELECT MAX(inner_events.attempt_id)
		FROM attempt_events inner_events
		JOIN attempts inner_attempts ON inner_attempts.id = inner_events.attempt_id
		WHERE inner_attempts.job_id = a.job_id
			AND inner_events.type = e.type
			AND inner_events.name = e.name
	)
ORDER BY a.job_id ASC, e.id ASC
`, args...)
	if err != nil {
		return fmt.Errorf("list latest attempt artifacts: %w", err)
	}
	defer closeRows(rows, &err, "close latest attempt artifacts")

	for rows.Next() {
		var (
			event   domain.AttemptEvent
			created string
			jobID   int64
		)
		if err := rows.Scan(&event.ID, &event.AttemptID, &event.Type, &event.Name,
			&event.Message, &event.Payload, &created, &jobID); err != nil {
			return fmt.Errorf("scan latest attempt artifact: %w", err)
		}
		event.CreatedAt = parseTime(created)
		result[domain.JobID(jobID)] = append(result[domain.JobID(jobID)], event)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate latest attempt artifacts: %w", err)
	}
	return nil
}

// LatestAttempt is what a job listing shows of a job's most recent attempt.
type LatestAttempt struct {
	Number    int
	State     domain.AttemptState
	StartedAt time.Time
	// FinishedAt is nil while the attempt runs.
	FinishedAt *time.Time
	// FlowName is the name of the flow the attempt took its snapshot of, or
	// empty when it recorded none.
	FlowName string
}

// LatestAttempts returns the most recent attempt of each named job, keyed by
// job. A job that never started has no entry. The ids travel as one JSON
// parameter, so a page of any size stays below SQLite's parameter limit.
func (s *SQLiteStore) LatestAttempts(ctx context.Context, ids []domain.JobID) (map[domain.JobID]LatestAttempt, error) {
	latest := make(map[domain.JobID]LatestAttempt, len(ids))
	if len(ids) == 0 {
		return latest, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("encode job ids: %w", err)
	}
	// resolved_flow_json is a BLOB column; json_extract reads it as text only
	// after a cast, and an attempt without a snapshot holds an empty value.
	err = s.eachRow(ctx, "latest attempts", `
SELECT a.job_id, a.number, a.state, a.started_at, a.finished_at,
	CASE WHEN json_valid(CAST(a.resolved_flow_json AS TEXT))
		THEN COALESCE(json_extract(CAST(a.resolved_flow_json AS TEXT), '$.name'), '') ELSE '' END
FROM attempts a
WHERE a.job_id IN (SELECT value FROM json_each(?))
	AND a.number = (SELECT max(number) FROM attempts WHERE job_id = a.job_id)
`, []any{string(encoded)}, func(rows *sql.Rows) error {
		var jobID domain.JobID
		var attempt LatestAttempt
		var startedAt string
		var finishedAt sql.NullString
		if err := rows.Scan(&jobID, &attempt.Number, &attempt.State, &startedAt, &finishedAt, &attempt.FlowName); err != nil {
			return err
		}
		attempt.StartedAt = parseTime(startedAt)
		attempt.FinishedAt = parseNullTimePtr(finishedAt)
		latest[jobID] = attempt
		return nil
	})
	if err != nil {
		return nil, err
	}
	return latest, nil
}

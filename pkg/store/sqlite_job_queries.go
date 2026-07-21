package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
)

func (s *SQLiteStore) GetJob(ctx context.Context, id domain.JobID) (domain.Job, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, slug, source_id, asset_id, library_name, priority, state, lease_owner,
	lease_deadline, heartbeat_at, attempt_count, last_error, input_size_bytes,
	output_size_bytes, created_at, updated_at, completed_at
FROM jobs
WHERE id = ?
`, int64(id))
	return scanJob(row)
}

func (s *SQLiteStore) GetJobBySlug(ctx context.Context, slug string) (domain.Job, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, slug, source_id, asset_id, library_name, priority, state, lease_owner,
	lease_deadline, heartbeat_at, attempt_count, last_error, input_size_bytes,
	output_size_bytes, created_at, updated_at, completed_at
FROM jobs
WHERE slug = ?
`, strings.TrimSpace(slug))
	return scanJob(row)
}

func (s *SQLiteStore) ResolveJobReference(ctx context.Context, reference string) (domain.Job, error) {
	reference = strings.TrimSpace(reference)
	if id, err := strconv.ParseInt(reference, 10, 64); err == nil && id > 0 {
		return s.GetJob(ctx, domain.JobID(id))
	}
	return s.GetJobBySlug(ctx, reference)
}

func (s *SQLiteStore) GetJobForTarget(ctx context.Context, sourceID domain.MediaSourceID, assetID domain.MediaAssetID) (domain.Job, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, slug, source_id, asset_id, library_name, priority, state, lease_owner,
	lease_deadline, heartbeat_at, attempt_count, last_error, input_size_bytes,
	output_size_bytes, created_at, updated_at, completed_at
FROM jobs
WHERE source_id = ?
	AND ifnull(asset_id, 0) = ?
ORDER BY id DESC
LIMIT 1
`, int64(sourceID), int64(assetID))
	return scanJob(row)
}

func (s *SQLiteStore) FindJobForTarget(ctx context.Context, sourceID domain.MediaSourceID, assetID domain.MediaAssetID) (domain.Job, bool, error) {
	job, err := s.GetJobForTarget(ctx, sourceID, assetID)
	if errors.Is(err, ErrNotFound) {
		return domain.Job{}, false, nil
	}
	if err != nil {
		return domain.Job{}, false, err
	}
	return job, true, nil
}

func (s *SQLiteStore) GetActiveJobForTarget(ctx context.Context, sourceID domain.MediaSourceID, assetID domain.MediaAssetID) (domain.Job, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, slug, source_id, asset_id, library_name, priority, state, lease_owner,
	lease_deadline, heartbeat_at, attempt_count, last_error, input_size_bytes,
	output_size_bytes, created_at, updated_at, completed_at
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

func (s *SQLiteStore) HasViableQueueWorkForLibrary(ctx context.Context, libraryName domain.LibraryName) (bool, error) {
	if strings.TrimSpace(string(libraryName)) == "" {
		return false, errors.New("library name is required")
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `
SELECT EXISTS (
	SELECT 1
	FROM jobs j
	JOIN media_sources s ON s.id = j.source_id
	LEFT JOIN media_assets a ON a.id = j.asset_id
	WHERE j.library_name = ?
		AND j.state IN (?, ?, ?)
		AND s.is_current = 1 AND s.status = ?
		AND (j.asset_id IS NULL OR (
			a.source_id = s.id AND a.is_current = 1 AND a.status = ?
		))
	LIMIT 1
)
`, string(libraryName),
		string(domain.JobStatePending), string(domain.JobStateLeased), string(domain.JobStateRetrying),
		string(domain.MediaSourceActive), string(domain.MediaAssetActive)).Scan(&exists); err != nil {
		return false, fmt.Errorf("check viable queue work for library %q: %w", libraryName, err)
	}
	return exists, nil
}

func (s *SQLiteStore) CountJobsByState(ctx context.Context) (counts map[domain.JobState]int64, err error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT state, COUNT(*)
FROM jobs
GROUP BY state
ORDER BY state
`)
	if err != nil {
		return nil, fmt.Errorf("count jobs by state: %w", err)
	}
	defer closeRows(rows, &err, "close job state counts")

	counts = make(map[domain.JobState]int64)
	for rows.Next() {
		var state domain.JobState
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return nil, fmt.Errorf("scan job state count: %w", err)
		}
		counts[state] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job state counts: %w", err)
	}
	return counts, nil
}

func (s *SQLiteStore) ListJobs(ctx context.Context, filter JobListFilter) (jobs []JobSummary, err error) {
	query := `
SELECT j.id, j.slug, j.source_id, j.asset_id, j.library_name, j.priority, j.state,
	j.lease_owner, j.lease_deadline, j.heartbeat_at, j.attempt_count,
	j.last_error, j.input_size_bytes, j.output_size_bytes, j.created_at,
	j.updated_at, j.completed_at,
	s.kind, s.relative_path, a.relative_path, a.role
FROM jobs j
JOIN media_sources s ON s.id = j.source_id
LEFT JOIN media_assets a ON a.id = j.asset_id
WHERE 1 = 1
`
	var args []any
	if filter.LibraryName != "" {
		query += " AND j.library_name = ?\n"
		args = append(args, string(filter.LibraryName))
	}
	if len(filter.States) > 0 {
		query += " AND j.state IN (" + placeholders(len(filter.States)) + ")\n"
		for _, state := range filter.States {
			args = append(args, string(state))
		}
	}
	query += "ORDER BY j.created_at DESC, j.id DESC\n"
	if filter.Limit > 0 {
		query += "LIMIT ?\n"
		args = append(args, filter.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer closeRows(rows, &err, "close jobs")

	for rows.Next() {
		job, err := scanJobSummary(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	return jobs, nil
}

func (s *SQLiteStore) GetJobSummary(ctx context.Context, id domain.JobID) (JobSummary, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT j.id, j.slug, j.source_id, j.asset_id, j.library_name, j.priority, j.state,
	j.lease_owner, j.lease_deadline, j.heartbeat_at, j.attempt_count,
	j.last_error, j.input_size_bytes, j.output_size_bytes, j.created_at,
	j.updated_at, j.completed_at,
	s.kind, s.relative_path, a.relative_path, a.role
FROM jobs j
JOIN media_sources s ON s.id = j.source_id
LEFT JOIN media_assets a ON a.id = j.asset_id
WHERE j.id = ?
`, int64(id))
	return scanJobSummary(row)
}

func (s *SQLiteStore) ListLibraryStats(ctx context.Context, filter LibraryStatsFilter) (stats []LibraryStats, err error) {
	query := `
SELECT library_name, COUNT(*), COALESCE(SUM(input_size_bytes), 0), COALESCE(SUM(output_size_bytes), 0)
FROM jobs
WHERE state = ?
	AND input_size_bytes > 0
	AND output_size_bytes > 0
`
	args := []any{string(domain.JobStateComplete)}
	if filter.LibraryName != "" {
		query += " AND library_name = ?\n"
		args = append(args, string(filter.LibraryName))
	}
	query += "GROUP BY library_name\nORDER BY library_name ASC\n"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list library stats: %w", err)
	}
	defer closeRows(rows, &err, "close library stats")

	for rows.Next() {
		var stat LibraryStats
		if err := rows.Scan(&stat.LibraryName, &stat.Jobs, &stat.InputSizeBytes, &stat.OutputSizeBytes); err != nil {
			return nil, fmt.Errorf("scan library stats: %w", err)
		}
		stat.SavedBytes = stat.InputSizeBytes - stat.OutputSizeBytes
		if stat.InputSizeBytes > 0 {
			stat.SavedPercent = float64(stat.SavedBytes) / float64(stat.InputSizeBytes) * 100
		}
		stats = append(stats, stat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate library stats: %w", err)
	}
	return stats, nil
}

func getJobTx(ctx context.Context, tx *sql.Tx, id domain.JobID) (domain.Job, error) {
	row := tx.QueryRowContext(ctx, `
SELECT id, slug, source_id, asset_id, library_name, priority, state, lease_owner,
	lease_deadline, heartbeat_at, attempt_count, last_error, input_size_bytes,
	output_size_bytes, created_at, updated_at, completed_at
FROM jobs
WHERE id = ?
`, int64(id))
	return scanJob(row)
}

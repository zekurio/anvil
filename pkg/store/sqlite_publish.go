package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
)

// PublishArtifactProtected reports whether an unresolved publish journal owns
// artifactPath. Callers use it before reclaiming a legacy unscoped part file,
// whose name cannot identify the owning job.
func (s *SQLiteStore) PublishArtifactProtected(ctx context.Context, artifactPath string) (protected bool, err error) {
	if artifactPath == "" {
		return false, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT job_id, stage, operation_json
FROM publish_operations
WHERE stage != ?
ORDER BY job_id
`, string(replacepkg.PublishStageCommitted))
	if err != nil {
		return false, fmt.Errorf("list unresolved publish artifacts: %w", err)
	}
	defer closeRows(rows, &err, "close unresolved publish artifacts")
	target := filepath.Clean(artifactPath)
	targetIdentity, err := replacepkg.StatFileIdentity(artifactPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("identify publish artifact candidate %q: %w", artifactPath, err)
	}
	for rows.Next() {
		var jobID domain.JobID
		var stage replacepkg.PublishStage
		var data []byte
		if err := rows.Scan(&jobID, &stage, &data); err != nil {
			return false, fmt.Errorf("scan unresolved publish artifact: %w", err)
		}
		var operation replacepkg.PublishOperation
		if err := json.Unmarshal(data, &operation); err != nil {
			return false, fmt.Errorf("decode publish operation for job %d: %w", jobID, err)
		}
		if operation.JobID != jobID {
			return false, fmt.Errorf("publish operation job mismatch: row is %d, journal is %d", jobID, operation.JobID)
		}
		if operation.Stage != stage {
			return false, fmt.Errorf("publish operation stage mismatch for job %d: row is %q, journal is %q", jobID, stage, operation.Stage)
		}
		if strings.TrimSpace(operation.ArtifactPath) == "" {
			return false, fmt.Errorf("publish operation for job %d has no artifact path", jobID)
		}
		if operation.ArtifactIdentity.Device == 0 || operation.ArtifactIdentity.Inode == 0 {
			return false, fmt.Errorf("publish operation for job %d has no usable artifact identity", jobID)
		}
		if filepath.Clean(operation.ArtifactPath) == target || replacepkg.SameFileObject(operation.ArtifactIdentity, targetIdentity) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate unresolved publish artifacts: %w", err)
	}
	return false, nil
}

func (s *SQLiteStore) GetPublishOperation(ctx context.Context, jobID domain.JobID) (replacepkg.PublishOperation, bool, error) {
	var data []byte
	var stage replacepkg.PublishStage
	err := s.db.QueryRowContext(ctx, `
SELECT stage, operation_json
FROM publish_operations
WHERE job_id = ?
`, int64(jobID)).Scan(&stage, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return replacepkg.PublishOperation{}, false, nil
	}
	if err != nil {
		return replacepkg.PublishOperation{}, false, fmt.Errorf("get publish operation: %w", err)
	}
	var operation replacepkg.PublishOperation
	if err := json.Unmarshal(data, &operation); err != nil {
		return replacepkg.PublishOperation{}, false, fmt.Errorf("decode publish operation: %w", err)
	}
	if operation.Stage != stage {
		return replacepkg.PublishOperation{}, false, fmt.Errorf("publish operation stage mismatch: row is %q, journal is %q", stage, operation.Stage)
	}
	return operation, true, nil
}

// CreatePublishOperation journals a publish before anything touches the
// destination. It refuses to journal one for a canceled job: CancelJobs refuses
// jobs that already journaled a publish, and this is the other half of that
// mutual exclusion, so an operator cancel and a starting publish can never both
// win. The store is the arbiter because it serializes both writers.
func (s *SQLiteStore) CreatePublishOperation(ctx context.Context, operation replacepkg.PublishOperation) error {
	data, err := json.Marshal(operation)
	if err != nil {
		return fmt.Errorf("encode publish operation: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO publish_operations (job_id, stage, operation_json, created_at, updated_at)
SELECT ?, ?, ?, ?, ?
WHERE NOT EXISTS (SELECT 1 FROM jobs WHERE id = ? AND state = ?)
`, int64(operation.JobID), operation.Stage, data, encodeTime(operation.CreatedAt), encodeTime(operation.UpdatedAt),
		int64(operation.JobID), string(domain.JobStateCanceled))
	if err != nil {
		return fmt.Errorf("create publish operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("create publish operation rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("create publish operation for job %d: %w", operation.JobID, ErrJobCanceled)
	}
	return nil
}

func (s *SQLiteStore) UpdatePublishOperation(ctx context.Context, operation replacepkg.PublishOperation, previous replacepkg.PublishStage) error {
	data, err := json.Marshal(operation)
	if err != nil {
		return fmt.Errorf("encode publish operation: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE publish_operations
SET stage = ?, operation_json = ?, updated_at = ?
WHERE job_id = ? AND stage = ?
`, operation.Stage, data, encodeTime(operation.UpdatedAt), int64(operation.JobID), previous)
	if err != nil {
		return fmt.Errorf("update publish operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update publish operation rows affected: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

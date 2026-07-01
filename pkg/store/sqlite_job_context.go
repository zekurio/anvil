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

func (s *SQLiteStore) GetJobPipelineContext(ctx context.Context, jobID domain.JobID) (domain.JobPipelineContext, bool, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `
SELECT pipeline_context_json
FROM jobs
WHERE id = ?
`, int64(jobID)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.JobPipelineContext{}, false, ErrNotFound
	}
	if err != nil {
		if missingPipelineContextColumn(err) {
			return domain.JobPipelineContext{}, false, nil
		}
		return domain.JobPipelineContext{}, false, err
	}
	if len(data) == 0 {
		return domain.JobPipelineContext{}, false, nil
	}

	var snapshot domain.JobPipelineContext
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return domain.JobPipelineContext{}, false, fmt.Errorf("decode job pipeline context: %w", err)
	}
	return snapshot, true, nil
}

func (s *SQLiteStore) SaveJobPipelineContext(ctx context.Context, jobID domain.JobID, snapshot domain.JobPipelineContext, now time.Time) error {
	if snapshot.Version == 0 {
		snapshot.Version = domain.JobPipelineContextVersion
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode job pipeline context: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET pipeline_context_json = ?, updated_at = ?
WHERE id = ?
`, data, encodeTime(defaultNow(now)), int64(jobID))
	if err != nil {
		return fmt.Errorf("save job pipeline context: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("save job pipeline context rows affected: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func missingPipelineContextColumn(err error) bool {
	return strings.Contains(err.Error(), "no such column: pipeline_context_json")
}

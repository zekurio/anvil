package store

import (
	"context"
	"fmt"

	"github.com/zekurio/anvil/pkg/domain"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
)

// JobProtectionReason explains why maintenance must leave a job, and anything
// on disk that belongs to it, alone. It is stable and machine-readable.
type JobProtectionReason string

const (
	// JobProtectedActive marks a job that has not reached a terminal state.
	// Its staging directory can still be the working directory of a live
	// attempt.
	JobProtectedActive JobProtectionReason = "job_not_terminal"
	// JobProtectedPublishJournal marks a job whose publish journal has not
	// reached committed. The journal owns the exact staged artifact and, past
	// the published stage, a destination file and possibly an .anvil-backup.
	// Only recovery for that job can resolve it, so nothing else may delete
	// the row or the files it names.
	JobProtectedPublishJournal JobProtectionReason = "unresolved_publish_journal"
)

// ProtectedJob names one job maintenance must not disturb.
type ProtectedJob struct {
	JobID  domain.JobID
	Slug   string
	State  domain.JobState
	Reason JobProtectionReason
}

// ProtectedJobs lists every job that is either still active or still owns an
// unresolved publish journal.
//
// Staging cleanup and job pruning both need this: a job's staging directory
// looks abandoned to a timestamp check long before it is, and deleting a job
// row cascades its publish journal away, which would strand the artifact,
// destination, and backup files the journal exists to resolve.
func (s *SQLiteStore) ProtectedJobs(ctx context.Context) (protected []ProtectedJob, err error) {
	terminal := terminalJobStates()
	args := make([]any, 0, len(terminal)+1)
	for _, state := range terminal {
		args = append(args, string(state))
	}
	args = append(args, string(replacepkg.PublishStageCommitted))
	rows, err := s.db.QueryContext(ctx, `
SELECT j.id, j.slug, j.state, p.stage
FROM jobs j
LEFT JOIN publish_operations p ON p.job_id = j.id
WHERE j.state NOT IN (`+placeholders(len(terminal))+`)
   OR (p.stage IS NOT NULL AND p.stage != ?)
ORDER BY j.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list protected jobs: %w", err)
	}
	defer closeRows(rows, &err, "close protected jobs")
	for rows.Next() {
		var job ProtectedJob
		var slug *string
		var stage *string
		if err := rows.Scan(&job.JobID, &slug, &job.State, &stage); err != nil {
			return nil, fmt.Errorf("scan protected job: %w", err)
		}
		if slug != nil {
			job.Slug = *slug
		}
		// The publish journal is the stronger statement of the two: it names
		// files outside the database, so it is reported even when the job is
		// also still active.
		job.Reason = JobProtectedActive
		if stage != nil && *stage != string(replacepkg.PublishStageCommitted) {
			job.Reason = JobProtectedPublishJournal
		}
		protected = append(protected, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate protected jobs: %w", err)
	}
	return protected, nil
}

func terminalJobStates() []domain.JobState {
	states := make([]domain.JobState, 0, len(domain.JobStates()))
	for _, state := range domain.JobStates() {
		if state.Terminal() {
			states = append(states, state)
		}
	}
	return states
}

// noUnresolvedPublishCondition is the SQL guard shared by every maintenance
// path that deletes job rows. It takes one bound argument, the committed
// stage, so the guard cannot drift between the counting and the deleting half
// of a prune.
const noUnresolvedPublishCondition = `NOT EXISTS (
	SELECT 1 FROM publish_operations p
	WHERE p.job_id = j.id AND p.stage != ?
)`

// unresolvedPublishCondition selects the jobs the guard above excludes, so a
// prune can report what it refused instead of silently skipping it.
const unresolvedPublishCondition = `EXISTS (
	SELECT 1 FROM publish_operations p
	WHERE p.job_id = j.id AND p.stage != ?
)`

// committedPublishStage is the bound argument both conditions expect.
func committedPublishStage() any {
	return string(replacepkg.PublishStageCommitted)
}

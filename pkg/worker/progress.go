package worker

import (
	"sync"

	"github.com/zekurio/anvil/pkg/domain"
)

// ProgressTracker holds the latest progress report of every running job so
// the web API can show it without touching the store. The zero value is ready
// to use, and a nil tracker ignores every call.
type ProgressTracker struct {
	mu   sync.Mutex
	jobs map[domain.JobID]domain.JobProgress
}

// Update replaces the progress recorded for a job.
func (t *ProgressTracker) Update(progress domain.JobProgress) {
	if t == nil || progress.JobID == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.jobs == nil {
		t.jobs = make(map[domain.JobID]domain.JobProgress)
	}
	t.jobs[progress.JobID] = progress
}

// Clear forgets a job once its attempt has ended.
func (t *ProgressTracker) Clear(jobID domain.JobID) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.jobs, jobID)
}

// Snapshot returns the latest progress of one job.
func (t *ProgressTracker) Snapshot(jobID domain.JobID) (domain.JobProgress, bool) {
	if t == nil {
		return domain.JobProgress{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	progress, ok := t.jobs[jobID]
	return progress, ok
}

// All returns the latest progress of every tracked job, in no fixed order.
func (t *ProgressTracker) All() []domain.JobProgress {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	all := make([]domain.JobProgress, 0, len(t.jobs))
	for _, progress := range t.jobs {
		all = append(all, progress)
	}
	return all
}

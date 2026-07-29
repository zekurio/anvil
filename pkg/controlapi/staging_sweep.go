package controlapi

import (
	"context"
	"sort"
	"time"

	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/staging"
	"github.com/zekurio/anvil/pkg/store"
)

// ProtectedJobsSource is the one thing a staging sweep needs from persistence:
// the jobs whose staging directories must not be touched.
type ProtectedJobsSource interface {
	ProtectedJobs(context.Context) ([]store.ProtectedJob, error)
}

// StagingSweep is one protected staging cleanup request.
type StagingSweep struct {
	OlderThan time.Duration
	Now       time.Time
	DryRun    bool
}

// StagingSweepResult is what the sweep did, including who it refused to touch.
type StagingSweepResult struct {
	Root          string
	Candidates    int
	Removed       int
	Skipped       int
	Protected     int
	ProtectedJobs []control.ProtectedJob
	Errors        []string
}

// SweepStaging removes stale staging directories without touching one that
// belongs to a job that is still active or still owns an unresolved publish
// journal. Directory age cannot tell an abandoned staging directory from the
// working directory of a multi-hour encode — the mtime stops moving as soon as
// the output file exists — so the job table decides, not the filesystem.
//
// The daemon's start-up sweep and the control command both go through here, so
// they can never disagree about what is protected. It fails closed: when the
// protected set cannot be loaded, nothing is removed.
func SweepStaging(ctx context.Context, protection ProtectedJobsSource, root string, sweep StagingSweep) (StagingSweepResult, error) {
	if protection == nil {
		return StagingSweepResult{}, newError(control.CodeInternal, "staging cleanup requires a source of protected jobs")
	}
	protected, err := protection.ProtectedJobs(ctx)
	if err != nil {
		return StagingSweepResult{}, err
	}
	held := make(map[int64]store.ProtectedJob, len(protected))
	for _, job := range protected {
		held[int64(job.JobID)] = job
	}
	result, err := staging.Manager{Root: root}.CleanupStale(staging.CleanupStaleOptions{
		OlderThan: sweep.OlderThan,
		Now:       sweep.Now,
		DryRun:    sweep.DryRun,
		Protected: func(jobID int64, _ int64) bool {
			_, ok := held[jobID]
			return ok
		},
	})
	if err != nil {
		return StagingSweepResult{}, err
	}
	swept := StagingSweepResult{
		Root:       root,
		Candidates: result.Candidates, Removed: result.Removed, Skipped: result.Skipped,
		Protected: result.Protected, Errors: result.Errors,
	}
	for _, jobID := range uniqueJobIDs(result.ProtectedJobs) {
		job := held[jobID]
		swept.ProtectedJobs = append(swept.ProtectedJobs, control.ProtectedJob{
			ID: jobID, Slug: job.Slug, Reason: string(job.Reason),
		})
	}
	return swept, nil
}

// uniqueJobIDs collapses the per-directory protection records into one entry
// per job, in a stable order: a job with several attempt directories is one
// refusal, not several.
func uniqueJobIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

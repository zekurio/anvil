package controlapi

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/zekurio/anvil/pkg/domain"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
	"github.com/zekurio/anvil/pkg/store"
)

// JobParts says where one job may have left an unpublished artifact on a
// destination filesystem. It is read while the job row exists and used once
// the row is gone, which is when nothing else can find those files any more.
type JobParts struct {
	JobID        domain.JobID
	destinations []stagedDestination
	// conflict is the job's publish journal. It is only set for a publish
	// that stopped in conflict.
	conflict *replacepkg.PublishOperation
}

// LibraryParts lists, ahead of deleting a library, the part files its jobs
// would leave behind. It reads and changes nothing on disk.
//
// A job whose publish stopped in conflict is one of them: the conflict keeps
// the artifact for an operator to look at, and deleting the library is the
// operator letting go of it. Jobs with any other unfinished publish are left
// out; the store refuses to delete their library.
func (s Service) LibraryParts(ctx context.Context, library string) ([]JobParts, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	// A completed job published its artifact or never wrote one.
	states := make([]domain.JobState, 0, len(allJobStates))
	for _, state := range allJobStates {
		if state != domain.JobStateComplete {
			states = append(states, state)
		}
	}
	jobs, err := s.Store.ListJobs(ctx, store.JobListFilter{LibraryName: domain.LibraryName(library), States: states})
	if err != nil {
		return nil, fmt.Errorf("list jobs of library %q: %w", library, err)
	}
	var parts []JobParts
	for _, summary := range jobs {
		job := summary.Job
		if job.AttemptCount == 0 {
			// Only an attempt stages anything.
			continue
		}
		operation, journaled, err := s.Store.GetPublishOperation(ctx, job.ID)
		if err != nil {
			return nil, fmt.Errorf("read publish journal of job %d: %w", job.ID, err)
		}
		if !journaled || operation.Stage != replacepkg.PublishStageConflict {
			continue
		}
		source, asset, err := s.jobOccurrence(ctx, job)
		if err != nil {
			return nil, err
		}
		destinations, err := stagedDestinations(ctx, s.Store, job, source, asset)
		if err != nil {
			// What did resolve is still worth reclaiming, and a snapshot that
			// cannot be read must not keep the library from being deleted.
			slog.Warn("some staged destinations of a job could not be resolved; its part files there stay", "job", job.ID, "error", err)
		}
		parts = append(parts, JobParts{JobID: job.ID, destinations: destinations, conflict: &operation})
	}
	return parts, nil
}

// RemoveParts removes the part files of jobs that were deleted together with
// their library. It must only be given jobs whose rows are gone: nothing can
// lease those any more, so no attempt is writing the files.
//
// Only names in Anvil's own namespace are removed, <destination>.job-<id>
// .anvil-part, through replace.CleanupPartFiles. The destination, the source,
// and a backup of the original are never touched. For a conflicted publish
// those are what the operator still has to look at, so they are logged.
func (s Service) RemoveParts(ctx context.Context, parts []JobParts) {
	for _, job := range parts {
		s.removeStagedParts(ctx, job.JobID, job.destinations)
		if job.conflict == nil {
			continue
		}
		var left []string
		for _, path := range []string{job.conflict.ArtifactPath, job.conflict.BackupPath} {
			if path == "" {
				continue
			}
			if _, err := os.Lstat(path); err == nil {
				left = append(left, path)
			}
		}
		slog.Warn("deleted a job whose publish had stopped in conflict; its unpublished artifact was removed, and the files listed as left were not touched",
			"job", job.JobID, "destination", job.conflict.DestinationPath, "conflict", job.conflict.ConflictDescription, "left", left)
	}
}

// removeStagedParts removes a job's own part files at every destination its
// attempts staged at, and the legacy unscoped part beside each destination
// unless an unresolved publish journal owns it.
func (s Service) removeStagedParts(ctx context.Context, jobID domain.JobID, destinations []stagedDestination) {
	for _, destination := range destinations {
		if err := replacepkg.CleanupPartFiles(destination.Library, destination.Path, replacepkg.PartJobLabel(jobID)); err != nil {
			slog.Warn("job part cleanup failed", "job", jobID, "destination", destination.Path, "error", err)
		}
		if err := replacepkg.CleanupLegacyPartFiles(ctx, s.Store, destination.Path); err != nil {
			slog.Warn("job legacy part cleanup failed", "job", jobID, "destination", destination.Path, "error", err)
		}
	}
}

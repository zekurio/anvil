package controlapi

import (
	"context"
	"log/slog"
	"os"

	"github.com/zekurio/anvil/pkg/domain"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
	"github.com/zekurio/anvil/pkg/store"
)

// AttemptsInFlight lists the jobs of a library a worker is still running an
// attempt for, by the worker's own account rather than by the job rows. A
// canceled job is terminal in the store the moment the cancel commits, while
// its worker may still be stopping ffmpeg and cleaning up after it; until
// the worker is done, the job is listed here. Without a scheduler (the
// daemon has not started one yet, or a test) it lists nothing.
func (s Service) AttemptsInFlight(library string) []domain.JobID {
	if s.RunningAttempts == nil {
		return nil
	}
	return s.RunningAttempts(domain.LibraryName(library))
}

// RemoveParts removes the part files of jobs that were deleted together with
// their library, from what the delete read about them in its transaction. It
// must only be given jobs whose rows are gone and that no worker was running
// an attempt for when they went (see store.SQLiteStore.DeleteLibrary):
// nothing can lease those any more, so no attempt is writing the files.
//
// Every job that ran an attempt and did not complete can have left one: a
// failed or skipped job that stale-lease recovery ended, or a job waiting to
// retry after a crash. Normally the next attempt, a cancel, or the recovery
// that ends the job reclaims it, but all of those need the job row.
//
// A job whose publish stopped in conflict is included: the conflict keeps the
// artifact for an operator to look at, and deleting the library is the
// operator letting go of it. Jobs with any other publish journal are left
// alone. A committed publish has removed its artifact, and for an interrupted
// one the store refuses to delete the library.
//
// Only names in Anvil's own namespace are removed, <destination>.job-<id>
// .anvil-part, through replace.CleanupPartFiles. The destination, the source,
// and a backup of the original are never touched. For a conflicted publish
// those are what the operator still has to look at, so they are logged.
func (s Service) RemoveParts(ctx context.Context, deleted []store.DeletedJob) {
	for _, job := range deleted {
		staged := job.Staged
		if staged == nil {
			continue
		}
		conflict := staged.Journal
		if conflict != nil && conflict.Stage != replacepkg.PublishStageConflict {
			continue
		}
		destinations, err := replacepkg.StagedDestinations(staged.Job, staged.Source, staged.Asset, staged.Attempts)
		if err != nil {
			// What did resolve is still worth reclaiming.
			slog.Warn("some staged destinations of a deleted job could not be resolved; its part files there stay", "job", job.ID, "error", err)
		}
		s.removeStagedParts(ctx, job.ID, destinations)
		if conflict == nil {
			continue
		}
		var left []string
		for _, path := range []string{conflict.ArtifactPath, conflict.BackupPath} {
			if path == "" {
				continue
			}
			if _, err := os.Lstat(path); err == nil {
				left = append(left, path)
			}
		}
		slog.Warn("deleted a job whose publish had stopped in conflict; its unpublished artifact was removed, and the files listed as left were not touched",
			"job", job.ID, "destination", conflict.DestinationPath, "conflict", conflict.ConflictDescription, "left", left)
	}
}

// ReclaimEndedJobs removes the part files of jobs that ended without a worker
// to clean up after them and will never run another attempt that would
// overwrite them: jobs stale-lease recovery failed or skipped, and waiting
// jobs a scan skipped because the rules now deny their files. Each one goes
// through cleanupOrphanedPart, so a job with a publish journal keeps its
// artifact.
//
// The jobs are already terminal, so this is the only chance to reclaim their
// parts: it does not stop with a daemon shutdown or a client that hung up.
func (s Service) ReclaimEndedJobs(ctx context.Context, jobs []domain.JobID) {
	ctx = context.WithoutCancel(ctx)
	for _, jobID := range jobs {
		s.cleanupOrphanedPart(ctx, jobID)
	}
}

// removeStagedParts removes a job's own part files at every destination its
// attempts staged at, and the legacy unscoped part beside each destination
// unless an unresolved publish journal owns it.
func (s Service) removeStagedParts(ctx context.Context, jobID domain.JobID, destinations []replacepkg.StagedDestination) {
	for _, destination := range destinations {
		if err := replacepkg.CleanupPartFiles(destination.Publish, destination.Path, replacepkg.PartJobLabel(jobID)); err != nil {
			slog.Warn("job part cleanup failed", "job", jobID, "destination", destination.Path, "error", err)
		}
		if err := replacepkg.CleanupLegacyPartFiles(ctx, s.Store, destination.Path); err != nil {
			slog.Warn("job legacy part cleanup failed", "job", jobID, "destination", destination.Path, "error", err)
		}
	}
}

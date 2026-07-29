package controlapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/staging"
	"github.com/zekurio/anvil/pkg/store"
)

// RetryJobs requeues jobs by reference, in bulk over failed jobs, or both. The
// bulk form is a separate flag rather than "no references", so an empty
// reference list can never turn into a queue-wide retry.
//
// Every reference is resolved before anything is written, and the writes then
// happen in one store transaction. A retry that fails halfway used to leave a
// committed bulk retry behind while reporting an error, so the operator's only
// record of the command disagreed with the queue.
func (s Service) RetryJobs(ctx context.Context, request control.JobRetryRequest) (control.JobRetryResponse, error) {
	if err := s.requireStore(); err != nil {
		return control.JobRetryResponse{}, err
	}
	library := strings.TrimSpace(request.Library)
	if !request.Failed && len(request.References) == 0 {
		return control.JobRetryResponse{}, invalidArgumentf("retry requires job references or the failed selector")
	}
	if library != "" && !request.Failed {
		return control.JobRetryResponse{}, invalidArgumentf("library only narrows a failed-job retry")
	}
	if err := requireConfiguredLibrary(s.runtimeConfig(), library); err != nil {
		return control.JobRetryResponse{}, err
	}
	ids, err := s.resolveJobReferences(ctx, nil, request.References)
	if err != nil {
		return control.JobRetryResponse{}, err
	}
	jobIDs := make([]domain.JobID, 0, len(ids))
	for _, id := range ids {
		jobIDs = append(jobIDs, domain.JobID(id))
	}

	now := s.now()
	result, err := s.Store.RetryJobs(ctx, store.RetryJobsInput{
		IDs: jobIDs, Failed: request.Failed, LibraryName: domain.LibraryName(library), Now: now,
	})
	if errors.Is(err, store.ErrNotFound) {
		return control.JobRetryResponse{}, notFoundf("%s", err.Error())
	}
	if err != nil {
		return control.JobRetryResponse{}, err
	}
	response := control.JobRetryResponse{
		APIVersion: control.Version, ServerTime: now,
		RetriedFailed: result.RetriedFailed,
		Jobs:          make([]control.JobRetryResult, 0, len(result.Jobs)),
	}
	for _, job := range result.Jobs {
		response.Jobs = append(response.Jobs, control.JobRetryResult{
			ID: int64(job.ID), Slug: job.Label(), Library: string(job.LibraryName), State: string(job.State),
		})
	}
	slog.Info("control retried jobs", "referenced", len(response.Jobs), "retried_failed", response.RetriedFailed, "library", library)
	return response, nil
}

// RecoverJobs releases stale leases the same way the daemon's own recovery loop
// does, using the daemon's live max-attempts policy.
func (s Service) RecoverJobs(ctx context.Context) (control.JobRecoverResponse, error) {
	if err := s.requireStore(); err != nil {
		return control.JobRecoverResponse{}, err
	}
	recovered, err := s.Store.RecoverStaleJobs(ctx, s.runtimeConfig().Daemon.MaxAttempts, s.now())
	if err != nil {
		return control.JobRecoverResponse{}, err
	}
	return control.JobRecoverResponse{APIVersion: control.Version, ServerTime: s.now(), RecoveredJobs: recovered}, nil
}

// PruneJobs deletes terminal jobs whose source occurrence is already missing.
// It is a dry run unless Apply is set, and it never deletes a job that still
// owns an unresolved publish journal.
func (s Service) PruneJobs(ctx context.Context, request control.JobPruneRequest) (control.JobPruneResponse, error) {
	if err := s.requireStore(); err != nil {
		return control.JobPruneResponse{}, err
	}
	library := strings.TrimSpace(request.Library)
	if err := requireConfiguredLibrary(s.runtimeConfig(), library); err != nil {
		return control.JobPruneResponse{}, err
	}
	states, err := control.ParseJobStates(request.States)
	if err != nil {
		return control.JobPruneResponse{}, err
	}
	for _, state := range states {
		if !state.Terminal() {
			return control.JobPruneResponse{}, invalidArgumentf("job state %q is not terminal and cannot be pruned", state)
		}
	}
	result, err := s.Store.PruneMissingSourceJobs(ctx, store.PruneMissingSourceJobsOptions{
		LibraryName: domain.LibraryName(library),
		States:      states,
		Apply:       request.Apply,
	})
	if err != nil {
		return control.JobPruneResponse{}, err
	}
	response := control.JobPruneResponse{
		APIVersion: control.Version, ServerTime: s.now(),
		DryRun: result.DryRun, MatchedJobs: result.MatchedJobs,
		AffectedSources: result.AffectedSources, DeletedJobs: result.DeletedJobs,
		ByState:       make(map[string]int64, len(result.ByState)),
		ProtectedJobs: protectedJobs(result.ProtectedJobs),
	}
	for state, count := range result.ByState {
		response.ByState[string(state)] = count
	}
	if !result.DryRun {
		slog.Info("control pruned jobs", "deleted", result.DeletedJobs, "matched", result.MatchedJobs, "protected", len(result.ProtectedJobs))
	}
	return response, nil
}

// ScanLibraries runs a scan inside the daemon, so it shares the daemon's store
// handle and occurrence bookkeeping instead of racing a second process.
func (s Service) ScanLibraries(ctx context.Context, request control.LibraryScanRequest) (control.LibraryScanResponse, error) {
	if s.Scanner == nil {
		return control.LibraryScanResponse{}, newError(control.CodeInternal, "control service scanner is required")
	}
	cfg := s.runtimeConfig()
	name := strings.TrimSpace(request.Library)
	if err := requireConfiguredLibrary(cfg, name); err != nil {
		return control.LibraryScanResponse{}, err
	}
	var result scanner.ScanResult
	var err error
	if name != "" {
		library, ok := cfg.FindLibrary(domain.LibraryName(name))
		if !ok {
			return control.LibraryScanResponse{}, notFoundf("library %q is not configured", name)
		}
		result, err = s.Scanner.ScanLibrary(ctx, library)
	} else {
		result, err = s.Scanner.Scan(ctx, cfg)
	}
	if err != nil {
		return control.LibraryScanResponse{}, err
	}
	response := control.LibraryScanResponse{
		APIVersion: control.Version, ServerTime: s.now(),
		Libraries: result.Libraries, Sources: result.Sources, Assets: result.Assets,
		EnqueuedJobs: result.EnqueuedJobs, ExistingJobs: result.ExistingJobs,
		SkippedIgnored: result.SkippedIgnored, SkippedUnstable: result.SkippedUnstable,
	}
	if !result.NextStableAt.IsZero() {
		nextStableAt := result.NextStableAt
		response.NextStableAt = &nextStableAt
	}
	return response, nil
}

func (s Service) LibraryStats(ctx context.Context, request control.LibraryStatsRequest) (control.LibraryStatsResponse, error) {
	if err := s.requireStore(); err != nil {
		return control.LibraryStatsResponse{}, err
	}
	library := strings.TrimSpace(request.Library)
	if err := requireConfiguredLibrary(s.runtimeConfig(), library); err != nil {
		return control.LibraryStatsResponse{}, err
	}
	stats, err := s.Store.ListLibraryStats(ctx, store.LibraryStatsFilter{LibraryName: domain.LibraryName(library)})
	if err != nil {
		return control.LibraryStatsResponse{}, err
	}
	response := control.LibraryStatsResponse{APIVersion: control.Version, ServerTime: s.now(), Libraries: make([]control.LibraryStatsEntry, 0, len(stats))}
	for _, stat := range stats {
		response.Libraries = append(response.Libraries, control.LibraryStatsEntry{
			Library: string(stat.LibraryName), Jobs: stat.Jobs,
			InputSizeBytes: stat.InputSizeBytes, OutputSizeBytes: stat.OutputSizeBytes,
			SavedBytes: stat.SavedBytes, SavedPercent: stat.SavedPercent,
		})
	}
	return response, nil
}

// ForceOccurrence resolves one exact library-relative media path through the
// configured scan rules and explicitly creates the next occurrence for it.
func (s Service) ForceOccurrence(ctx context.Context, request control.ForceOccurrenceRequest) (control.ForceOccurrenceResponse, error) {
	if err := s.requireStore(); err != nil {
		return control.ForceOccurrenceResponse{}, err
	}
	if s.Scanner == nil {
		return control.ForceOccurrenceResponse{}, newError(control.CodeInternal, "control service scanner is required")
	}
	name := strings.TrimSpace(request.Library)
	if name == "" {
		return control.ForceOccurrenceResponse{}, invalidArgumentf("a library is required")
	}
	library, ok := s.runtimeConfig().FindLibrary(domain.LibraryName(name))
	if !ok {
		return control.ForceOccurrenceResponse{}, notFoundf("library %q is not configured", name)
	}
	relativePath, err := control.CleanRelativePath(request.Path)
	if err != nil {
		return control.ForceOccurrenceResponse{}, err
	}
	candidate, err := s.forceOccurrenceCandidate(ctx, library, relativePath)
	if err != nil {
		return control.ForceOccurrenceResponse{}, err
	}
	result, err := s.Store.ForceOccurrence(ctx, store.ForceOccurrenceInput{
		LibraryName:        domain.LibraryName(library.Name),
		SourceKind:         candidate.SourceKind,
		SourceRelativePath: candidate.SourceRelativePath,
		SourceFingerprint: domain.FileFingerprint{
			SizeBytes: candidate.SourceSizeBytes,
			ModTime:   candidate.SourceModTime,
		},
		AssetRelativePath: candidate.AssetRelativePath,
		AssetRole:         candidate.Role,
		AssetFingerprint: domain.FileFingerprint{
			SizeBytes: candidate.SizeBytes,
			ModTime:   candidate.ModTime,
		},
		Priority: library.Priority,
		Now:      s.now(),
	})
	if errors.Is(err, store.ErrActiveWork) {
		// Active work is an operator-fixable state, not a daemon failure: the
		// answer is to wait for or cancel the running job.
		return control.ForceOccurrenceResponse{}, invalidArgumentf("library %q path %q still has active work; wait for it or cancel it first", library.Name, relativePath)
	}
	if err != nil {
		return control.ForceOccurrenceResponse{}, fmt.Errorf("force occurrence for library %q path %q: %w", library.Name, relativePath, err)
	}
	slog.Info("control forced an occurrence", "library", library.Name, "path", relativePath, "job", result.Job.Label())
	return control.ForceOccurrenceResponse{
		APIVersion: control.Version, ServerTime: s.now(),
		Library: library.Name, Path: relativePath,
		SourceID: int64(result.Source.ID), SourceGeneration: result.Source.Generation,
		AssetID: int64(result.Asset.ID), AssetGeneration: result.Asset.Generation,
		JobID: int64(result.Job.ID), JobSlug: result.Job.Label(), JobState: string(result.Job.State),
	}, nil
}

// forceOccurrenceCandidate refuses everything the scanner itself would refuse.
// Forcing an occurrence bypasses discovery, not the safety rules discovery
// applies: an ignored, unstable, ambiguous, or non-enqueueable target is a
// mistake in every case, and enqueueing it anyway is how a half-written
// download gets encoded.
func (s Service) forceOccurrenceCandidate(ctx context.Context, library config.LibraryConfig, relativePath string) (scanner.CandidatePlan, error) {
	plan, err := s.Scanner.PlanLibrary(ctx, library)
	if err != nil {
		return scanner.CandidatePlan{}, fmt.Errorf("plan library %q for forced occurrence: %w", library.Name, err)
	}
	var target scanner.CandidatePlan
	found := false
	for _, candidate := range plan.Candidates {
		if candidate.LibraryRelativePath != relativePath {
			continue
		}
		if found {
			return scanner.CandidatePlan{}, invalidArgumentf("target %q is ambiguous in library %q", relativePath, library.Name)
		}
		target = candidate
		found = true
	}
	switch {
	case !found:
		return scanner.CandidatePlan{}, notFoundf("target %q was not found in library %q", relativePath, library.Name)
	case target.Ignored:
		return scanner.CandidatePlan{}, invalidArgumentf("target %q is ignored: %s", relativePath, target.IgnoreReason)
	case target.Unstable:
		return scanner.CandidatePlan{}, invalidArgumentf("target %q is still unstable", relativePath)
	case !target.Enqueueable:
		return scanner.CandidatePlan{}, invalidArgumentf("target %q is not enqueueable", relativePath)
	}
	return target, nil
}

// CleanupStaging removes stale staging directories. It never touches a
// directory belonging to a job that is still active or still owns an unresolved
// publish journal, because directory age cannot distinguish an abandoned
// staging directory from the working directory of a long encode.
func (s Service) CleanupStaging(ctx context.Context, request control.StagingCleanupRequest) (control.StagingCleanupResponse, error) {
	if err := s.requireStore(); err != nil {
		return control.StagingCleanupResponse{}, err
	}
	cfg := s.runtimeConfig()
	age := cfg.StagingCleanupAge()
	requested := strings.TrimSpace(request.OlderThan)
	if requested != "" {
		parsed, err := time.ParseDuration(requested)
		if err != nil {
			return control.StagingCleanupResponse{}, invalidArgumentf("parse older_than: %v", err)
		}
		age = parsed
	}
	// An age of zero removes nothing: pkg/staging refuses to sweep without a
	// cutoff, because "everything older than now" is every directory including
	// the one an encode created a second ago. Reporting that as a completed
	// cleanup of zero candidates is how an operator concludes staging is clean
	// when it was never looked at, so it is refused instead — differently
	// depending on whether they chose the zero or inherited it.
	switch {
	case age < 0:
		return control.StagingCleanupResponse{}, invalidArgumentf("staging cleanup age must be non-negative")
	case age == 0 && requested == "":
		return control.StagingCleanupResponse{}, invalidArgumentf(
			"daemon.staging_cleanup_age is 0s, which disables age-based cleanup; pass an explicit older_than such as 24h")
	case age == 0:
		return control.StagingCleanupResponse{}, invalidArgumentf(
			"older_than must be greater than zero; an age of 0s would name every staging directory, including one a running encode just created")
	}
	result, err := SweepStaging(ctx, s.Store, staging.Root(cfg.Daemon.TempDir), StagingSweep{
		OlderThan: age, Now: s.now(), DryRun: request.DryRun,
	})
	if err != nil {
		return control.StagingCleanupResponse{}, err
	}
	response := control.StagingCleanupResponse{
		APIVersion: control.Version, ServerTime: s.now(), DryRun: request.DryRun,
		Root: result.Root, OlderThan: age.String(),
		Candidates: result.Candidates, Removed: result.Removed, Skipped: result.Skipped,
		Protected: result.Protected, Errors: result.Errors, ProtectedJobs: result.ProtectedJobs,
	}
	if !request.DryRun && (result.Removed > 0 || len(result.Errors) > 0) {
		slog.Info("control cleaned staging", "removed", result.Removed, "protected", result.Protected, "errors", len(result.Errors))
	}
	return response, nil
}

// BackupStore writes a consistent snapshot of the live SQLite store. The
// destination is resolved by the daemon, which refuses URI destinations, the
// live database, and any path that already exists.
func (s Service) BackupStore(ctx context.Context, request control.StoreBackupRequest) (control.StoreBackupResponse, error) {
	if err := s.requireStore(); err != nil {
		return control.StoreBackupResponse{}, err
	}
	destination := strings.TrimSpace(request.Destination)
	if destination == "" {
		return control.StoreBackupResponse{}, invalidArgumentf("a backup destination path is required")
	}
	if !filepath.IsAbs(destination) {
		// The daemon's working directory is not the operator's, so a relative
		// destination would land somewhere neither of them predicted.
		return control.StoreBackupResponse{}, invalidArgumentf("backup destination %q must be absolute, because the daemon resolves it", destination)
	}
	result, err := s.Store.Backup(ctx, destination)
	if err != nil {
		return control.StoreBackupResponse{}, err
	}
	slog.Info("control backed up the store", "path", result.Path, "size_bytes", result.SizeBytes)
	return control.StoreBackupResponse{
		APIVersion: control.Version, ServerTime: s.now(),
		Path: result.Path, SizeBytes: result.SizeBytes, Integrity: result.Integrity,
	}, nil
}

func protectedJobs(jobs []store.ProtectedJob) []control.ProtectedJob {
	if len(jobs) == 0 {
		return nil
	}
	result := make([]control.ProtectedJob, 0, len(jobs))
	for _, job := range jobs {
		result = append(result, control.ProtectedJob{ID: int64(job.JobID), Slug: job.Slug, Reason: string(job.Reason)})
	}
	return result
}

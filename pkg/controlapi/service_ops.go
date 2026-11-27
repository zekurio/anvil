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
	"github.com/zekurio/anvil/pkg/replace"
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
	plan, err := s.Scanner.PlanLibrary(ctx, library)
	if err != nil {
		return control.ForceOccurrenceResponse{}, fmt.Errorf("plan library %q for forced occurrence: %w", library.Name, err)
	}
	return s.forcePlannedOccurrence(ctx, library, plan, request.Path)
}

// ForceRefusal names why a path was not requeued. It is stable and
// machine-readable; the error next to it carries the words.
type ForceRefusal string

const (
	// ForceRefusedNotFound: the library holds no media file at the path.
	ForceRefusedNotFound ForceRefusal = "not_found"
	// ForceRefusedDenied: the library's rules deny the file.
	ForceRefusedDenied ForceRefusal = "denied"
	// ForceRefusedIgnored: the scanner leaves the file out on its own
	// account: a sample, download residue, or one of Anvil's own outputs.
	ForceRefusedIgnored ForceRefusal = "ignored"
	// ForceRefusedUnstable: the download is still being written.
	ForceRefusedUnstable ForceRefusal = "unstable"
	// ForceRefusedActive: the file has a job that is queued or running.
	ForceRefusedActive ForceRefusal = "active"
	// ForceRefusedInvalid: the path cannot name one file to queue.
	ForceRefusedInvalid ForceRefusal = "invalid"
)

// forceRefusedError is a refusal together with the control error that words
// it, so both front doors report the same sentence.
type forceRefusedError struct {
	refusal ForceRefusal
	err     error
}

func (e *forceRefusedError) Error() string { return e.err.Error() }
func (e *forceRefusedError) Unwrap() error { return e.err }

func refused(refusal ForceRefusal, err error) error {
	return &forceRefusedError{refusal: refusal, err: err}
}

// ForcedOccurrence is the outcome of one path of ForceOccurrences: the job it
// created, or why it created none.
type ForcedOccurrence struct {
	Path     string
	Response control.ForceOccurrenceResponse
	Err      error
	// Refusal is set when Err is a refusal that concerns this path alone. An
	// Err without one is a failure of the daemon.
	Refusal ForceRefusal
}

// ForceOccurrences is ForceOccurrence for many paths of one library. The
// library is planned once, so requeueing a selection of files walks it once
// and not once per file. One path's refusal does not stop the others; only a
// failure to plan the library fails the call.
func (s Service) ForceOccurrences(ctx context.Context, libraryName string, paths []string) ([]ForcedOccurrence, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	if s.Scanner == nil {
		return nil, newError(control.CodeInternal, "control service scanner is required")
	}
	name := strings.TrimSpace(libraryName)
	library, ok := s.runtimeConfig().FindLibrary(domain.LibraryName(name))
	if !ok {
		return nil, notFoundf("library %q is not configured", name)
	}
	plan, err := s.Scanner.PlanLibrary(ctx, library)
	if err != nil {
		return nil, fmt.Errorf("plan library %q for forced occurrence: %w", library.Name, err)
	}
	results := make([]ForcedOccurrence, 0, len(paths))
	for _, path := range paths {
		response, err := s.forcePlannedOccurrence(ctx, library, plan, path)
		result := ForcedOccurrence{Path: path, Response: response, Err: err}
		var refusal *forceRefusedError
		if errors.As(err, &refusal) {
			result.Refusal = refusal.refusal
		}
		results = append(results, result)
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
	}
	return results, nil
}

// forcePlannedOccurrence forces one path against a library plan that was just
// built.
func (s Service) forcePlannedOccurrence(ctx context.Context, library config.LibraryConfig, plan scanner.LibraryPlan, path string) (control.ForceOccurrenceResponse, error) {
	relativePath, err := control.CleanRelativePath(path)
	if err != nil {
		return control.ForceOccurrenceResponse{}, refused(ForceRefusedInvalid, err)
	}
	candidate, err := forceOccurrenceCandidate(library, plan, relativePath)
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
		return control.ForceOccurrenceResponse{}, refused(ForceRefusedActive, invalidArgumentf("library %q path %q still has active work; wait for it or cancel it first", library.Name, relativePath))
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
func forceOccurrenceCandidate(library config.LibraryConfig, plan scanner.LibraryPlan, relativePath string) (scanner.CandidatePlan, error) {
	var target scanner.CandidatePlan
	found := false
	for _, candidate := range plan.Candidates {
		if candidate.LibraryRelativePath != relativePath {
			continue
		}
		if found {
			return scanner.CandidatePlan{}, refused(ForceRefusedInvalid, invalidArgumentf("target %q is ambiguous in library %q", relativePath, library.Name))
		}
		target = candidate
		found = true
	}
	switch {
	case !found:
		// A scan never looks inside a directory the rules prune, so a file
		// below one is not among the candidates. It is denied all the same,
		// and saying "not found" about a file the operator can see would send
		// them looking for the wrong problem.
		if rules, err := config.CompileRules(library.Rules, library.RuleFallback); err == nil && rules.Prunes(relativePath, false) {
			return scanner.CandidatePlan{}, refused(ForceRefusedDenied, invalidArgumentf("target %q is ignored: %s", relativePath, rules.Decide(config.RuleFile{Path: relativePath}).Reason))
		}
		return scanner.CandidatePlan{}, refused(ForceRefusedNotFound, notFoundf("target %q was not found in library %q", relativePath, library.Name))
	case target.Denied:
		return scanner.CandidatePlan{}, refused(ForceRefusedDenied, invalidArgumentf("target %q is ignored: %s", relativePath, target.IgnoreReason))
	case target.Ignored:
		return scanner.CandidatePlan{}, refused(ForceRefusedIgnored, invalidArgumentf("target %q is ignored: %s", relativePath, target.IgnoreReason))
	case target.Unstable:
		return scanner.CandidatePlan{}, refused(ForceRefusedUnstable, invalidArgumentf("target %q is still unstable", relativePath))
	case !target.Enqueueable:
		return scanner.CandidatePlan{}, refused(ForceRefusedInvalid, invalidArgumentf("target %q is not enqueueable", relativePath))
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
	if request.LegacyParts {
		var roots []string
		for _, library := range cfg.Libraries {
			root := library.Path
			if library.IsDownload() {
				root = library.Download.HandoffPath
			}
			absolute, err := filepath.Abs(root)
			if err != nil {
				return response, fmt.Errorf("resolve legacy cleanup root: %w", err)
			}
			roots = append(roots, absolute)
		}
		legacy, err := replace.SweepLegacyParts(ctx, s.Store, replace.LegacySweepOptions{
			Roots: roots, OlderThan: age, Now: s.now(), DryRun: request.DryRun,
		})
		response.LegacyParts = &control.LegacyPartCleanupSummary{Candidates: legacy.Candidates, Removed: legacy.Removed, Protected: legacy.Protected}
		if err != nil {
			response.Errors = append(response.Errors, err.Error())
		}
	}
	legacyRemoved := 0
	if response.LegacyParts != nil {
		legacyRemoved = response.LegacyParts.Removed
	}
	if !request.DryRun && (result.Removed > 0 || legacyRemoved > 0 || len(response.Errors) > 0) {
		slog.Info("control cleaned staging", "removed", result.Removed, "protected", result.Protected, "legacy_removed", legacyRemoved, "errors", len(response.Errors))
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

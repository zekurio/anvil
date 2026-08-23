package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zekurio/anvil/internal/textout"
	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/mediapath"
	"github.com/zekurio/anvil/pkg/pipeline"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/staging"
	"github.com/zekurio/anvil/pkg/store"
)

type preflightStore interface {
	FindMediaSourceByPath(context.Context, domain.LibraryName, string) (domain.MediaSource, bool, error)
	FindMediaAssetByPath(context.Context, domain.MediaSourceID, string) (domain.MediaAsset, bool, error)
	FindJobForTarget(context.Context, domain.MediaSourceID, domain.MediaAssetID) (domain.Job, bool, error)
}

type preflightReport struct {
	Summary    preflightSummary     `json:"summary"`
	Warnings   []string             `json:"warnings"`
	Candidates []preflightCandidate `json:"candidates"`
}

type preflightSummary struct {
	Libraries       int    `json:"libraries"`
	Candidates      int    `json:"candidates"`
	Shown           int    `json:"shown"`
	Ignored         int    `json:"ignored"`
	Unstable        int    `json:"unstable"`
	Enqueueable     int    `json:"enqueueable"`
	ExistingJobs    int    `json:"existing_jobs"`
	WouldEnqueue    int    `json:"would_enqueue"`
	Limit           int    `json:"limit"`
	StorePath       string `json:"store_path"`
	StoreAvailable  bool   `json:"store_available"`
	StoreReadOnly   bool   `json:"store_read_only"`
	LibraryFiltered string `json:"library_filter,omitempty"`
}

type preflightCandidate struct {
	Library     preflightLibrary `json:"library"`
	Source      preflightSource  `json:"source"`
	Asset       preflightAsset   `json:"asset"`
	Status      preflightStatus  `json:"status"`
	Paths       preflightPaths   `json:"paths"`
	Publish     preflightPublish `json:"publish"`
	Cleanup     preflightCleanup `json:"cleanup"`
	Warnings    []string         `json:"warnings"`
	Description string           `json:"description,omitempty"`
}

type preflightLibrary struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Root string `json:"root"`
}

type preflightSource struct {
	RelativePath string                   `json:"relative_path"`
	Kind         domain.SourceKind        `json:"kind"`
	Generation   int                      `json:"generation"`
	Current      bool                     `json:"current"`
	Status       domain.MediaSourceStatus `json:"status"`
	SizeBytes    int64                    `json:"size_bytes"`
	ModTime      time.Time                `json:"mod_time"`
}

type preflightAsset struct {
	RelativePath        string                  `json:"relative_path"`
	LibraryRelativePath string                  `json:"library_relative_path"`
	Role                domain.MediaAssetRole   `json:"role"`
	Generation          int                     `json:"generation"`
	Current             bool                    `json:"current"`
	Status              domain.MediaAssetStatus `json:"status"`
	SizeBytes           int64                   `json:"size_bytes"`
	ModTime             time.Time               `json:"mod_time"`
}

type preflightStatus struct {
	Ignored             bool            `json:"ignored"`
	IgnoreReason        string          `json:"ignore_reason,omitempty"`
	Unstable            bool            `json:"unstable"`
	Enqueueable         bool            `json:"enqueueable"`
	ExistingSource      bool            `json:"existing_source"`
	ExistingAsset       bool            `json:"existing_asset"`
	AlreadyHasJob       bool            `json:"already_has_job"`
	WouldEnqueueNewJob  bool            `json:"would_enqueue_new_job"`
	ExistingJobID       domain.JobID    `json:"existing_job_id,omitempty"`
	ExistingJobSlug     string          `json:"existing_job_slug,omitempty"`
	ExistingJobState    domain.JobState `json:"existing_job_state,omitempty"`
	ExistingAttemptHint string          `json:"existing_attempt_hint,omitempty"`
	OccurrenceAction    string          `json:"occurrence_action"`
}

type preflightPaths struct {
	Input       string `json:"input"`
	StagingDir  string `json:"staging_dir"`
	Destination string `json:"destination"`
	Output      string `json:"output"`
}

type preflightPublish struct {
	Action             string            `json:"action"`
	Mode               string            `json:"mode,omitempty"`
	CopyPath           string            `json:"copy_path,omitempty"`
	ReplaceTarget      string            `json:"replace_target,omitempty"`
	ReplacementBackup  string            `json:"replacement_backup,omitempty"`
	HandoffDestination string            `json:"handoff_destination,omitempty"`
	Destructive        bool              `json:"destructive"`
	Plan               map[string]string `json:"plan,omitempty"`
}

type preflightCleanup struct {
	DownloadCleanupSource    bool                        `json:"download_cleanup_source_media"`
	DownloadPruneEmptyDirs   bool                        `json:"download_prune_empty_dirs"`
	DownloadSourceMediaPath  string                      `json:"download_source_media_path,omitempty"`
	DownloadPruneStart       string                      `json:"download_prune_start,omitempty"`
	DownloadCleanupEntries   []replacepkg.CleanupEntry   `json:"download_cleanup_entries,omitempty"`
	DownloadCleanupDirs      []replacepkg.CleanupEntry   `json:"download_cleanup_directories,omitempty"`
	DownloadCleanupBlockers  []replacepkg.CleanupBlocker `json:"download_cleanup_blockers,omitempty"`
	DownloadCleanupPlanError string                      `json:"download_cleanup_plan_error,omitempty"`
	DownloadCleanupTriggered bool                        `json:"download_cleanup_triggered_by_handoff"`
}

func runPreflightCommand(ctx context.Context, cfg config.Config, opts options) error {
	state, storeAvailable, err := openPreflightStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)

	// The nil *store.SQLiteStore from the store-not-found path must not enter
	// the preflightStore interface: a typed nil passes the interface nil check
	// in buildPreflightCandidate and panics on the first lookup.
	var lookup preflightStore
	if storeAvailable {
		lookup = state
	}
	report, err := buildPreflightReport(ctx, cfg, opts, lookup, storeAvailable)
	if err != nil {
		return err
	}
	if opts.jsonOutput {
		return textout.WriteJSON(os.Stdout, report)
	}
	return writePreflightReport(os.Stdout, report)
}

func openPreflightStore(ctx context.Context, cfg config.Config) (*store.SQLiteStore, bool, error) {
	state, err := store.OpenReadOnly(ctx, cfg.Daemon.StorePath)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return state, true, nil
}

func buildPreflightReport(ctx context.Context, cfg config.Config, opts options, state preflightStore, storeAvailable bool) (preflightReport, error) {
	names, err := preflightLibraryNames(cfg, opts.libraryName)
	if err != nil {
		return preflightReport{}, err
	}

	report := preflightReport{
		Summary: preflightSummary{
			Limit:           opts.preflightLimit,
			StorePath:       cfg.Daemon.StorePath,
			StoreAvailable:  storeAvailable,
			StoreReadOnly:   true,
			LibraryFiltered: opts.libraryName,
		},
	}
	if opts.libraryName == "" && len(names) > 1 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("broad preflight scans %d configured libraries; use --library to inspect one library", len(names)))
	}
	if opts.libraryName == "" && opts.preflightLimit == 0 {
		report.Warnings = append(report.Warnings, "broad preflight has no --limit; output can be large on real libraries")
	}
	if !storeAvailable {
		report.Warnings = append(report.Warnings, "SQLite store was not found; existing source, asset, and job status is reported as absent")
	}

	for _, name := range names {
		libraryConfig, ok := cfg.FindLibrary(domain.LibraryName(name))
		if !ok {
			return preflightReport{}, fmt.Errorf("library %q not found", name)
		}
		library, _, err := cfg.ResolveForLibrary(domain.LibraryName(name))
		if err != nil {
			return preflightReport{}, err
		}
		plan, err := (scanner.Scanner{}).PlanLibrary(ctx, libraryConfig)
		if err != nil {
			return preflightReport{}, fmt.Errorf("preflight library %q: %w", name, err)
		}
		report.Summary.Libraries++
		for _, candidate := range plan.Candidates {
			item, err := buildPreflightCandidate(ctx, cfg, state, candidate, library)
			if err != nil {
				return preflightReport{}, err
			}
			addPreflightSummary(&report.Summary, item)
			for _, warning := range item.Warnings {
				if strings.Contains(warning, "explicit exclude") {
					appendUnique(&report.Warnings, fmt.Sprintf("%s: %s", item.Library.Name, warning))
				}
			}
			if opts.preflightLimit <= 0 || report.Summary.Shown < opts.preflightLimit {
				report.Candidates = append(report.Candidates, item)
				report.Summary.Shown++
			}
		}
	}
	return report, nil
}

func buildPreflightCandidate(ctx context.Context, cfg config.Config, state preflightStore, candidate scanner.CandidatePlan, library domain.Library) (preflightCandidate, error) {
	source := domain.MediaSource{
		LibraryName:  candidate.LibraryName,
		Kind:         candidate.SourceKind,
		RelativePath: candidate.SourceRelativePath,
		Generation:   1,
		Current:      true,
		Status:       domain.MediaSourceActive,
		Fingerprint: domain.FileFingerprint{
			SizeBytes: candidate.SourceSizeBytes,
			ModTime:   candidate.SourceModTime,
		},
	}
	asset := domain.MediaAsset{
		RelativePath: candidate.AssetRelativePath,
		Generation:   1,
		Current:      true,
		Role:         candidate.Role,
		Status:       domain.MediaAssetActive,
		Fingerprint: domain.FileFingerprint{
			SizeBytes: candidate.SizeBytes,
			ModTime:   candidate.ModTime,
		},
	}
	status := preflightStatus{
		Ignored:            candidate.Ignored,
		IgnoreReason:       candidate.IgnoreReason,
		Unstable:           candidate.Unstable,
		Enqueueable:        candidate.Enqueueable,
		WouldEnqueueNewJob: candidate.Enqueueable,
		OccurrenceAction:   "new_source_generation",
	}
	if candidate.Ignored {
		status.OccurrenceAction = "ignored"
	} else if candidate.Unstable {
		status.OccurrenceAction = "deferred_unstable"
	}
	if state != nil && !candidate.Ignored && !candidate.Unstable {
		storedSource, ok, err := state.FindMediaSourceByPath(ctx, candidate.LibraryName, candidate.SourceRelativePath)
		if err != nil {
			return preflightCandidate{}, err
		}
		if ok {
			status.ExistingSource = true
			storedGeneration := max(storedSource.Generation, 1)
			reuseSource := storedSource.Current && (candidate.SourceKind == domain.SourceKindPackage || preflightFingerprintMatches(storedSource.Fingerprint, source.Fingerprint))
			if reuseSource {
				source.ID = storedSource.ID
				source.Generation = storedGeneration
				source.Status = storedSource.Status
				status.OccurrenceAction = "retain_occurrence"
			} else {
				source.Generation = storedGeneration + 1
			}
			if reuseSource {
				storedAsset, ok, err := state.FindMediaAssetByPath(ctx, source.ID, candidate.AssetRelativePath)
				if err != nil {
					return preflightCandidate{}, err
				}
				if !ok {
					status.OccurrenceAction = "new_asset_generation"
					source.Status = domain.MediaSourceActive
				} else {
					status.ExistingAsset = true
					storedGeneration := max(storedAsset.Generation, 1)
					reuseAsset := storedAsset.Current && preflightFingerprintMatches(storedAsset.Fingerprint, asset.Fingerprint)
					if !reuseAsset {
						asset.Generation = storedGeneration + 1
						status.OccurrenceAction = "new_asset_generation"
						source.Status = domain.MediaSourceActive
					} else {
						asset.ID = storedAsset.ID
						asset.Generation = storedGeneration
						asset.Status = storedAsset.Status
						if storedAsset.Status == domain.MediaAssetProcessed {
							status.WouldEnqueueNewJob = false
						}
						job, ok, err := state.FindJobForTarget(ctx, source.ID, storedAsset.ID)
						if err != nil {
							return preflightCandidate{}, err
						}
						if ok {
							status.AlreadyHasJob = true
							status.WouldEnqueueNewJob = false
							status.ExistingJobID = job.ID
							status.ExistingJobSlug = job.Label()
							status.ExistingJobState = job.State
							status.ExistingAttemptHint = "attempt-<new>"
						}
					}
				}
			}
		}
	}

	inputPath := mediapath.Input(library.Path, source, asset)
	jobLabel := "<new>"
	if status.AlreadyHasJob {
		jobLabel = status.ExistingJobSlug
	}
	stagingDir, err := staging.Manager{Root: staging.Root(cfg.Daemon.TempDir)}.Plan(jobLabel, "<new>")
	if err != nil {
		return preflightCandidate{}, err
	}
	jobContext := &pipeline.JobContext{
		Source:     source,
		Asset:      asset,
		Library:    library,
		InputPath:  inputPath,
		StagingDir: stagingDir,
	}
	destination, err := replacepkg.PlanDestination(jobContext)
	if err != nil {
		return preflightCandidate{}, err
	}
	jobContext.DestinationPath = destination
	jobContext.OutputPath = replacepkg.PartPath(destination, "<new>")

	item := preflightCandidate{
		Library: preflightLibrary{
			Name: string(library.Name),
			Kind: string(library.Kind),
			Root: library.Path,
		},
		Source: preflightSource{
			RelativePath: candidate.SourceRelativePath,
			Kind:         candidate.SourceKind,
			Generation:   source.Generation,
			Current:      source.Current,
			Status:       source.Status,
			SizeBytes:    candidate.SourceSizeBytes,
			ModTime:      candidate.SourceModTime,
		},
		Asset: preflightAsset{
			RelativePath:        candidate.AssetRelativePath,
			LibraryRelativePath: candidate.LibraryRelativePath,
			Role:                candidate.Role,
			Generation:          asset.Generation,
			Current:             asset.Current,
			Status:              asset.Status,
			SizeBytes:           candidate.SizeBytes,
			ModTime:             candidate.ModTime,
		},
		Status: status,
		Paths: preflightPaths{
			Input:       inputPath,
			StagingDir:  stagingDir,
			Destination: destination,
			Output:      jobContext.OutputPath,
		},
		Cleanup: preflightCleanupPlan(library, jobContext),
	}
	item.Publish, item.Warnings = preflightPublishPlan(library, jobContext)
	if item.Cleanup.DownloadCleanupSource {
		item.Warnings = append(item.Warnings, "download cleanup_source_media=true would remove source media after handoff")
	}
	if item.Cleanup.DownloadCleanupSource && item.Cleanup.DownloadPruneEmptyDirs {
		switch {
		case item.Cleanup.DownloadCleanupPlanError != "":
			item.Warnings = append(item.Warnings, "download residue cleanup planning failed: "+item.Cleanup.DownloadCleanupPlanError)
		case len(item.Cleanup.DownloadCleanupBlockers) > 0:
			item.Warnings = append(item.Warnings, fmt.Sprintf("download residue cleanup is blocked by %d unignorable or unsafe package entries; no package residue or directories would be removed", len(item.Cleanup.DownloadCleanupBlockers)))
		default:
			item.Warnings = append(item.Warnings, "download prune_empty_dirs=true would prune empty parent directories after journaled source and residue cleanup")
			if len(item.Cleanup.DownloadCleanupEntries) > 0 {
				item.Warnings = append(item.Warnings, fmt.Sprintf("download residue cleanup would journal and remove %d explicitly ignorable package files", len(item.Cleanup.DownloadCleanupEntries)))
			}
		}
	}
	item.Warnings = append(item.Warnings, preflightExcludeWarnings(candidate)...)
	item.Description = preflightDescription(item.Status)
	return item, nil
}

func preflightPublishPlan(library domain.Library, job *pipeline.JobContext) (preflightPublish, []string) {
	publish := preflightPublish{Action: "none"}
	var warnings []string
	if library.Kind == domain.LibraryKindDownload {
		plan, err := replacepkg.PlanHandoff(job)
		if err != nil {
			return preflightPublish{Action: "error", Plan: map[string]string{"error": err.Error()}}, warnings
		}
		publish.Action = "handoff"
		publish.Mode = plan.Action
		publish.HandoffDestination = plan.Destination
		publish.Destructive = plan.Mode == domain.HandoffModeMove
		if plan.Mode == domain.HandoffModeMove {
			warnings = append(warnings, "download handoff mode is move; encoded output is moved to the handoff destination")
		}
		return publish, warnings
	}
	plan, err := replacepkg.PlanReplacement(job.InputPath, filepath.Ext(job.DestinationPath), library.Media.ReplacementMode)
	if err != nil {
		return preflightPublish{Action: "error", Plan: map[string]string{"error": err.Error()}}, warnings
	}
	publish.Action = plan.Action
	publish.Mode = string(plan.Mode)
	publish.CopyPath = plan.CopyPath
	publish.ReplaceTarget = plan.ReplaceTarget
	publish.ReplacementBackup = plan.BackupPath
	publish.Destructive = plan.Action == "replace"
	if plan.Action == "replace" {
		warnings = append(warnings, "media replacement mode is replace; source media would be moved through an .anvil.bak backup and replaced")
	}
	return publish, warnings
}

func preflightCleanupPlan(library domain.Library, job *pipeline.JobContext) preflightCleanup {
	var cleanup preflightCleanup
	if library.Kind != domain.LibraryKindDownload {
		return cleanup
	}
	cleanup.DownloadCleanupTriggered = true
	cleanup.DownloadCleanupSource = library.Download.CleanupSourceMedia
	cleanup.DownloadPruneEmptyDirs = library.Download.PruneEmptyDirs
	cleanup.DownloadSourceMediaPath = job.InputPath
	cleanup.DownloadPruneStart = filepath.Dir(job.InputPath)
	if !cleanup.DownloadCleanupSource || !cleanup.DownloadPruneEmptyDirs {
		return cleanup
	}
	plan, err := replacepkg.PlanResidueCleanup(library.Path, job.InputPath, library.Download.IgnorableGlobs)
	if err != nil {
		cleanup.DownloadCleanupPlanError = err.Error()
		return cleanup
	}
	cleanup.DownloadPruneStart = plan.Start
	cleanup.DownloadCleanupEntries = append([]replacepkg.CleanupEntry(nil), plan.Entries...)
	cleanup.DownloadCleanupDirs = append([]replacepkg.CleanupEntry(nil), plan.Directories...)
	cleanup.DownloadCleanupBlockers = append([]replacepkg.CleanupBlocker(nil), plan.Blockers...)
	return cleanup
}

func preflightExcludeWarnings(candidate scanner.CandidatePlan) []string {
	lower := strings.ToLower(candidate.LibraryRelativePath)
	var warnings []string
	if replacepkg.IsAnvilCopyOutputPath(candidate.LibraryRelativePath) && !candidate.Ignored {
		warnings = append(warnings, "candidate path looks like an Anvil output; add an explicit exclude for .anvil outputs")
	}
	if (strings.Contains(lower, "/.staging/") || strings.HasPrefix(lower, ".staging/")) && !candidate.Ignored {
		warnings = append(warnings, "candidate path appears to be under staging; add an explicit staging exclude")
	}
	if candidate.IgnoreReason == "sample" {
		warnings = append(warnings, "sample was skipped by filename heuristic; add explicit sample excludes if this library contains sample files")
	}
	return warnings
}

func addPreflightSummary(summary *preflightSummary, item preflightCandidate) {
	summary.Candidates++
	if item.Status.Ignored {
		summary.Ignored++
	}
	if item.Status.Unstable {
		summary.Unstable++
	}
	if item.Status.Enqueueable {
		summary.Enqueueable++
	}
	if item.Status.AlreadyHasJob {
		summary.ExistingJobs++
	}
	if item.Status.WouldEnqueueNewJob {
		summary.WouldEnqueue++
	}
}

func preflightLibraryNames(cfg config.Config, libraryName string) ([]string, error) {
	if libraryName != "" {
		if _, ok := cfg.Libraries[libraryName]; !ok {
			return nil, fmt.Errorf("library %q not found", libraryName)
		}
		return []string{libraryName}, nil
	}
	names := make([]string, 0, len(cfg.Libraries))
	for name := range cfg.Libraries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func preflightFingerprintMatches(left, right domain.FileFingerprint) bool {
	return left.SizeBytes == right.SizeBytes && left.ModTime.Equal(right.ModTime) &&
		left.HashAlgorithm == right.HashAlgorithm && left.HashValue == right.HashValue
}

func preflightDescription(status preflightStatus) string {
	switch {
	case status.Ignored:
		if status.IgnoreReason != "" {
			return "ignored: " + status.IgnoreReason
		}
		return "ignored"
	case status.Unstable:
		return "unstable"
	case status.AlreadyHasJob:
		return "already has job"
	case status.WouldEnqueueNewJob:
		return "would enqueue new job"
	case status.Enqueueable:
		return "enqueueable"
	default:
		return "not enqueueable"
	}
}

func writePreflightReport(out io.Writer, report preflightReport) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Printf("preflight libraries=%d candidates=%d shown=%d ignored=%d unstable=%d enqueueable=%d existing_jobs=%d would_enqueue=%d store_read_only=%t\n",
			report.Summary.Libraries,
			report.Summary.Candidates,
			report.Summary.Shown,
			report.Summary.Ignored,
			report.Summary.Unstable,
			report.Summary.Enqueueable,
			report.Summary.ExistingJobs,
			report.Summary.WouldEnqueue,
			report.Summary.StoreReadOnly,
		)
		for _, warning := range report.Warnings {
			w.Printf("warning: %s\n", warning)
		}
		for _, item := range report.Candidates {
			w.Printf("\n[%s] %s %s role=%s source=%s source_generation=%d asset_generation=%d asset_size=%d asset_mod=%s\n",
				item.Description,
				item.Library.Name,
				item.Asset.LibraryRelativePath,
				item.Asset.Role,
				item.Source.Kind,
				item.Source.Generation,
				item.Asset.Generation,
				item.Asset.SizeBytes,
				item.Asset.ModTime.Format(time.RFC3339),
			)
			w.Printf("  library: kind=%s root=%s\n", item.Library.Kind, item.Library.Root)
			w.Printf("  source: %s kind=%s size=%d mod=%s asset: %s\n",
				item.Source.RelativePath,
				item.Source.Kind,
				item.Source.SizeBytes,
				item.Source.ModTime.Format(time.RFC3339),
				item.Asset.RelativePath,
			)
			w.Printf("  status: ignored=%t unstable=%t enqueueable=%t existing_job=%t would_enqueue=%t occurrence_action=%s\n",
				item.Status.Ignored,
				item.Status.Unstable,
				item.Status.Enqueueable,
				item.Status.AlreadyHasJob,
				item.Status.WouldEnqueueNewJob,
				item.Status.OccurrenceAction,
			)
			if item.Status.AlreadyHasJob {
				w.Printf("  job: %s (id=%d) state=%s attempt=%s\n", item.Status.ExistingJobSlug, item.Status.ExistingJobID, item.Status.ExistingJobState, item.Status.ExistingAttemptHint)
			}
			w.Printf("  input: %s\n", item.Paths.Input)
			w.Printf("  staging: %s -> %s\n", item.Paths.StagingDir, item.Paths.Output)
			writePreflightPublish(w, item.Publish)
			w.Printf("  cleanup: download_cleanup_source=%t prune_empty_dirs=%t\n",
				item.Cleanup.DownloadCleanupSource,
				item.Cleanup.DownloadPruneEmptyDirs,
			)
			if item.Cleanup.DownloadCleanupTriggered {
				w.Printf("  download cleanup: source=%s prune_start=%s\n", item.Cleanup.DownloadSourceMediaPath, item.Cleanup.DownloadPruneStart)
			}
			if item.Cleanup.DownloadCleanupPlanError != "" {
				w.Printf("  download residue cleanup planning error: %s\n", item.Cleanup.DownloadCleanupPlanError)
			}
			for _, entry := range item.Cleanup.DownloadCleanupEntries {
				w.Printf("  download residue cleanup planned: %s\n", entry.Path)
			}
			for _, directory := range item.Cleanup.DownloadCleanupDirs {
				w.Printf("  download empty directory cleanup planned: %s\n", directory.Path)
			}
			for _, blocker := range item.Cleanup.DownloadCleanupBlockers {
				w.Printf("  download residue cleanup blocker: %s: %s\n", blocker.Path, blocker.Reason)
			}
			for _, warning := range item.Warnings {
				w.Printf("  warning: %s\n", warning)
			}
		}
	})
}

func writePreflightPublish(w *textout.Writer, publish preflightPublish) {
	switch publish.Action {
	case "copy":
		w.Printf("  publish: copy %s\n", publish.CopyPath)
	case "replace":
		w.Printf("  publish: replace target=%s backup=%s\n", publish.ReplaceTarget, publish.ReplacementBackup)
	case "handoff":
		w.Printf("  publish: handoff mode=%s destination=%s\n", publish.Mode, publish.HandoffDestination)
	case "error":
		w.Printf("  publish: error %v\n", publish.Plan)
	default:
		w.Println("  publish: none")
	}
}

func appendUnique(values *[]string, value string) {
	for _, existing := range *values {
		if existing == value {
			return
		}
	}
	*values = append(*values, value)
}

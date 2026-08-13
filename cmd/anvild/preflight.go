package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	Library     preflightLibrary      `json:"library"`
	Source      preflightSource       `json:"source"`
	Asset       preflightAsset        `json:"asset"`
	Status      preflightStatus       `json:"status"`
	Flow        preflightFlow         `json:"flow"`
	Profile     preflightProfile      `json:"profile"`
	Search      preflightSearchPolicy `json:"search_policy"`
	Encode      preflightEncode       `json:"encode"`
	Paths       preflightPaths        `json:"paths"`
	Publish     preflightPublish      `json:"publish"`
	Cleanup     preflightCleanup      `json:"cleanup"`
	Warnings    []string              `json:"warnings"`
	Description string                `json:"description,omitempty"`
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

type preflightFlow struct {
	Name  domain.FlowName `json:"name"`
	Steps []string        `json:"steps"`
}

type preflightProfile struct {
	Name               domain.ProfileName       `json:"name"`
	Container          string                   `json:"container"`
	VideoCodec         string                   `json:"video_codec"`
	VideoAccelerator   string                   `json:"video_accelerator"`
	VideoBitDepth      int                      `json:"video_bit_depth"`
	CRFMin             int                      `json:"crf_min"`
	CRFMax             int                      `json:"crf_max"`
	Metric             domain.QualityMetric     `json:"metric"`
	Target             float64                  `json:"target"`
	ForceEncodeOnNoFit bool                     `json:"force_encode_on_no_fit"`
	SkipEncode         bool                     `json:"skip_encode"`
	FFmpegArgs         []string                 `json:"ffmpeg_args,omitempty"`
	ABAV1Args          []string                 `json:"ab_av1_args,omitempty"`
	DolbyVision        preflightDolbyVision     `json:"dolby_vision"`
	VideoOverrides     []preflightVideoOverride `json:"video_overrides,omitempty"`
}

// preflightDolbyVision reports the Dolby Vision policy only; the encoder
// settings used for Dolby Vision sources live in the dolby_vision entry of
// VideoOverrides.
type preflightDolbyVision struct {
	Mode            domain.DolbyVisionMode `json:"mode,omitempty"`
	RemoveHDR10Plus bool                   `json:"remove_hdr10plus"`
}

// preflightVideoOverride mirrors domain.VideoOverride so that a field the
// operator never configured stays absent instead of being reported as a zero.
type preflightVideoOverride struct {
	Key                string                `json:"key"`
	Codec              *string               `json:"codec,omitempty"`
	Accelerator        *string               `json:"accelerator,omitempty"`
	Preset             *string               `json:"preset,omitempty"`
	BitDepth           *int                  `json:"bit_depth,omitempty"`
	CRFMin             *int                  `json:"crf_min,omitempty"`
	CRFMax             *int                  `json:"crf_max,omitempty"`
	Metric             *domain.QualityMetric `json:"metric,omitempty"`
	Target             *float64              `json:"target,omitempty"`
	MinSavingsPercent  *float64              `json:"min_savings_percent,omitempty"`
	ForceEncodeOnNoFit *bool                 `json:"force_encode_on_no_fit,omitempty"`
	SkipEncode         *bool                 `json:"skip_encode,omitempty"`
	FFmpegArgs         []string              `json:"ffmpeg_args,omitempty"`
	ABAV1Args          []string              `json:"ab_av1_args,omitempty"`
}

type preflightSearchPolicy struct {
	Enabled                      bool     `json:"enabled"`
	Tool                         string   `json:"tool,omitempty"`
	CRFMin                       int      `json:"crf_min,omitempty"`
	CRFMax                       int      `json:"crf_max,omitempty"`
	Metric                       string   `json:"metric,omitempty"`
	Target                       string   `json:"target,omitempty"`
	SavingsPolicy                string   `json:"savings_policy,omitempty"`
	ForceEncodeOnNoFit           bool     `json:"force_encode_on_no_fit"`
	CustomArgs                   []string `json:"custom_args,omitempty"`
	DolbyVisionCustomArgs        []string `json:"dolby_vision_custom_args,omitempty"`
	MayDecideAV1FitNotWorthwhile bool     `json:"may_decide_av1_fit_not_worthwhile"`
	NoFitBehavior                string   `json:"no_fit_behavior,omitempty"`
	FlowCanFallbackToRemux       bool     `json:"flow_can_fallback_to_remux"`
}

type preflightEncode struct {
	Enabled               bool     `json:"enabled"`
	VideoAction           string   `json:"video_action"`
	Codec                 string   `json:"codec,omitempty"`
	CRFSource             string   `json:"crf_source,omitempty"`
	Output                string   `json:"output,omitempty"`
	NoFitAction           string   `json:"no_fit_action,omitempty"`
	AudioAction           string   `json:"audio_action,omitempty"`
	SubtitleAction        string   `json:"subtitle_action,omitempty"`
	MetadataAction        string   `json:"metadata_action,omitempty"`
	CustomArgs            []string `json:"custom_args,omitempty"`
	DolbyVisionAction     string   `json:"dolby_vision_action,omitempty"`
	DolbyVisionCustomArgs []string `json:"dolby_vision_custom_args,omitempty"`
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
	StagingCleanupStep       bool                        `json:"staging_cleanup_step"`
	StagingCleanupAction     string                      `json:"staging_cleanup_action"`
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
		library, flow, profile, err := cfg.ResolveForLibrary(domain.LibraryName(name))
		if err != nil {
			return preflightReport{}, err
		}
		plan, err := (scanner.Scanner{}).PlanLibrary(ctx, libraryConfig)
		if err != nil {
			return preflightReport{}, fmt.Errorf("preflight library %q: %w", name, err)
		}
		report.Summary.Libraries++
		for _, candidate := range plan.Candidates {
			item, err := buildPreflightCandidate(ctx, cfg, state, candidate, library, flow, profile)
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

func buildPreflightCandidate(ctx context.Context, cfg config.Config, state preflightStore, candidate scanner.CandidatePlan, library domain.Library, flow domain.Flow, profile domain.Profile) (preflightCandidate, error) {
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
	stagingPlan, err := staging.Manager{Root: staging.Root(cfg.Daemon.TempDir)}.Plan(jobLabel, "<new>")
	if err != nil {
		return preflightCandidate{}, err
	}
	jobContext := &pipeline.JobContext{
		Source:     source,
		Asset:      asset,
		Library:    library,
		Flow:       flow,
		Profile:    profile,
		InputPath:  inputPath,
		StagingDir: stagingPlan.StagingDir,
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
		Status:  status,
		Flow:    preflightFlow{Name: flow.Name, Steps: flowStepNames(flow)},
		Profile: preflightProfileFromDomain(profile),
		Search:  preflightSearch(flow, profile),
		Encode:  preflightEncodePlan(flow, profile, jobContext.OutputPath),
		Paths: preflightPaths{
			Input:       inputPath,
			StagingDir:  stagingPlan.StagingDir,
			Destination: destination,
			Output:      jobContext.OutputPath,
		},
		Cleanup: preflightCleanupPlan(flow, library, jobContext),
	}
	item.Publish, item.Warnings = preflightPublishPlan(flow, library, jobContext)
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

func preflightPublishPlan(flow domain.Flow, library domain.Library, job *pipeline.JobContext) (preflightPublish, []string) {
	publish := preflightPublish{Action: "none"}
	var warnings []string
	if library.Kind == domain.LibraryKindDownload && flowHasStep(flow, "handoff") {
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
	if library.Kind == domain.LibraryKindMedia && flowHasStep(flow, "replace") {
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
	return publish, warnings
}

func preflightCleanupPlan(flow domain.Flow, library domain.Library, job *pipeline.JobContext) preflightCleanup {
	cleanup := preflightCleanup{
		StagingCleanupStep:   flowHasStep(flow, "cleanup"),
		StagingCleanupAction: "none",
	}
	if cleanup.StagingCleanupStep {
		cleanup.StagingCleanupAction = "remove staging dir after configured cleanup step"
	}
	if library.Kind != domain.LibraryKindDownload || !flowHasStep(flow, "handoff") {
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

func preflightProfileFromDomain(profile domain.Profile) preflightProfile {
	return preflightProfile{
		Name:               profile.Name,
		Container:          profile.Container,
		VideoCodec:         profile.Video.Codec,
		VideoAccelerator:   profile.Video.Accelerator,
		VideoBitDepth:      profile.Video.BitDepth,
		CRFMin:             profile.Video.CRFMin,
		CRFMax:             profile.Video.CRFMax,
		Metric:             profile.Video.Metric,
		Target:             profile.Video.Target,
		ForceEncodeOnNoFit: profile.Video.ForceEncodeOnNoFit,
		SkipEncode:         profile.Video.SkipEncode,
		FFmpegArgs:         append([]string(nil), profile.Video.FFmpegArgs...),
		ABAV1Args:          append([]string(nil), profile.Video.ABAV1Args...),
		DolbyVision: preflightDolbyVision{
			Mode:            profile.Video.DolbyVision.Mode,
			RemoveHDR10Plus: profile.Video.DolbyVision.RemoveHDR10Plus,
		},
		VideoOverrides: preflightVideoOverrides(profile.Video),
	}
}

// preflightVideoOverrides lists overrides in the order they would be applied
// at runtime: source codec families sorted by key, then the reserved
// dolby_vision override, which applies last.
func preflightVideoOverrides(video domain.VideoProfile) []preflightVideoOverride {
	if len(video.Overrides) == 0 {
		return nil
	}
	keys := make([]string, 0, len(video.Overrides))
	for key := range video.Overrides {
		if key == domain.VideoOverrideDolbyVision {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if _, ok := video.Overrides[domain.VideoOverrideDolbyVision]; ok {
		keys = append(keys, domain.VideoOverrideDolbyVision)
	}
	overrides := make([]preflightVideoOverride, 0, len(keys))
	for _, key := range keys {
		override := video.Overrides[key]
		overrides = append(overrides, preflightVideoOverride{
			Key:                key,
			Codec:              clonePreflightValue(override.Codec),
			Accelerator:        clonePreflightValue(override.Accelerator),
			Preset:             clonePreflightValue(override.Preset),
			BitDepth:           clonePreflightValue(override.BitDepth),
			CRFMin:             clonePreflightValue(override.CRFMin),
			CRFMax:             clonePreflightValue(override.CRFMax),
			Metric:             clonePreflightValue(override.Metric),
			Target:             clonePreflightValue(override.Target),
			MinSavingsPercent:  clonePreflightValue(override.MinSavingsPercent),
			ForceEncodeOnNoFit: clonePreflightValue(override.ForceEncodeOnNoFit),
			SkipEncode:         clonePreflightValue(override.SkipEncode),
			FFmpegArgs:         append([]string(nil), override.FFmpegArgs...),
			ABAV1Args:          append([]string(nil), override.ABAV1Args...),
		})
	}
	return overrides
}

func clonePreflightValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// preflightDolbyVisionOverride returns the override that would shape a Dolby
// Vision encode. It reports false when Dolby Vision handling is off or the
// dolby_vision override has no codec, because the runtime only selects the
// Dolby Vision encoder when overrides.dolby_vision.codec is configured.
func preflightDolbyVisionOverride(profile domain.Profile) (domain.VideoOverride, bool) {
	if profile.Video.DolbyVision.Mode == domain.DolbyVisionModeOff {
		return domain.VideoOverride{}, false
	}
	override, ok := profile.Video.Overrides[domain.VideoOverrideDolbyVision]
	if !ok || override.Codec == nil || strings.TrimSpace(*override.Codec) == "" {
		return domain.VideoOverride{}, false
	}
	return override, true
}

func preflightSearch(flow domain.Flow, profile domain.Profile) preflightSearchPolicy {
	searchEnabled := flowHasStep(flow, "crf-search")
	if !searchEnabled || profile.Video.SkipEncode {
		return preflightSearchPolicy{Enabled: false, FlowCanFallbackToRemux: flowHasStep(flow, "encode")}
	}
	var dolbyVisionArgs []string
	if override, ok := preflightDolbyVisionOverride(profile); ok {
		dolbyVisionArgs = append([]string(nil), override.ABAV1Args...)
	}
	return preflightSearchPolicy{
		Enabled:                      true,
		Tool:                         "ab-av1 crf-search",
		CRFMin:                       profile.Video.CRFMin,
		CRFMax:                       profile.Video.CRFMax,
		Metric:                       string(profile.Video.Metric),
		Target:                       formatFloat(profile.Video.Target),
		SavingsPolicy:                "ab-av1/search policy; explicit min-savings is not configured",
		ForceEncodeOnNoFit:           profile.Video.ForceEncodeOnNoFit,
		CustomArgs:                   append([]string(nil), profile.Video.ABAV1Args...),
		DolbyVisionCustomArgs:        dolbyVisionArgs,
		MayDecideAV1FitNotWorthwhile: true,
		NoFitBehavior:                noFitBehavior(profile.Video.ForceEncodeOnNoFit),
		FlowCanFallbackToRemux:       !profile.Video.ForceEncodeOnNoFit && flowHasStep(flow, "encode"),
	}
}

func preflightEncodePlan(flow domain.Flow, profile domain.Profile, outputPath string) preflightEncode {
	if !flowHasStep(flow, "encode") {
		return preflightEncode{Enabled: false, VideoAction: "none"}
	}
	encode := preflightEncode{
		Enabled:        true,
		VideoAction:    strings.ToUpper(profile.Video.Codec) + " encode using CRF selected by search",
		Codec:          profile.Video.Codec,
		CRFSource:      "ab-av1 crf-search result",
		Output:         outputPath,
		AudioAction:    "copy/remux after configured audio cleanup selections",
		SubtitleAction: "copy/remux after configured subtitle cleanup selections",
		MetadataAction: fmt.Sprintf("apply metadata=%s, track_titles=%s, attachments=%s, chapters=%s, and Anvil marker policies", profile.Metadata.Mode, profile.Metadata.TrackTitles, profile.Attachments.Mode, profile.Chapters.Mode),
		CustomArgs:     append([]string(nil), profile.Video.FFmpegArgs...),
	}
	if profile.Video.SkipEncode {
		encode.VideoAction = "copy/remux video because encoding is disabled by profile"
		encode.Codec = ""
		encode.CRFSource = ""
		encode.CustomArgs = nil
		return encode
	}
	if override, ok := preflightDolbyVisionOverride(profile); ok {
		dolbyVisionAccelerator := profile.Video.Accelerator
		if override.Accelerator != nil {
			dolbyVisionAccelerator = *override.Accelerator
		}
		encode.DolbyVisionAction = fmt.Sprintf("if source has Dolby Vision and dovi_tool is available, use %s/%s instead of %s/%s", *override.Codec, textout.OrNone(dolbyVisionAccelerator), profile.Video.Codec, textout.OrNone(profile.Video.Accelerator))
		encode.DolbyVisionCustomArgs = append([]string(nil), override.FFmpegArgs...)
	}
	if !flowHasStep(flow, "crf-search") {
		encode.VideoAction = "encode/remux using profile defaults"
		encode.CRFSource = "profile default"
		return encode
	}
	encode.NoFitAction = noFitEncodeAction(profile.Video.ForceEncodeOnNoFit)
	return encode
}

func noFitBehavior(forceEncode bool) string {
	if forceEncode {
		return "if search cannot find a fitting CRF, force an encode with the lowest tested CRF instead of falling back to video-copy/remux"
	}
	return "if search decides AV1 fitting is not worthwhile, continue remaining configured actions as video-copy/remux/metadata processing without applying an AV1 CRF encode"
}

func noFitEncodeAction(forceEncode bool) string {
	if forceEncode {
		return "if search policy cannot find a fitting CRF, encode with the lowest tested CRF reported by ab-av1"
	}
	return "if search policy decides AV1 fitting is not worthwhile, skip AV1 CRF encode and continue remaining configured actions as video-copy/remux/metadata processing"
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

func flowStepNames(flow domain.Flow) []string {
	steps := make([]string, 0, len(flow.Steps))
	for _, step := range flow.Steps {
		steps = append(steps, step.Name)
	}
	return steps
}

func flowHasStep(flow domain.Flow, name string) bool {
	for _, step := range flow.Steps {
		if strings.EqualFold(step.Name, name) {
			return true
		}
	}
	return false
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
			w.Printf("  flow: %s [%s]\n", item.Flow.Name, strings.Join(item.Flow.Steps, " -> "))
			w.Printf("  profile: %s container=%s codec=%s accelerator=%s bit_depth=%d skip_encode=%t\n",
				item.Profile.Name,
				item.Profile.Container,
				item.Profile.VideoCodec,
				item.Profile.VideoAccelerator,
				item.Profile.VideoBitDepth,
				item.Profile.SkipEncode,
			)
			if item.Profile.DolbyVision.Mode != "" {
				w.Printf("  dolby-vision: mode=%s remove_hdr10plus=%t\n",
					item.Profile.DolbyVision.Mode,
					item.Profile.DolbyVision.RemoveHDR10Plus,
				)
			}
			for _, override := range item.Profile.VideoOverrides {
				fields := preflightVideoOverrideFields(override)
				if len(fields) == 0 {
					w.Printf("  video override %s: inherits base profile\n", override.Key)
					continue
				}
				w.Printf("  video override %s: %s\n", override.Key, strings.Join(fields, " "))
			}
			if item.Search.Enabled {
				w.Printf("  search: %s crf=%d..%d metric=%s target=%s savings_policy=%s\n",
					item.Search.Tool,
					item.Search.CRFMin,
					item.Search.CRFMax,
					item.Search.Metric,
					item.Search.Target,
					item.Search.SavingsPolicy,
				)
				if len(item.Search.CustomArgs) > 0 || len(item.Search.DolbyVisionCustomArgs) > 0 {
					w.Printf("  search args: custom=%v dolby_vision=%v\n", item.Search.CustomArgs, item.Search.DolbyVisionCustomArgs)
				}
				w.Printf("  no-fit: %s\n", item.Search.NoFitBehavior)
			}
			w.Printf("  encode: enabled=%t video=%s output=%s\n", item.Encode.Enabled, item.Encode.VideoAction, item.Encode.Output)
			if len(item.Encode.CustomArgs) > 0 || len(item.Encode.DolbyVisionCustomArgs) > 0 {
				w.Printf("  encode args: custom=%v dolby_vision=%v\n", item.Encode.CustomArgs, item.Encode.DolbyVisionCustomArgs)
			}
			if item.Encode.DolbyVisionAction != "" {
				w.Printf("  encode dolby-vision: %s\n", item.Encode.DolbyVisionAction)
			}
			if item.Encode.NoFitAction != "" {
				w.Printf("  encode no-fit: %s\n", item.Encode.NoFitAction)
			}
			w.Printf("  input: %s\n", item.Paths.Input)
			w.Printf("  staging: %s -> %s\n", item.Paths.StagingDir, item.Paths.Output)
			writePreflightPublish(w, item.Publish)
			w.Printf("  cleanup: staging=%s download_cleanup_source=%t prune_empty_dirs=%t\n",
				item.Cleanup.StagingCleanupAction,
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

// preflightVideoOverrideFields renders only the fields the override actually
// sets, so an inherited field is never shown as a configured zero.
func preflightVideoOverrideFields(override preflightVideoOverride) []string {
	var fields []string
	if override.Codec != nil {
		fields = append(fields, "codec="+textout.OrNone(*override.Codec))
	}
	if override.Accelerator != nil {
		fields = append(fields, "accelerator="+textout.OrNone(*override.Accelerator))
	}
	if override.Preset != nil {
		fields = append(fields, "preset="+textout.OrNone(*override.Preset))
	}
	if override.BitDepth != nil {
		fields = append(fields, fmt.Sprintf("bit_depth=%d", *override.BitDepth))
	}
	if override.CRFMin != nil {
		fields = append(fields, fmt.Sprintf("crf_min=%d", *override.CRFMin))
	}
	if override.CRFMax != nil {
		fields = append(fields, fmt.Sprintf("crf_max=%d", *override.CRFMax))
	}
	if override.Metric != nil {
		fields = append(fields, "metric="+string(*override.Metric))
	}
	if override.Target != nil {
		fields = append(fields, "target="+strconv.FormatFloat(*override.Target, 'f', -1, 64))
	}
	if override.MinSavingsPercent != nil {
		fields = append(fields, "min_savings_percent="+strconv.FormatFloat(*override.MinSavingsPercent, 'f', -1, 64))
	}
	if override.ForceEncodeOnNoFit != nil {
		fields = append(fields, fmt.Sprintf("force_encode_on_no_fit=%t", *override.ForceEncodeOnNoFit))
	}
	if override.SkipEncode != nil {
		fields = append(fields, fmt.Sprintf("skip_encode=%t", *override.SkipEncode))
	}
	if len(override.FFmpegArgs) > 0 {
		fields = append(fields, fmt.Sprintf("ffmpeg_args=%v", override.FFmpegArgs))
	}
	if len(override.ABAV1Args) > 0 {
		fields = append(fields, fmt.Sprintf("ab_av1_args=%v", override.ABAV1Args))
	}
	return fields
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

func formatFloat(value float64) string {
	if value == 0 {
		return ""
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

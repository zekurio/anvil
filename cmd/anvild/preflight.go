package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/staging"
	"github.com/zekurio/anvil/pkg/store"
	"github.com/zekurio/anvil/pkg/worker"
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
	RelativePath string            `json:"relative_path"`
	Kind         domain.SourceKind `json:"kind"`
	SizeBytes    int64             `json:"size_bytes"`
	ModTime      time.Time         `json:"mod_time"`
}

type preflightAsset struct {
	RelativePath        string                `json:"relative_path"`
	LibraryRelativePath string                `json:"library_relative_path"`
	Role                domain.MediaAssetRole `json:"role"`
	SizeBytes           int64                 `json:"size_bytes"`
	ModTime             time.Time             `json:"mod_time"`
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
	ExistingJobState    domain.JobState `json:"existing_job_state,omitempty"`
	ExistingAttemptHint string          `json:"existing_attempt_hint,omitempty"`
}

type preflightFlow struct {
	Name  domain.FlowName `json:"name"`
	Steps []string        `json:"steps"`
}

type preflightProfile struct {
	Name       domain.ProfileName `json:"name"`
	Container  string             `json:"container"`
	VideoCodec string             `json:"video_codec"`
	CRFMin     int                `json:"crf_min"`
	CRFMax     int                `json:"crf_max"`
	TargetVMAF float64            `json:"target_vmaf"`
}

type preflightSearchPolicy struct {
	Enabled                      bool   `json:"enabled"`
	Tool                         string `json:"tool,omitempty"`
	CRFMin                       int    `json:"crf_min,omitempty"`
	CRFMax                       int    `json:"crf_max,omitempty"`
	TargetVMAF                   string `json:"target_vmaf,omitempty"`
	SavingsPolicy                string `json:"savings_policy,omitempty"`
	MayDecideAV1FitNotWorthwhile bool   `json:"may_decide_av1_fit_not_worthwhile"`
	NoFitBehavior                string `json:"no_fit_behavior,omitempty"`
	FlowCanFallbackToRemux       bool   `json:"flow_can_fallback_to_remux"`
}

type preflightEncode struct {
	Enabled        bool   `json:"enabled"`
	VideoAction    string `json:"video_action"`
	Codec          string `json:"codec,omitempty"`
	CRFSource      string `json:"crf_source,omitempty"`
	Output         string `json:"output,omitempty"`
	NoFitAction    string `json:"no_fit_action,omitempty"`
	AudioAction    string `json:"audio_action,omitempty"`
	MetadataAction string `json:"metadata_action,omitempty"`
}

type preflightPaths struct {
	Input      string `json:"input"`
	StagingDir string `json:"staging_dir"`
	Output     string `json:"output"`
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
	StagingCleanupStep       bool   `json:"staging_cleanup_step"`
	StagingCleanupAction     string `json:"staging_cleanup_action"`
	DownloadCleanupSource    bool   `json:"download_cleanup_source_media"`
	DownloadPruneEmptyDirs   bool   `json:"download_prune_empty_dirs"`
	DownloadSourceMediaPath  string `json:"download_source_media_path,omitempty"`
	DownloadPruneStart       string `json:"download_prune_start,omitempty"`
	DownloadCleanupTriggered bool   `json:"download_cleanup_triggered_by_handoff"`
}

func runPreflightCommand(ctx context.Context, cfg config.Config, opts options) error {
	state, storeAvailable, err := openPreflightStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)

	report, err := buildPreflightReport(ctx, cfg, opts, state, storeAvailable)
	if err != nil {
		return err
	}
	if opts.jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	printPreflightReport(report)
	return nil
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
		Fingerprint: domain.FileFingerprint{
			SizeBytes: candidate.SourceSizeBytes,
			ModTime:   candidate.SourceModTime,
		},
	}
	asset := domain.MediaAsset{
		RelativePath: candidate.AssetRelativePath,
		Role:         candidate.Role,
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
	}
	if state != nil {
		storedSource, ok, err := state.FindMediaSourceByPath(ctx, candidate.LibraryName, candidate.SourceRelativePath)
		if err != nil {
			return preflightCandidate{}, err
		}
		if ok {
			status.ExistingSource = true
			source.ID = storedSource.ID
			storedAsset, ok, err := state.FindMediaAssetByPath(ctx, storedSource.ID, candidate.AssetRelativePath)
			if err != nil {
				return preflightCandidate{}, err
			}
			if ok {
				status.ExistingAsset = true
				asset.ID = storedAsset.ID
				job, ok, err := state.FindJobForTarget(ctx, storedSource.ID, storedAsset.ID)
				if err != nil {
					return preflightCandidate{}, err
				}
				if ok {
					status.AlreadyHasJob = true
					status.WouldEnqueueNewJob = false
					status.ExistingJobID = job.ID
					status.ExistingJobState = job.State
					status.ExistingAttemptHint = "attempt-<new>"
				}
			}
		}
	}

	inputPath := worker.InputPath(library.Path, source, asset)
	jobLabel := "<new>"
	if status.AlreadyHasJob {
		jobLabel = strconv.FormatInt(int64(status.ExistingJobID), 10)
	}
	stagingPlan, err := staging.Manager{Root: stagingRoot(cfg)}.Plan(jobLabel, "<new>", profile.Container, inputPath)
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
		OutputPath: stagingPlan.OutputPath,
		StagingDir: stagingPlan.StagingDir,
	}

	item := preflightCandidate{
		Library: preflightLibrary{
			Name: string(library.Name),
			Kind: string(library.Kind),
			Root: library.Path,
		},
		Source: preflightSource{
			RelativePath: candidate.SourceRelativePath,
			Kind:         candidate.SourceKind,
			SizeBytes:    candidate.SourceSizeBytes,
			ModTime:      candidate.SourceModTime,
		},
		Asset: preflightAsset{
			RelativePath:        candidate.AssetRelativePath,
			LibraryRelativePath: candidate.LibraryRelativePath,
			Role:                candidate.Role,
			SizeBytes:           candidate.SizeBytes,
			ModTime:             candidate.ModTime,
		},
		Status:  status,
		Flow:    preflightFlow{Name: flow.Name, Steps: flowStepNames(flow)},
		Profile: preflightProfile{Name: profile.Name, Container: profile.Container, VideoCodec: profile.Video.Codec, CRFMin: profile.Video.CRFMin, CRFMax: profile.Video.CRFMax, TargetVMAF: profile.Video.TargetVMAF},
		Search:  preflightSearch(flow, profile),
		Encode:  preflightEncodePlan(flow, profile, stagingPlan.OutputPath),
		Paths: preflightPaths{
			Input:      inputPath,
			StagingDir: stagingPlan.StagingDir,
			Output:     stagingPlan.OutputPath,
		},
		Cleanup: preflightCleanupPlan(flow, library, jobContext),
	}
	item.Publish, item.Warnings = preflightPublishPlan(flow, library, jobContext)
	if item.Cleanup.DownloadCleanupSource {
		item.Warnings = append(item.Warnings, "download cleanup_source_media=true would remove source media after handoff")
	}
	if item.Cleanup.DownloadCleanupSource && item.Cleanup.DownloadPruneEmptyDirs {
		item.Warnings = append(item.Warnings, "download prune_empty_dirs=true would prune empty parent directories after source cleanup")
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
		plan, err := replacepkg.PlanReplacement(job.InputPath, job.OutputPath, library.Media.ReplacementMode)
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
	if library.Kind == domain.LibraryKindDownload && flowHasStep(flow, "handoff") {
		cleanup.DownloadCleanupTriggered = true
		cleanup.DownloadCleanupSource = library.Download.CleanupSourceMedia
		cleanup.DownloadPruneEmptyDirs = library.Download.PruneEmptyDirs
		cleanup.DownloadSourceMediaPath = job.InputPath
		cleanup.DownloadPruneStart = filepath.Dir(job.InputPath)
	}
	return cleanup
}

func preflightSearch(flow domain.Flow, profile domain.Profile) preflightSearchPolicy {
	searchEnabled := flowHasStep(flow, "crf-search")
	if !searchEnabled {
		return preflightSearchPolicy{Enabled: false, FlowCanFallbackToRemux: flowHasStep(flow, "encode")}
	}
	return preflightSearchPolicy{
		Enabled:                      true,
		Tool:                         "ab-av1 crf-search",
		CRFMin:                       profile.Video.CRFMin,
		CRFMax:                       profile.Video.CRFMax,
		TargetVMAF:                   formatFloat(profile.Video.TargetVMAF),
		SavingsPolicy:                "ab-av1/search policy; explicit min-savings is not configured",
		MayDecideAV1FitNotWorthwhile: true,
		NoFitBehavior:                "if search decides AV1 fitting is not worthwhile, continue remaining configured actions as video-copy/remux/metadata processing without applying an AV1 CRF encode",
		FlowCanFallbackToRemux:       flowHasStep(flow, "encode"),
	}
}

func preflightEncodePlan(flow domain.Flow, profile domain.Profile, outputPath string) preflightEncode {
	if !flowHasStep(flow, "encode") {
		return preflightEncode{Enabled: false, VideoAction: "none"}
	}
	encode := preflightEncode{
		Enabled:        true,
		VideoAction:    "AV1 encode using CRF selected by search",
		Codec:          profile.Video.Codec,
		CRFSource:      "ab-av1 crf-search result",
		Output:         outputPath,
		AudioAction:    "copy/remux after configured audio cleanup selections",
		MetadataAction: "apply configured metadata, attachment, chapter, and Anvil marker policies",
	}
	if !flowHasStep(flow, "crf-search") {
		encode.VideoAction = "encode/remux using profile defaults"
		encode.CRFSource = "profile default"
		return encode
	}
	encode.NoFitAction = "if search policy decides AV1 fitting is not worthwhile, skip AV1 CRF encode and continue remaining configured actions as video-copy/remux/metadata processing"
	return encode
}

func preflightExcludeWarnings(candidate scanner.CandidatePlan) []string {
	lower := strings.ToLower(candidate.LibraryRelativePath)
	var warnings []string
	if strings.Contains(lower, ".anvil") && !candidate.Ignored {
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

func printPreflightReport(report preflightReport) {
	fmt.Fprintf(os.Stdout, "preflight libraries=%d candidates=%d shown=%d ignored=%d unstable=%d enqueueable=%d existing_jobs=%d would_enqueue=%d store_read_only=%t\n",
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
		fmt.Fprintf(os.Stdout, "warning: %s\n", warning)
	}
	for _, item := range report.Candidates {
		fmt.Fprintf(os.Stdout, "\n[%s] %s %s role=%s source=%s asset_size=%d asset_mod=%s\n",
			item.Description,
			item.Library.Name,
			item.Asset.LibraryRelativePath,
			item.Asset.Role,
			item.Source.Kind,
			item.Asset.SizeBytes,
			item.Asset.ModTime.Format(time.RFC3339),
		)
		fmt.Fprintf(os.Stdout, "  library: kind=%s root=%s\n", item.Library.Kind, item.Library.Root)
		fmt.Fprintf(os.Stdout, "  source: %s kind=%s size=%d mod=%s asset: %s\n",
			item.Source.RelativePath,
			item.Source.Kind,
			item.Source.SizeBytes,
			item.Source.ModTime.Format(time.RFC3339),
			item.Asset.RelativePath,
		)
		fmt.Fprintf(os.Stdout, "  status: ignored=%t unstable=%t enqueueable=%t existing_job=%t would_enqueue=%t\n",
			item.Status.Ignored,
			item.Status.Unstable,
			item.Status.Enqueueable,
			item.Status.AlreadyHasJob,
			item.Status.WouldEnqueueNewJob,
		)
		if item.Status.AlreadyHasJob {
			fmt.Fprintf(os.Stdout, "  job: id=%d state=%s attempt=%s\n", item.Status.ExistingJobID, item.Status.ExistingJobState, item.Status.ExistingAttemptHint)
		}
		fmt.Fprintf(os.Stdout, "  flow: %s [%s]\n", item.Flow.Name, strings.Join(item.Flow.Steps, " -> "))
		fmt.Fprintf(os.Stdout, "  profile: %s container=%s codec=%s\n", item.Profile.Name, item.Profile.Container, item.Profile.VideoCodec)
		if item.Search.Enabled {
			fmt.Fprintf(os.Stdout, "  search: %s crf=%d..%d target_vmaf=%s savings_policy=%s\n",
				item.Search.Tool,
				item.Search.CRFMin,
				item.Search.CRFMax,
				item.Search.TargetVMAF,
				item.Search.SavingsPolicy,
			)
			fmt.Fprintf(os.Stdout, "  no-fit: %s\n", item.Search.NoFitBehavior)
		}
		fmt.Fprintf(os.Stdout, "  encode: enabled=%t video=%s output=%s\n", item.Encode.Enabled, item.Encode.VideoAction, item.Encode.Output)
		if item.Encode.NoFitAction != "" {
			fmt.Fprintf(os.Stdout, "  encode no-fit: %s\n", item.Encode.NoFitAction)
		}
		fmt.Fprintf(os.Stdout, "  input: %s\n", item.Paths.Input)
		fmt.Fprintf(os.Stdout, "  staging: %s -> %s\n", item.Paths.StagingDir, item.Paths.Output)
		printPreflightPublish(item.Publish)
		fmt.Fprintf(os.Stdout, "  cleanup: staging=%s download_cleanup_source=%t prune_empty_dirs=%t\n",
			item.Cleanup.StagingCleanupAction,
			item.Cleanup.DownloadCleanupSource,
			item.Cleanup.DownloadPruneEmptyDirs,
		)
		for _, warning := range item.Warnings {
			fmt.Fprintf(os.Stdout, "  warning: %s\n", warning)
		}
	}
}

func printPreflightPublish(publish preflightPublish) {
	switch publish.Action {
	case "copy":
		fmt.Fprintf(os.Stdout, "  publish: copy %s\n", publish.CopyPath)
	case "replace":
		fmt.Fprintf(os.Stdout, "  publish: replace target=%s backup=%s\n", publish.ReplaceTarget, publish.ReplacementBackup)
	case "handoff":
		fmt.Fprintf(os.Stdout, "  publish: handoff mode=%s destination=%s\n", publish.Mode, publish.HandoffDestination)
	case "error":
		fmt.Fprintf(os.Stdout, "  publish: error %v\n", publish.Plan)
	default:
		fmt.Fprintln(os.Stdout, "  publish: none")
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

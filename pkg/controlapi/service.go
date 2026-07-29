package controlapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/mediapath"
	"github.com/zekurio/anvil/pkg/pipeline"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
	"github.com/zekurio/anvil/pkg/store"
)

type Store interface {
	ListJobs(context.Context, store.JobListFilter) ([]store.JobSummary, error)
	GetMediaSource(context.Context, domain.MediaSourceID) (domain.MediaSource, error)
	GetMediaAsset(context.Context, domain.MediaAssetID) (domain.MediaAsset, error)
	GetPublishOperation(context.Context, domain.JobID) (replacepkg.PublishOperation, bool, error)
	CountJobsByState(context.Context) (map[domain.JobState]int64, error)
	CancelJobs(context.Context, store.CancelJobsInput) ([]store.CancelJobResult, error)
	LatestAttemptArtifacts(context.Context, string, []domain.JobID) (map[domain.JobID][]domain.AttemptEvent, error)
}

type Service struct {
	Store         Store
	Config        func() config.Config
	ActiveWorkers func() int
	StartedAt     time.Time
	DaemonVersion string
	Now           func() time.Time
	// CancelRunningJob signals the worker currently executing a job and
	// reports whether one was signaled. It is optional: without it, cancel
	// still records the terminal state but cannot stop a live process.
	CancelRunningJob func(domain.JobID) bool
}

func (s Service) Status(ctx context.Context) (StatusResponse, error) {
	if s.Store == nil {
		return StatusResponse{}, errors.New("control API store is required")
	}
	counts, err := s.Store.CountJobsByState(ctx)
	if err != nil {
		return StatusResponse{}, err
	}
	queue := make(map[string]int64, len(allJobStates))
	for _, state := range allJobStates {
		queue[string(state)] = counts[state]
	}
	configured := 0
	if s.Config != nil {
		configured = s.Config().Daemon.WorkerCount
	}
	active := 0
	if s.ActiveWorkers != nil {
		active = s.ActiveWorkers()
	}
	startedAt := s.StartedAt.UTC()
	if startedAt.IsZero() {
		startedAt = s.now()
	}
	version := strings.TrimSpace(s.DaemonVersion)
	if version == "" {
		version = "dev"
	}
	return StatusResponse{
		APIVersion: Version,
		ServerTime: s.now(),
		Daemon:     DaemonStatus{State: "ready", StartedAt: startedAt, Version: version},
		Workers:    WorkerStatus{Configured: configured, Active: active},
		Queue:      queue,
	}, nil
}

func (s Service) ListJobs(ctx context.Context, query JobQuery) (JobListResponse, error) {
	if s.Store == nil {
		return JobListResponse{}, errors.New("control API store is required")
	}
	relativePath, absolutePath, states, err := normalizeJobQuery(query)
	if err != nil {
		return JobListResponse{}, err
	}
	filter := store.JobListFilter{LibraryName: domain.LibraryName(strings.TrimSpace(query.Library)), States: states}
	summaries, err := s.Store.ListJobs(ctx, filter)
	if err != nil {
		return JobListResponse{}, err
	}
	cfg := config.Config{}
	if s.Config != nil {
		cfg = s.Config()
	}
	response := JobListResponse{APIVersion: Version, ServerTime: s.now(), Jobs: make([]JobResponse, 0)}
	for _, summary := range summaries {
		item, keys, current, err := s.jobResponse(ctx, cfg, summary)
		if err != nil {
			return JobListResponse{}, err
		}
		if query.CurrentOnly && !current {
			continue
		}
		if relativePath != "" && !keys.matchesRelative(relativePath) {
			continue
		}
		if absolutePath != "" {
			sides, ok := keys.matchesAbsolute(absolutePath)
			if !ok {
				continue
			}
			item.MatchedOn = sides
		}
		response.Matched++
		if query.Limit > 0 && len(response.Jobs) >= query.Limit {
			response.Truncated = true
			continue
		}
		response.Jobs = append(response.Jobs, item)
	}
	if absolutePath != "" && response.Matched == 0 {
		response.PathOutsideLibraries = !pathUnderAnyLibrary(cfg, absolutePath)
	}
	if query.WithSelection {
		if err := s.attachStreamSelections(ctx, response.Jobs); err != nil {
			return JobListResponse{}, err
		}
	}
	return response, nil
}

// pathUnderAnyLibrary reports whether an absolute path lies under a configured
// library root or one of its handoff destinations. A path that lies under none
// of them can never match a job, which is a different answer from "no job
// matched" and has to stay distinguishable from it.
func pathUnderAnyLibrary(cfg config.Config, absolutePath string) bool {
	for name := range cfg.Libraries {
		library, ok := cfg.FindLibrary(domain.LibraryName(name))
		if !ok {
			continue
		}
		if pathUnderRoot(library.Path, absolutePath) {
			return true
		}
		if pathUnderRoot(library.Download.HandoffPath, absolutePath) {
			return true
		}
	}
	return false
}

func pathUnderRoot(root, absolutePath string) bool {
	root = strings.TrimSpace(root)
	if root == "" {
		return false
	}
	root = filepath.Clean(root)
	if absolutePath == root {
		return true
	}
	relative, err := filepath.Rel(root, absolutePath)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// attachStreamSelections fills in the recorded stream selection decisions for
// the listed jobs. A job that recorded none keeps a nil slice, so an absent
// decision stays distinguishable from one that kept every stream.
func (s Service) attachStreamSelections(ctx context.Context, jobs []JobResponse) error {
	if len(jobs) == 0 {
		return nil
	}
	ids := make([]domain.JobID, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, domain.JobID(job.ID))
	}
	events, err := s.Store.LatestAttemptArtifacts(ctx, pipeline.StreamSelectionArtifact, ids)
	if err != nil {
		return fmt.Errorf("load stream selections: %w", err)
	}
	for i, job := range jobs {
		recorded := events[domain.JobID(job.ID)]
		if len(recorded) == 0 {
			continue
		}
		selections := make([]StreamSelectionResponse, 0, len(recorded))
		for _, event := range recorded {
			selection := StreamSelectionResponse{AttemptID: int64(event.AttemptID), RecordedAt: event.CreatedAt}
			decision, err := pipeline.DecodeStreamSelection(event.Payload)
			if err != nil {
				// A decision Anvil cannot read is reported as unreadable rather
				// than omitted, so a consumer never reads it as "nothing here".
				// The decision itself stays absent for the same reason.
				selection.DecisionError = err.Error()
			} else {
				selection.Decision = &decision
			}
			selections = append(selections, selection)
		}
		jobs[i].StreamSelection = selections
	}
	return nil
}

// CancelJobs cancels every job the equivalent job list would return. It
// requires an explicit selector, and is idempotent for already terminal jobs.
func (s Service) CancelJobs(ctx context.Context, request JobCancelRequest) (JobCancelResponse, error) {
	if s.Store == nil {
		return JobCancelResponse{}, errors.New("control API store is required")
	}
	if !request.hasSelector() {
		return JobCancelResponse{}, invalidArgumentf("cancel requires at least one selector")
	}
	ids := make(map[domain.JobID]struct{}, len(request.IDs))
	for _, id := range request.IDs {
		if id <= 0 {
			return JobCancelResponse{}, invalidArgumentf("invalid job id %d", id)
		}
		ids[domain.JobID(id)] = struct{}{}
	}
	_, _, states, err := normalizeJobQuery(request.query())
	if err != nil {
		return JobCancelResponse{}, err
	}
	listed, err := s.ListJobs(ctx, request.query())
	if err != nil {
		return JobCancelResponse{}, err
	}
	targets := make([]domain.JobID, 0, len(listed.Jobs))
	matchedIDs := make(map[domain.JobID]struct{}, len(listed.Jobs))
	for _, job := range listed.Jobs {
		jobID := domain.JobID(job.ID)
		if len(ids) > 0 {
			if _, ok := ids[jobID]; !ok {
				continue
			}
		}
		matchedIDs[jobID] = struct{}{}
		targets = append(targets, jobID)
	}
	if missing := missingJobIDs(request.IDs, matchedIDs); missing != "" {
		return JobCancelResponse{}, invalidArgumentf("no job matched the selector for ids %s", missing)
	}
	response := JobCancelResponse{APIVersion: Version, ServerTime: s.now(), Jobs: make([]JobCancelResult, 0, len(targets))}
	if len(targets) == 0 {
		return response, nil
	}
	reason := strings.TrimSpace(request.Reason)
	results, err := s.Store.CancelJobs(ctx, store.CancelJobsInput{IDs: targets, States: states, Reason: reason, Now: s.now()})
	if err != nil {
		return JobCancelResponse{}, err
	}
	for _, result := range results {
		item := JobCancelResult{
			ID: int64(result.JobID), Slug: result.Slug, Library: string(result.LibraryName),
			PreviousState: string(result.PreviousState), State: string(result.State),
			Canceled: result.Canceled, SkipReason: string(result.SkipReason),
		}
		if result.Canceled && s.CancelRunningJob != nil {
			item.WorkerSignaled = s.CancelRunningJob(result.JobID)
		}
		if result.Canceled {
			response.Canceled++
		}
		response.Matched++
		response.Jobs = append(response.Jobs, item)
	}
	slog.Info("control API canceled jobs", "matched", response.Matched, "canceled", response.Canceled, "reason", reason)
	return response, nil
}

func missingJobIDs(requested []int64, matched map[domain.JobID]struct{}) string {
	missing := make([]string, 0, len(requested))
	seen := make(map[int64]struct{}, len(requested))
	for _, id := range requested {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if _, ok := matched[domain.JobID(id)]; !ok {
			missing = append(missing, strconv.FormatInt(id, 10))
		}
	}
	return strings.Join(missing, ", ")
}

// absolutePathKey is one absolute path a job answers to, tagged with which of
// the job's paths it is so a match can report the side it hit.
type absolutePathKey struct {
	path string
	side PathMatchSide
}

type jobPathKeys struct {
	relative []string
	absolute []absolutePathKey
}

func (k jobPathKeys) matchesRelative(target string) bool {
	for _, candidate := range k.relative {
		if candidate == target {
			return true
		}
	}
	return false
}

// matchesAbsolute reports every side that matched, in the order the keys were
// built. One path is legitimately several sides at once: an in-place
// replacement writes the converted file back over its own source, so returning
// a single side would report that output as "not a destination".
func (k jobPathKeys) matchesAbsolute(target string) ([]PathMatchSide, bool) {
	var sides []PathMatchSide
	for _, candidate := range k.absolute {
		if candidate.path == target {
			sides = append(sides, candidate.side)
		}
	}
	return sides, len(sides) > 0
}

// uniqueAbsoluteKeys normalizes paths and drops empty ones and exact repeats of
// the same path and side. A path repeated under a different side is kept, so a
// match can report all of them.
func uniqueAbsoluteKeys(keys ...absolutePathKey) []absolutePathKey {
	result := make([]absolutePathKey, 0, len(keys))
	seen := make(map[absolutePathKey]struct{}, len(keys))
	for _, key := range keys {
		key.path = strings.TrimSpace(key.path)
		if key.path == "" {
			continue
		}
		key.path = filepath.Clean(key.path)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}

func (s Service) jobResponse(ctx context.Context, cfg config.Config, summary store.JobSummary) (JobResponse, jobPathKeys, bool, error) {
	source, err := s.Store.GetMediaSource(ctx, summary.Job.SourceID)
	if err != nil {
		return JobResponse{}, jobPathKeys{}, false, fmt.Errorf("get source for job %d: %w", summary.Job.ID, err)
	}
	var asset domain.MediaAsset
	if summary.Job.AssetID != 0 {
		asset, err = s.Store.GetMediaAsset(ctx, summary.Job.AssetID)
		if err != nil {
			return JobResponse{}, jobPathKeys{}, false, fmt.Errorf("get asset for job %d: %w", summary.Job.ID, err)
		}
	}
	library, libraryOK := cfg.FindLibrary(summary.Job.LibraryName)
	sourceAbsolute := ""
	assetAbsolute := ""
	if libraryOK {
		sourceAbsolute = filepath.Clean(filepath.Join(library.Path, filepath.FromSlash(source.RelativePath)))
		assetAbsolute = filepath.Clean(mediapath.Input(library.Path, source, asset))
	}
	item := JobResponse{
		ID: int64(summary.Job.ID), Slug: summary.Job.Label(), Library: string(summary.Job.LibraryName),
		State: string(summary.Job.State), AttemptCount: summary.Job.AttemptCount,
		CreatedAt: summary.Job.CreatedAt, UpdatedAt: summary.Job.UpdatedAt, CompletedAt: summary.Job.CompletedAt,
		LeaseOwner: summary.Job.LeaseOwner, LeaseDeadline: summary.Job.LeaseDeadline,
		HeartbeatAt: summary.Job.HeartbeatAt, LastError: summary.Job.LastError,
		Source: OccurrenceResponse{
			Path: source.RelativePath, AbsolutePath: sourceAbsolute, Generation: source.Generation,
			Current: source.Current, Status: string(source.Status),
		},
	}
	if asset.ID != 0 {
		item.Asset = &OccurrenceResponse{
			Path: asset.RelativePath, AbsolutePath: assetAbsolute, Generation: asset.Generation,
			Current: asset.Current, Status: string(asset.Status),
		}
	}
	operation, hasOperation, err := s.Store.GetPublishOperation(ctx, summary.Job.ID)
	if err != nil {
		return JobResponse{}, jobPathKeys{}, false, err
	}
	if hasOperation {
		item.DestinationPath = filepath.Clean(operation.DestinationPath)
		item.PublishStage = string(operation.Stage)
	} else if libraryOK {
		item.DestinationPath = plannedDestination(cfg, summary.Job, source, asset)
	}
	keys := jobPathKeys{
		relative: uniquePaths(
			cleanStoredRelative(source.RelativePath),
			cleanStoredRelative(filepath.ToSlash(mediapath.Relative(source, asset))),
		),
		absolute: uniqueAbsoluteKeys(
			absolutePathKey{path: sourceAbsolute, side: PathMatchSource},
			absolutePathKey{path: assetAbsolute, side: PathMatchAsset},
			absolutePathKey{path: item.DestinationPath, side: PathMatchDestination},
		),
	}
	if source.Kind == domain.SourceKindPackage && item.DestinationPath != "" {
		packageDestinations := append(keys.absolute,
			absolutePathKey{path: filepath.Dir(item.DestinationPath), side: PathMatchDestinationDirectory},
		)
		if hasOperation && strings.TrimSpace(operation.HandoffRoot) != "" {
			packageDestinations = append(packageDestinations, absolutePathKey{
				path: filepath.Join(operation.HandoffRoot, filepath.FromSlash(source.RelativePath)),
				side: PathMatchDestinationDirectory,
			})
		} else if !hasOperation && libraryOK && library.Download.PreserveRelativePath && strings.TrimSpace(library.Download.HandoffPath) != "" {
			packageDestinations = append(packageDestinations, absolutePathKey{
				path: filepath.Join(library.Download.HandoffPath, filepath.FromSlash(source.RelativePath)),
				side: PathMatchDestinationDirectory,
			})
		}
		keys.absolute = uniqueAbsoluteKeys(packageDestinations...)
	}
	current := source.Current && (asset.ID == 0 || asset.Current)
	return item, keys, current, nil
}

func plannedDestination(cfg config.Config, job domain.Job, source domain.MediaSource, asset domain.MediaAsset) string {
	library, flow, profile, err := cfg.ResolveForLibrary(job.LibraryName)
	if err != nil {
		return ""
	}
	inputPath := mediapath.Input(library.Path, source, asset)
	ext := strings.TrimSpace(profile.Container)
	if ext == "" {
		ext = strings.TrimPrefix(filepath.Ext(inputPath), ".")
	}
	outputPath := "output"
	if ext != "" {
		outputPath += "." + strings.TrimPrefix(ext, ".")
	}
	jobContext := &pipeline.JobContext{
		Job: job, Source: source, Asset: asset, Library: library, Flow: flow,
		Profile: profile, InputPath: inputPath, OutputPath: outputPath,
	}
	for _, step := range flow.Steps {
		switch step.Name {
		case "handoff":
			plan, err := replacepkg.PlanHandoff(jobContext)
			if err == nil {
				return filepath.Clean(plan.Destination)
			}
			return ""
		case "replace":
			plan, err := replacepkg.PlanReplacement(inputPath, outputPath, library.Media.ReplacementMode)
			if err != nil {
				return ""
			}
			if plan.CopyPath != "" {
				return filepath.Clean(plan.CopyPath)
			}
			return filepath.Clean(plan.ReplaceTarget)
		}
	}
	return ""
}

// invalidArgumentError marks a caller-fixable request problem so transports can
// map it to a client error without matching error strings.
type invalidArgumentError struct {
	err error
}

func (e invalidArgumentError) Error() string {
	return e.err.Error()
}

func (e invalidArgumentError) Unwrap() error {
	return e.err
}

func invalidArgumentf(format string, args ...any) error {
	return invalidArgumentError{err: fmt.Errorf(format, args...)}
}

func normalizeJobQuery(query JobQuery) (string, string, []domain.JobState, error) {
	if query.Limit < 0 {
		return "", "", nil, invalidArgumentf("limit must be non-negative")
	}
	if strings.TrimSpace(query.Path) != "" && strings.TrimSpace(query.AbsolutePath) != "" {
		return "", "", nil, invalidArgumentf("path and absolute_path are mutually exclusive")
	}
	relativePath := ""
	if strings.TrimSpace(query.Path) != "" {
		if strings.TrimSpace(query.Library) == "" {
			return "", "", nil, invalidArgumentf("library is required with path")
		}
		cleaned, err := cleanRelativePath(query.Path)
		if err != nil {
			return "", "", nil, err
		}
		relativePath = cleaned
	}
	absolutePath := ""
	if strings.TrimSpace(query.AbsolutePath) != "" {
		if strings.ContainsRune(query.AbsolutePath, '\x00') || !filepath.IsAbs(query.AbsolutePath) {
			return "", "", nil, invalidArgumentf("absolute_path must be absolute")
		}
		absolutePath = filepath.Clean(query.AbsolutePath)
	}
	states := make([]domain.JobState, 0, len(query.States))
	seen := make(map[domain.JobState]struct{}, len(query.States))
	for _, value := range query.States {
		for _, part := range strings.Split(value, ",") {
			state := domain.JobState(strings.TrimSpace(part))
			if state == "" {
				continue
			}
			if !domain.ValidJobState(state) {
				return "", "", nil, invalidArgumentf("unknown job state %q", state)
			}
			if _, ok := seen[state]; ok {
				continue
			}
			seen[state] = struct{}{}
			states = append(states, state)
		}
	}
	return relativePath, absolutePath, states, nil
}

func cleanRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, '\x00') || filepath.IsAbs(value) {
		return "", invalidArgumentf("path must be relative to the library root")
	}
	cleaned := path.Clean(filepath.ToSlash(value))
	if cleaned == "." || cleaned == ".." || path.IsAbs(cleaned) || strings.HasPrefix(cleaned, "../") {
		return "", invalidArgumentf("path must stay within the library root")
	}
	return cleaned, nil
}

func cleanStoredRelative(value string) string {
	cleaned, err := cleanRelativePath(value)
	if err != nil {
		return ""
	}
	return cleaned
}

func uniquePaths(values ...string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if filepath.IsAbs(value) {
			value = filepath.Clean(value)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

var allJobStates = domain.JobStates()

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

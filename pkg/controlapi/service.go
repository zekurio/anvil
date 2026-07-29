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
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/store"
)

// Store is everything the control surface needs from persistence. It is wide
// because the daemon owns every live operation an operator can ask for; the
// point of the interface is that the control surface names exactly what it
// uses, and that tests can stand in for parts of it.
type Store interface {
	ListJobs(context.Context, store.JobListFilter) ([]store.JobSummary, error)
	GetJobSummary(context.Context, domain.JobID) (store.JobSummary, error)
	ResolveJobReference(context.Context, string) (domain.Job, error)
	GetMediaSource(context.Context, domain.MediaSourceID) (domain.MediaSource, error)
	GetMediaAsset(context.Context, domain.MediaAssetID) (domain.MediaAsset, error)
	GetPublishOperation(context.Context, domain.JobID) (replacepkg.PublishOperation, bool, error)
	CountJobsByState(context.Context) (map[domain.JobState]int64, error)
	CancelJobs(context.Context, store.CancelJobsInput) ([]store.CancelJobResult, error)
	LatestAttemptArtifacts(context.Context, string, []domain.JobID) (map[domain.JobID][]domain.AttemptEvent, error)
	ListAttemptsForJob(context.Context, domain.JobID) ([]domain.Attempt, error)
	ListAttemptEvents(context.Context, domain.AttemptID) ([]domain.AttemptEvent, error)
	GetJobPipelineContext(context.Context, domain.JobID) (domain.JobPipelineContext, bool, error)
	RetryJob(context.Context, domain.JobID, time.Time) (domain.Job, error)
	RetryFailedJobs(context.Context, domain.LibraryName, time.Time) (int64, error)
	RecoverStaleJobs(context.Context, int, time.Time) (int64, error)
	ListLibraryStats(context.Context, store.LibraryStatsFilter) ([]store.LibraryStats, error)
	PruneMissingSourceJobs(context.Context, store.PruneMissingSourceJobsOptions) (store.PruneMissingSourceJobsResult, error)
	ForceOccurrence(context.Context, store.ForceOccurrenceInput) (store.ForceOccurrenceResult, error)
	Backup(context.Context, string) (store.BackupResult, error)
	ProtectedJobs(context.Context) ([]store.ProtectedJob, error)
}

// Scanner is the daemon's own scanner. Scans requested over the control socket
// run in the daemon so they share its store handle, occurrence bookkeeping, and
// configuration instead of racing a second process against them.
type Scanner interface {
	Scan(context.Context, config.Config) (scanner.ScanResult, error)
	ScanLibrary(context.Context, config.LibraryConfig) (scanner.ScanResult, error)
	PlanLibrary(context.Context, config.LibraryConfig) (scanner.LibraryPlan, error)
}

type Service struct {
	Store         Store
	Scanner       Scanner
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
	if err := s.requireStore(); err != nil {
		return StatusResponse{}, err
	}
	counts, err := s.Store.CountJobsByState(ctx)
	if err != nil {
		return StatusResponse{}, err
	}
	queue := make(map[string]int64, len(allJobStates))
	for _, state := range allJobStates {
		queue[string(state)] = counts[state]
	}
	configured := s.runtimeConfig().Daemon.WorkerCount
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
	if err := s.requireStore(); err != nil {
		return JobListResponse{}, err
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
	cfg := s.runtimeConfig()
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
// library root or one of its handoff destinations. A path under none of them
// almost certainly cannot match a job, which is a different answer from "no job
// matched" and has to stay distinguishable from it.
//
// Containment is lexical, matching how paths are compared everywhere else here,
// so the flag can never contradict matched_on.
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

// assetKeyPath drops the asset key for a job that has no asset, so a match can
// never report an "asset" side the response itself omits.
func assetKeyPath(asset domain.MediaAsset, assetAbsolute string) string {
	if asset.ID == 0 {
		return ""
	}
	return assetAbsolute
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
	if err := s.requireStore(); err != nil {
		return JobCancelResponse{}, err
	}
	if !request.hasSelector() {
		return JobCancelResponse{}, invalidArgumentf("cancel requires at least one selector")
	}
	requestedIDs, err := s.resolveJobReferences(ctx, request.IDs, request.References)
	if err != nil {
		return JobCancelResponse{}, err
	}
	ids := make(map[domain.JobID]struct{}, len(requestedIDs))
	for _, id := range requestedIDs {
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
	// A named job outside the selector is refused rather than cancelled: the
	// two disagree about what the operator meant, and guessing either way can
	// cancel work nobody asked about.
	if missing := missingJobIDs(requestedIDs, matchedIDs); missing != "" {
		return JobCancelResponse{}, invalidArgumentf("no job matched the selector for job ids %s", missing)
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
	slog.Info("control canceled jobs", "matched", response.Matched, "canceled", response.Canceled, "reason", reason)
	return response, nil
}

// resolveJobReferences turns operator-supplied job references into ids. Both
// forms are accepted everywhere a job can be named: an id is what shows up in
// logs, a slug is what shows up in listings, and making an operator translate
// between them under pressure is how the wrong job gets canceled.
func (s Service) resolveJobReferences(ctx context.Context, ids []int64, references []string) ([]int64, error) {
	resolved := make([]int64, 0, len(ids)+len(references))
	for _, id := range ids {
		if id <= 0 {
			return nil, invalidArgumentf("invalid job id %d", id)
		}
		resolved = append(resolved, id)
	}
	for _, reference := range references {
		reference = strings.TrimSpace(reference)
		if reference == "" {
			return nil, invalidArgumentf("job reference must not be empty")
		}
		job, err := s.Store.ResolveJobReference(ctx, reference)
		if errors.Is(err, store.ErrNotFound) {
			return nil, notFoundf("no job matches reference %q", reference)
		}
		if err != nil {
			return nil, fmt.Errorf("resolve job %q: %w", reference, err)
		}
		resolved = append(resolved, int64(job.ID))
	}
	return resolved, nil
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
			absolutePathKey{path: assetKeyPath(asset, assetAbsolute), side: PathMatchAsset},
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

// invalidArgumentf marks a caller-fixable request problem. It produces the same
// structured error the transport returns, so the code an operator sees is
// decided once, here, rather than by string matching at the edge.
func invalidArgumentf(format string, args ...any) error {
	return newError(CodeInvalidArgument, format, args...)
}

func notFoundf(format string, args ...any) error {
	return newError(CodeNotFound, format, args...)
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

// runtimeConfig is the daemon's live configuration. Control commands use it
// instead of re-reading the config file, so an operator can never act on a
// config the running daemon has not accepted.
func (s Service) runtimeConfig() config.Config {
	if s.Config == nil {
		return config.Config{}
	}
	return s.Config()
}

// requireStore keeps every command's first failure identical instead of each
// one inventing its own wording for a service that was never wired up.
func (s Service) requireStore() error {
	if s.Store == nil {
		return newError(CodeInternal, "control service store is required")
	}
	return nil
}

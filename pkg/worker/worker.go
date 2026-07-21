package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zekurio/anvil/pkg/audio"
	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/crop"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/ffmpeg"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/probe"
	"github.com/zekurio/anvil/pkg/process"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
	"github.com/zekurio/anvil/pkg/scheduler"
	"github.com/zekurio/anvil/pkg/search"
	"github.com/zekurio/anvil/pkg/staging"
	"github.com/zekurio/anvil/pkg/store"
	"github.com/zekurio/anvil/pkg/subtitle"
	"github.com/zekurio/anvil/pkg/validate"
)

type Store interface {
	GetMediaSource(ctx context.Context, id domain.MediaSourceID) (domain.MediaSource, error)
	GetMediaAsset(ctx context.Context, id domain.MediaAssetID) (domain.MediaAsset, error)
	StartAttempt(ctx context.Context, jobID domain.JobID, workerID string, resolvedLibrary []byte, resolvedFlow []byte, resolvedProfile []byte, now time.Time) (domain.Attempt, error)
	FinishAttempt(ctx context.Context, attemptID domain.AttemptID, state domain.AttemptState, message string, finishedAt time.Time) (domain.Attempt, error)
	TransitionJob(ctx context.Context, jobID domain.JobID, to domain.JobState, now time.Time, lastError string) (domain.Job, error)
	CompleteJobOccurrence(ctx context.Context, input store.CompleteJobOccurrenceInput) (domain.Job, error)
	HeartbeatJob(ctx context.Context, jobID domain.JobID, workerID string, leaseDeadline time.Time, now time.Time) (domain.Job, error)
	RecordAttemptEvent(ctx context.Context, event domain.AttemptEvent) (domain.AttemptEvent, error)
	GetJobPipelineContext(ctx context.Context, jobID domain.JobID) (domain.JobPipelineContext, bool, error)
	SaveJobPipelineContext(ctx context.Context, jobID domain.JobID, snapshot domain.JobPipelineContext, now time.Time) error
}

type ConfigProvider func() config.Config

type MetadataResolver interface {
	ResolveJobMetadata(context.Context, domain.Library, domain.MediaSource, domain.MediaAsset, string) (domain.JobMetadata, error)
}

var ErrInputOccurrenceChanged = errors.New("input occurrence changed")

type Runner struct {
	Store             Store
	ConfigProvider    ConfigProvider
	MetadataResolver  MetadataResolver
	VerifyFingerprint func(string, domain.FileFingerprint) error
	Pipeline          pipeline.Runner
	TempDir           string
	MaxAttempts       int
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	Now               func() time.Time
}

func (r Runner) Run(ctx context.Context, assignment scheduler.Assignment) error {
	if r.Store == nil {
		return errors.New("worker store is required")
	}
	if r.ConfigProvider == nil {
		return errors.New("worker config provider is required")
	}

	cfg := r.ConfigProvider()
	library, flow, profile, resolveErr := cfg.ResolveForLibrary(assignment.Job.LibraryName)
	resolvedLibrary, resolvedFlow, resolvedProfile := snapshots(library, flow, profile, resolveErr)

	attempt, err := r.Store.StartAttempt(ctx, assignment.Job.ID, assignment.WorkerID, resolvedLibrary, resolvedFlow, resolvedProfile, r.now())
	if err != nil {
		return fmt.Errorf("start attempt: %w", err)
	}
	slog.Info("worker attempt started", "worker", assignment.WorkerID, "job", assignment.Job.Label(), "attempt", attempt.Number, "library", string(assignment.Job.LibraryName), "threads", assignment.Resources.Threads)
	stopHeartbeat := r.startHeartbeat(ctx, assignment.Job.ID, assignment.WorkerID, cfg)
	defer stopHeartbeat()

	if resolveErr != nil {
		return r.fail(ctx, assignment.Job, attempt, cfg, resolveErr)
	}
	source, err := r.Store.GetMediaSource(ctx, assignment.Job.SourceID)
	if err != nil {
		return r.fail(ctx, assignment.Job, attempt, cfg, fmt.Errorf("get media source: %w", err))
	}
	var asset domain.MediaAsset
	if assignment.Job.AssetID != 0 {
		asset, err = r.Store.GetMediaAsset(ctx, assignment.Job.AssetID)
		if err != nil {
			return r.fail(ctx, assignment.Job, attempt, cfg, fmt.Errorf("get media asset: %w", err))
		}
	}

	inputPath := InputPath(library.Path, source, asset)
	if err := r.verifyOccurrenceInput(inputPath, source, asset); err != nil {
		return r.fail(ctx, assignment.Job, attempt, cfg, err)
	}
	metadata, err := r.resolveMetadata(ctx, library, source, asset, inputPath)
	if err != nil {
		metadata.StreamCleanupDisabled = true
		metadata.StreamCleanupDisabledReason = err.Error()
	}
	disableUnsafeStreamCleanup(profile, &metadata)
	initialMetadata := metadata

	jobContext := &pipeline.JobContext{
		Job:       assignment.Job,
		Attempt:   attempt,
		Source:    source,
		Asset:     asset,
		Library:   library,
		Flow:      flow,
		Profile:   profile,
		Resources: assignment.Resources,
		Metadata:  metadata,
		InputPath: inputPath,
	}
	pipelineRunner := r.Pipeline
	beforeStep := pipelineRunner.BeforeStep
	pipelineRunner.BeforeStep = func(ctx context.Context, step string, job *pipeline.JobContext) error {
		if beforeStep != nil {
			if err := beforeStep(ctx, step, job); err != nil {
				return err
			}
		}
		if step == "replace" || step == "handoff" {
			return r.verifyCurrentOccurrence(ctx, job)
		}
		return nil
	}
	contextPersistence := newPipelineContextPersistence(ctx, r.Store, jobContext, resolvedLibrary, resolvedFlow, resolvedProfile, initialMetadata, probeMetadataRefresh(pipelineRunner), r.now)
	slog.Info("worker pipeline started", "worker", assignment.WorkerID, "job", assignment.Job.Label(), "attempt", attempt.Number, "library", string(library.Name), "flow", string(flow.Name), "profile", string(profile.Name), "input", inputPath)

	if pipelineRunner.Events == nil {
		pipelineRunner.Events = r.Store
	}
	if pipelineRunner.StepPersistence == nil {
		pipelineRunner.StepPersistence = contextPersistence
	}
	stepContext := pipelineRunner.StepContext
	pipelineRunner.StepContext = func(ctx context.Context, step string) context.Context {
		if stepContext != nil {
			ctx = stepContext(ctx, step)
		}
		return process.WithStep(ctx, step)
	}
	pipelineCtx := process.WithLogger(ctx, &processLogRecorder{
		root:      filepath.Join(r.tempDir(cfg), "process-logs"),
		jobID:     assignment.Job.ID,
		jobSlug:   assignment.Job.Slug,
		attemptID: attempt.ID,
		attempt:   attempt.Number,
		events:    r.Store,
		now:       r.now,
	})
	if err := pipelineRunner.Run(pipelineCtx, jobContext); err != nil {
		r.cleanupFailedStaging(ctx, jobContext, cfg)
		return r.fail(ctx, assignment.Job, attempt, cfg, err)
	}
	if err := r.complete(ctx, jobContext, flow); err != nil {
		return err
	}
	slog.Info("worker job completed", "worker", assignment.WorkerID, "job", assignment.Job.Label(), "attempt", attempt.Number, "library", string(library.Name), "final_path", jobContext.FinalPath)
	return nil
}

func (r Runner) verifyCurrentOccurrence(ctx context.Context, job *pipeline.JobContext) error {
	if job == nil {
		return errors.New("pipeline job context is required")
	}
	source, err := r.Store.GetMediaSource(ctx, job.Source.ID)
	if err != nil {
		return fmt.Errorf("refresh source occurrence before publish: %w", err)
	}
	asset := job.Asset
	if asset.ID != 0 {
		asset, err = r.Store.GetMediaAsset(ctx, asset.ID)
		if err != nil {
			return fmt.Errorf("refresh asset occurrence before publish: %w", err)
		}
	}
	return r.verifyOccurrenceInput(job.InputPath, source, asset)
}

type probeMetadataRefresher interface {
	RefreshDolbyVision(context.Context, *pipeline.JobContext) error
}

func probeMetadataRefresh(runner pipeline.Runner) resumeProbeMetadataRefresher {
	block, ok := runner.Registry.Block("probe")
	if ok {
		if refresher, ok := block.(probeMetadataRefresher); ok {
			return refresher.RefreshDolbyVision
		}
	}
	return probe.Block{}.RefreshDolbyVision
}

func DefaultPipeline(tempDir string) pipeline.Runner {
	stageManager := staging.Manager{Root: filepath.Join(tempDir, "staging")}
	prober := probe.FFProbe{}
	return pipeline.Runner{
		Registry: pipeline.NewRegistry(
			probe.Block{Prober: prober},
			crop.Block{},
			audio.Block{},
			subtitle.Block{},
			staging.StageBlock{Manager: stageManager},
			search.Block{},
			ffmpeg.Block{},
			ffmpeg.DolbyVisionBlock{},
			validate.Block{Validator: validate.Validator{Prober: prober}},
			replacepkg.ReplaceBlock{},
			replacepkg.HandoffBlock{},
			staging.CleanupBlock{Manager: stageManager},
		),
	}
}

func InputPath(root string, source domain.MediaSource, asset domain.MediaAsset) string {
	if source.Kind == domain.SourceKindPackage && asset.RelativePath != "" {
		return filepath.Join(root, filepath.FromSlash(source.RelativePath), filepath.FromSlash(asset.RelativePath))
	}
	return filepath.Join(root, filepath.FromSlash(source.RelativePath))
}

func (r Runner) verifyOccurrenceInput(inputPath string, source domain.MediaSource, asset domain.MediaAsset) error {
	if !source.Current || source.Status != domain.MediaSourceActive {
		return fmt.Errorf("%w: source occurrence %d generation %d is no longer active", ErrInputOccurrenceChanged, source.ID, source.Generation)
	}
	expected := source.Fingerprint
	if asset.ID != 0 {
		if !asset.Current || asset.Status != domain.MediaAssetActive {
			return fmt.Errorf("%w: asset occurrence %d generation %d is no longer active", ErrInputOccurrenceChanged, asset.ID, asset.Generation)
		}
		expected = asset.Fingerprint
	}
	verify := r.VerifyFingerprint
	if verify == nil {
		verify = verifyFileFingerprint
	}
	if err := verify(inputPath, expected); err != nil {
		return fmt.Errorf("%w: input fingerprint changed for occurrence generation %d: %v", ErrInputOccurrenceChanged, occurrenceGeneration(source, asset), err)
	}
	return nil
}

func verifyFileFingerprint(path string, expected domain.FileFingerprint) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat input: %w", err)
	}
	if info.Size() != expected.SizeBytes {
		return fmt.Errorf("size is %d, expected %d", info.Size(), expected.SizeBytes)
	}
	if !expected.ModTime.IsZero() && !info.ModTime().Equal(expected.ModTime) {
		return fmt.Errorf("modification time is %s, expected %s", info.ModTime().UTC().Format(time.RFC3339Nano), expected.ModTime.UTC().Format(time.RFC3339Nano))
	}
	if expected.HashAlgorithm == "" && expected.HashValue == "" {
		return nil
	}
	if expected.HashAlgorithm != "sha256" {
		return fmt.Errorf("unsupported fingerprint hash algorithm %q", expected.HashAlgorithm)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open input for fingerprint hash: %w", err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash input: %w", errors.Join(err, file.Close()))
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close input after fingerprint hash: %w", err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected.HashValue {
		return fmt.Errorf("sha256 hash is %s, expected %s", actual, expected.HashValue)
	}
	return nil
}

func occurrenceGeneration(source domain.MediaSource, asset domain.MediaAsset) int {
	if asset.ID != 0 {
		return asset.Generation
	}
	return source.Generation
}

func (r Runner) resolveMetadata(ctx context.Context, library domain.Library, source domain.MediaSource, asset domain.MediaAsset, inputPath string) (domain.JobMetadata, error) {
	if r.MetadataResolver == nil {
		if library.Metadata.Provider != domain.MetadataProviderNone {
			return domain.JobMetadata{}, errors.New("metadata resolver is unavailable")
		}
		return domain.JobMetadata{}, nil
	}
	metadata, err := r.MetadataResolver.ResolveJobMetadata(ctx, library, source, asset, inputPath)
	if err != nil {
		return domain.JobMetadata{}, fmt.Errorf("resolve job metadata: %w", err)
	}
	return metadata, nil
}

func disableUnsafeStreamCleanup(profile domain.Profile, metadata *domain.JobMetadata) {
	if metadata == nil || !usesOriginalLanguage(profile) || metadata.OriginalLanguage != "" {
		return
	}
	metadata.StreamCleanupDisabled = true
	if metadata.StreamCleanupDisabledReason == "" {
		metadata.StreamCleanupDisabledReason = "original language metadata is unavailable"
	}
}

func usesOriginalLanguage(profile domain.Profile) bool {
	for _, value := range profile.Audio.LanguagesToKeep {
		if strings.EqualFold(strings.TrimSpace(value), audio.OriginalLanguageToken) {
			return true
		}
	}
	for _, value := range profile.Subtitles.LanguagesToKeep {
		if strings.EqualFold(strings.TrimSpace(value), subtitle.OriginalLanguageToken) {
			return true
		}
	}
	return false
}

func jobInputSize(job *pipeline.JobContext) int64 {
	if job.Asset.Fingerprint.SizeBytes > 0 {
		return job.Asset.Fingerprint.SizeBytes
	}
	if job.Source.Fingerprint.SizeBytes > 0 {
		return job.Source.Fingerprint.SizeBytes
	}
	return statFileSize(job.InputPath)
}

func jobOutputSize(job *pipeline.JobContext) int64 {
	path := strings.TrimSpace(job.FinalPath)
	if path == "" {
		path = strings.TrimSpace(job.OutputPath)
	}
	return statFileSize(path)
}

func statFileSize(path string) int64 {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 {
		return 0
	}
	return info.Size()
}

func (r Runner) complete(ctx context.Context, job *pipeline.JobContext, flow domain.Flow) error {
	now := r.now()
	if _, err := r.Store.TransitionJob(ctx, job.Job.ID, domain.JobStateValidating, now, ""); err != nil {
		return fmt.Errorf("transition job to validating: %w", err)
	}
	if flowNeedsReplacing(flow) {
		if _, err := r.Store.TransitionJob(ctx, job.Job.ID, domain.JobStateReplacing, r.now(), ""); err != nil {
			return fmt.Errorf("transition job to replacing: %w", err)
		}
	}
	if _, err := r.Store.CompleteJobOccurrence(ctx, store.CompleteJobOccurrenceInput{
		JobID: job.Job.ID, AttemptID: job.Attempt.ID,
		InputSizeBytes: jobInputSize(job), OutputSizeBytes: jobOutputSize(job),
		SourceMediaRemoved:    sourceMediaRemovedOnCompletion(job, flow),
		FinalInputFingerprint: finalInputFingerprint(job),
		CompletedAt:           r.now(),
	}); err != nil {
		return fmt.Errorf("complete job occurrence: %w", err)
	}
	return nil
}

func sourceMediaRemovedOnCompletion(job *pipeline.JobContext, flow domain.Flow) bool {
	return job != nil && job.Library.Kind == domain.LibraryKindDownload && job.Library.Download.CleanupSourceMedia && flowHasStep(flow, "handoff")
}

func finalInputFingerprint(job *pipeline.JobContext) *domain.FileFingerprint {
	if job == nil || strings.TrimSpace(job.FinalPath) == "" || filepath.Clean(job.FinalPath) != filepath.Clean(job.InputPath) {
		return nil
	}
	info, err := os.Stat(job.InputPath)
	if err != nil {
		return nil
	}
	fingerprint := domain.FileFingerprint{SizeBytes: info.Size(), ModTime: info.ModTime().UTC()}
	return &fingerprint
}

func (r Runner) fail(ctx context.Context, job domain.Job, attempt domain.Attempt, cfg config.Config, cause error) error {
	message := cause.Error()
	if _, err := r.Store.FinishAttempt(ctx, attempt.ID, domain.AttemptStateFailed, message, r.now()); err != nil {
		return fmt.Errorf("finish failed attempt: %w", err)
	}
	if errors.Is(cause, ErrInputOccurrenceChanged) {
		slog.Warn("worker input occurrence changed; job skipped", "worker", attempt.WorkerID, "job", job.Label(), "attempt", attempt.Number, "library", string(job.LibraryName), "error", cause)
		if _, err := r.Store.TransitionJob(ctx, job.ID, domain.JobStateSkipped, r.now(), message); err != nil {
			return fmt.Errorf("transition changed occurrence job to skipped: %w", err)
		}
		return cause
	}
	maxAttempts := r.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = cfg.Daemon.MaxAttempts
	}
	if maxAttempts <= 0 {
		maxAttempts = config.DefaultMaxAttempts
	}
	if attempt.Number >= maxAttempts {
		slog.Error("worker attempt failed; job failed", "worker", attempt.WorkerID, "job", job.Label(), "attempt", attempt.Number, "library", string(job.LibraryName), "max_attempts", maxAttempts, "error", cause)
		if _, err := r.Store.TransitionJob(ctx, job.ID, domain.JobStateFailed, r.now(), message); err != nil {
			return fmt.Errorf("transition job to failed: %w", err)
		}
		return cause
	}
	slog.Warn("worker attempt failed; job will retry", "worker", attempt.WorkerID, "job", job.Label(), "attempt", attempt.Number, "library", string(job.LibraryName), "max_attempts", maxAttempts, "error", cause)
	if _, err := r.Store.TransitionJob(ctx, job.ID, domain.JobStateRetrying, r.now(), message); err != nil {
		return fmt.Errorf("transition job to retrying: %w", err)
	}
	if _, err := r.Store.TransitionJob(ctx, job.ID, domain.JobStatePending, r.now(), message); err != nil {
		return fmt.Errorf("transition job to pending: %w", err)
	}
	return cause
}

func (r Runner) cleanupFailedStaging(ctx context.Context, job *pipeline.JobContext, cfg config.Config) {
	if job == nil || job.StagingDir == "" {
		return
	}
	err := staging.Manager{Root: filepath.Join(r.tempDir(cfg), "staging")}.Cleanup(job)
	if err == nil || r.Store == nil {
		return
	}
	_, _ = r.Store.RecordAttemptEvent(ctx, domain.AttemptEvent{ //nolint:errcheck // failed cleanup telemetry is best-effort
		AttemptID: job.Attempt.ID,
		Type:      domain.AttemptEventArtifact,
		Name:      "failed-staging-cleanup",
		Message:   err.Error(),
		CreatedAt: r.now(),
	})
}

func (r Runner) tempDir(cfg config.Config) string {
	tempDir := strings.TrimSpace(r.TempDir)
	if tempDir == "" {
		tempDir = cfg.Daemon.TempDir
	}
	return tempDir
}

func (r Runner) startHeartbeat(ctx context.Context, jobID domain.JobID, workerID string, cfg config.Config) func() {
	leaseDuration := r.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = cfg.LeaseDuration()
	}
	interval := r.HeartbeatInterval
	if interval <= 0 {
		interval = leaseDuration / 3
	}
	if interval <= 0 {
		interval = time.Minute
	}

	heartbeatCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				now := r.now()
				_, _ = r.Store.HeartbeatJob(heartbeatCtx, jobID, workerID, now.Add(leaseDuration), now) //nolint:errcheck // next heartbeat or stale recovery handles transient heartbeat failure
			}
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

func snapshots(library domain.Library, flow domain.Flow, profile domain.Profile, resolveErr error) ([]byte, []byte, []byte) {
	if resolveErr != nil {
		return nil, nil, nil
	}
	return mustJSON(library), mustJSON(flow), mustJSON(profile)
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func flowNeedsReplacing(flow domain.Flow) bool {
	for _, step := range flow.Steps {
		name := strings.ToLower(step.Name)
		if name == "replace" || name == "handoff" {
			return true
		}
	}
	return false
}

func flowHasStep(flow domain.Flow, wanted string) bool {
	for _, step := range flow.Steps {
		if strings.EqualFold(step.Name, wanted) {
			return true
		}
	}
	return false
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

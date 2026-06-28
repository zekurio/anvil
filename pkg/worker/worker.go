package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	replacepkg "github.com/zekurio/anvil/pkg/replace"
	"github.com/zekurio/anvil/pkg/scheduler"
	"github.com/zekurio/anvil/pkg/search"
	"github.com/zekurio/anvil/pkg/staging"
	"github.com/zekurio/anvil/pkg/validate"
)

type Store interface {
	GetMediaSource(ctx context.Context, id domain.MediaSourceID) (domain.MediaSource, error)
	GetMediaAsset(ctx context.Context, id domain.MediaAssetID) (domain.MediaAsset, error)
	StartAttempt(ctx context.Context, jobID domain.JobID, workerID string, resolvedLibrary []byte, resolvedFlow []byte, resolvedProfile []byte, now time.Time) (domain.Attempt, error)
	FinishAttempt(ctx context.Context, attemptID domain.AttemptID, state domain.AttemptState, message string, finishedAt time.Time) (domain.Attempt, error)
	TransitionJob(ctx context.Context, jobID domain.JobID, to domain.JobState, now time.Time, lastError string) (domain.Job, error)
	HeartbeatJob(ctx context.Context, jobID domain.JobID, workerID string, leaseDeadline time.Time, now time.Time) (domain.Job, error)
	RecordAttemptEvent(ctx context.Context, event domain.AttemptEvent) (domain.AttemptEvent, error)
}

type ConfigProvider func() config.Config

type MetadataResolver interface {
	ResolveJobMetadata(context.Context, domain.Library, domain.MediaSource, domain.MediaAsset, string) (domain.JobMetadata, error)
}

type Runner struct {
	Store             Store
	ConfigProvider    ConfigProvider
	MetadataResolver  MetadataResolver
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
	metadata, err := r.resolveMetadata(ctx, library, source, asset, inputPath)
	if err != nil {
		metadata.StreamCleanupDisabled = true
		metadata.StreamCleanupDisabledReason = err.Error()
	}
	disableUnsafeStreamCleanup(profile, &metadata)

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
	if pipelineRunner.Events == nil {
		pipelineRunner.Events = r.Store
	}
	if err := pipelineRunner.Run(ctx, jobContext); err != nil {
		return r.fail(ctx, assignment.Job, attempt, cfg, err)
	}
	if _, err := r.Store.FinishAttempt(ctx, attempt.ID, domain.AttemptStateSucceeded, "", r.now()); err != nil {
		return fmt.Errorf("finish successful attempt: %w", err)
	}
	if err := r.complete(ctx, assignment.Job.ID, flow); err != nil {
		return err
	}
	return nil
}

func DefaultPipeline(tempDir string) pipeline.Runner {
	stageManager := staging.Manager{Root: filepath.Join(tempDir, "staging")}
	prober := probe.FFProbe{}
	return pipeline.Runner{
		Registry: pipeline.NewRegistry(
			probe.Block{Prober: prober},
			crop.Block{},
			audio.Block{},
			staging.StageBlock{Manager: stageManager},
			search.Block{},
			ffmpeg.Block{},
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
	return false
}

func (r Runner) complete(ctx context.Context, jobID domain.JobID, flow domain.Flow) error {
	now := r.now()
	if _, err := r.Store.TransitionJob(ctx, jobID, domain.JobStateValidating, now, ""); err != nil {
		return fmt.Errorf("transition job to validating: %w", err)
	}
	if flowNeedsReplacing(flow) {
		if _, err := r.Store.TransitionJob(ctx, jobID, domain.JobStateReplacing, r.now(), ""); err != nil {
			return fmt.Errorf("transition job to replacing: %w", err)
		}
	}
	if _, err := r.Store.TransitionJob(ctx, jobID, domain.JobStateComplete, r.now(), ""); err != nil {
		return fmt.Errorf("transition job to complete: %w", err)
	}
	return nil
}

func (r Runner) fail(ctx context.Context, job domain.Job, attempt domain.Attempt, cfg config.Config, cause error) error {
	message := cause.Error()
	if _, err := r.Store.FinishAttempt(ctx, attempt.ID, domain.AttemptStateFailed, message, r.now()); err != nil {
		return fmt.Errorf("finish failed attempt: %w", err)
	}
	maxAttempts := r.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = cfg.Daemon.MaxAttempts
	}
	if maxAttempts <= 0 {
		maxAttempts = config.DefaultMaxAttempts
	}
	if attempt.Number >= maxAttempts {
		if _, err := r.Store.TransitionJob(ctx, job.ID, domain.JobStateFailed, r.now(), message); err != nil {
			return fmt.Errorf("transition job to failed: %w", err)
		}
		return cause
	}
	if _, err := r.Store.TransitionJob(ctx, job.ID, domain.JobStateRetrying, r.now(), message); err != nil {
		return fmt.Errorf("transition job to retrying: %w", err)
	}
	if _, err := r.Store.TransitionJob(ctx, job.ID, domain.JobStatePending, r.now(), message); err != nil {
		return fmt.Errorf("transition job to pending: %w", err)
	}
	return cause
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
				_, _ = r.Store.HeartbeatJob(heartbeatCtx, jobID, workerID, now.Add(leaseDuration), now)
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

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

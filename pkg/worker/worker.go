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
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/flowstep"
	"github.com/zekurio/anvil/pkg/marker"
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
	GetFlow(ctx context.Context, id int64) (store.Flow, error)
	StartAttempt(ctx context.Context, jobID domain.JobID, workerID string, resolvedLibrary []byte, resolvedFlow []byte, resolvedProfile []byte, now time.Time) (domain.Attempt, error)
	SaveAttemptProfile(ctx context.Context, attemptID domain.AttemptID, resolvedProfile []byte) error
	ListAttemptsForJob(ctx context.Context, jobID domain.JobID) ([]domain.Attempt, error)
	FinishAttempt(ctx context.Context, attemptID domain.AttemptID, state domain.AttemptState, message string, finishedAt time.Time) (domain.Attempt, error)
	TransitionJob(ctx context.Context, jobID domain.JobID, to domain.JobState, now time.Time, lastError string) (domain.Job, error)
	GetJob(ctx context.Context, id domain.JobID) (domain.Job, error)
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
	// Progress receives live progress of running jobs; nil disables tracking.
	Progress          *ProgressTracker
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

	// The library and its flow are read once, here, and snapshotted on the
	// attempt. Everything the attempt does follows from these two values, so
	// an edit made while it runs takes effect on the next attempt, never
	// halfway through this one.
	cfg := r.ConfigProvider()
	library, resolveErr := cfg.ResolveLibrary(assignment.Job.LibraryName)
	var resolvedLibrary, resolvedFlow []byte
	var flowSnapshot domain.FlowSnapshot
	var flowFingerprint string
	if resolveErr == nil {
		resolvedLibrary = mustJSON(library)
		flowSnapshot, flowFingerprint, resolveErr = r.loadFlow(ctx, library)
		if flowSnapshot.ID != 0 {
			resolvedFlow = mustJSON(flowSnapshot)
		}
	}

	attempt, err := r.Store.StartAttempt(ctx, assignment.Job.ID, assignment.WorkerID, resolvedLibrary, resolvedFlow, nil, r.now())
	if errors.Is(err, store.ErrNotFound) {
		// The job is no longer this worker's: it was canceled, or its library
		// deleted, between the lease and now, or the lease went to another
		// worker. Nothing has been touched on disk yet, so there is nothing
		// to undo either.
		slog.Info("worker attempt not started; the job is no longer leased to this worker", "worker", assignment.WorkerID, "job", assignment.Job.Label(), "library", string(assignment.Job.LibraryName))
		return nil
	}
	if err != nil {
		return fmt.Errorf("start attempt: %w", err)
	}
	slog.Info("worker attempt started", "worker", assignment.WorkerID, "job", assignment.Job.Label(), "attempt", attempt.Number, "library", string(assignment.Job.LibraryName), "threads", assignment.Resources.Threads)
	stopHeartbeat := r.startHeartbeat(ctx, assignment.Job.ID, assignment.WorkerID, cfg)
	defer stopHeartbeat()
	defer r.Progress.Clear(assignment.Job.ID)

	source, asset, err := r.loadOccurrence(ctx, assignment.Job.SourceID, assignment.Job.AssetID)
	if err != nil {
		return r.fail(ctx, assignment.Job, attempt, cfg, errors.Join(resolveErr, err))
	}
	jobContext := pipeline.NewJobContext(assignment.Job, source, asset, library)
	jobContext.RenderDevice = cfg.Daemon.RenderDevice
	jobContext.Attempt = attempt
	jobContext.Resources = assignment.Resources
	jobContext.Flow = flowSnapshot
	inputPath := jobContext.InputPath
	pipelineRunner := r.Pipeline
	// A journaled publish is finished before anything else is looked at. It
	// needs the library, which names the destination, but neither the flow nor
	// a profile: the artifact it publishes already exists. A flow that was
	// deleted or broken since must therefore not keep a half-published file
	// from being resolved, which is why resolveErr only counts when it left
	// no library to work with.
	if resolvedLibrary != nil {
		if recovered, recoverErr := recoverPendingPublish(ctx, pipelineRunner, jobContext); recoverErr != nil {
			return r.fail(ctx, assignment.Job, attempt, cfg, recoverErr)
		} else if recovered {
			return r.finishSuccessful(context.WithoutCancel(ctx), assignment, library, jobContext)
		}
	}
	if resolveErr != nil {
		return r.fail(ctx, assignment.Job, attempt, cfg, resolveErr)
	}
	if err := r.verifyOccurrenceInput(inputPath, source, asset); err != nil {
		return r.fail(ctx, assignment.Job, attempt, cfg, err)
	}
	metadata, err := r.resolveMetadata(ctx, library, source, asset, inputPath)
	if err != nil {
		metadata.StreamCleanupDisabled = true
		metadata.StreamCleanupDisabledReason = err.Error()
	}
	// The profile is not known yet: the flow step resolves it from the probe,
	// and then also decides whether stream cleanup is safe without an
	// original language (flowstep.DisableUnsafeStreamCleanup).
	initialMetadata := metadata
	jobContext.Metadata = metadata
	pipelineRunner.BeforeStep = func(ctx context.Context, step string, job *pipeline.JobContext) error {
		if step != "probe" && shouldSkipProcessed(job) {
			return errAlreadyProcessed
		}
		if step == "publish" {
			return r.verifyCurrentOccurrence(ctx, job)
		}
		return nil
	}
	contextPersistence := newPipelineContextPersistence(ctx, r.Store, jobContext, resolvedLibrary, flowFingerprint, initialMetadata, r.now)
	slog.Info("worker pipeline started", "worker", assignment.WorkerID, "job", assignment.Job.Label(), "attempt", attempt.Number, "library", string(library.Name), "flow", flowSnapshot.Name, "flow_revision", flowSnapshot.Revision, "input", inputPath)

	if pipelineRunner.Events == nil {
		pipelineRunner.Events = r.Store
	}
	if pipelineRunner.StepPersistence == nil {
		pipelineRunner.StepPersistence = contextPersistence
	}
	pipelineCtx := process.WithLogger(ctx, &processLogRecorder{
		root:      staging.ProcessLogRoot(r.tempDir(cfg)),
		jobID:     assignment.Job.ID,
		jobSlug:   assignment.Job.Slug,
		attemptID: attempt.ID,
		attempt:   attempt.Number,
		events:    r.Store,
		tracker:   r.Progress,
		now:       r.now,
	})
	if err := pipelineRunner.Run(pipelineCtx, jobContext); err != nil {
		if errors.Is(err, errAlreadyProcessed) {
			return r.skipProcessed(context.WithoutCancel(ctx), assignment.Job, attempt)
		}
		if errors.Is(err, flowstep.ErrSkipped) {
			return r.skipByFlow(context.WithoutCancel(ctx), jobContext)
		}
		if !errors.Is(err, replacepkg.ErrPublishPending) {
			// A pending publish keeps its staging artifact so a resumed job can
			// still publish it. The detached context only carries the cleanup
			// telemetry, which has to outlive the cancellation that caused it.
			r.cleanupFailedStaging(context.WithoutCancel(ctx), jobContext, cfg)
		}
		return r.fail(ctx, assignment.Job, attempt, cfg, err)
	}
	// A finished pipeline is authoritative: the artifact is published, so the
	// completion bookkeeping must run even if a cancel landed in the meantime.
	return r.finishSuccessful(context.WithoutCancel(ctx), assignment, library, jobContext)
}

// loadFlow reads the library's flow and returns the snapshot the attempt
// keeps, together with a fingerprint of what the flow means. The fingerprint
// is taken from flow.Canonical, which leaves out positions and labels, so
// tidying a canvas does not invalidate checkpoints while any change to what
// the flow does to a file invalidates all of them.
func (r Runner) loadFlow(ctx context.Context, library domain.Library) (domain.FlowSnapshot, string, error) {
	stored, err := r.Store.GetFlow(ctx, library.FlowID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.FlowSnapshot{}, "", fmt.Errorf("library %q is assigned flow %d, which does not exist", library.Name, library.FlowID)
	}
	if err != nil {
		return domain.FlowSnapshot{}, "", fmt.Errorf("load flow %d of library %q: %w", library.FlowID, library.Name, err)
	}
	snapshot := domain.FlowSnapshot{ID: stored.ID, Name: stored.Name, Revision: stored.Revision, Graph: mustJSON(stored.Graph)}
	fingerprint, err := flowFingerprint(stored.Graph)
	if err != nil {
		return snapshot, "", fmt.Errorf("flow %q: %w", stored.Name, err)
	}
	return snapshot, fingerprint, nil
}

// flowFingerprint identifies what a flow means. It fails for a graph with
// error issues, which has no meaning to identify.
func flowFingerprint(graph flow.Graph) (string, error) {
	canonical, err := flow.Canonical(graph)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

var errAlreadyProcessed = errors.New("input has Anvil processed marker")

func processedInput(job *pipeline.JobContext) bool {
	return job != nil && job.Probe != nil && marker.Processed(*job.Probe)
}

func shouldSkipProcessed(job *pipeline.JobContext) bool {
	if !processedInput(job) {
		return false
	}
	// A library that reprocesses what it finds again would loop on its own
	// output, so it skips every marked input. Otherwise only a first
	// generation is skipped: a forced requeue starts a new one and runs.
	return job.Library.Reprocess || job.Source.Generation <= 1
}

func (r Runner) skipProcessed(ctx context.Context, job domain.Job, attempt domain.Attempt) error {
	if _, err := r.Store.FinishAttempt(ctx, attempt.ID, domain.AttemptStateSucceeded, "", r.now()); err != nil {
		return fmt.Errorf("finish skipped attempt: %w", err)
	}
	if _, err := r.Store.TransitionJob(ctx, job.ID, domain.JobStateSkipped, r.now(), errAlreadyProcessed.Error()); err != nil {
		return fmt.Errorf("transition processed input job to skipped: %w", err)
	}
	slog.Info("worker skipped processed input", "worker", attempt.WorkerID, "job", job.Label(), "attempt", attempt.Number, "library", string(job.LibraryName))
	return nil
}

// skipByFlow ends an attempt whose flow reached a skip node: the attempt
// succeeded, and the job is skipped with the condition that sent it there as
// its reason. The file is left as it is.
//
// The flow step runs before anything is staged, so this attempt has nothing
// to clean up. An earlier attempt of the same job may have: one that crashed
// mid-encode leaves its part file where it staged, and the next attempt
// would have overwritten it. A skipped job has no next attempt, so the part
// is reclaimed here. It cannot belong to a publish in flight; that would
// have been recovered before the pipeline started.
//
// Where an earlier attempt staged follows from what it recorded, its library
// snapshot and the profile its flow resolved, not from this attempt: the
// flow that now skips the file says nothing about where a move it made
// before put its part.
func (r Runner) skipByFlow(ctx context.Context, job *pipeline.JobContext) error {
	attempts, err := r.Store.ListAttemptsForJob(ctx, job.Job.ID)
	errs := []error{err}
	destinations, err := replacepkg.StagedDestinations(job.Job, job.Source, job.Asset, attempts)
	errs = append(errs, err)
	for _, destination := range destinations {
		errs = append(errs, replacepkg.CleanupPartFiles(destination.Publish, destination.Path, replacepkg.PartJobLabel(job.Job.ID)))
	}
	r.recordCleanupFailure(ctx, job, errors.Join(errs...))
	reason := flowstep.SkipReason(job.FlowResolution)
	if _, err := r.Store.FinishAttempt(ctx, job.Attempt.ID, domain.AttemptStateSucceeded, "", r.now()); err != nil {
		return fmt.Errorf("finish skipped attempt: %w", err)
	}
	if _, err := r.Store.TransitionJob(ctx, job.Job.ID, domain.JobStateSkipped, r.now(), reason); err != nil {
		return fmt.Errorf("transition job skipped by its flow: %w", err)
	}
	slog.Info("worker skipped input by flow", "worker", job.Attempt.WorkerID, "job", job.Job.Label(), "attempt", job.Attempt.Number, "library", string(job.Job.LibraryName), "reason", reason)
	return nil
}

func (r Runner) finishSuccessful(ctx context.Context, assignment scheduler.Assignment, library domain.Library, jobContext *pipeline.JobContext) error {
	if err := r.complete(ctx, jobContext); err != nil {
		return err
	}
	slog.Info("worker job completed", "worker", assignment.WorkerID, "job", assignment.Job.Label(), "attempt", jobContext.Attempt.Number, "library", string(library.Name), "final_path", jobContext.FinalPath)
	return nil
}

type publishRecoverer interface {
	Recover(context.Context, *pipeline.JobContext) (bool, error)
}

func recoverPendingPublish(ctx context.Context, runner pipeline.Runner, job *pipeline.JobContext) (bool, error) {
	if job == nil {
		return false, nil
	}
	for _, block := range runner.Blocks {
		recoverer, ok := block.(publishRecoverer)
		if !ok {
			continue
		}
		recovered, err := recoverer.Recover(ctx, job)
		if err != nil {
			return true, fmt.Errorf("recover publish operation: %w", err)
		}
		if recovered {
			return true, nil
		}
	}
	return false, nil
}

func (r Runner) verifyCurrentOccurrence(ctx context.Context, job *pipeline.JobContext) error {
	if job == nil {
		return errors.New("pipeline job context is required")
	}
	source, asset, err := r.loadOccurrence(ctx, job.Source.ID, job.Asset.ID)
	if err != nil {
		return fmt.Errorf("refresh occurrence before publish: %w", err)
	}
	return r.verifyOccurrenceInput(job.InputPath, source, asset)
}

// loadOccurrence reads the current state of a job's source and, when the job
// has one, its asset. A zero assetID returns the zero asset.
func (r Runner) loadOccurrence(ctx context.Context, sourceID domain.MediaSourceID, assetID domain.MediaAssetID) (domain.MediaSource, domain.MediaAsset, error) {
	source, err := r.Store.GetMediaSource(ctx, sourceID)
	if err != nil {
		return domain.MediaSource{}, domain.MediaAsset{}, fmt.Errorf("get media source: %w", err)
	}
	if assetID == 0 {
		return source, domain.MediaAsset{}, nil
	}
	asset, err := r.Store.GetMediaAsset(ctx, assetID)
	if err != nil {
		return domain.MediaSource{}, domain.MediaAsset{}, fmt.Errorf("get media asset: %w", err)
	}
	return source, asset, nil
}

// DefaultPipeline is the fixed order of steps every job runs. The flow step
// sits right after the probe: it needs nothing but the probe, and every step
// behind it needs the profile it resolves.
func DefaultPipeline(tempDir string, journal replacepkg.PublishJournal, capabilities func() ffmpeg.EncoderCapabilities) pipeline.Runner {
	stageManager := staging.Manager{Root: staging.Root(tempDir)}
	prober := probe.FFProbe{}
	publishManager := replacepkg.Manager{Journal: journal}
	return pipeline.Runner{
		Blocks: []pipeline.Block{
			probe.Block{Prober: prober},
			flowstep.Block{EncoderCapabilities: capabilities},
			crop.Block{},
			audio.Block{},
			subtitle.Block{},
			staging.StageBlock{Manager: stageManager},
			search.Block{},
			ffmpeg.Block{},
			validate.Block{Validator: validate.Validator{Prober: prober}},
			replacepkg.PublishBlock{Manager: publishManager},
			staging.CleanupBlock{Manager: stageManager},
		},
	}
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

func (r Runner) complete(ctx context.Context, job *pipeline.JobContext) error {
	now := r.now()
	if _, err := r.Store.TransitionJob(ctx, job.Job.ID, domain.JobStateValidating, now, ""); err != nil {
		return fmt.Errorf("transition job to validating: %w", err)
	}
	if _, err := r.Store.TransitionJob(ctx, job.Job.ID, domain.JobStateReplacing, r.now(), ""); err != nil {
		return fmt.Errorf("transition job to replacing: %w", err)
	}
	if _, err := r.Store.CompleteJobOccurrence(ctx, store.CompleteJobOccurrenceInput{
		JobID: job.Job.ID, AttemptID: job.Attempt.ID,
		InputSizeBytes: jobInputSize(job), OutputSizeBytes: jobOutputSize(job),
		// From the committed publish operation, never from the library: see
		// pipeline.JobContext.SourceMediaRemoved.
		SourceMediaRemoved:    job.SourceMediaRemoved,
		FinalInputFingerprint: finalInputFingerprint(job),
		CompletedAt:           r.now(),
	}); err != nil {
		return fmt.Errorf("complete job occurrence: %w", err)
	}
	return nil
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

// jobCancellationCause reports the operator cancellation that ended this
// attempt, or nil when it ended for any other reason such as daemon shutdown.
// A store rejection counts even before the worker's own context observes the
// cancel, because the job row is already terminally canceled.
func jobCancellationCause(ctx context.Context, cause error) error {
	if errors.Is(cause, store.ErrJobCanceled) {
		return cause
	}
	if ctx.Err() == nil {
		return nil
	}
	if canceled := context.Cause(ctx); errors.Is(canceled, scheduler.ErrJobCanceled) {
		return canceled
	}
	return nil
}

// cancel records an operator cancellation. It runs on a context detached from
// the cancellation so the terminal state is always persisted.
func (r Runner) cancel(ctx context.Context, job domain.Job, attempt domain.Attempt, cause error) error {
	message := cause.Error()
	if _, err := r.Store.FinishAttempt(ctx, attempt.ID, domain.AttemptStateCanceled, message, r.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("finish canceled attempt: %w", err)
	}
	if _, err := r.Store.TransitionJob(ctx, job.ID, domain.JobStateCanceled, r.now(), message); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return jobDeleted(job, attempt)
		}
		return fmt.Errorf("transition job to canceled: %w", err)
	}
	slog.Warn("worker job canceled", "worker", attempt.WorkerID, "job", job.Label(), "attempt", attempt.Number, "library", string(job.LibraryName), "reason", message)
	return nil
}

func (r Runner) fail(ctx context.Context, job domain.Job, attempt domain.Attempt, cfg config.Config, cause error) error {
	// A pending publish is never made terminal, whatever ended the attempt: the
	// journaled operation has to stay resumable, and only a job that can be
	// re-leased ever reaches recoverPendingPublish.
	//
	// ConflictError.Is reports ErrPublishPending too, so a conflicted publish
	// takes this branch as well. That is deliberate: a conflict also needs the
	// journal kept, and the store leaves conflicted jobs cancelable so the
	// attempt may already have been canceled underneath us.
	pendingPublish := errors.Is(cause, replacepkg.ErrPublishPending)
	if canceled := jobCancellationCause(ctx, cause); canceled != nil && !pendingPublish {
		return r.cancel(context.WithoutCancel(ctx), job, attempt, canceled)
	}
	// Recording the outcome has to outlive whatever ended the attempt, or the
	// job keeps a lease nobody owns until stale-job recovery expires it.
	ctx = context.WithoutCancel(ctx)
	message := cause.Error()
	// A cancel that raced this attempt already finished it, so a missing running
	// attempt is the store having recorded the outcome first, not an error.
	if _, err := r.Store.FinishAttempt(ctx, attempt.ID, domain.AttemptStateFailed, message, r.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("finish failed attempt: %w", err)
	}
	if _, err := r.Store.GetJob(ctx, job.ID); errors.Is(err, store.ErrNotFound) {
		return jobDeleted(job, attempt)
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

// jobDeleted ends an attempt whose job row is gone: an operator deleted the
// job's library, or pruned the job, after a cancel had already made it
// terminal. There is no outcome left to record. The attempt cleaned up its
// staging and part file before it got here, which is all a deleted job can
// still own on disk, so this is reported once and not as an error.
func jobDeleted(job domain.Job, attempt domain.Attempt) error {
	slog.Info("worker attempt ended after its job was deleted", "worker", attempt.WorkerID, "job", job.Label(), "attempt", attempt.Number, "library", string(job.LibraryName))
	return nil
}

func (r Runner) cleanupFailedStaging(ctx context.Context, job *pipeline.JobContext, cfg config.Config) {
	if job == nil {
		return
	}
	var errs []error
	if job.StagingDir != "" {
		err := staging.Manager{Root: staging.Root(r.tempDir(cfg))}.Cleanup(job)
		errs = append(errs, err)
	}
	// The artifact lives on the destination filesystem, not in scratch.
	errs = append(errs, replacepkg.CleanupPartFiles(job.Profile.Publish, job.DestinationPath, replacepkg.PartJobLabel(job.Job.ID)))
	r.recordCleanupFailure(ctx, job, errors.Join(errs...))
}

// recordCleanupFailure notes on the attempt what a cleanup could not remove.
func (r Runner) recordCleanupFailure(ctx context.Context, job *pipeline.JobContext, err error) {
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
	wg.Go(func() {
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
	})
	return func() {
		cancel()
		wg.Wait()
	}
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

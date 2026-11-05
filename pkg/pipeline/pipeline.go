package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/mediapath"
	"github.com/zekurio/anvil/pkg/process"
)

type JobContext struct {
	Job       domain.Job
	Attempt   domain.Attempt
	Source    domain.MediaSource
	Asset     domain.MediaAsset
	Library   domain.Library
	Profile   domain.Profile
	Resources domain.ResourceAllocation
	Metadata  domain.JobMetadata
	InputPath string
	// DestinationPath is the final publish path planned by the stage step;
	// OutputPath is the temporary name the artifact is written to (see
	// replace.PartPath) until publish links it into place.
	DestinationPath string
	OutputPath      string
	StagingDir      string
	FinalPath       string

	Probe      *domain.ProbeResult
	Audio      *domain.AudioSelection
	Subtitles  *domain.SubtitleSelection
	Crop       *domain.CropResult
	Search     *domain.SearchResult
	EncodePlan *domain.EncodePlan
	Validation *domain.ValidationResult
}

// NewJobContext starts a job context for one occurrence of a library's media.
// The input path follows from the library root and the occurrence paths.
func NewJobContext(job domain.Job, source domain.MediaSource, asset domain.MediaAsset, library domain.Library, profile domain.Profile) *JobContext {
	return &JobContext{
		Job:       job,
		Source:    source,
		Asset:     asset,
		Library:   library,
		Profile:   profile,
		InputPath: mediapath.Input(library.Path, source, asset),
	}
}

type Block interface {
	Name() string
	Run(ctx context.Context, job *JobContext) error
}

type EventRecorder interface {
	RecordAttemptEvent(context.Context, domain.AttemptEvent) (domain.AttemptEvent, error)
}

type StepPersistence interface {
	ResumeStep(context.Context, string, *JobContext) (bool, error)
	StepSucceeded(context.Context, string, *JobContext) error
}

type Runner struct {
	Blocks          []Block
	Events          EventRecorder
	StepPersistence StepPersistence
	BeforeStep      func(context.Context, string, *JobContext) error
	Now             func() time.Time
}

func (r Runner) Run(ctx context.Context, job *JobContext) error {
	if job == nil {
		return errors.New("pipeline job context is required")
	}
	for index, block := range r.Blocks {
		if err := r.runStep(ctx, index, block, job); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) runStep(ctx context.Context, index int, block Block, job *JobContext) error {
	step := block.Name()
	log := slog.With("job", job.Job.Label(), "attempt", job.Attempt.Number, "step", step, "step_index", index)
	resumed, err := r.resumeStep(ctx, step, job)
	if err != nil {
		return err
	}
	payload := map[string]any{"step_index": index}
	if resumed {
		payload["resumed"] = true
	}
	if err := r.record(ctx, job.Attempt.ID, domain.AttemptEventBlockStarted, step, "", payload); err != nil {
		return err
	}
	if resumed {
		log.Info("pipeline step resumed")
		// The block itself did not run, so re-emit its decision: the log of
		// the attempt that originally decided may already be rotated away.
		if decision, ok := blockDecision(block, job); ok {
			LogStreamSelection(job, decision)
		}
		return r.recordOutcome(ctx, block, job, payload)
	}
	if r.BeforeStep != nil {
		if err := r.BeforeStep(ctx, step, job); err != nil {
			_ = r.record(ctx, job.Attempt.ID, domain.AttemptEventBlockFailed, step, err.Error(), payload) //nolint:errcheck // preserve the pre-step error
			return fmt.Errorf("before block %q: %w", step, err)
		}
	}
	log.Info("pipeline step started")
	started := time.Now()
	if err := block.Run(process.WithStep(ctx, step), job); err != nil {
		log.Error("pipeline step failed", "duration", time.Since(started), "error", err)
		// A failing block can still have decided something worth keeping —
		// a fail_job stream selection is exactly the case where the record
		// explains the failure the error message cannot.
		_ = r.recordDecision(ctx, block, job)                                                         //nolint:errcheck // preserve the block error; decision recording is best-effort
		_ = r.recordBlockArtifact(ctx, block, job)                                                    //nolint:errcheck // preserve the block error; result recording is best-effort
		_ = r.record(ctx, job.Attempt.ID, domain.AttemptEventBlockFailed, step, err.Error(), payload) //nolint:errcheck // preserve the block error; failed-event recording is best-effort
		return fmt.Errorf("run block %q: %w", step, err)
	}
	log.Info("pipeline step finished", "duration", time.Since(started))
	if err := r.stepSucceeded(ctx, step, job); err != nil {
		return err
	}
	return r.recordOutcome(ctx, block, job, payload)
}

// recordOutcome records what a finished or resumed step decided and produced,
// then marks it finished.
func (r Runner) recordOutcome(ctx context.Context, block Block, job *JobContext, payload map[string]any) error {
	if err := r.recordDecision(ctx, block, job); err != nil {
		return err
	}
	if err := r.recordBlockArtifact(ctx, block, job); err != nil {
		return err
	}
	return r.record(ctx, job.Attempt.ID, domain.AttemptEventBlockFinished, block.Name(), "", payload)
}

func (r Runner) resumeStep(ctx context.Context, step string, job *JobContext) (bool, error) {
	if r.StepPersistence == nil {
		return false, nil
	}
	resumed, err := r.StepPersistence.ResumeStep(ctx, step, job)
	if err != nil {
		return false, fmt.Errorf("resume pipeline step %q: %w", step, err)
	}
	return resumed, nil
}

func (r Runner) stepSucceeded(ctx context.Context, step string, job *JobContext) error {
	if r.StepPersistence == nil {
		return nil
	}
	if err := r.StepPersistence.StepSucceeded(ctx, step, job); err != nil {
		return fmt.Errorf("persist pipeline step %q: %w", step, err)
	}
	return nil
}

func blockDecision(block Block, job *JobContext) (domain.StreamSelectionDecision, bool) {
	reporter, ok := block.(DecisionReporter)
	if !ok {
		return domain.StreamSelectionDecision{}, false
	}
	return reporter.Decision(job)
}

func (r Runner) recordDecision(ctx context.Context, block Block, job *JobContext) error {
	decision, ok := blockDecision(block, job)
	if !ok {
		return nil
	}
	message := fmt.Sprintf("%s %s", decision.Kind, decision.Rule)
	return r.record(ctx, job.Attempt.ID, domain.AttemptEventArtifact, StreamSelectionArtifact, message, decision)
}

func (r Runner) recordBlockArtifact(ctx context.Context, block Block, job *JobContext) error {
	reporter, ok := block.(ArtifactReporter)
	if !ok {
		return nil
	}
	artifact, ok := reporter.Artifact(job)
	if !ok {
		return nil
	}
	return r.record(ctx, job.Attempt.ID, domain.AttemptEventArtifact, artifact.Name, artifact.Message, artifact.Payload)
}

func (r Runner) record(ctx context.Context, attemptID domain.AttemptID, eventType domain.AttemptEventType, name string, message string, payload any) error {
	if r.Events == nil || attemptID == 0 {
		return nil
	}
	var data []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode attempt event payload: %w", err)
		}
		data = encoded
	}
	_, err := r.Events.RecordAttemptEvent(ctx, domain.AttemptEvent{
		AttemptID: attemptID,
		Type:      eventType,
		Name:      name,
		Message:   message,
		Payload:   data,
		CreatedAt: r.now(),
	})
	if err != nil {
		return fmt.Errorf("record attempt event %q for %q: %w", eventType, name, err)
	}
	return nil
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

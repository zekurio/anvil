package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

type JobContext struct {
	Job        domain.Job
	Attempt    domain.Attempt
	Source     domain.MediaSource
	Asset      domain.MediaAsset
	Library    domain.Library
	Flow       domain.Flow
	Profile    domain.Profile
	Resources  domain.ResourceAllocation
	Metadata   domain.JobMetadata
	InputPath  string
	OutputPath string
	StagingDir string
	FinalPath  string

	Probe      *domain.ProbeResult
	Audio      *domain.AudioSelection
	Subtitles  *domain.SubtitleSelection
	Crop       *domain.CropResult
	Search     *domain.SearchResult
	EncodePlan *domain.EncodePlan
	Validation *domain.ValidationResult
}

type Block interface {
	Name() string
	Run(ctx context.Context, job *JobContext) error
}

type BlockFunc struct {
	BlockName string
	Fn        func(ctx context.Context, job *JobContext) error
}

func (b BlockFunc) Name() string {
	return b.BlockName
}

func (b BlockFunc) Run(ctx context.Context, job *JobContext) error {
	return b.Fn(ctx, job)
}

type Registry struct {
	blocks map[string]Block
}

func NewRegistry(blocks ...Block) Registry {
	registry := Registry{blocks: make(map[string]Block, len(blocks))}
	for _, block := range blocks {
		registry.Register(block)
	}
	return registry
}

func (r *Registry) Register(block Block) {
	if r.blocks == nil {
		r.blocks = make(map[string]Block)
	}
	r.blocks[block.Name()] = block
}

func (r Registry) Block(name string) (Block, bool) {
	block, ok := r.blocks[name]
	return block, ok
}

type EventRecorder interface {
	RecordAttemptEvent(context.Context, domain.AttemptEvent) (domain.AttemptEvent, error)
}

type StepPersistence interface {
	ResumeStep(context.Context, string, *JobContext) (bool, error)
	StepSucceeded(context.Context, string, *JobContext) error
}

type Runner struct {
	Registry        Registry
	Events          EventRecorder
	StepPersistence StepPersistence
	BeforeStep      func(context.Context, string, *JobContext) error
	StepContext     func(context.Context, string) context.Context
	Now             func() time.Time
}

func (r Runner) Run(ctx context.Context, job *JobContext) error {
	if job == nil {
		return errors.New("pipeline job context is required")
	}
	for index, step := range job.Flow.Steps {
		block, ok := r.Registry.Block(step.Name)
		if !ok {
			return fmt.Errorf("pipeline block %q is not registered", step.Name)
		}
		resumed, err := r.resumeStep(ctx, step.Name, job)
		if err != nil {
			return err
		}
		startPayload := map[string]any{"step_index": index}
		if resumed {
			startPayload["resumed"] = true
		}
		if err := r.record(ctx, job.Attempt.ID, domain.AttemptEventBlockStarted, step.Name, "", startPayload); err != nil {
			return err
		}
		started := time.Now()
		if resumed {
			slog.Info("pipeline step resumed", "job", job.Job.Label(), "attempt", job.Attempt.Number, "step", step.Name, "step_index", index)
			if err := r.record(ctx, job.Attempt.ID, domain.AttemptEventBlockFinished, step.Name, "", map[string]any{"step_index": index, "resumed": true}); err != nil {
				return err
			}
			continue
		}
		if r.BeforeStep != nil {
			if err := r.BeforeStep(ctx, step.Name, job); err != nil {
				_ = r.record(ctx, job.Attempt.ID, domain.AttemptEventBlockFailed, step.Name, err.Error(), map[string]any{"step_index": index}) //nolint:errcheck // preserve the pre-step error
				return fmt.Errorf("before block %q: %w", step.Name, err)
			}
		}
		slog.Info("pipeline step started", "job", job.Job.Label(), "attempt", job.Attempt.Number, "step", step.Name, "step_index", index)
		stepCtx := ctx
		if r.StepContext != nil {
			stepCtx = r.StepContext(ctx, step.Name)
		}
		if err := block.Run(stepCtx, job); err != nil {
			slog.Error("pipeline step failed", "job", job.Job.Label(), "attempt", job.Attempt.Number, "step", step.Name, "step_index", index, "duration", time.Since(started), "error", err)
			_ = r.record(ctx, job.Attempt.ID, domain.AttemptEventBlockFailed, step.Name, err.Error(), map[string]any{"step_index": index}) //nolint:errcheck // preserve the block error; failed-event recording is best-effort
			return fmt.Errorf("run block %q: %w", step.Name, err)
		}
		slog.Info("pipeline step finished", "job", job.Job.Label(), "attempt", job.Attempt.Number, "step", step.Name, "step_index", index, "duration", time.Since(started))
		if err := r.stepSucceeded(ctx, step.Name, job); err != nil {
			return err
		}
		if err := r.record(ctx, job.Attempt.ID, domain.AttemptEventBlockFinished, step.Name, "", map[string]any{"step_index": index}); err != nil {
			return err
		}
	}
	return nil
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

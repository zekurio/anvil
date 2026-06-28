package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestRunnerExecutesBlocksAndRecordsEvents(t *testing.T) {
	ctx := context.Background()
	var ran []string
	recorder := &fakeEventRecorder{}
	runner := Runner{
		Registry: NewRegistry(
			BlockFunc{BlockName: "probe", Fn: func(_ context.Context, _ *JobContext) error {
				ran = append(ran, "probe")
				return nil
			}},
			BlockFunc{BlockName: "encode", Fn: func(_ context.Context, _ *JobContext) error {
				ran = append(ran, "encode")
				return nil
			}},
		),
		Events: recorder,
		Now: func() time.Time {
			return time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
		},
	}
	job := &JobContext{
		Attempt: domain.Attempt{ID: 10},
		Flow: domain.Flow{Steps: []domain.FlowStep{
			{Name: "probe"},
			{Name: "encode"},
		}},
	}

	if err := runner.Run(ctx, job); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := len(ran); got != 2 {
		t.Fatalf("ran blocks = %d, want 2", got)
	}
	if got := len(recorder.events); got != 4 {
		t.Fatalf("recorded events = %d, want 4", got)
	}
	if recorder.events[0].Type != domain.AttemptEventBlockStarted || recorder.events[1].Type != domain.AttemptEventBlockFinished {
		t.Fatalf("first block events = %q/%q, want started/finished", recorder.events[0].Type, recorder.events[1].Type)
	}
}

func TestRunnerStopsAndRecordsFailedEvent(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("boom")
	recorder := &fakeEventRecorder{}
	runner := Runner{
		Registry: NewRegistry(BlockFunc{BlockName: "encode", Fn: func(_ context.Context, _ *JobContext) error {
			return wantErr
		}}),
		Events: recorder,
	}
	job := &JobContext{
		Attempt: domain.Attempt{ID: 10},
		Flow:    domain.Flow{Steps: []domain.FlowStep{{Name: "encode"}}},
	}

	if err := runner.Run(ctx, job); !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if got := len(recorder.events); got != 2 {
		t.Fatalf("recorded events = %d, want 2", got)
	}
	if recorder.events[1].Type != domain.AttemptEventBlockFailed {
		t.Fatalf("second event type = %q, want failed", recorder.events[1].Type)
	}
}

func TestRunnerAppliesStepContext(t *testing.T) {
	ctx := context.Background()
	type stepKey struct{}
	var gotStep string
	runner := Runner{
		Registry: NewRegistry(BlockFunc{BlockName: "encode", Fn: func(ctx context.Context, _ *JobContext) error {
			gotStep, _ = ctx.Value(stepKey{}).(string)
			return nil
		}}),
		StepContext: func(ctx context.Context, step string) context.Context {
			return context.WithValue(ctx, stepKey{}, step)
		},
	}
	job := &JobContext{
		Flow: domain.Flow{Steps: []domain.FlowStep{{Name: "encode"}}},
	}

	if err := runner.Run(ctx, job); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotStep != "encode" {
		t.Fatalf("step context = %q, want encode", gotStep)
	}
}

type fakeEventRecorder struct {
	events []domain.AttemptEvent
}

func (f *fakeEventRecorder) RecordAttemptEvent(_ context.Context, event domain.AttemptEvent) (domain.AttemptEvent, error) {
	event.ID = domain.AttemptEventID(len(f.events) + 1)
	f.events = append(f.events, event)
	return event, nil
}

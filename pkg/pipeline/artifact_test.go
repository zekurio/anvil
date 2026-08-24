package pipeline

import (
	"context"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

type artifactTestBlock struct{}

func (artifactTestBlock) Name() string { return "test" }

func (artifactTestBlock) Run(context.Context, *JobContext) error { return nil }

func (artifactTestBlock) Artifact(*JobContext) (ArtifactReport, bool) {
	return ArtifactReport{
		Name:    "test-result",
		Message: "result recorded",
		Payload: map[string]string{"decision": "safe"},
	}, true
}

type artifactEventRecorder struct {
	events []domain.AttemptEvent
}

func (r *artifactEventRecorder) RecordAttemptEvent(_ context.Context, event domain.AttemptEvent) (domain.AttemptEvent, error) {
	r.events = append(r.events, event)
	return event, nil
}

func TestRunnerRecordsBlockArtifact(t *testing.T) {
	events := &artifactEventRecorder{}
	runner := Runner{Blocks: []Block{artifactTestBlock{}}, Events: events}
	job := &JobContext{Attempt: domain.Attempt{ID: 1}}

	if err := runner.Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if len(events.events) != 3 {
		t.Fatalf("recorded %d events, want 3", len(events.events))
	}
	artifact := events.events[1]
	if artifact.Type != domain.AttemptEventArtifact || artifact.Name != "test-result" || artifact.Message != "result recorded" {
		t.Fatalf("artifact = %#v", artifact)
	}
	if string(artifact.Payload) != `{"decision":"safe"}` {
		t.Fatalf("payload = %s", artifact.Payload)
	}
}

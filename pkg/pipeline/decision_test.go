package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

var failJobDecision = domain.StreamSelectionDecision{
	Kind:               domain.StreamKindAudio,
	Rule:               domain.StreamSelectionRuleFallbackFailJob,
	RequestedLanguages: []string{"deu"},
	ResolvedLanguages:  []string{"deu"},
	MissingLanguages:   []string{"deu"},
	Streams: []domain.StreamDecision{
		{Index: 1, Codec: "aac", Language: "eng", ResolvedLanguage: "eng", Reason: domain.StreamDroppedLanguage},
	},
}

type decisionBlock struct {
	BlockFunc
	decision domain.StreamSelectionDecision
	reported bool
}

func (b *decisionBlock) Decision(*JobContext) (domain.StreamSelectionDecision, bool) {
	if !b.reported {
		return domain.StreamSelectionDecision{}, false
	}
	return b.decision, true
}

func TestRunnerRecordsBlockDecision(t *testing.T) {
	decision := domain.StreamSelectionDecision{
		Kind:               domain.StreamKindAudio,
		Rule:               domain.StreamSelectionRuleFallbackKeepFirst,
		OriginalLanguage:   "jpn",
		RequestedLanguages: []string{"orig", "deu"},
		ResolvedLanguages:  []string{"jpn", "deu"},
		MissingLanguages:   []string{"deu"},
		Streams: []domain.StreamDecision{
			{Index: 1, Codec: "aac", Language: "jpn", ResolvedLanguage: "jpn", Kept: true, Reason: domain.StreamKeptFallbackKeepFirst},
			{Index: 2, Codec: "ac3", Language: "eng", ResolvedLanguage: "eng", Reason: domain.StreamDroppedLanguage},
		},
	}

	tests := []struct {
		name     string
		reported bool
		resumed  bool
		want     int
	}{
		{name: "block reports a decision", reported: true, want: 3},
		{name: "block reports nothing", reported: false, want: 2},
		{name: "resumed block still records the decision", reported: true, resumed: true, want: 3},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &fakeEventRecorder{}
			block := &decisionBlock{
				BlockFunc: BlockFunc{BlockName: "audio-cleanup", Fn: func(context.Context, *JobContext) error { return nil }},
				decision:  decision,
				reported:  test.reported,
			}
			runner := Runner{Registry: NewRegistry(block), Events: recorder}
			if test.resumed {
				runner.StepPersistence = resumingPersistence{}
			}
			job := &JobContext{
				Attempt: domain.Attempt{ID: 7},
				Flow:    domain.Flow{Steps: []domain.FlowStep{{Name: "audio-cleanup"}}},
			}

			if err := runner.Run(context.Background(), job); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if got := len(recorder.events); got != test.want {
				t.Fatalf("recorded events = %d, want %d", got, test.want)
			}
			if !test.reported {
				return
			}
			event := recorder.events[1]
			if event.Type != domain.AttemptEventArtifact || event.Name != StreamSelectionArtifact {
				t.Fatalf("decision event = %s/%s, want %s/%s", event.Type, event.Name, domain.AttemptEventArtifact, StreamSelectionArtifact)
			}
			if want := "audio fallback_keep_first"; event.Message != want {
				t.Fatalf("decision message = %q, want %q", event.Message, want)
			}
			var recorded domain.StreamSelectionDecision
			if err := json.Unmarshal(event.Payload, &recorded); err != nil {
				t.Fatalf("decode decision payload: %v", err)
			}
			if got, want := recorded.KeptIndexes(), []int{1}; len(got) != len(want) || got[0] != want[0] {
				t.Fatalf("kept indexes = %v, want %v", got, want)
			}
			if got, want := recorded.Streams[1].Reason, domain.StreamDroppedLanguage; got != want {
				t.Fatalf("dropped stream reason = %q, want %q", got, want)
			}
			if got, want := recorded.MissingLanguages, []string{"deu"}; len(got) != len(want) || got[0] != want[0] {
				t.Fatalf("missing languages = %v, want %v", got, want)
			}
		})
	}
}

// TestRunnerRecordsBlockDecisionWhenBlockFails covers the fail_job stream
// selection: the block error says only "nothing matched", the decision record
// is what says why.
func TestRunnerRecordsBlockDecisionWhenBlockFails(t *testing.T) {
	wantErr := errors.New("no audio streams matched languages [deu]")
	recorder := &fakeEventRecorder{}
	block := &decisionBlock{
		BlockFunc: BlockFunc{BlockName: "audio-cleanup", Fn: func(context.Context, *JobContext) error { return wantErr }},
		decision:  failJobDecision,
		reported:  true,
	}
	runner := Runner{Registry: NewRegistry(block), Events: recorder}
	job := &JobContext{
		Attempt: domain.Attempt{ID: 12},
		Flow:    domain.Flow{Steps: []domain.FlowStep{{Name: "audio-cleanup"}}},
	}

	if err := runner.Run(context.Background(), job); !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if got, want := len(recorder.events), 3; got != want {
		t.Fatalf("recorded events = %d, want %d", got, want)
	}
	decisionEvent := recorder.events[1]
	if decisionEvent.Type != domain.AttemptEventArtifact || decisionEvent.Name != StreamSelectionArtifact {
		t.Fatalf("decision event = %s/%s, want %s/%s", decisionEvent.Type, decisionEvent.Name, domain.AttemptEventArtifact, StreamSelectionArtifact)
	}
	if want := "audio fallback_fail_job"; decisionEvent.Message != want {
		t.Fatalf("decision message = %q, want %q", decisionEvent.Message, want)
	}
	var recorded domain.StreamSelectionDecision
	if err := json.Unmarshal(decisionEvent.Payload, &recorded); err != nil {
		t.Fatalf("decode decision payload: %v", err)
	}
	if got, want := recorded.MissingLanguages, []string{"deu"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("missing languages = %v, want %v", got, want)
	}
	if got := recorded.KeptIndexes(); len(got) != 0 {
		t.Fatalf("kept indexes = %v, want none", got)
	}
	if failed := recorder.events[2]; failed.Type != domain.AttemptEventBlockFailed {
		t.Fatalf("last event = %s, want %s", failed.Type, domain.AttemptEventBlockFailed)
	}
}

// TestRunnerLogsResumedDecision pins that a resumed selection is still visible
// in the log of the attempt that used it; the deciding attempt's log may have
// rotated away long ago.
func TestRunnerLogsResumedDecision(t *testing.T) {
	tests := []struct {
		name     string
		resumed  bool
		wantLogs bool
	}{
		{name: "resumed block re-emits the decision log", resumed: true, wantLogs: true},
		{name: "fresh block leaves logging to the block itself", resumed: false, wantLogs: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logs := captureLogs(t)
			block := &decisionBlock{
				BlockFunc: BlockFunc{BlockName: "audio-cleanup", Fn: func(context.Context, *JobContext) error { return nil }},
				decision:  failJobDecision,
				reported:  true,
			}
			runner := Runner{Registry: NewRegistry(block), Events: &fakeEventRecorder{}}
			if test.resumed {
				runner.StepPersistence = resumingPersistence{}
			}
			job := &JobContext{
				Attempt: domain.Attempt{ID: 13},
				Flow:    domain.Flow{Steps: []domain.FlowStep{{Name: "audio-cleanup"}}},
			}

			if err := runner.Run(context.Background(), job); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			for _, message := range []string{"stream selection decided", "stream selection requested language missing from source"} {
				if got := strings.Contains(logs.String(), message); got != test.wantLogs {
					t.Fatalf("log contains %q = %t, want %t", message, got, test.wantLogs)
				}
			}
		})
	}
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buffer
}

type resumingPersistence struct{}

func (resumingPersistence) ResumeStep(context.Context, string, *JobContext) (bool, error) {
	return true, nil
}

func (resumingPersistence) StepSucceeded(context.Context, string, *JobContext) error {
	return nil
}

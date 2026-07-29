package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

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

type resumingPersistence struct{}

func (resumingPersistence) ResumeStep(context.Context, string, *JobContext) (bool, error) {
	return true, nil
}

func (resumingPersistence) StepSucceeded(context.Context, string, *JobContext) error {
	return nil
}

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

func streamSelectionEvent(t *testing.T, attemptID domain.AttemptID, id domain.AttemptEventID, decision domain.StreamSelectionDecision) domain.AttemptEvent {
	t.Helper()
	payload, err := json.Marshal(decision)
	if err != nil {
		t.Fatalf("encode decision: %v", err)
	}
	return domain.AttemptEvent{
		ID:        id,
		AttemptID: attemptID,
		Type:      domain.AttemptEventArtifact,
		Name:      pipeline.StreamSelectionArtifact,
		Message:   string(decision.Kind) + " " + string(decision.Rule),
		Payload:   payload,
		CreatedAt: time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC),
	}
}

func audioDecision() domain.StreamSelectionDecision {
	return domain.StreamSelectionDecision{
		Kind:               domain.StreamKindAudio,
		Rule:               domain.StreamSelectionRuleLanguageFilter,
		OriginalLanguage:   "jpn",
		RequestedLanguages: []string{"orig", "deu"},
		ResolvedLanguages:  []string{"jpn", "deu"},
		MissingLanguages:   []string{"deu"},
		Streams: []domain.StreamDecision{
			{Index: 1, Codec: "aac", Language: "jpn", ResolvedLanguage: "jpn", Title: "Japanese", Channels: 2, Kept: true, Reason: domain.StreamKeptOriginalLanguage},
			{Index: 2, Codec: "ac3", Language: "eng", ResolvedLanguage: "eng", Title: "English", Channels: 6, Reason: domain.StreamDroppedLanguage},
		},
	}
}

func TestInspectEventDecodesStreamSelection(t *testing.T) {
	event := inspectEventFromDomain(streamSelectionEvent(t, 4, 9, audioDecision()))

	if event.StreamSelection == nil {
		t.Fatal("stream selection = nil, want decoded decision")
	}
	if event.Payload != nil {
		t.Fatalf("payload = %v, want nil for a decoded decision", event.Payload)
	}
	if got, want := event.StreamSelection.Rule, domain.StreamSelectionRuleLanguageFilter; got != want {
		t.Fatalf("rule = %q, want %q", got, want)
	}
	if got, want := event.StreamSelection.DroppedIndexes(), 1; len(got) != want {
		t.Fatalf("dropped indexes = %v, want %d entry", got, want)
	}
}

func TestInspectReportSurfacesLatestAttemptStreamSelection(t *testing.T) {
	older := audioDecision()
	older.Rule = domain.StreamSelectionRuleFallbackKeepFirst
	report := inspectReport{
		Job: inspectJob{ID: 3, Slug: "show-s01e01"},
		Attempts: []inspectAttempt{
			{ID: 1, Number: 1, Events: []inspectEvent{inspectEventFromDomain(streamSelectionEvent(t, 1, 1, older))}},
			{ID: 2, Number: 2, Events: []inspectEvent{inspectEventFromDomain(streamSelectionEvent(t, 2, 2, audioDecision()))}},
		},
	}
	report.StreamSelection = latestStreamSelection(report.Attempts)

	if got := len(report.StreamSelection); got != 1 {
		t.Fatalf("stream selections = %d, want 1", got)
	}
	if got, want := report.StreamSelection[0].AttemptNumber, 2; got != want {
		t.Fatalf("attempt number = %d, want %d", got, want)
	}
	if got, want := report.StreamSelection[0].Decision.Rule, domain.StreamSelectionRuleLanguageFilter; got != want {
		t.Fatalf("rule = %q, want %q", got, want)
	}

	var text bytes.Buffer
	if err := writeInspectReport(&text, report); err != nil {
		t.Fatalf("writeInspectReport() error = %v", err)
	}
	for _, want := range []string{
		"Stream selection (attempt 2):",
		"audio streams: rule=language_filter",
		"missing from source: deu",
		"kept: 1",
		"dropped: 2",
		"[2] eng ac3 \"English\" dropped (language_not_requested)",
	} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("inspect text output missing %q:\n%s", want, text.String())
		}
	}

	var encoded bytes.Buffer
	if err := writeIndentedJSON(&encoded, report); err != nil {
		t.Fatalf("writeIndentedJSON() error = %v", err)
	}
	var decoded struct {
		StreamSelection []struct {
			AttemptNumber int                            `json:"attempt_number"`
			Decision      domain.StreamSelectionDecision `json:"decision"`
		} `json:"stream_selection"`
	}
	if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil {
		t.Fatalf("decode json report: %v", err)
	}
	if got := len(decoded.StreamSelection); got != 1 {
		t.Fatalf("json stream selections = %d, want 1", got)
	}
	if got, want := decoded.StreamSelection[0].Decision.Streams[0].Reason, domain.StreamKeptOriginalLanguage; got != want {
		t.Fatalf("json kept reason = %q, want %q", got, want)
	}
}

// TestInspectReportSurfacesAnUnreadableLatestSelection pins that a corrupt
// newest record is reported as unreadable rather than skipped. Skipping it made
// latestStreamSelection walk back to an older attempt and present a superseded
// decision as the current state of the file, which is the exact trap the
// stream-selection record exists to remove.
func TestInspectReportSurfacesAnUnreadableLatestSelection(t *testing.T) {
	corrupt := domain.AttemptEvent{
		ID: 2, AttemptID: 2, Type: domain.AttemptEventArtifact,
		Name: pipeline.StreamSelectionArtifact, Payload: []byte("{not json"),
	}
	report := inspectReport{
		Job: inspectJob{ID: 3, Slug: "show-s01e01"},
		Attempts: []inspectAttempt{
			{ID: 1, Number: 1, Events: []inspectEvent{inspectEventFromDomain(streamSelectionEvent(t, 1, 1, audioDecision()))}},
			{ID: 2, Number: 2, Events: []inspectEvent{inspectEventFromDomain(corrupt)}},
		},
	}
	report.StreamSelection = latestStreamSelection(report.Attempts)

	if got := len(report.StreamSelection); got != 1 {
		t.Fatalf("stream selections = %d, want the unreadable record", got)
	}
	selection := report.StreamSelection[0]
	if selection.AttemptNumber != 2 {
		t.Fatalf("attempt number = %d, want 2 rather than a fallback to the stale attempt", selection.AttemptNumber)
	}
	if selection.Decision != nil {
		t.Fatalf("decision = %+v, want none so it cannot read as \"nothing dropped\"", selection.Decision)
	}
	if selection.DecisionError == "" {
		t.Fatal("decision error = \"\", want the decode failure reported")
	}

	var text bytes.Buffer
	if err := writeInspectReport(&text, report); err != nil {
		t.Fatalf("writeInspectReport() error = %v", err)
	}
	if !strings.Contains(text.String(), "unreadable:") {
		t.Fatalf("inspect text output missing the unreadable record:\n%s", text.String())
	}
}

// TestInspectReportIgnoresUnreadableUnrelatedArtifacts keeps a corrupt payload
// of some other artifact from being reported as a stream selection, and from
// hiding the real decision recorded on the same attempt.
func TestInspectReportIgnoresUnreadableUnrelatedArtifacts(t *testing.T) {
	corrupt := domain.AttemptEvent{
		ID: 3, AttemptID: 2, Type: domain.AttemptEventArtifact,
		Name: processOutputArtifactName, Payload: []byte("{not json"),
	}
	report := inspectReport{
		Job: inspectJob{ID: 3, Slug: "show-s01e01"},
		Attempts: []inspectAttempt{{ID: 2, Number: 2, Events: []inspectEvent{
			inspectEventFromDomain(corrupt),
			inspectEventFromDomain(streamSelectionEvent(t, 2, 4, audioDecision())),
		}}},
	}
	report.StreamSelection = latestStreamSelection(report.Attempts)

	if got := len(report.StreamSelection); got != 1 {
		t.Fatalf("stream selections = %d, want only the real decision", got)
	}
	if report.StreamSelection[0].Decision == nil {
		t.Fatal("decision = nil, want the readable stream selection")
	}
}

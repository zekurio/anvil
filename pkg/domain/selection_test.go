package domain

import (
	"testing"
)

func TestStreamSelectionDecisionIndexes(t *testing.T) {
	decision := StreamSelectionDecision{Streams: []StreamDecision{
		{Index: 1, Kept: true, Reason: StreamKeptLanguageMatch},
		{Index: 2, Reason: StreamDroppedLanguage},
		{Index: 3, Kept: true, Reason: StreamKeptOriginalLanguage},
	}}

	if got, want := decision.KeptIndexes(), []int{1, 3}; !equalInts(got, want) {
		t.Fatalf("KeptIndexes() = %v, want %v", got, want)
	}
	if got, want := decision.DroppedIndexes(), []int{2}; !equalInts(got, want) {
		t.Fatalf("DroppedIndexes() = %v, want %v", got, want)
	}
}

func TestMissingRequestedLanguages(t *testing.T) {
	tests := []struct {
		name      string
		requested []string
		streams   []StreamDecision
		want      []string
	}{
		{
			name:      "language absent from source",
			requested: []string{"jpn", "deu"},
			streams: []StreamDecision{
				{Index: 1, ResolvedLanguage: "jpn"},
				{Index: 2, ResolvedLanguage: "eng"},
			},
			want: []string{"deu"},
		},
		{
			name:      "every language present",
			requested: []string{"jpn"},
			streams:   []StreamDecision{{Index: 1, ResolvedLanguage: "jpn"}},
			want:      nil,
		},
		{
			name:      "no streams at all",
			requested: []string{"deu"},
			streams:   nil,
			want:      []string{"deu"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := MissingRequestedLanguages(test.requested, test.streams); !equalStrings(got, test.want) {
				t.Fatalf("MissingRequestedLanguages() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestStreamSelectionDecisionFellBack(t *testing.T) {
	tests := []struct {
		name string
		rule StreamSelectionRule
		want bool
	}{
		{name: "language filter", rule: StreamSelectionRuleLanguageFilter, want: false},
		{name: "cleanup disabled", rule: StreamSelectionRuleCleanupDisabled, want: false},
		{name: "keep first fallback", rule: StreamSelectionRuleFallbackKeepFirst, want: true},
		{name: "keep all fallback", rule: StreamSelectionRuleFallbackKeepAll, want: true},
		{name: "fail job fallback", rule: StreamSelectionRuleFallbackFailJob, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := StreamSelectionDecision{Rule: test.rule}
			if got := decision.FellBack(); got != test.want {
				t.Fatalf("FellBack() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestStreamDecisionSummary(t *testing.T) {
	tests := []struct {
		name     string
		decision StreamDecision
		want     string
	}{
		{
			name:     "kept stream",
			decision: StreamDecision{Index: 1, Codec: "aac", Language: "jpn", Kept: true, Reason: StreamKeptOriginalLanguage},
			want:     "1:jpn:aac:kept:original_language",
		},
		{
			name:     "dropped stream without language",
			decision: StreamDecision{Index: 3, Reason: StreamDroppedLanguage},
			want:     "3:und:unknown:dropped:language_not_requested",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.decision.Summary(); got != test.want {
				t.Fatalf("Summary() = %q, want %q", got, test.want)
			}
		})
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

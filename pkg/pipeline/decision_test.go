package pipeline

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestLogStreamSelectionWarnsOnlyAboutSurprises(t *testing.T) {
	tests := []struct {
		name     string
		decision domain.StreamSelectionDecision
		warning  string
	}{
		{"keep all is what the profile asked for", domain.StreamSelectionDecision{Kind: domain.StreamKindAudio, Rule: domain.StreamSelectionRuleKeepAll}, ""},
		{"remove all is what the profile asked for", domain.StreamSelectionDecision{Kind: domain.StreamKindSubtitle, Rule: domain.StreamSelectionRuleRemoveAll}, ""},
		{"language filter", domain.StreamSelectionDecision{Kind: domain.StreamKindAudio, Rule: domain.StreamSelectionRuleLanguageFilter}, ""},
		{
			"disabled cleanup",
			domain.StreamSelectionDecision{Kind: domain.StreamKindAudio, Rule: domain.StreamSelectionRuleCleanupDisabled, CleanupDisabledReason: "original language metadata is unavailable"},
			"stream cleanup disabled",
		},
		{"fallback", domain.StreamSelectionDecision{Kind: domain.StreamKindAudio, Rule: domain.StreamSelectionRuleFallbackKeepAll}, "stream selection fell back"},
		{
			"missing language",
			domain.StreamSelectionDecision{Kind: domain.StreamKindSubtitle, Rule: domain.StreamSelectionRuleBitmapOnly, MissingLanguages: []string{"eng"}},
			"requested language missing from source",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
			defer slog.SetDefault(previous)

			LogStreamSelection(&JobContext{}, tt.decision)

			logged := output.String()
			if !strings.Contains(logged, "level=INFO") || !strings.Contains(logged, "rule="+string(tt.decision.Rule)) {
				t.Fatalf("decision summary missing: %s", logged)
			}
			if tt.warning == "" && strings.Contains(logged, "level=WARN") {
				t.Fatalf("unexpected warning: %s", logged)
			}
			if tt.warning != "" && !strings.Contains(logged, tt.warning) {
				t.Fatalf("warning %q missing: %s", tt.warning, logged)
			}
		})
	}
}

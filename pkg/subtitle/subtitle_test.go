package subtitle

import (
	"reflect"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestSelect(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 0, Type: "video", Codec: "hevc"},
		{Index: 3, Type: "subtitle", Codec: "hdmv_pgs_subtitle", Language: "eng"},
		{Index: 4, Type: "subtitle", Codec: "subrip", Language: "deu"},
		{Index: 5, Type: "subtitle", Codec: "dvd_subtitle", Language: "deu"},
		{Index: 6, Type: "subtitle", Codec: "ass", Language: "jpn"},
	}}
	bitmapOnly := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 2, Type: "subtitle", Codec: "hdmv_pgs_subtitle", Language: "eng"},
		{Index: 3, Type: "subtitle", Codec: "dvb_subtitle", Language: "eng"},
		{Index: 4, Type: "subtitle", Codec: "XSUB", Language: "eng"},
	}}
	const (
		bitmap   = domain.StreamDroppedBitmap
		language = domain.StreamDroppedLanguage
	)
	tests := []struct {
		name        string
		probe       *domain.ProbeResult
		profile     domain.SubtitleProfile
		metadata    domain.JobMetadata
		wantRule    domain.StreamSelectionRule
		wantKept    []int
		wantReasons []domain.StreamDecisionReason
		wantMissing []string
		wantErr     bool
	}{
		{
			name:        "no languages keeps every track",
			probe:       probe,
			wantRule:    domain.StreamSelectionRuleKeepAll,
			wantKept:    []int{3, 4, 5, 6},
			wantReasons: []domain.StreamDecisionReason{domain.StreamKeptKeepAll, domain.StreamKeptKeepAll, domain.StreamKeptKeepAll, domain.StreamKeptKeepAll},
		},
		{
			name:        "remove all overrides languages and fallback",
			probe:       probe,
			profile:     domain.SubtitleProfile{RemoveAll: true, LanguagesToKeep: []string{"deu"}, Fallback: domain.StreamFallbackKeepAll, DropBitmap: true},
			wantRule:    domain.StreamSelectionRuleRemoveAll,
			wantReasons: []domain.StreamDecisionReason{domain.StreamDroppedRemoveAll, domain.StreamDroppedRemoveAll, domain.StreamDroppedRemoveAll, domain.StreamDroppedRemoveAll},
		},
		{
			name:     "remove all overrides a disabled cleanup",
			probe:    probe,
			profile:  domain.SubtitleProfile{RemoveAll: true, LanguagesToKeep: []string{"orig"}},
			metadata: domain.JobMetadata{StreamCleanupDisabled: true},
			wantRule: domain.StreamSelectionRuleRemoveAll,
			wantReasons: []domain.StreamDecisionReason{
				domain.StreamDroppedRemoveAll, domain.StreamDroppedRemoveAll, domain.StreamDroppedRemoveAll, domain.StreamDroppedRemoveAll,
			},
		},
		{
			name:        "drop bitmap without languages keeps the text tracks",
			probe:       probe,
			profile:     domain.SubtitleProfile{DropBitmap: true},
			wantRule:    domain.StreamSelectionRuleKeepAll,
			wantKept:    []int{4, 6},
			wantReasons: []domain.StreamDecisionReason{bitmap, domain.StreamKeptKeepAll, bitmap, domain.StreamKeptKeepAll},
		},
		{
			name:        "drop bitmap runs before the language rules",
			probe:       probe,
			profile:     domain.SubtitleProfile{DropBitmap: true, LanguagesToKeep: []string{"deu"}},
			wantRule:    domain.StreamSelectionRuleLanguageFilter,
			wantKept:    []int{4},
			wantReasons: []domain.StreamDecisionReason{bitmap, domain.StreamKeptLanguageMatch, bitmap, language},
		},
		{
			name:        "a language that only exists as a bitmap track is missing",
			probe:       probe,
			profile:     domain.SubtitleProfile{DropBitmap: true, LanguagesToKeep: []string{"eng"}, Fallback: domain.StreamFallbackKeepFirst},
			wantRule:    domain.StreamSelectionRuleFallbackKeepFirst,
			wantKept:    []int{4},
			wantReasons: []domain.StreamDecisionReason{bitmap, domain.StreamKeptFallbackKeepFirst, bitmap, language},
			wantMissing: []string{"eng"},
		},
		{
			name:        "keep-all fallback does not resurrect bitmap tracks",
			probe:       probe,
			profile:     domain.SubtitleProfile{DropBitmap: true, LanguagesToKeep: []string{"fra"}, Fallback: domain.StreamFallbackKeepAll},
			wantRule:    domain.StreamSelectionRuleFallbackKeepAll,
			wantKept:    []int{4, 6},
			wantReasons: []domain.StreamDecisionReason{bitmap, domain.StreamKeptFallbackKeepAll, bitmap, domain.StreamKeptFallbackKeepAll},
			wantMissing: []string{"fra"},
		},
		{
			name:        "a source with only bitmap tracks keeps none, whatever the fallback",
			probe:       bitmapOnly,
			profile:     domain.SubtitleProfile{DropBitmap: true, LanguagesToKeep: []string{"eng"}, Fallback: domain.StreamFallbackFailJob},
			wantRule:    domain.StreamSelectionRuleBitmapOnly,
			wantReasons: []domain.StreamDecisionReason{bitmap, bitmap, bitmap},
			wantMissing: []string{"eng"},
		},
		{
			name:        "a disabled cleanup still drops bitmap tracks",
			probe:       probe,
			profile:     domain.SubtitleProfile{DropBitmap: true, LanguagesToKeep: []string{"orig"}},
			metadata:    domain.JobMetadata{StreamCleanupDisabled: true},
			wantRule:    domain.StreamSelectionRuleCleanupDisabled,
			wantKept:    []int{4, 6},
			wantReasons: []domain.StreamDecisionReason{bitmap, domain.StreamKeptCleanupDisabled, bitmap, domain.StreamKeptCleanupDisabled},
		},
		{
			name:     "a disabled cleanup keeps every track otherwise",
			probe:    probe,
			profile:  domain.SubtitleProfile{LanguagesToKeep: []string{"orig"}},
			metadata: domain.JobMetadata{StreamCleanupDisabled: true},
			wantRule: domain.StreamSelectionRuleCleanupDisabled,
			wantKept: []int{3, 4, 5, 6},
			wantReasons: []domain.StreamDecisionReason{
				domain.StreamKeptCleanupDisabled, domain.StreamKeptCleanupDisabled, domain.StreamKeptCleanupDisabled, domain.StreamKeptCleanupDisabled,
			},
		},
		{
			name:        "language filter keeps bitmap tracks unless told otherwise",
			probe:       probe,
			profile:     domain.SubtitleProfile{LanguagesToKeep: []string{"deu"}},
			wantRule:    domain.StreamSelectionRuleLanguageFilter,
			wantKept:    []int{4, 5},
			wantReasons: []domain.StreamDecisionReason{language, domain.StreamKeptLanguageMatch, domain.StreamKeptLanguageMatch, language},
		},
		{
			name:        "fail-job fallback reports its decision with the error",
			probe:       probe,
			profile:     domain.SubtitleProfile{LanguagesToKeep: []string{"fra"}, Fallback: domain.StreamFallbackFailJob},
			wantRule:    domain.StreamSelectionRuleFallbackFailJob,
			wantReasons: []domain.StreamDecisionReason{language, language, language, language},
			wantMissing: []string{"fra"},
			wantErr:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection, err := (Selector{}).Select(tt.probe, tt.profile, tt.metadata)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, want error %v", err, tt.wantErr)
			}
			decision := selection.Decision
			if decision == nil || decision.Rule != tt.wantRule {
				t.Fatalf("decision = %+v, want rule %q", decision, tt.wantRule)
			}
			if !reflect.DeepEqual(selection.StreamIndexes, tt.wantKept) || !reflect.DeepEqual(decision.KeptIndexes(), append([]int{}, tt.wantKept...)) {
				t.Fatalf("kept = %v, decision kept = %v, want %v", selection.StreamIndexes, decision.KeptIndexes(), tt.wantKept)
			}
			reasons := make([]domain.StreamDecisionReason, 0, len(decision.Streams))
			for _, stream := range decision.Streams {
				reasons = append(reasons, stream.Reason)
			}
			if !reflect.DeepEqual(reasons, tt.wantReasons) {
				t.Fatalf("reasons = %v\nwant      %v", reasons, tt.wantReasons)
			}
			if !reflect.DeepEqual(decision.MissingLanguages, tt.wantMissing) {
				t.Fatalf("missing = %v, want %v", decision.MissingLanguages, tt.wantMissing)
			}
		})
	}
}

func TestSelectWithoutSubtitleStreams(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{{Index: 0, Type: "video"}}}
	selection, err := (Selector{}).Select(probe, domain.SubtitleProfile{RemoveAll: true, DropBitmap: true}, domain.JobMetadata{})
	if err != nil || selection.Decision.Rule != domain.StreamSelectionRuleNoStreams || len(selection.StreamIndexes) != 0 {
		t.Fatalf("selection = %+v, %v", selection, err)
	}
}

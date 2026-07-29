package subtitle

import (
	"context"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

func TestSelectExpandsOriginalAndDeduplicatesLanguages(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 0, Type: "video"},
		{Index: 1, Type: "subtitle", Language: "ger"},
		{Index: 2, Type: "subtitle", Language: "eng"},
	}}
	selection, err := (Selector{}).Select(probe, domain.SubtitleProfile{
		LanguagesToKeep: []string{"orig", "deu"},
		Fallback:        domain.StreamFallbackFailJob,
	}, domain.JobMetadata{OriginalLanguage: "German"})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if got, want := selection.StreamIndexes, []int{1}; !equalInts(got, want) {
		t.Fatalf("stream indexes = %v, want %v", got, want)
	}
	if got, want := selection.LanguagesToKeep, []string{"deu"}; !equalStrings(got, want) {
		t.Fatalf("languages to keep = %v, want %v", got, want)
	}
}

func TestSelectWithoutLanguageFilterPreservesAllSubtitleStreams(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 1, Type: "subtitle", Language: "eng", Title: "Director commentary"},
		{Index: 2, Type: "subtitle", Language: "deu", Disposition: map[string]bool{"hearing_impaired": true}},
	}}
	selection, err := (Selector{}).Select(probe, domain.SubtitleProfile{
		Fallback: domain.StreamFallbackKeepAll,
	}, domain.JobMetadata{})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if got, want := selection.StreamIndexes, []int{1, 2}; !equalInts(got, want) {
		t.Fatalf("stream indexes = %v, want %v", got, want)
	}
}

func TestSelectKeepsOriginalAndConfiguredLanguage(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 1, Type: "subtitle", Language: "jpn"},
		{Index: 2, Type: "subtitle", Language: "deu"},
		{Index: 3, Type: "subtitle", Language: "eng"},
	}}
	selection, err := (Selector{}).Select(probe, domain.SubtitleProfile{
		LanguagesToKeep: []string{"orig", "deu"},
		Fallback:        domain.StreamFallbackFailJob,
	}, domain.JobMetadata{OriginalLanguage: "jpn"})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if got, want := selection.StreamIndexes, []int{1, 2}; !equalInts(got, want) {
		t.Fatalf("stream indexes = %v, want %v", got, want)
	}
}

func TestSelectDropsSpecialPurposeSubtitlesByDefault(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 1, Type: "subtitle", Language: "eng", Disposition: map[string]bool{"forced": true}},
		{Index: 2, Type: "subtitle", Language: "eng", Disposition: map[string]bool{"hearing_impaired": true}},
		{Index: 3, Type: "subtitle", Language: "eng", Title: "Director commentary"},
		{Index: 4, Type: "subtitle", Language: "eng", Title: "Main"},
	}}
	selection, err := (Selector{}).Select(probe, domain.SubtitleProfile{
		LanguagesToKeep: []string{"eng"},
		Fallback:        domain.StreamFallbackKeepFirst,
	}, domain.JobMetadata{})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if got, want := selection.StreamIndexes, []int{4}; !equalInts(got, want) {
		t.Fatalf("stream indexes = %v, want %v", got, want)
	}
}

func TestSelectKeepsSpecialPurposeSubtitlesWhenConfigured(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 1, Type: "subtitle", Language: "eng", Disposition: map[string]bool{"forced": true}},
		{Index: 2, Type: "subtitle", Language: "eng", Disposition: map[string]bool{"captions": true}},
		{Index: 3, Type: "subtitle", Language: "eng", Title: "Director commentary"},
	}}
	selection, err := (Selector{}).Select(probe, domain.SubtitleProfile{
		LanguagesToKeep: []string{"eng"},
		KeepForced:      true,
		KeepSDH:         true,
		KeepCommentary:  true,
		Fallback:        domain.StreamFallbackFailJob,
	}, domain.JobMetadata{})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if got, want := selection.StreamIndexes, []int{1, 2, 3}; !equalInts(got, want) {
		t.Fatalf("stream indexes = %v, want %v", got, want)
	}
}

func TestSelectCanTreatUnknownLanguageAsOriginal(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 1, Type: "subtitle", Language: "und", Title: "Main"},
		{Index: 2, Type: "subtitle", Language: "deu", Title: "Main"},
	}}
	selection, err := (Selector{}).Select(probe, domain.SubtitleProfile{
		LanguagesToKeep:   []string{"orig"},
		UnknownAsOriginal: true,
		Fallback:          domain.StreamFallbackFailJob,
	}, domain.JobMetadata{OriginalLanguage: "English"})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if got, want := selection.StreamIndexes, []int{1}; !equalInts(got, want) {
		t.Fatalf("stream indexes = %v, want %v", got, want)
	}
}

func TestSelectFallbackKeepFirstWhenNoLanguageMatches(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 1, Type: "subtitle", Language: "eng"},
		{Index: 2, Type: "subtitle", Language: "jpn"},
	}}
	selection, err := (Selector{}).Select(probe, domain.SubtitleProfile{
		LanguagesToKeep: []string{"deu"},
		Fallback:        domain.StreamFallbackKeepFirst,
	}, domain.JobMetadata{})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if got, want := selection.StreamIndexes, []int{1}; !equalInts(got, want) {
		t.Fatalf("stream indexes = %v, want %v", got, want)
	}
}

func TestSelectFallsBackWhenOriginalLanguageIsUnavailable(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 1, Type: "subtitle", Language: "eng"},
		{Index: 2, Type: "subtitle", Language: "jpn"},
	}}
	selection, err := (Selector{}).Select(probe, domain.SubtitleProfile{
		LanguagesToKeep: []string{"orig"},
		Fallback:        domain.StreamFallbackKeepFirst,
	}, domain.JobMetadata{})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if got, want := selection.StreamIndexes, []int{1, 2}; !equalInts(got, want) {
		t.Fatalf("stream indexes = %v, want %v", got, want)
	}
}

func TestSelectPreservesAllSubtitlesWhenStreamCleanupIsDisabled(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 1, Type: "subtitle", Language: "eng", Title: "Director commentary"},
		{Index: 2, Type: "subtitle", Language: "jpn", Title: "Main"},
	}}
	selection, err := (Selector{}).Select(probe, domain.SubtitleProfile{
		LanguagesToKeep: []string{"orig"},
		Fallback:        domain.StreamFallbackFailJob,
	}, domain.JobMetadata{
		StreamCleanupDisabled: true,
	})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if got, want := selection.StreamIndexes, []int{1, 2}; !equalInts(got, want) {
		t.Fatalf("stream indexes = %v, want %v", got, want)
	}
}

func TestSelectRecordsStreamDecision(t *testing.T) {
	tests := []struct {
		name        string
		streams     []domain.MediaStream
		profile     domain.SubtitleProfile
		metadata    domain.JobMetadata
		wantRule    domain.StreamSelectionRule
		wantKept    []int
		wantDropped []int
		wantReasons map[int]domain.StreamDecisionReason
		wantMissing []string
	}{
		{
			name: "explicit language and original language are distinguishable",
			streams: []domain.MediaStream{
				{Index: 1, Type: "subtitle", Codec: "subrip", Language: "jpn"},
				{Index: 2, Type: "subtitle", Codec: "subrip", Language: "deu"},
				{Index: 3, Type: "subtitle", Codec: "subrip", Language: "eng"},
			},
			profile:     domain.SubtitleProfile{LanguagesToKeep: []string{"orig", "deu"}, Fallback: domain.StreamFallbackKeepAll},
			metadata:    domain.JobMetadata{OriginalLanguage: "jpn"},
			wantRule:    domain.StreamSelectionRuleLanguageFilter,
			wantKept:    []int{1, 2},
			wantDropped: []int{3},
			wantReasons: map[int]domain.StreamDecisionReason{
				1: domain.StreamKeptOriginalLanguage,
				2: domain.StreamKeptLanguageMatch,
				3: domain.StreamDroppedLanguage,
			},
		},
		{
			name: "unknown language kept as original language",
			streams: []domain.MediaStream{
				{Index: 1, Type: "subtitle", Language: "und"},
				{Index: 2, Type: "subtitle", Language: "deu"},
			},
			profile:     domain.SubtitleProfile{LanguagesToKeep: []string{"orig"}, UnknownAsOriginal: true, Fallback: domain.StreamFallbackKeepAll},
			metadata:    domain.JobMetadata{OriginalLanguage: "English"},
			wantRule:    domain.StreamSelectionRuleLanguageFilter,
			wantKept:    []int{1},
			wantDropped: []int{2},
			wantReasons: map[int]domain.StreamDecisionReason{
				1: domain.StreamKeptUnknownAsOriginal,
				2: domain.StreamDroppedLanguage,
			},
		},
		{
			name: "special purpose subtitles report their own reason",
			streams: []domain.MediaStream{
				{Index: 1, Type: "subtitle", Language: "eng", Disposition: map[string]bool{"forced": true}},
				{Index: 2, Type: "subtitle", Language: "eng", Disposition: map[string]bool{"hearing_impaired": true}},
				{Index: 3, Type: "subtitle", Language: "eng", Title: "Director commentary"},
				{Index: 4, Type: "subtitle", Language: "eng", Title: "Main"},
			},
			profile:     domain.SubtitleProfile{LanguagesToKeep: []string{"eng"}, Fallback: domain.StreamFallbackKeepAll},
			wantRule:    domain.StreamSelectionRuleLanguageFilter,
			wantKept:    []int{4},
			wantDropped: []int{1, 2, 3},
			wantReasons: map[int]domain.StreamDecisionReason{
				1: domain.StreamDroppedForced,
				2: domain.StreamDroppedSDH,
				3: domain.StreamDroppedCommentary,
				4: domain.StreamKeptLanguageMatch,
			},
		},
		{
			name: "keep first fallback when no language matches",
			streams: []domain.MediaStream{
				{Index: 1, Type: "subtitle", Language: "eng"},
				{Index: 2, Type: "subtitle", Language: "jpn"},
			},
			profile:     domain.SubtitleProfile{LanguagesToKeep: []string{"deu"}, Fallback: domain.StreamFallbackKeepFirst},
			wantRule:    domain.StreamSelectionRuleFallbackKeepFirst,
			wantKept:    []int{1},
			wantDropped: []int{2},
			wantReasons: map[int]domain.StreamDecisionReason{
				1: domain.StreamKeptFallbackKeepFirst,
				2: domain.StreamDroppedLanguage,
			},
			wantMissing: []string{"deu"},
		},
		{
			name: "keep all fallback when no language matches",
			streams: []domain.MediaStream{
				{Index: 1, Type: "subtitle", Language: "eng"},
				{Index: 2, Type: "subtitle", Language: "jpn"},
			},
			profile:  domain.SubtitleProfile{LanguagesToKeep: []string{"deu"}, Fallback: domain.StreamFallbackKeepAll},
			wantRule: domain.StreamSelectionRuleFallbackKeepAll,
			wantKept: []int{1, 2},
			wantReasons: map[int]domain.StreamDecisionReason{
				1: domain.StreamKeptFallbackKeepAll,
				2: domain.StreamKeptFallbackKeepAll,
			},
			wantMissing: []string{"deu"},
		},
		{
			name: "cleanup disabled keeps every stream",
			streams: []domain.MediaStream{
				{Index: 1, Type: "subtitle", Language: "eng"},
				{Index: 2, Type: "subtitle", Language: "jpn"},
			},
			profile:  domain.SubtitleProfile{LanguagesToKeep: []string{"orig"}, Fallback: domain.StreamFallbackFailJob},
			metadata: domain.JobMetadata{StreamCleanupDisabled: true, StreamCleanupDisabledReason: "metadata lookup failed"},
			wantRule: domain.StreamSelectionRuleCleanupDisabled,
			wantKept: []int{1, 2},
			wantReasons: map[int]domain.StreamDecisionReason{
				1: domain.StreamKeptCleanupDisabled,
				2: domain.StreamKeptCleanupDisabled,
			},
		},
		{
			name:     "source without subtitle streams",
			streams:  []domain.MediaStream{{Index: 0, Type: "video"}},
			profile:  domain.SubtitleProfile{LanguagesToKeep: []string{"deu"}, Fallback: domain.StreamFallbackKeepAll},
			wantRule: domain.StreamSelectionRuleNoStreams,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selection, err := (Selector{}).Select(&domain.ProbeResult{Streams: test.streams}, test.profile, test.metadata)
			if err != nil {
				t.Fatalf("Select() error = %v", err)
			}
			decision := selection.Decision
			if decision == nil {
				t.Fatal("Select() decision = nil, want record")
			}
			if decision.Kind != domain.StreamKindSubtitle {
				t.Fatalf("decision kind = %q, want %q", decision.Kind, domain.StreamKindSubtitle)
			}
			if decision.Rule != test.wantRule {
				t.Fatalf("decision rule = %q, want %q", decision.Rule, test.wantRule)
			}
			if got := decision.KeptIndexes(); !equalInts(got, test.wantKept) {
				t.Fatalf("kept indexes = %v, want %v", got, test.wantKept)
			}
			if !equalInts(decision.KeptIndexes(), selection.StreamIndexes) {
				t.Fatalf("kept indexes = %v, want selection %v", decision.KeptIndexes(), selection.StreamIndexes)
			}
			if test.wantDropped != nil {
				if got := decision.DroppedIndexes(); !equalInts(got, test.wantDropped) {
					t.Fatalf("dropped indexes = %v, want %v", got, test.wantDropped)
				}
			}
			if got := decision.MissingLanguages; !equalStrings(got, test.wantMissing) {
				t.Fatalf("missing languages = %v, want %v", got, test.wantMissing)
			}
			for index, reason := range test.wantReasons {
				stream, ok := streamDecision(decision.Streams, index)
				if !ok {
					t.Fatalf("decision for stream %d is missing", index)
				}
				if stream.Reason != reason {
					t.Fatalf("stream %d reason = %q, want %q", index, stream.Reason, reason)
				}
			}
		})
	}
}

func TestBlockExposesDecisionForAttemptEvent(t *testing.T) {
	job := &pipeline.JobContext{
		Probe: &domain.ProbeResult{Streams: []domain.MediaStream{
			{Index: 1, Type: "subtitle", Language: "jpn"},
			{Index: 2, Type: "subtitle", Language: "eng"},
		}},
		Profile: domain.Profile{Subtitles: domain.SubtitleProfile{
			LanguagesToKeep: []string{"orig"},
			Fallback:        domain.StreamFallbackKeepAll,
		}},
		Metadata: domain.JobMetadata{OriginalLanguage: "jpn"},
	}
	block := Block{}
	if _, ok := block.Decision(job); ok {
		t.Fatal("Decision() before Run reported a decision")
	}
	if err := block.Run(context.Background(), job); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	decision, ok := block.Decision(job)
	if !ok {
		t.Fatal("Decision() after Run = false, want true")
	}
	if got, want := decision.KeptIndexes(), []int{1}; !equalInts(got, want) {
		t.Fatalf("kept indexes = %v, want %v", got, want)
	}
}

func streamDecision(streams []domain.StreamDecision, index int) (domain.StreamDecision, bool) {
	for _, stream := range streams {
		if stream.Index == index {
			return stream, true
		}
	}
	return domain.StreamDecision{}, false
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

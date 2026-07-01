package subtitle

import (
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
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

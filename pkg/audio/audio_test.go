package audio

import (
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestSelectExpandsOriginalAndDeduplicatesLanguages(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 0, Type: "video"},
		{Index: 1, Type: "audio", Language: "ger"},
		{Index: 2, Type: "audio", Language: "eng"},
	}}
	selection, err := (Selector{}).Select(probe, domain.AudioProfile{
		Mode:            domain.StreamPolicyCleanup,
		LanguagesToKeep: []string{"orig", "deu"},
		Fallback:        domain.StreamFallbackFailJob,
	}, domain.JobMetadata{OriginalLanguage: "de"})
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

func TestSelectPreserveModeKeepsAllAudioStreams(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 1, Type: "audio", Language: "eng", Title: "Director commentary"},
		{Index: 2, Type: "audio", Language: "deu", Title: "Main"},
	}}
	selection, err := (Selector{}).Select(probe, domain.AudioProfile{
		Mode:     domain.StreamPolicyPreserve,
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
		{Index: 1, Type: "audio", Language: "jpn"},
		{Index: 2, Type: "audio", Language: "deu"},
		{Index: 3, Type: "audio", Language: "eng"},
	}}
	selection, err := (Selector{}).Select(probe, domain.AudioProfile{
		Mode:            domain.StreamPolicyCleanup,
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

func TestSelectDropsCommentaryAndFallsBack(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 1, Type: "audio", Language: "eng", Title: "Director commentary"},
		{Index: 2, Type: "audio", Language: "eng", Title: "Main"},
	}}
	selection, err := (Selector{}).Select(probe, domain.AudioProfile{
		Mode:            domain.StreamPolicyCleanup,
		LanguagesToKeep: []string{"eng"},
		Fallback:        domain.StreamFallbackKeepFirst,
		MaxTracks:       1,
	}, domain.JobMetadata{})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if got, want := selection.StreamIndexes, []int{2}; !equalInts(got, want) {
		t.Fatalf("stream indexes = %v, want %v", got, want)
	}
}

func TestSelectKeepsCommentaryWhenConfigured(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 1, Type: "audio", Language: "eng", Title: "Director commentary"},
		{Index: 2, Type: "audio", Language: "eng", Title: "Main"},
	}}
	selection, err := (Selector{}).Select(probe, domain.AudioProfile{
		Mode:            domain.StreamPolicyCleanup,
		LanguagesToKeep: []string{"eng"},
		KeepCommentary:  true,
		Fallback:        domain.StreamFallbackFailJob,
	}, domain.JobMetadata{})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if got, want := selection.StreamIndexes, []int{1, 2}; !equalInts(got, want) {
		t.Fatalf("stream indexes = %v, want %v", got, want)
	}
}

func TestSelectKeepsOtherTracksWhenConfigured(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 1, Type: "audio", Language: "eng", Title: "Main"},
		{Index: 2, Type: "audio", Language: "jpn", Title: "Main"},
		{Index: 3, Type: "audio", Language: "deu", Title: "Director commentary"},
	}}
	selection, err := (Selector{}).Select(probe, domain.AudioProfile{
		Mode:            domain.StreamPolicyCleanup,
		LanguagesToKeep: []string{"eng"},
		KeepOtherTracks: true,
		Fallback:        domain.StreamFallbackFailJob,
	}, domain.JobMetadata{})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if got, want := selection.StreamIndexes, []int{1, 2}; !equalInts(got, want) {
		t.Fatalf("stream indexes = %v, want %v", got, want)
	}
}

func TestSelectFallbackKeepFirstWhenNoLanguageMatches(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 1, Type: "audio", Language: "eng"},
		{Index: 2, Type: "audio", Language: "jpn"},
	}}
	selection, err := (Selector{}).Select(probe, domain.AudioProfile{
		Mode:            domain.StreamPolicyCleanup,
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
		{Index: 1, Type: "audio", Language: "eng"},
		{Index: 2, Type: "audio", Language: "jpn"},
	}}
	selection, err := (Selector{}).Select(probe, domain.AudioProfile{
		Mode:            domain.StreamPolicyCleanup,
		LanguagesToKeep: []string{"orig"},
		Fallback:        domain.StreamFallbackKeepFirst,
	}, domain.JobMetadata{})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if got, want := selection.StreamIndexes, []int{1}; !equalInts(got, want) {
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

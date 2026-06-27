package audio

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

const OriginalLanguageToken = "orig"

type Selector struct{}

func (Selector) Select(probe *domain.ProbeResult, profile domain.AudioProfile, metadata domain.JobMetadata) (domain.AudioSelection, error) {
	if probe == nil {
		return domain.AudioSelection{}, errors.New("audio selection requires probe result")
	}
	audioStreams := audioStreams(probe.Streams)
	selection := domain.AudioSelection{
		OriginalLanguage: normalizeLanguage(metadata.OriginalLanguage),
		LanguagesToKeep:  expandedLanguages(profile, metadata),
	}
	if len(audioStreams) == 0 {
		return selection, nil
	}
	if !filtersEnabled(profile) {
		selection.StreamIndexes = streamIndexes(audioStreams)
		return selection, nil
	}

	keep := languageSet(selection.LanguagesToKeep)
	filterLanguages := languageFilterEnabled(profile)
	for _, stream := range audioStreams {
		if filterLanguages && !keep[normalizeLanguage(stream.Language)] {
			continue
		}
		if !profile.KeepCommentary && commentary(stream) {
			continue
		}
		if !profile.KeepDescriptiveAudio && descriptiveAudio(stream) {
			continue
		}
		selection.StreamIndexes = append(selection.StreamIndexes, stream.Index)
		if profile.MaxTracks > 0 && len(selection.StreamIndexes) >= profile.MaxTracks {
			break
		}
	}
	if len(selection.StreamIndexes) > 0 {
		return selection, nil
	}

	switch profile.Fallback {
	case domain.StreamFallbackFailJob:
		return selection, fmt.Errorf("no audio streams matched languages %v", selection.LanguagesToKeep)
	case domain.StreamFallbackKeepFirst:
		selection.StreamIndexes = []int{audioStreams[0].Index}
	default:
		selection.StreamIndexes = streamIndexes(audioStreams)
	}
	return selection, nil
}

type Block struct {
	Selector Selector
}

func (Block) Name() string {
	return "audio-cleanup"
}

func (b Block) Run(_ context.Context, job *pipeline.JobContext) error {
	selection, err := b.Selector.Select(job.Probe, job.Profile.Audio, job.Metadata)
	if err != nil {
		return err
	}
	job.Audio = &selection
	return nil
}

func filtersEnabled(profile domain.AudioProfile) bool {
	return profile.Mode == domain.StreamPolicyCleanup ||
		profile.Mode == domain.StreamPolicyPrefer ||
		languageFilterEnabled(profile) ||
		profile.MaxTracks > 0
}

func languageFilterEnabled(profile domain.AudioProfile) bool {
	return len(profile.LanguagesToKeep) > 0 ||
		len(profile.PreferredLanguages) > 0 ||
		profile.KeepOriginalLanguage
}

func expandedLanguages(profile domain.AudioProfile, metadata domain.JobMetadata) []string {
	values := profile.LanguagesToKeep
	if len(values) == 0 {
		values = profile.PreferredLanguages
	}
	if len(values) == 0 && profile.KeepOriginalLanguage {
		values = []string{OriginalLanguageToken}
	}
	expanded := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" {
			continue
		}
		if value == OriginalLanguageToken {
			original := normalizeLanguage(metadata.OriginalLanguage)
			if original == "" {
				continue
			}
			value = original
		} else {
			value = normalizeLanguage(value)
		}
		if value != "" && !slices.Contains(expanded, value) {
			expanded = append(expanded, value)
		}
	}
	return expanded
}

func audioStreams(streams []domain.MediaStream) []domain.MediaStream {
	var result []domain.MediaStream
	for _, stream := range streams {
		if stream.Type == "audio" {
			result = append(result, stream)
		}
	}
	return result
}

func streamIndexes(streams []domain.MediaStream) []int {
	indexes := make([]int, 0, len(streams))
	for _, stream := range streams {
		indexes = append(indexes, stream.Index)
	}
	return indexes
}

func languageSet(languages []string) map[string]bool {
	set := make(map[string]bool, len(languages))
	for _, language := range languages {
		if language != "" {
			set[language] = true
		}
	}
	return set
}

func normalizeLanguage(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	language = strings.ReplaceAll(language, "_", "-")
	if language == "" || language == "und" || language == "unknown" {
		return ""
	}
	if i := strings.Index(language, "-"); i >= 0 {
		language = language[:i]
	}
	switch language {
	case "de", "ger", "deu":
		return "deu"
	case "en", "eng":
		return "eng"
	case "ja", "jpn", "jp":
		return "jpn"
	case "fr", "fre", "fra":
		return "fra"
	case "es", "spa":
		return "spa"
	case "it", "ita":
		return "ita"
	case "ko", "kor":
		return "kor"
	case "zh", "chi", "zho":
		return "zho"
	default:
		return language
	}
}

func commentary(stream domain.MediaStream) bool {
	if stream.Disposition["comment"] || stream.Disposition["commentary"] {
		return true
	}
	title := strings.ToLower(stream.Title)
	return strings.Contains(title, "commentary") || strings.Contains(title, "commentar")
}

func descriptiveAudio(stream domain.MediaStream) bool {
	if stream.Disposition["descriptions"] || stream.Disposition["visual_impaired"] {
		return true
	}
	title := strings.ToLower(stream.Title)
	return strings.Contains(title, "descriptive") ||
		strings.Contains(title, "described") ||
		strings.Contains(title, "audiodeskription")
}

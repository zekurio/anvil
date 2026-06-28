package audio

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/language"
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
		OriginalLanguage: language.Normalize(metadata.OriginalLanguage),
		LanguagesToKeep:  expandedLanguages(profile, metadata),
	}
	if len(audioStreams) == 0 {
		return selection, nil
	}
	if cleanupDisabled(profile, metadata, selection) {
		selection.StreamIndexes = streamIndexes(audioStreams)
		return selection, nil
	}

	keep := languageSet(selection.LanguagesToKeep)
	for _, stream := range audioStreams {
		streamLanguage := streamLanguage(stream.Language, selection.OriginalLanguage, profile.UnknownAsOriginal)
		if !keep[streamLanguage] {
			continue
		}
		if !profile.KeepCommentary && commentary(stream) {
			continue
		}
		selection.StreamIndexes = append(selection.StreamIndexes, stream.Index)
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

func cleanupDisabled(profile domain.AudioProfile, metadata domain.JobMetadata, selection domain.AudioSelection) bool {
	return metadata.StreamCleanupDisabled ||
		len(selection.LanguagesToKeep) == 0 ||
		(requiresOriginalLanguage(profile) && selection.OriginalLanguage == "")
}

func expandedLanguages(profile domain.AudioProfile, metadata domain.JobMetadata) []string {
	values := profile.LanguagesToKeep
	expanded := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" {
			continue
		}
		if value == OriginalLanguageToken {
			original := language.Normalize(metadata.OriginalLanguage)
			if original == "" {
				continue
			}
			value = original
		} else {
			value = language.Normalize(value)
		}
		if value != "" && !slices.Contains(expanded, value) {
			expanded = append(expanded, value)
		}
	}
	return expanded
}

func requiresOriginalLanguage(profile domain.AudioProfile) bool {
	for _, value := range profile.LanguagesToKeep {
		if strings.EqualFold(strings.TrimSpace(value), OriginalLanguageToken) {
			return true
		}
	}
	return false
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

func streamLanguage(value string, originalLanguage string, unknownAsOriginal bool) string {
	if unknownAsOriginal && originalLanguage != "" && language.IsUnknown(value) {
		return originalLanguage
	}
	return language.Normalize(value)
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

func commentary(stream domain.MediaStream) bool {
	if stream.Disposition["comment"] || stream.Disposition["commentary"] {
		return true
	}
	title := strings.ToLower(stream.Title)
	return strings.Contains(title, "commentary") || strings.Contains(title, "commentar")
}

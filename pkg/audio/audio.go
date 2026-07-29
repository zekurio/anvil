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
	requested := requestedLanguages(profile, metadata)
	selection := domain.AudioSelection{
		OriginalLanguage: language.Normalize(metadata.OriginalLanguage),
		LanguagesToKeep:  resolvedLanguages(requested),
	}
	decision := domain.StreamSelectionDecision{
		Kind:               domain.StreamKindAudio,
		OriginalLanguage:   selection.OriginalLanguage,
		RequestedLanguages: configuredLanguages(profile.LanguagesToKeep),
		ResolvedLanguages:  selection.LanguagesToKeep,
	}
	if len(audioStreams) == 0 {
		decision.Rule = domain.StreamSelectionRuleNoStreams
		// Every requested language is trivially missing when the source has no
		// audio at all; report it so the warning fires here too.
		decision.MissingLanguages = domain.MissingRequestedLanguages(selection.LanguagesToKeep, nil)
		selection.Decision = &decision
		return selection, nil
	}
	if reason, disabled := cleanupDisabled(profile, metadata, selection); disabled {
		selection.StreamIndexes = streamIndexes(audioStreams)
		decision.Rule = domain.StreamSelectionRuleCleanupDisabled
		decision.CleanupDisabledReason = reason
		decision.Streams = keptStreams(audioStreams, profile, selection, domain.StreamKeptCleanupDisabled)
		selection.Decision = &decision
		return selection, nil
	}

	keep := languageSet(selection.LanguagesToKeep)
	decision.Streams = make([]domain.StreamDecision, 0, len(audioStreams))
	for _, stream := range audioStreams {
		record := domain.NewStreamDecision(stream)
		record.ResolvedLanguage = streamLanguage(stream.Language, selection.OriginalLanguage, profile.UnknownAsOriginal)
		switch {
		case !keep[record.ResolvedLanguage]:
			record.Reason = domain.StreamDroppedLanguage
		case !profile.KeepCommentary && commentary(stream):
			record.Reason = domain.StreamDroppedCommentary
		default:
			record.Kept = true
			record.Reason = keepReason(record, requested, selection.OriginalLanguage)
			selection.StreamIndexes = append(selection.StreamIndexes, stream.Index)
		}
		decision.Streams = append(decision.Streams, record)
	}
	decision.MissingLanguages = domain.MissingRequestedLanguages(selection.LanguagesToKeep, decision.Streams)
	if len(selection.StreamIndexes) > 0 {
		decision.Rule = domain.StreamSelectionRuleLanguageFilter
		selection.Decision = &decision
		return selection, nil
	}

	switch profile.Fallback {
	case domain.StreamFallbackFailJob:
		decision.Rule = domain.StreamSelectionRuleFallbackFailJob
		selection.Decision = &decision
		return selection, fmt.Errorf("no audio streams matched languages %v", selection.LanguagesToKeep)
	case domain.StreamFallbackKeepFirst:
		decision.Rule = domain.StreamSelectionRuleFallbackKeepFirst
		selection.StreamIndexes = []int{audioStreams[0].Index}
		decision.Streams[0].Kept = true
		decision.Streams[0].Reason = domain.StreamKeptFallbackKeepFirst
	default:
		decision.Rule = domain.StreamSelectionRuleFallbackKeepAll
		selection.StreamIndexes = streamIndexes(audioStreams)
		for i := range decision.Streams {
			decision.Streams[i].Kept = true
			decision.Streams[i].Reason = domain.StreamKeptFallbackKeepAll
		}
	}
	selection.Decision = &decision
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
	if err != nil && selection.Decision == nil {
		return err
	}
	// A fail_job fallback returns both a decision and an error. The decision is
	// attached anyway because it is the only record of why nothing matched, and
	// the pipeline aborts on the returned error before any later block can read
	// the empty selection.
	job.Audio = &selection
	if selection.Decision != nil {
		pipeline.LogStreamSelection(job, *selection.Decision)
	}
	return err
}

// Decision exposes the selection record so the pipeline runner can persist it
// as an attempt event. Dropped tracks leave no trace once the source file is
// deleted, so the decision has to be recoverable from Anvil alone.
func (Block) Decision(job *pipeline.JobContext) (domain.StreamSelectionDecision, bool) {
	if job == nil || job.Audio == nil || job.Audio.Decision == nil {
		return domain.StreamSelectionDecision{}, false
	}
	return *job.Audio.Decision, true
}

func cleanupDisabled(profile domain.AudioProfile, metadata domain.JobMetadata, selection domain.AudioSelection) (string, bool) {
	switch {
	case metadata.StreamCleanupDisabled:
		reason := strings.TrimSpace(metadata.StreamCleanupDisabledReason)
		if reason == "" {
			reason = "stream cleanup disabled for job"
		}
		return reason, true
	case len(selection.LanguagesToKeep) == 0:
		return "no languages configured", true
	case requiresOriginalLanguage(profile) && selection.OriginalLanguage == "":
		return "original language is unavailable", true
	default:
		return "", false
	}
}

// requestedLanguage links a configured token to the language it resolves to,
// so a kept stream can report whether it matched an explicit language or the
// original-language token.
type requestedLanguage struct {
	Token    string
	Language string
}

func requestedLanguages(profile domain.AudioProfile, metadata domain.JobMetadata) []requestedLanguage {
	original := language.Normalize(metadata.OriginalLanguage)
	result := make([]requestedLanguage, 0, len(profile.LanguagesToKeep))
	for _, value := range profile.LanguagesToKeep {
		token := strings.TrimSpace(strings.ToLower(value))
		if token == "" {
			continue
		}
		resolved := original
		if token != OriginalLanguageToken {
			resolved = language.Normalize(token)
		}
		if resolved == "" {
			continue
		}
		index := slices.IndexFunc(result, func(entry requestedLanguage) bool { return entry.Language == resolved })
		if index < 0 {
			result = append(result, requestedLanguage{Token: token, Language: resolved})
			continue
		}
		if result[index].Token == OriginalLanguageToken && token != OriginalLanguageToken {
			result[index].Token = token
		}
	}
	return result
}

func resolvedLanguages(requested []requestedLanguage) []string {
	result := make([]string, 0, len(requested))
	for _, entry := range requested {
		result = append(result, entry.Language)
	}
	return result
}

func configuredLanguages(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value != "" && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func keepReason(record domain.StreamDecision, requested []requestedLanguage, originalLanguage string) domain.StreamDecisionReason {
	if originalLanguage != "" && record.ResolvedLanguage == originalLanguage && language.IsUnknown(record.Language) {
		return domain.StreamKeptUnknownAsOriginal
	}
	for _, entry := range requested {
		if entry.Language != record.ResolvedLanguage {
			continue
		}
		if entry.Token == OriginalLanguageToken {
			return domain.StreamKeptOriginalLanguage
		}
		return domain.StreamKeptLanguageMatch
	}
	return domain.StreamKeptLanguageMatch
}

func keptStreams(streams []domain.MediaStream, profile domain.AudioProfile, selection domain.AudioSelection, reason domain.StreamDecisionReason) []domain.StreamDecision {
	result := make([]domain.StreamDecision, 0, len(streams))
	for _, stream := range streams {
		record := domain.NewStreamDecision(stream)
		record.ResolvedLanguage = streamLanguage(stream.Language, selection.OriginalLanguage, profile.UnknownAsOriginal)
		record.Kept = true
		record.Reason = reason
		result = append(result, record)
	}
	return result
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

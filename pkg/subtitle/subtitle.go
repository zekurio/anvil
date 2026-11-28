package subtitle

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

func (Selector) Select(probe *domain.ProbeResult, profile domain.SubtitleProfile, metadata domain.JobMetadata) (domain.SubtitleSelection, error) {
	if probe == nil {
		return domain.SubtitleSelection{}, errors.New("subtitle selection requires probe result")
	}
	subtitleStreams := subtitleStreams(probe.Streams)
	requested := requestedLanguages(profile, metadata)
	selection := domain.SubtitleSelection{
		OriginalLanguage: language.Normalize(metadata.OriginalLanguage),
		LanguagesToKeep:  resolvedLanguages(requested),
	}
	decision := domain.StreamSelectionDecision{
		Kind:               domain.StreamKindSubtitle,
		OriginalLanguage:   selection.OriginalLanguage,
		RequestedLanguages: configuredLanguages(profile.LanguagesToKeep),
		ResolvedLanguages:  selection.LanguagesToKeep,
	}
	// Every path below fills in this one record, including the fail_job
	// fallback that also returns an error.
	selection.Decision = &decision
	if len(subtitleStreams) == 0 {
		decision.Rule = domain.StreamSelectionRuleNoStreams
		// Every requested language is trivially missing when the source has no
		// subtitles at all; report it so the warning fires here too.
		decision.MissingLanguages = domain.MissingRequestedLanguages(selection.LanguagesToKeep, nil)
		return selection, nil
	}
	decision.Streams = make([]domain.StreamDecision, 0, len(subtitleStreams))
	for _, stream := range subtitleStreams {
		record := domain.NewStreamDecision(stream)
		record.ResolvedLanguage = streamLanguage(stream.Language, selection.OriginalLanguage, profile.UnknownAsOriginal)
		decision.Streams = append(decision.Streams, record)
	}
	if profile.RemoveAll {
		decision.Rule = domain.StreamSelectionRuleRemoveAll
		for i := range decision.Streams {
			decision.Streams[i].Reason = domain.StreamDroppedRemoveAll
		}
		return selection, nil
	}

	// candidates are the positions the remaining rules judge. Image-based
	// tracks leave before any of them, so neither a disabled cleanup nor a
	// fallback can bring one back.
	candidates := make([]int, 0, len(subtitleStreams))
	judged := make([]domain.StreamDecision, 0, len(subtitleStreams))
	for i, stream := range subtitleStreams {
		if profile.DropBitmap && bitmap(stream) {
			decision.Streams[i].Reason = domain.StreamDroppedBitmap
			continue
		}
		candidates = append(candidates, i)
		judged = append(judged, decision.Streams[i])
	}
	keep := func(position int, reason domain.StreamDecisionReason) {
		decision.Streams[position].Kept = true
		decision.Streams[position].Reason = reason
		selection.StreamIndexes = append(selection.StreamIndexes, subtitleStreams[position].Index)
	}
	if len(candidates) == 0 {
		decision.Rule = domain.StreamSelectionRuleBitmapOnly
		decision.MissingLanguages = domain.MissingRequestedLanguages(selection.LanguagesToKeep, nil)
		return selection, nil
	}
	if reason, disabled := cleanupDisabled(profile, metadata, selection); disabled {
		decision.Rule = domain.StreamSelectionRuleCleanupDisabled
		decision.CleanupDisabledReason = reason
		for _, position := range candidates {
			keep(position, domain.StreamKeptCleanupDisabled)
		}
		return selection, nil
	}
	// A profile without languages keeps every track. That is what it asks
	// for, not a disabled cleanup, so it is recorded without a warning.
	if len(selection.LanguagesToKeep) == 0 {
		decision.Rule = domain.StreamSelectionRuleKeepAll
		for _, position := range candidates {
			keep(position, domain.StreamKeptKeepAll)
		}
		return selection, nil
	}

	wanted := languageSet(selection.LanguagesToKeep)
	for _, position := range candidates {
		stream := subtitleStreams[position]
		record := &decision.Streams[position]
		switch {
		case !wanted[record.ResolvedLanguage]:
			record.Reason = domain.StreamDroppedLanguage
		case !profile.KeepForced && forced(stream):
			record.Reason = domain.StreamDroppedForced
		case !profile.KeepSDH && sdh(stream):
			record.Reason = domain.StreamDroppedSDH
		case !profile.KeepCommentary && commentary(stream):
			record.Reason = domain.StreamDroppedCommentary
		default:
			keep(position, keepReason(*record, requested, selection.OriginalLanguage))
		}
	}
	// A language that only exists as a dropped image-based track is missing.
	decision.MissingLanguages = domain.MissingRequestedLanguages(selection.LanguagesToKeep, judged)
	if len(selection.StreamIndexes) > 0 {
		decision.Rule = domain.StreamSelectionRuleLanguageFilter
		return selection, nil
	}

	switch profile.Fallback {
	case domain.StreamFallbackFailJob:
		decision.Rule = domain.StreamSelectionRuleFallbackFailJob
		return selection, fmt.Errorf("no subtitle streams matched languages %v", selection.LanguagesToKeep)
	case domain.StreamFallbackKeepFirst:
		decision.Rule = domain.StreamSelectionRuleFallbackKeepFirst
		keep(candidates[0], domain.StreamKeptFallbackKeepFirst)
	default:
		decision.Rule = domain.StreamSelectionRuleFallbackKeepAll
		for _, position := range candidates {
			keep(position, domain.StreamKeptFallbackKeepAll)
		}
	}
	return selection, nil
}

// bitmap reports whether the track is image-based. These are the subtitle
// codecs ffmpeg flags as bitmap, by their ffprobe names.
func bitmap(stream domain.MediaStream) bool {
	switch strings.ToLower(strings.TrimSpace(stream.Codec)) {
	case "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle", "xsub":
		return true
	default:
		return false
	}
}

type Block struct {
	Selector Selector
}

func (Block) Name() string {
	return "subtitle-cleanup"
}

func (b Block) Run(_ context.Context, job *pipeline.JobContext) error {
	selection, err := b.Selector.Select(job.Probe, job.Profile.Subtitles, job.Metadata)
	if err != nil && selection.Decision == nil {
		return err
	}
	// A fail_job fallback returns both a decision and an error. The decision is
	// attached anyway because it is the only record of why nothing matched, and
	// the pipeline aborts on the returned error before any later block can read
	// the empty selection.
	job.Subtitles = &selection
	if selection.Decision != nil {
		pipeline.LogStreamSelection(job, *selection.Decision)
	}
	return err
}

// Decision exposes the selection record so the pipeline runner can persist it
// as an attempt event. Dropped tracks leave no trace once the source file is
// deleted, so the decision has to be recoverable from Anvil alone.
func (Block) Decision(job *pipeline.JobContext) (domain.StreamSelectionDecision, bool) {
	if job == nil || job.Subtitles == nil || job.Subtitles.Decision == nil {
		return domain.StreamSelectionDecision{}, false
	}
	return *job.Subtitles.Decision, true
}

// cleanupDisabled reports why the language rules cannot be trusted for this
// job. A profile that names no language at all is not such a case.
func cleanupDisabled(profile domain.SubtitleProfile, metadata domain.JobMetadata, selection domain.SubtitleSelection) (string, bool) {
	switch {
	case metadata.StreamCleanupDisabled:
		reason := strings.TrimSpace(metadata.StreamCleanupDisabledReason)
		if reason == "" {
			reason = "stream cleanup disabled for job"
		}
		return reason, true
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

func requestedLanguages(profile domain.SubtitleProfile, metadata domain.JobMetadata) []requestedLanguage {
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

func requiresOriginalLanguage(profile domain.SubtitleProfile) bool {
	for _, value := range profile.LanguagesToKeep {
		if strings.EqualFold(strings.TrimSpace(value), OriginalLanguageToken) {
			return true
		}
	}
	return false
}

func subtitleStreams(streams []domain.MediaStream) []domain.MediaStream {
	var result []domain.MediaStream
	for _, stream := range streams {
		if stream.Type == "subtitle" {
			result = append(result, stream)
		}
	}
	return result
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

func forced(stream domain.MediaStream) bool {
	if stream.Disposition["forced"] {
		return true
	}
	return strings.Contains(strings.ToLower(stream.Title), "forced")
}

func sdh(stream domain.MediaStream) bool {
	if stream.Disposition["hearing_impaired"] || stream.Disposition["captions"] || stream.Disposition["descriptions"] {
		return true
	}
	title := strings.ToLower(stream.Title)
	return strings.Contains(title, "sdh") ||
		strings.Contains(title, "hearing impaired") ||
		strings.Contains(title, "closed caption")
}

func commentary(stream domain.MediaStream) bool {
	if stream.Disposition["comment"] || stream.Disposition["commentary"] {
		return true
	}
	title := strings.ToLower(stream.Title)
	return strings.Contains(title, "commentary") || strings.Contains(title, "commentar")
}

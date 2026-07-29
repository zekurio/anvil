package domain

import (
	"fmt"
	"slices"
)

// StreamKind identifies the stream family a selection decision covers.
type StreamKind string

const (
	StreamKindAudio    StreamKind = "audio"
	StreamKindSubtitle StreamKind = "subtitle"
)

// StreamSelectionRule names the rule that produced a whole selection. It is
// the answer to "why does the output look like this" and stays stable so it
// can be grepped in logs and queried in attempt events.
type StreamSelectionRule string

const (
	StreamSelectionRuleNoStreams         StreamSelectionRule = "no_streams"
	StreamSelectionRuleCleanupDisabled   StreamSelectionRule = "cleanup_disabled"
	StreamSelectionRuleLanguageFilter    StreamSelectionRule = "language_filter"
	StreamSelectionRuleFallbackKeepAll   StreamSelectionRule = "fallback_keep_all"
	StreamSelectionRuleFallbackKeepFirst StreamSelectionRule = "fallback_keep_first"
	StreamSelectionRuleFallbackFailJob   StreamSelectionRule = "fallback_fail_job"
)

// StreamDecisionReason explains why a single stream survived or was dropped.
type StreamDecisionReason string

const (
	StreamKeptLanguageMatch     StreamDecisionReason = "language_match"
	StreamKeptOriginalLanguage  StreamDecisionReason = "original_language"
	StreamKeptUnknownAsOriginal StreamDecisionReason = "unknown_as_original"
	StreamKeptCleanupDisabled   StreamDecisionReason = "cleanup_disabled"
	StreamKeptFallbackKeepAll   StreamDecisionReason = "fallback_keep_all"
	StreamKeptFallbackKeepFirst StreamDecisionReason = "fallback_keep_first"

	StreamDroppedLanguage   StreamDecisionReason = "language_not_requested"
	StreamDroppedCommentary StreamDecisionReason = "commentary"
	StreamDroppedForced     StreamDecisionReason = "forced"
	StreamDroppedSDH        StreamDecisionReason = "sdh"
)

// StreamDecision records what happened to one input stream. It carries enough
// identity to answer "which track was that" after the source file is gone.
type StreamDecision struct {
	Index            int                  `json:"index"`
	Codec            string               `json:"codec,omitempty"`
	Language         string               `json:"language,omitempty"`
	ResolvedLanguage string               `json:"resolved_language,omitempty"`
	Title            string               `json:"title,omitempty"`
	Channels         int                  `json:"channels,omitempty"`
	Default          bool                 `json:"default,omitempty"`
	Forced           bool                 `json:"forced,omitempty"`
	Kept             bool                 `json:"kept"`
	Reason           StreamDecisionReason `json:"reason"`
}

// NewStreamDecision captures the identifying fields of a probed stream.
func NewStreamDecision(stream MediaStream) StreamDecision {
	return StreamDecision{
		Index:    stream.Index,
		Codec:    stream.Codec,
		Language: stream.Language,
		Title:    stream.Title,
		Channels: stream.Channels,
		Default:  stream.Disposition["default"],
		Forced:   stream.Disposition["forced"],
	}
}

// Summary renders the stream as one compact, log-friendly value.
func (d StreamDecision) Summary() string {
	state := "dropped"
	if d.Kept {
		state = "kept"
	}
	return fmt.Sprintf("%d:%s:%s:%s:%s", d.Index, displayValue(d.Language, "und"), displayValue(d.Codec, "unknown"), state, d.Reason)
}

// StreamSelectionDecision is the durable record of one stream-selection block.
// Selection is the only pipeline stage that irreversibly discards
// user-visible content, so this record has to survive log rotation and
// deletion of the source file.
type StreamSelectionDecision struct {
	Kind                  StreamKind          `json:"kind"`
	Rule                  StreamSelectionRule `json:"rule"`
	OriginalLanguage      string              `json:"original_language,omitempty"`
	RequestedLanguages    []string            `json:"requested_languages,omitempty"`
	ResolvedLanguages     []string            `json:"resolved_languages,omitempty"`
	MissingLanguages      []string            `json:"missing_languages,omitempty"`
	CleanupDisabledReason string              `json:"cleanup_disabled_reason,omitempty"`
	Streams               []StreamDecision    `json:"streams,omitempty"`
}

// KeptIndexes returns the input stream indexes that survived selection.
func (d StreamSelectionDecision) KeptIndexes() []int {
	return d.indexes(true)
}

// DroppedIndexes returns the input stream indexes selection discarded.
func (d StreamSelectionDecision) DroppedIndexes() []int {
	return d.indexes(false)
}

func (d StreamSelectionDecision) indexes(kept bool) []int {
	result := make([]int, 0, len(d.Streams))
	for _, stream := range d.Streams {
		if stream.Kept == kept {
			result = append(result, stream.Index)
		}
	}
	return result
}

// SourceLanguages returns the distinct languages the source actually offered
// for this stream kind, in stream order.
func (d StreamSelectionDecision) SourceLanguages() []string {
	result := make([]string, 0, len(d.Streams))
	for _, stream := range d.Streams {
		value := displayValue(stream.ResolvedLanguage, "und")
		if !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

// StreamSummaries renders every stream decision as a compact log value.
func (d StreamSelectionDecision) StreamSummaries() []string {
	result := make([]string, 0, len(d.Streams))
	for _, stream := range d.Streams {
		result = append(result, stream.Summary())
	}
	return result
}

// FellBack reports whether the configured language policy matched nothing and
// a fallback produced the outcome instead.
func (d StreamSelectionDecision) FellBack() bool {
	switch d.Rule {
	case StreamSelectionRuleFallbackKeepAll, StreamSelectionRuleFallbackKeepFirst, StreamSelectionRuleFallbackFailJob:
		return true
	default:
		return false
	}
}

// MissingRequestedLanguages returns the requested languages that no source
// stream provides. It is the "you asked for deu, there was no deu" case.
func MissingRequestedLanguages(requested []string, streams []StreamDecision) []string {
	present := make(map[string]bool, len(streams))
	for _, stream := range streams {
		present[stream.ResolvedLanguage] = true
	}
	var missing []string
	for _, value := range requested {
		if value != "" && !present[value] && !slices.Contains(missing, value) {
			missing = append(missing, value)
		}
	}
	return missing
}

func displayValue(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

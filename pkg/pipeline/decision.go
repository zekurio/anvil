package pipeline

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/zekurio/anvil/pkg/domain"
)

// StreamSelectionArtifact is the attempt-event name under which stream
// selection decisions are stored. Attempt events are the only record that
// outlives log rotation and deletion of the source media.
const StreamSelectionArtifact = "stream-selection"

// IsStreamSelectionEvent reports whether an attempt event carries a stream
// selection decision. The runner writes these as artifacts because the store
// constrains the event type, so the name is what identifies them.
func IsStreamSelectionEvent(event domain.AttemptEvent) bool {
	return event.Type == domain.AttemptEventArtifact && event.Name == StreamSelectionArtifact
}

// DecodeStreamSelection decodes a recorded stream selection decision. The
// runner owns the payload format, so it owns the decoder every reader shares.
func DecodeStreamSelection(payload []byte) (domain.StreamSelectionDecision, error) {
	var decision domain.StreamSelectionDecision
	if err := json.Unmarshal(payload, &decision); err != nil {
		return domain.StreamSelectionDecision{}, fmt.Errorf("decode stream selection: %w", err)
	}
	return decision, nil
}

// DecisionReporter is implemented by blocks that produce a decision worth
// keeping beyond the process log. The runner records it as an attempt event
// once the block has run, including when the block was resumed from a
// checkpoint, so every attempt carries its own record.
type DecisionReporter interface {
	Decision(job *JobContext) (domain.StreamSelectionDecision, bool)
}

// LogStreamSelection emits the single INFO summary of a stream selection plus
// the warnings for outcomes that surprise operators: a requested language the
// source never had, and the paths that ignore the configured language policy.
func LogStreamSelection(job *JobContext, decision domain.StreamSelectionDecision) {
	if job == nil {
		return
	}
	base := []any{
		"job", job.Job.Label(),
		"attempt", job.Attempt.Number,
		"kind", string(decision.Kind),
		"rule", string(decision.Rule),
	}
	slog.Info("stream selection decided", append(base,
		"original_language", decision.OriginalLanguage,
		"requested_languages", decision.RequestedLanguages,
		"resolved_languages", decision.ResolvedLanguages,
		"missing_languages", decision.MissingLanguages,
		"kept_indexes", decision.KeptIndexes(),
		"dropped_indexes", decision.DroppedIndexes(),
		"streams", decision.StreamSummaries(),
	)...)
	if len(decision.MissingLanguages) > 0 {
		slog.Warn("stream selection requested language missing from source", append(base,
			"missing_languages", decision.MissingLanguages,
			"source_languages", decision.SourceLanguages(),
		)...)
	}
	if decision.Rule == domain.StreamSelectionRuleCleanupDisabled {
		slog.Warn("stream cleanup disabled; keeping every stream", append(base,
			"cleanup_disabled_reason", decision.CleanupDisabledReason,
		)...)
		return
	}
	if decision.FellBack() {
		slog.Warn("stream selection fell back; no stream matched the configured languages", append(base,
			"resolved_languages", decision.ResolvedLanguages,
			"source_languages", decision.SourceLanguages(),
			"kept_indexes", decision.KeptIndexes(),
		)...)
	}
}

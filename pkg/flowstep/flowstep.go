package flowstep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zekurio/anvil/pkg/audio"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/subtitle"
)

// StepName is the step's event name.
const StepName = "flow"

// ArtifactName is the attempt-event name the resolution is recorded under.
const ArtifactName = "flow-resolution"

// ErrSkipped ends the pipeline when the flow reached a skip node. It wraps
// pipeline.ErrStop: the step succeeded, and what it decided is that nothing
// else is to happen to the file.
var ErrSkipped = fmt.Errorf("the flow ended at a skip node: %w", pipeline.ErrStop)

// Block evaluates job.Flow against the probe and the job metadata. On a
// process outcome it sets job.Profile; on a skip outcome it returns
// ErrSkipped. Either way job.FlowResolution says what was decided and why.
//
// The step is not checkpointed. Evaluating a flow costs nothing, and it is a
// pure function of the flow, the probe, and the metadata, so running it again
// on every attempt is what keeps a resumed attempt honest: the profile it
// continues with is the one the flow gives now.
type Block struct{}

func (Block) Name() string {
	return StepName
}

func (Block) Run(_ context.Context, job *pipeline.JobContext) error {
	if job.Probe == nil {
		return errors.New("the flow needs the probe of the input, and none has run")
	}
	if len(job.Flow.Graph) == 0 {
		return errors.New("the attempt has no flow to evaluate")
	}
	var graph flow.Graph
	if err := json.Unmarshal(job.Flow.Graph, &graph); err != nil {
		return fmt.Errorf("decode flow %q: %w", job.Flow.Name, err)
	}
	resolution, err := flow.Evaluate(graph, flow.Facts{Probe: *job.Probe, Metadata: job.Metadata})
	if err != nil {
		return fmt.Errorf("flow %q: %w", job.Flow.Name, err)
	}
	job.FlowResolution = &domain.FlowResolution{
		FlowID:    job.Flow.ID,
		FlowName:  job.Flow.Name,
		Revision:  job.Flow.Revision,
		Outcome:   string(resolution.Outcome),
		Path:      resolution.Path,
		Decisions: resolution.Decisions,
		Plan:      resolution.Plan,
	}
	if resolution.Outcome == flow.OutcomeSkip {
		return ErrSkipped
	}

	profile := resolution.Profile
	if name := strings.TrimSpace(job.Flow.Name); name != "" {
		profile.Name = domain.ProfileName(name)
	}
	job.Profile = profile
	DisableUnsafeStreamCleanup(profile, &job.Metadata)
	return nil
}

// Artifact records the resolution on every attempt, also on one the flow
// skipped: it is the only record of why a file was left alone.
func (Block) Artifact(job *pipeline.JobContext) (pipeline.ArtifactReport, bool) {
	if job == nil || job.FlowResolution == nil {
		return pipeline.ArtifactReport{}, false
	}
	resolution := job.FlowResolution
	message := fmt.Sprintf("%s (revision %d): %s via %s",
		resolution.FlowName, resolution.Revision, resolution.Outcome, strings.Join(resolution.Path, " → "))
	return pipeline.ArtifactReport{Name: ArtifactName, Message: message, Payload: resolution}, true
}

// SkipReason says in one line why a flow skipped a file: the last condition
// on the path is what sent it to the skip node.
func SkipReason(resolution *domain.FlowResolution) string {
	if resolution == nil {
		return "skipped by the flow"
	}
	reason := fmt.Sprintf("skipped by flow %q", resolution.FlowName)
	if len(resolution.Decisions) == 0 {
		return reason
	}
	return reason + ": " + resolution.Decisions[len(resolution.Decisions)-1]
}

// DecodeResolution decodes a recorded "flow-resolution" artifact.
func DecodeResolution(payload []byte) (domain.FlowResolution, error) {
	var resolution domain.FlowResolution
	if err := json.Unmarshal(payload, &resolution); err != nil {
		return domain.FlowResolution{}, fmt.Errorf("decode flow resolution: %w", err)
	}
	return resolution, nil
}

// DisableUnsafeStreamCleanup turns the language rules off when the profile
// keeps the original language but nobody could say what that language is.
// Dropping tracks on a guess is how the only useful audio track gets lost.
func DisableUnsafeStreamCleanup(profile domain.Profile, metadata *domain.JobMetadata) {
	if metadata == nil || !usesOriginalLanguage(profile) || metadata.OriginalLanguage != "" {
		return
	}
	metadata.StreamCleanupDisabled = true
	if metadata.StreamCleanupDisabledReason == "" {
		metadata.StreamCleanupDisabledReason = "original language metadata is unavailable"
	}
}

func usesOriginalLanguage(profile domain.Profile) bool {
	for _, value := range profile.Audio.LanguagesToKeep {
		if strings.EqualFold(strings.TrimSpace(value), audio.OriginalLanguageToken) {
			return true
		}
	}
	for _, value := range profile.Subtitles.LanguagesToKeep {
		if strings.EqualFold(strings.TrimSpace(value), subtitle.OriginalLanguageToken) {
			return true
		}
	}
	return false
}

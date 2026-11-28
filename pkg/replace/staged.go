package replace

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

// StagedDestination is a publish destination together with the publish
// policy that placed it, which also decides where its part file was written.
type StagedDestination struct {
	Publish domain.PublishPolicy
	Path    string
}

// StagedDestinations reports every destination the job's attempts staged at.
// The destination is fixed by the stage step under the attempt's library
// snapshot and the profile its flow resolved, so those persisted snapshots —
// not the current runtime config or flow, which may have changed since —
// decide where an orphaned part file can be.
func StagedDestinations(job domain.Job, source domain.MediaSource, asset domain.MediaAsset, attempts []domain.Attempt) ([]StagedDestination, error) {
	var destinations []StagedDestination
	seen := make(map[string]struct{})
	var errs []error
	for _, attempt := range attempts {
		jobContext, ok, err := AttemptJobContext(job, source, asset, attempt)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			continue
		}
		destination, err := PlanDestination(jobContext)
		if err != nil {
			errs = append(errs, fmt.Errorf("plan destination for attempt %d: %w", attempt.Number, err))
			continue
		}
		// Two attempts can plan the same destination from different move
		// folders, and the part path follows the folder, so dedup on the part.
		publish := jobContext.Profile.Publish
		part := ArtifactPath(publish, destination, PartJobLabel(job.ID))
		if _, ok := seen[part]; !ok {
			seen[part] = struct{}{}
			destinations = append(destinations, StagedDestination{Publish: publish, Path: destination})
		}
	}
	return destinations, errors.Join(errs...)
}

// AttemptJobContext rebuilds the job context an attempt staged under from its
// library snapshot and resolved profile. ok is false for an attempt that
// recorded no profile: the flow step records it before anything is staged,
// so such an attempt never staged.
func AttemptJobContext(job domain.Job, source domain.MediaSource, asset domain.MediaAsset, attempt domain.Attempt) (*pipeline.JobContext, bool, error) {
	if len(attempt.ResolvedLibrary) == 0 || len(attempt.ResolvedProfile) == 0 {
		return nil, false, nil
	}
	var library domain.Library
	if err := json.Unmarshal(attempt.ResolvedLibrary, &library); err != nil {
		return nil, false, fmt.Errorf("decode attempt %d resolved library: %w", attempt.Number, err)
	}
	var profile domain.Profile
	if err := json.Unmarshal(attempt.ResolvedProfile, &profile); err != nil {
		return nil, false, fmt.Errorf("decode attempt %d resolved profile: %w", attempt.Number, err)
	}
	jobContext := pipeline.NewJobContext(job, source, asset, library)
	jobContext.Profile = profile
	return jobContext, true, nil
}

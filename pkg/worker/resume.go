package worker

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/crop"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flowstep"
	"github.com/zekurio/anvil/pkg/pipeline"
)

// pipelineContextPersistence checkpoints the results of the reusable steps
// and hands them back to a later attempt of the same job.
//
// Reuse is guarded twice, because a reused result decides what is written
// over the operator's media.
//
// Before the pipeline runs, the checkpoint as a whole is matched against a
// fingerprint of everything known at that point: the input file, the resolved
// library, the metadata, and what the library's flow means (flow.Canonical,
// which ignores canvas positions and labels). A flow edited or reassigned
// between attempts so that it does something else drops the whole checkpoint.
//
// That fingerprint cannot name the profile, though: the profile is the flow
// evaluated against the probe, so it only exists once the flow step has run.
// The second guard therefore sits right behind that step. The checkpoint
// records the effective profile its audio, crop, and search results were
// computed under, and when the profile the flow resolves now differs in any
// field, those results are discarded before a step can resume from them. The
// probe is kept: it describes the file, not what is done to it.
type pipelineContextPersistence struct {
	store   Store
	base    domain.JobPipelineContext
	current domain.JobPipelineContext
	cached  *domain.JobPipelineContext
	now     func() time.Time
}

func newPipelineContextPersistence(ctx context.Context, store Store, job *pipeline.JobContext, resolvedLibrary []byte, flowFingerprint string, initialMetadata domain.JobMetadata, now func() time.Time) *pipelineContextPersistence {
	base := basePipelineContext(job, resolvedLibrary, flowFingerprint, initialMetadata)
	persistence := &pipelineContextPersistence{
		store:   store,
		base:    base,
		current: base,
		now:     now,
	}
	if store == nil || job == nil || job.Job.ID == 0 {
		return persistence
	}

	snapshot, ok, err := store.GetJobPipelineContext(ctx, job.Job.ID)
	if err != nil {
		slog.Warn("job pipeline context ignored", "job", job.Job.Label(), "error", err)
		return persistence
	}
	if !ok {
		return persistence
	}
	if !pipelineContextMatches(base, snapshot) {
		slog.Info("job pipeline context is stale; rebuilding", "job", job.Job.Label())
		return persistence
	}
	persistence.cached = &snapshot
	persistence.current = snapshot
	slog.Info("job pipeline context loaded", "job", job.Job.Label())
	return persistence
}

func (p *pipelineContextPersistence) ResumeStep(ctx context.Context, step string, job *pipeline.JobContext) (bool, error) {
	if p == nil || p.cached == nil || job == nil || !resumableStep(step) {
		return false, nil
	}
	switch step {
	case "probe":
		if p.cached.Probe == nil {
			return false, nil
		}
		// Only the probe comes back. The metadata the job carries is the
		// initial metadata, which the fingerprint already matched; what later
		// steps add to it (the stream-cleanup decision of the flow step, the
		// crop filter) is theirs to set again under the profile in effect now.
		job.Probe = p.cached.Probe
	case "audio-cleanup":
		if p.cached.Audio == nil {
			return false, nil
		}
		job.Audio = p.cached.Audio
	case "crop-detect":
		if p.cached.Crop == nil {
			return false, nil
		}
		// A result cached while the profile had detection off never looked at
		// the picture. Detect now, and drop the search that measured the
		// uncropped frame.
		if p.cached.Crop.Disabled && !job.Profile.Crop.Disabled {
			p.cached.Search = nil
			p.current.Search = nil
			return false, nil
		}
		// ApplySafetyPolicy also discards a cached crop once the profile has
		// turned detection off.
		result := crop.ApplySafetyPolicy(*p.cached.Crop, job.Probe, job.Profile.Crop)
		cropChanged := result.Filter != p.cached.Crop.Filter
		job.Crop = &result
		job.Metadata.CropFilter = result.Filter
		if cropChanged {
			p.cached.Search = nil
			p.current.Search = nil
		}
		if !reflect.DeepEqual(result, *p.cached.Crop) || p.current.Metadata.CropFilter != result.Filter || cropChanged {
			p.current.Crop = &result
			p.current.Metadata = job.Metadata
			if p.store != nil && job.Job.ID != 0 {
				if err := p.store.SaveJobPipelineContext(ctx, job.Job.ID, p.current, p.timestamp()); err != nil {
					return false, err
				}
			}
		}
	case "crf-search":
		if p.cached.Search == nil {
			return false, nil
		}
		// A fixed CRF costs nothing to restate, and a result from one quality
		// mode must never decide an encode in the other. The profile guard
		// behind the flow step already separates the modes; this holds even if
		// that guard ever stops covering them.
		if job.Profile.Video.QualityMode == domain.VideoQualityFixed || p.cached.Search.QualityMode == domain.VideoQualityFixed {
			return false, nil
		}
		job.Search = p.cached.Search
	default:
		return false, nil
	}
	return true, nil
}

func (p *pipelineContextPersistence) StepSucceeded(ctx context.Context, step string, job *pipeline.JobContext) error {
	if p == nil || job == nil {
		return nil
	}
	if step == flowstep.StepName {
		return p.profileResolved(ctx, job)
	}
	if p.store == nil || job.Job.ID == 0 || !resumableStep(step) {
		return nil
	}
	p.current = p.capture(job)
	return p.store.SaveJobPipelineContext(ctx, job.Job.ID, p.current, p.timestamp())
}

// profileResolved runs once the flow step has set job.Profile. It records the
// profile on the attempt, and it is the second checkpoint guard: results
// computed under a different effective profile are dropped here, before the
// first step that could resume from them.
func (p *pipelineContextPersistence) profileResolved(ctx context.Context, job *pipeline.JobContext) error {
	fingerprint := profileFingerprint(job.Profile)
	stale := p.cached != nil && p.cached.ResolvedProfileJSON != fingerprint &&
		(p.cached.Audio != nil || p.cached.Crop != nil || p.cached.Search != nil)
	if stale {
		slog.Info("job pipeline context was computed under another profile; keeping only the probe", "job", job.Job.Label())
		p.cached.Audio, p.cached.Crop, p.cached.Search = nil, nil, nil
		p.current.Audio, p.current.Crop, p.current.Search = nil, nil, nil
	}
	if p.cached != nil {
		p.cached.ResolvedProfileJSON = fingerprint
	}
	p.current.ResolvedProfileJSON = fingerprint
	p.current.Metadata = job.Metadata
	if p.store == nil {
		return nil
	}
	if job.Attempt.ID != 0 {
		if err := p.store.SaveAttemptProfile(ctx, job.Attempt.ID, mustJSON(job.Profile)); err != nil {
			return err
		}
	}
	if !stale || job.Job.ID == 0 {
		return nil
	}
	// Written out at once, so the discarded results are gone from the store
	// too and not only from this attempt's view of it.
	return p.store.SaveJobPipelineContext(ctx, job.Job.ID, p.current, p.timestamp())
}

// profileFingerprint encodes everything about a profile that decides what
// happens to a file. The name is left out: it is the flow's name, and
// renaming a flow changes nothing about its results.
func profileFingerprint(profile domain.Profile) string {
	profile.Name = ""
	data, err := json.Marshal(profile)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func (p *pipelineContextPersistence) capture(job *pipeline.JobContext) domain.JobPipelineContext {
	snapshot := p.current
	if snapshot.Version == 0 {
		snapshot = p.base
	}
	snapshot.Metadata = job.Metadata
	snapshot.Probe = job.Probe
	snapshot.Audio = job.Audio
	snapshot.Crop = job.Crop
	snapshot.Search = job.Search
	return snapshot
}

func (p *pipelineContextPersistence) timestamp() time.Time {
	if p.now != nil {
		return p.now().UTC()
	}
	return time.Now().UTC()
}

func basePipelineContext(job *pipeline.JobContext, resolvedLibrary []byte, flowFingerprint string, initialMetadata domain.JobMetadata) domain.JobPipelineContext {
	snapshot := domain.JobPipelineContext{
		Version:             domain.JobPipelineContextVersion,
		InitialMetadata:     initialMetadata,
		ResolvedLibraryJSON: string(resolvedLibrary),
		FlowFingerprint:     flowFingerprint,
		Metadata:            initialMetadata,
	}
	if job == nil {
		return snapshot
	}
	snapshot.InputPath = job.InputPath
	snapshot.SourceFingerprint = job.Source.Fingerprint
	snapshot.AssetFingerprint = job.Asset.Fingerprint
	return snapshot
}

// pipelineContextMatches is the first checkpoint guard: everything known
// before the pipeline runs has to be what the checkpoint was built on. An
// empty flow fingerprint never matches, so a checkpoint is not reused when
// the flow could not be read.
func pipelineContextMatches(base domain.JobPipelineContext, cached domain.JobPipelineContext) bool {
	return cached.Version == domain.JobPipelineContextVersion &&
		strings.TrimSpace(cached.InputPath) == strings.TrimSpace(base.InputPath) &&
		fingerprintMatches(cached.SourceFingerprint, base.SourceFingerprint) &&
		fingerprintMatches(cached.AssetFingerprint, base.AssetFingerprint) &&
		cached.ResolvedLibraryJSON == base.ResolvedLibraryJSON &&
		base.FlowFingerprint != "" && cached.FlowFingerprint == base.FlowFingerprint &&
		reflect.DeepEqual(cached.InitialMetadata, base.InitialMetadata)
}

func fingerprintMatches(left domain.FileFingerprint, right domain.FileFingerprint) bool {
	return left.SizeBytes == right.SizeBytes &&
		left.HashAlgorithm == right.HashAlgorithm &&
		left.HashValue == right.HashValue &&
		timesEqual(left.ModTime, right.ModTime)
}

func timesEqual(left time.Time, right time.Time) bool {
	if left.IsZero() || right.IsZero() {
		return left.IsZero() && right.IsZero()
	}
	return left.Equal(right)
}

func resumableStep(step string) bool {
	switch step {
	case "probe", "audio-cleanup", "crop-detect", "crf-search":
		return true
	default:
		return false
	}
}

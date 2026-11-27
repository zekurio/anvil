package worker

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/crop"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

type pipelineContextPersistence struct {
	store   Store
	base    domain.JobPipelineContext
	current domain.JobPipelineContext
	cached  *domain.JobPipelineContext
	now     func() time.Time
}

func newPipelineContextPersistence(ctx context.Context, store Store, job *pipeline.JobContext, resolvedLibrary, resolvedProfile []byte, initialMetadata domain.JobMetadata, now func() time.Time) *pipelineContextPersistence {
	base := basePipelineContext(job, resolvedLibrary, resolvedProfile, initialMetadata)
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
		job.Probe = p.cached.Probe
		job.Metadata = p.cached.Metadata
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
		// mode must never decide an encode in the other. The profile
		// fingerprint already separates the modes; this holds even if the
		// fingerprint ever stops covering them.
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
	if p == nil || p.store == nil || job == nil || job.Job.ID == 0 || !resumableStep(step) {
		return nil
	}
	p.current = p.capture(job)
	return p.store.SaveJobPipelineContext(ctx, job.Job.ID, p.current, p.timestamp())
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

func basePipelineContext(job *pipeline.JobContext, resolvedLibrary, resolvedProfile []byte, initialMetadata domain.JobMetadata) domain.JobPipelineContext {
	snapshot := domain.JobPipelineContext{
		Version:             domain.JobPipelineContextVersion,
		InitialMetadata:     initialMetadata,
		ResolvedLibraryJSON: string(resolvedLibrary),
		ResolvedProfileJSON: string(resolvedProfile),
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

func pipelineContextMatches(base domain.JobPipelineContext, cached domain.JobPipelineContext) bool {
	return cached.Version == domain.JobPipelineContextVersion &&
		strings.TrimSpace(cached.InputPath) == strings.TrimSpace(base.InputPath) &&
		fingerprintMatches(cached.SourceFingerprint, base.SourceFingerprint) &&
		fingerprintMatches(cached.AssetFingerprint, base.AssetFingerprint) &&
		cached.ResolvedLibraryJSON == base.ResolvedLibraryJSON &&
		cached.ResolvedProfileJSON == base.ResolvedProfileJSON &&
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

package worker

import (
	"context"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

func TestResumeCropRevalidatesCachedCandidate(t *testing.T) {
	cached := domain.JobPipelineContext{
		Crop:   &domain.CropResult{Filter: "crop=176:64:996:64"},
		Search: &domain.SearchResult{CRF: 27},
	}
	persistence := &pipelineContextPersistence{
		cached:  &cached,
		current: cached,
	}
	job := &pipeline.JobContext{
		Probe: &domain.ProbeResult{Streams: []domain.MediaStream{{
			Type: "video", Width: 1920, Height: 1080,
		}}},
	}

	resumed, err := persistence.ResumeStep(context.Background(), "crop-detect", job)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed {
		t.Fatal("crop step was not resumed")
	}
	if job.Crop == nil {
		t.Fatal("crop result is nil")
	}
	if job.Crop.Filter != "" || job.Metadata.CropFilter != "" {
		t.Fatalf("cached crop was applied: %#v", job.Crop)
	}
	if job.Crop.CandidateFilter != "crop=176:64:996:64" {
		t.Fatalf("CandidateFilter = %q", job.Crop.CandidateFilter)
	}
	if !strings.Contains(job.Crop.RejectionReason, "retained area 0.54%") {
		t.Fatalf("RejectionReason = %q", job.Crop.RejectionReason)
	}
	if persistence.current.Crop == nil || persistence.current.Crop.RejectionReason == "" {
		t.Fatalf("revalidated crop was not retained in current context: %#v", persistence.current.Crop)
	}
	if persistence.current.Search != nil {
		t.Fatalf("cached search was retained after crop changed: %#v", persistence.current.Search)
	}
	if resumed, err := persistence.ResumeStep(context.Background(), "crf-search", job); err != nil || resumed {
		t.Fatalf("CRF search resume = %v, %v; want rerun", resumed, err)
	}
}

func TestResumeCropRejectsCachedFailedWindow(t *testing.T) {
	cached := domain.JobPipelineContext{
		Crop: &domain.CropResult{
			CandidateFilter: "crop=1920:800:0:140",
			Filter:          "crop=1920:800:0:140",
			Samples: []domain.CropSample{
				{Filter: "crop=1920:800:0:140"},
				{Filter: "crop=1920:1080:0:0", Error: "decode failed"},
				{Filter: "crop=1920:800:0:140"},
			},
		},
		Metadata: domain.JobMetadata{CropFilter: "crop=1920:800:0:140"},
		Search:   &domain.SearchResult{CRF: 27},
	}
	persistence := &pipelineContextPersistence{cached: &cached, current: cached}
	job := &pipeline.JobContext{
		Metadata: cached.Metadata,
		Probe: &domain.ProbeResult{Streams: []domain.MediaStream{{
			Type: "video", Width: 1920, Height: 1080,
		}}},
	}
	resumed, err := persistence.ResumeStep(context.Background(), "crop-detect", job)
	if err != nil || !resumed {
		t.Fatalf("crop resume = %v, %v", resumed, err)
	}
	if job.Crop.Filter != "" || job.Metadata.CropFilter != "" || job.Crop.RejectionReason != "crop sample failed" {
		t.Fatalf("unsafe cached crop retained: %#v", job.Crop)
	}
	if persistence.current.Crop.Filter != "" || persistence.current.Metadata.CropFilter != "" || persistence.current.Search != nil {
		t.Fatalf("unsafe checkpoint retained: %#v", persistence.current)
	}
	if resumed, err := persistence.ResumeStep(context.Background(), "crf-search", job); err != nil || resumed {
		t.Fatalf("CRF search resume = %v, %v; want rerun", resumed, err)
	}
}

func TestNativeSearchRejectsOldCheckpoint(t *testing.T) {
	base := domain.JobPipelineContext{Version: domain.JobPipelineContextVersion, InputPath: "movie.mkv"}
	cached := base
	cached.Version = 4
	cached.Search = &domain.SearchResult{CRF: 27}
	if pipelineContextMatches(base, cached) {
		t.Fatal("accepted pre-native-search checkpoint")
	}
}

func TestResumeDropsCachedCropOnceDisabled(t *testing.T) {
	cached := domain.JobPipelineContext{
		Crop: &domain.CropResult{
			CandidateFilter: "crop=1920:800:0:140",
			Filter:          "crop=1920:800:0:140",
			Samples:         []domain.CropSample{{Filter: "crop=1920:800:0:140"}, {Filter: "crop=1920:800:0:140"}},
		},
		Metadata: domain.JobMetadata{CropFilter: "crop=1920:800:0:140"},
		Search:   &domain.SearchResult{CRF: 27},
	}
	persistence := &pipelineContextPersistence{cached: &cached, current: cached}
	job := &pipeline.JobContext{
		Metadata: cached.Metadata,
		Profile:  domain.Profile{Crop: domain.CropPolicy{Disabled: true}},
		Probe:    &domain.ProbeResult{Streams: []domain.MediaStream{{Type: "video", Width: 1920, Height: 1080}}},
	}
	resumed, err := persistence.ResumeStep(context.Background(), "crop-detect", job)
	if err != nil || !resumed {
		t.Fatalf("crop resume = %v, %v", resumed, err)
	}
	if !job.Crop.Disabled || job.Crop.Filter != "" || job.Metadata.CropFilter != "" {
		t.Fatalf("cached crop survived a disabled policy: %#v", job.Crop)
	}
	if persistence.current.Metadata.CropFilter != "" || persistence.current.Search != nil {
		t.Fatalf("checkpoint kept the crop or the search measured with it: %#v", persistence.current)
	}
	if resumed, err := persistence.ResumeStep(context.Background(), "crf-search", job); err != nil || resumed {
		t.Fatalf("CRF search resume = %v, %v; want rerun", resumed, err)
	}
}

func TestResumeDetectsAgainOnceCropIsEnabled(t *testing.T) {
	cached := domain.JobPipelineContext{
		Crop:   &domain.CropResult{Disabled: true, SourceWidth: 1920, SourceHeight: 1080, OutputWidth: 1920, OutputHeight: 1080, RetainedAreaPercent: 100},
		Search: &domain.SearchResult{CRF: 27},
	}
	persistence := &pipelineContextPersistence{cached: &cached, current: cached}
	job := &pipeline.JobContext{Probe: &domain.ProbeResult{Streams: []domain.MediaStream{{Type: "video", Width: 1920, Height: 1080}}}}
	if resumed, err := persistence.ResumeStep(context.Background(), "crop-detect", job); err != nil || resumed {
		t.Fatalf("crop resume = %v, %v; want detection to run", resumed, err)
	}
	if job.Crop != nil {
		t.Fatalf("crop = %#v", job.Crop)
	}
	if resumed, err := persistence.ResumeStep(context.Background(), "crf-search", job); err != nil || resumed {
		t.Fatalf("CRF search resume = %v, %v; the cached search measured an uncropped frame", resumed, err)
	}
}

func TestResumeSearchAcrossQualityModes(t *testing.T) {
	searched := domain.SearchResult{CRF: 31, Metric: domain.QualityMetricVMAF, VMAF: 95.2}
	fixed := domain.SearchResult{QualityMode: domain.VideoQualityFixed, CRF: 24}
	tests := []struct {
		name   string
		cached domain.SearchResult
		mode   domain.VideoQualityMode
		want   bool
	}{
		{"search result resumes in search mode", searched, domain.VideoQualitySearch, true},
		{"search result resumes under the zero mode", searched, "", true},
		{"search result never decides a fixed encode", searched, domain.VideoQualityFixed, false},
		{"fixed result never stands in for a search", fixed, domain.VideoQualitySearch, false},
		{"fixed result is restated, not resumed", fixed, domain.VideoQualityFixed, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cached := domain.JobPipelineContext{Search: &tt.cached}
			persistence := &pipelineContextPersistence{cached: &cached, current: cached}
			job := &pipeline.JobContext{Profile: domain.Profile{Video: domain.VideoProfile{QualityMode: tt.mode, CRF: 24}}}
			resumed, err := persistence.ResumeStep(context.Background(), "crf-search", job)
			if err != nil || resumed != tt.want {
				t.Fatalf("resume = %v, %v; want %v", resumed, err, tt.want)
			}
			if (job.Search != nil) != tt.want {
				t.Fatalf("search = %+v", job.Search)
			}
		})
	}
}

// The checkpoint fingerprint is the resolved profile as JSON, so it has to
// change with every field that changes what a checkpointed step decides.
func TestProfileFingerprintCoversFlowFields(t *testing.T) {
	base := domain.Profile{Video: domain.VideoProfile{CRFMin: 18, CRFMax: 40}}
	changes := map[string]func(*domain.Profile){
		"quality mode": func(p *domain.Profile) { p.Video.QualityMode = domain.VideoQualityFixed },
		"fixed CRF":    func(p *domain.Profile) { p.Video.CRF = 24 },
		"crop opt-out": func(p *domain.Profile) { p.Crop.Disabled = true },
		"audio encode": func(p *domain.Profile) { p.Audio.Encode = &domain.AudioEncode{Codec: "opus"} },
		"encode bitrate": func(p *domain.Profile) {
			p.Audio.Encode = &domain.AudioEncode{Codec: "opus", BitrateKbpsPerChannel: 64}
		},
		"audio downmix": func(p *domain.Profile) { p.Audio.Downmix = &domain.AudioDownmix{MaxChannels: 2, Codec: "aac"} },
		"remove subs":   func(p *domain.Profile) { p.Subtitles.RemoveAll = true },
		"drop bitmap":   func(p *domain.Profile) { p.Subtitles.DropBitmap = true },
	}
	job := &pipeline.JobContext{InputPath: "movie.mkv"}
	cached := basePipelineContext(job, nil, mustJSON(base), domain.JobMetadata{})
	if !pipelineContextMatches(cached, cached) {
		t.Fatal("unchanged profile does not match its own checkpoint")
	}
	for name, change := range changes {
		profile := base
		change(&profile)
		if pipelineContextMatches(basePipelineContext(job, nil, mustJSON(profile), domain.JobMetadata{}), cached) {
			t.Fatalf("checkpoint survived a changed %s", name)
		}
	}
}

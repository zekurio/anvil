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

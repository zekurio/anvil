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

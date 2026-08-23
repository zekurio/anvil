package worker

import (
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/marker"
	"github.com/zekurio/anvil/pkg/pipeline"
)

func TestProcessedInput(t *testing.T) {
	job := &pipeline.JobContext{Probe: &domain.ProbeResult{Streams: []domain.MediaStream{{
		Type: "video",
		Tags: map[string]string{marker.TagProcessed: "true"},
	}}}}
	if !processedInput(job) {
		t.Fatal("processedInput = false, want true")
	}
	job.Probe.Streams[0].Tags[marker.TagProcessed] = "yes"
	if processedInput(job) {
		t.Fatal("processedInput accepted a non-canonical marker value")
	}
}

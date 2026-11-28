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

func TestForcedGenerationDoesNotSkipProcessedInput(t *testing.T) {
	job := &pipeline.JobContext{
		Library: domain.Library{},
		Source:  domain.MediaSource{Generation: 2},
		Probe: &domain.ProbeResult{Streams: []domain.MediaStream{{
			Type: "video", Tags: map[string]string{marker.TagProcessed: "true"},
		}}},
	}
	if shouldSkipProcessed(job) {
		t.Fatal("forced generation was skipped")
	}
	// A library that reprocesses what it finds again would loop on its own
	// output.
	job.Library.Reprocess = true
	if !shouldSkipProcessed(job) {
		t.Fatal("processed generation of a reprocessing library was not skipped")
	}
}

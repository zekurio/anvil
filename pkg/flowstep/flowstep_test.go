package flowstep

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/ffmpeg"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/pipeline"
)

func TestFlowBlocksOnlyKnownUnavailableEncode(t *testing.T) {
	graph, err := json.Marshal(flow.Default())
	if err != nil {
		t.Fatal(err)
	}
	report := ffmpeg.EncoderCapabilities{State: ffmpeg.EncoderProbeReady, RenderDevice: "/dev/dri/renderD128",
		Encoders: []ffmpeg.EncoderCapability{{
			Name: "libsvtav1", BitDepths: []ffmpeg.BitDepthCapability{{BitDepth: 10, Reason: "ffmpeg has no libsvtav1 encoder."}},
		}},
	}
	block := Block{EncoderCapabilities: func() ffmpeg.EncoderCapabilities { return report }}
	job := pipeline.JobContext{
		Flow: domain.FlowSnapshot{Graph: graph}, RenderDevice: report.RenderDevice,
		Probe: &domain.ProbeResult{Streams: []domain.MediaStream{{Type: "video", Codec: "h264"}}},
	}
	if err := block.Run(t.Context(), &job); err == nil || !strings.Contains(err.Error(), "encoder libsvtav1 is unavailable") {
		t.Errorf("known unavailable encoder: %v", err)
	}
	for _, state := range []ffmpeg.EncoderProbeState{ffmpeg.EncoderProbeProbing, ffmpeg.EncoderProbeFailed} {
		report.State = state
		if err := block.Run(t.Context(), &job); err != nil {
			t.Errorf("probe %s blocked encode: %v", state, err)
		}
	}
	report.State = ffmpeg.EncoderProbeReady
	job.RenderDevice = "/dev/dri/renderD129"
	if err := block.Run(t.Context(), &job); err != nil {
		t.Errorf("report for another device blocked encode: %v", err)
	}
}

package webapi

import (
	"net/http"

	"github.com/zekurio/anvil/pkg/ffmpeg"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/video"
)

func (s *Server) encoders(_ http.ResponseWriter, _ *http.Request) (int, any, error) {
	if s.EncoderCapabilities != nil {
		return ok(s.EncoderCapabilities())
	}
	return ok(Encoders{State: ffmpeg.EncoderProbeFailed, Encoders: []ffmpeg.EncoderCapability{},
		Reason: "Encoder probing is unavailable."})
}

func (s *Server) flowCheck(graph flow.Graph) FlowCheck {
	check := flowCheck(graph)
	if s.EncoderCapabilities == nil || len(graph.Nodes) > flow.MaxNodes || len(graph.Edges) > flow.MaxEdges {
		return check
	}
	report := s.EncoderCapabilities()
	if report.State != ffmpeg.EncoderProbeReady {
		return check
	}
	for _, node := range graph.Nodes {
		settings, ok := node.VideoEncodeSettings()
		if !ok {
			continue
		}
		name := video.ResolveEncoder(settings.Codec, settings.Accelerator)
		for _, encoder := range report.Encoders {
			if encoder.Name != name {
				continue
			}
			field := "bit_depth"
			if !encoder.Available {
				field = "accelerator"
			}
			if reason := report.Unavailable(name, settings.BitDepth); reason != "" {
				check.Issues = append(check.Issues, flow.Issue{
					Severity: flow.SeverityWarning, NodeID: node.ID, Field: field, Message: reason,
				})
			}
			break
		}
	}
	return check
}

package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/process"
	"github.com/zekurio/anvil/pkg/video"
)

type EncoderProbeState string

const (
	EncoderProbeProbing EncoderProbeState = "probing"
	EncoderProbeReady   EncoderProbeState = "ready"
	EncoderProbeFailed  EncoderProbeState = "failed"
)

// EncoderCapabilities describes one probe. The availability flags are
// conclusive only in the ready state.
type EncoderCapabilities struct {
	State        EncoderProbeState   `json:"state"`
	RenderDevice string              `json:"render_device"`
	Encoders     []EncoderCapability `json:"encoders"`
	Reason       string              `json:"reason,omitempty"`
}

type EncoderCapability struct {
	Name         string               `json:"name"`
	Codec        string               `json:"codec"`
	Backend      string               `json:"backend"`
	QualityLabel string               `json:"quality_label"`
	Available    bool                 `json:"available"`
	BitDepths    []BitDepthCapability `json:"bit_depths"`
	Reason       string               `json:"reason,omitempty"`
}

type BitDepthCapability struct {
	BitDepth  int    `json:"bit_depth"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// Unavailable returns an explanation for a known unsupported depth.
func (c EncoderCapabilities) Unavailable(name string, depth int) string {
	if c.State != EncoderProbeReady {
		return ""
	}
	for _, encoder := range c.Encoders {
		if encoder.Name != name {
			continue
		}
		for _, result := range encoder.BitDepths {
			if result.BitDepth == depth && !result.Available {
				return result.Reason
			}
		}
		break
	}
	return ""
}

type EncoderProber struct {
	Runner  process.Runner
	Timeout time.Duration
}

func (p EncoderProber) Probe(ctx context.Context, renderDevice string) EncoderCapabilities {
	runner := p.Runner
	if runner == nil {
		runner = process.OSRunner{}
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	run := func(args []string, fullStdout bool) (process.Result, error) {
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return runner.Run(probeCtx, process.Command{Name: "ffmpeg", Args: args, RequireFullStdout: fullStdout})
	}
	result, err := run([]string{"-hide_banner", "-encoders"}, true)
	if err != nil {
		return failedEncoderProbe(renderDevice, result, err)
	}
	compiled := make(map[string]bool)
	for line := range strings.SplitSeq(string(result.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && len(fields[0]) == 6 && fields[0][0] == 'V' && fields[1] != "=" {
			compiled[fields[1]] = true
		}
	}
	if len(compiled) == 0 {
		return failedEncoderProbe(renderDevice, result, errors.New("ffmpeg returned no video encoder list"))
	}
	report := EncoderCapabilities{State: EncoderProbeReady, RenderDevice: renderDevice, Encoders: []EncoderCapability{}}
	for _, encoder := range video.Encoders() {
		entry := EncoderCapability{Name: encoder.Name, Codec: encoder.Codec, Backend: encoder.Backend,
			QualityLabel: encoder.Quality.Label, BitDepths: make([]BitDepthCapability, 0, len(encoder.BitDepths))}
		for _, depth := range encoder.BitDepths {
			if err := ctx.Err(); err != nil {
				return failedEncoderProbe(renderDevice, process.Result{}, err)
			}
			item := BitDepthCapability{BitDepth: depth}
			switch {
			case !compiled[encoder.Name]:
				item.Reason = "ffmpeg has no " + encoder.Name + " encoder."
			case encoder.Backend != video.AcceleratorSoftware:
				result, err := run(EncoderProbeArgs(encoder.Name, depth, renderDevice), false)
				if err != nil {
					var exitErr *exec.ExitError
					if errors.Is(err, exec.ErrNotFound) || result.ExitCode < 0 && !errors.Is(err, context.DeadlineExceeded) && !errors.As(err, &exitErr) {
						return failedEncoderProbe(renderDevice, result, err)
					}
					item.Reason = fmt.Sprintf("%s could not encode %d-bit %s: %s", encoder.Name, depth, encoder.Codec, probeDiagnostic(result, err))
				}
			}
			item.Available = item.Reason == ""
			entry.Available = entry.Available || item.Available
			entry.BitDepths = append(entry.BitDepths, item)
		}
		if !entry.Available {
			entry.Reason = entry.BitDepths[0].Reason
		}
		report.Encoders = append(report.Encoders, entry)
	}
	return report
}

func failedEncoderProbe(renderDevice string, result process.Result, err error) EncoderCapabilities {
	return EncoderCapabilities{State: EncoderProbeFailed, RenderDevice: renderDevice, Encoders: []EncoderCapability{},
		Reason: "Could not probe ffmpeg encoders: " + probeDiagnostic(result, err)}
}

func probeDiagnostic(result process.Result, err error) string {
	tail := strings.TrimSpace(string(result.Stderr))
	if tail == "" || errors.Is(err, context.DeadlineExceeded) {
		return err.Error()
	}
	// FFmpeg writes the cause before its encoder and muxer shutdown errors.
	// Prefer the start of the captured tail so those errors cannot bury it.
	runes := []rune(strings.Join(strings.Fields(tail), " "))
	if len(runes) > 240 {
		return string(runes[:239]) + "…"
	}
	return string(runes)
}

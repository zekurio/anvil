package ffmpeg

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/process"
	"github.com/zekurio/anvil/pkg/video"
)

func TestProbeEncoderCapabilities(t *testing.T) {
	t.Parallel()
	device := "/dev/dri/renderD129"
	var calls int
	prober := EncoderProber{Runner: destinationRunner(func(ctx context.Context, command process.Command) (process.Result, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok || command.Name != "ffmpeg" {
			t.Fatalf("probe has no deadline or wrong command: %+v", command)
		}
		if calls == 1 {
			if !slices.Equal(command.Args, []string{"-hide_banner", "-encoders"}) || !command.RequireFullStdout {
				t.Fatalf("encoder list command: %+v", command)
			}
			return process.Result{Stdout: []byte(" V..... = Video\n V..... libsvtav1\n V..... hevc_vaapi\n")}, nil
		}
		depth := 8
		if calls == 3 {
			depth = 10
		}
		if !slices.Equal(command.Args, EncoderProbeArgs("hevc_vaapi", depth, device)) {
			t.Fatalf("unexpected probe arguments: %v", command.Args)
		}
		if depth == 8 {
			return process.Result{}, nil
		}
		return process.Result{ExitCode: 1, Stderr: []byte("device refused\n")}, errors.New("exit 1")
	})}
	report := prober.Probe(t.Context(), device)
	if report.State != EncoderProbeReady || report.RenderDevice != device || len(report.Encoders) != len(video.Encoders()) || calls != 3 {
		t.Fatalf("report = %+v, calls = %d", report, calls)
	}
	if reason := report.Unavailable("hevc_vaapi", 10); !strings.Contains(reason, "device refused") {
		t.Errorf("10-bit HEVC reason = %q", reason)
	}
	if report.Unavailable("hevc_vaapi", 8) != "" || report.Unavailable("libsvtav1", 10) != "" {
		t.Error("available depth marked unavailable")
	}
	if reason := report.Unavailable("av1_vaapi", 8); reason != "ffmpeg has no av1_vaapi encoder." {
		t.Errorf("missing encoder reason = %q", reason)
	}
	for _, encoder := range report.Encoders {
		if encoder.Name == "hevc_vaapi" && (!encoder.Available || encoder.Reason != "") {
			t.Errorf("HEVC is available at one depth: %+v", encoder)
		}
	}
}

func TestEncoderProbeListFailureIsInconclusive(t *testing.T) {
	t.Parallel()
	prober := EncoderProber{Runner: destinationRunner(func(context.Context, process.Command) (process.Result, error) {
		return process.Result{ExitCode: -1}, errors.New("ffmpeg missing")
	})}
	report := prober.Probe(t.Context(), "")
	if report.State != EncoderProbeFailed || report.Encoders == nil || !strings.Contains(report.Reason, "ffmpeg missing") ||
		report.Unavailable("hevc_vaapi", 8) != "" {
		t.Fatalf("unexpected failed probe: %+v", report)
	}
}

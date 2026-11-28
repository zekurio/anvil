package ffmpeg

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/process"
)

func TestVAAPIUploadsAfterSoftwareCrop(t *testing.T) {
	for _, test := range []struct {
		depth                int
		device, init, format string
	}{
		{8, "", "vaapi=anvil:/dev/dri/renderD128", "nv12"},
		{10, "/dev/dri/renderD129", "vaapi=anvil:/dev/dri/renderD129", "p010le"},
	} {
		plan := domain.EncodePlan{VideoCodec: "hevc_vaapi", RenderDevice: test.device, BitDepth: test.depth,
			InputWidth: 256, InputHeight: 256, CropFilter: "crop=224:224:16:16", CRF: 23}
		for _, args := range [][]string{Args(plan), SampleArgs(plan)} {
			init := slices.Index(args, "-init_hw_device")
			filterDevice := slices.Index(args, "-filter_hw_device")
			input := slices.Index(args, "-i")
			filter := slices.Index(args, "-vf")
			if init < 0 || filterDevice < 0 || init >= input || filterDevice >= input ||
				args[init+1] != test.init || args[filterDevice+1] != "anvil" ||
				filter < input || !strings.HasSuffix(args[filter+1], plan.CropFilter+",format="+test.format+",hwupload") ||
				slices.Contains(args, "-hwaccel") || slices.Contains(args, "-preset") {
				t.Fatalf("unexpected VAAPI args: %q", args)
			}
		}
		if ReferenceFilter(plan) != plan.CropFilter {
			t.Fatalf("metric filter includes device setup: %q", ReferenceFilter(plan))
		}
	}
}

func TestConfiguredQSVDevice(t *testing.T) {
	plan := domain.EncodePlan{VideoCodec: "hevc_qsv", InputVideoCodec: "hevc", Accelerator: "qsv", RenderDevice: "/dev/dri/renderD129", BitDepth: 10}
	final, sample := Args(plan), SampleArgs(plan)
	for _, args := range [][]string{final, sample} {
		if !slices.Contains(args, "vaapi=anvil_va:/dev/dri/renderD129") || !slices.Contains(args, "qsv=anvil@anvil_va") {
			t.Fatalf("configured device missing: %q", args)
		}
	}
	device := slices.Index(final, "-hwaccel_device")
	if device < 0 || final[device+1] != "anvil" || slices.Contains(sample, "-hwaccel") ||
		!strings.Contains(strings.Join(sample, " "), "format=p010le,hwupload") {
		t.Fatalf("unexpected input pipeline: final %q, sample %q", final, sample)
	}
}

func TestAMFQualityOptions(t *testing.T) {
	for _, name := range []string{"av1_amf", "hevc_amf", "h264_amf"} {
		plan := domain.EncodePlan{VideoCodec: name, Preset: "speed", CRF: 23, BitDepth: 8}
		for _, args := range [][]string{Args(plan), SampleArgs(plan)} {
			options := strings.Join(args, " ")
			if !strings.Contains(options, "-rc cqp -qp_i 23 -qp_p 23") ||
				!strings.Contains(options, "-quality speed") || slices.Contains(args, "-preset") ||
				slices.Contains(args, "-qp_b") != (name == "h264_amf") {
				t.Fatalf("unexpected AMF options: %q", args)
			}
		}
	}
}

func TestHardwareEncoder(t *testing.T) {
	backend := os.Getenv("ANVIL_HW_TEST")
	if backend == "" {
		t.Skip("set ANVIL_HW_TEST=vaapi or qsv to run a real hardware encode")
	}
	if backend != "vaapi" && backend != "qsv" {
		t.Fatalf("unsupported ANVIL_HW_TEST value %q", backend)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	args := EncoderProbeArgs("h264_"+backend, 8, os.Getenv("ANVIL_HW_RENDER_DEVICE"))
	result, err := (process.OSRunner{}).Run(ctx, process.Command{Name: "ffmpeg", Args: args})
	if err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, result.Stderr)
	}
}

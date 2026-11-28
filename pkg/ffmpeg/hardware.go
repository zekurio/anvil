package ffmpeg

import (
	"slices"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/video"
)

func planEncoder(plan domain.EncodePlan) video.Encoder {
	encoder, _ := video.EncoderByName(valueOr(plan.VideoCodec, "libsvtav1"))
	return encoder
}

func deviceArgs(plan domain.EncodePlan) []string {
	if plan.VideoCopy {
		return nil
	}
	switch planEncoder(plan).Device {
	case "vaapi":
		return []string{"-init_hw_device", "vaapi=anvil:" + valueOr(plan.RenderDevice, "/dev/dri/renderD128"), "-filter_hw_device", "anvil"}
	case "qsv":
		if plan.RenderDevice != "" {
			return []string{"-init_hw_device", "vaapi=anvil_va:" + plan.RenderDevice,
				"-init_hw_device", "qsv=anvil@anvil_va", "-filter_hw_device", "anvil"}
		}
	}
	return nil
}

func softwareInputFilter(plan domain.EncodePlan, filter string) string {
	encoder := planEncoder(plan)
	var filters []string
	if filter != "" {
		filters = append(filters, filter)
	}
	if encoder.Device != "" {
		filters = append(filters, "format="+encoder.SurfaceFormat(plan.BitDepth))
		switch {
		case encoder.Device == "vaapi":
			filters = append(filters, "hwupload")
		case plan.RenderDevice != "":
			// QSV may need a fixed surface pool on older Media SDK versions.
			filters = append(filters, "hwupload=extra_hw_frames=32")
		}
	}
	return strings.Join(filters, ",")
}

// EncoderProbeArgs builds a one-frame encode of a synthetic source. It returns
// nil for an unknown encoder or unsupported bit depth. No file is opened or
// written by the command.
func EncoderProbeArgs(name string, bitDepth int, renderDevice string) []string {
	encoder, ok := video.EncoderByName(name)
	if !ok || !slices.Contains(encoder.BitDepths, bitDepth) {
		return nil
	}
	plan := domain.EncodePlan{VideoCodec: encoder.Name, BitDepth: bitDepth,
		RenderDevice: renderDevice, CRF: encoder.Quality.Default, Threads: 2}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-xerror"}
	args = append(args, deviceArgs(plan)...)
	args = append(args, "-f", "lavfi", "-i", "testsrc2=size=256x256:rate=25", "-frames:v", "1")
	args = append(args, videoOutputArgs(plan, softwareInputFilter(plan, ""))...)
	return append(args, "-f", "null", "-")
}

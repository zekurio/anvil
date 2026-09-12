package ffmpeg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
)

// SampleArgs encodes a video-only reference clip using the final encode's
// decoder, crop, rate control, pixel format, preset, and encoder options.
func SampleArgs(plan domain.EncodePlan) []string {
	args := []string{"-hide_banner", "-nostdin", "-n", "-xerror", "-nostats", "-progress", "pipe:1"}
	args = append(args, inputArgs(plan)...)
	args = append(args, "-threads", strconv.Itoa(max(plan.Threads, 1)), "-i", plan.InputPath,
		"-map", "0:v:0")
	// Cutting a reordered GOP can leave gaps near the end of a sample. QSV
	// can emit backwards DTS when flushing those frames. Samples are compared
	// by frame order, so give them a continuous timeline without dropping frames.
	// Keep the reported frame rate, or use 25 when the input rate is unknown.
	filter := "setpts='N/(if(gt(FRAME_RATE,0),FRAME_RATE,25)*TB)'"
	if crop := videoFilter(plan); crop != "" {
		filter += "," + crop
	}
	args = append(args, videoOutputArgs(plan, filter)...)
	return append(args, "-an", "-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1", "-f", "matroska", plan.OutputPath)
}

func videoOutputArgs(plan domain.EncodePlan, filter string) []string {
	var args []string
	if filter != "" && !plan.VideoCopy {
		args = append(args, "-vf", filter)
	}
	args = append(args, videoArgs(plan)...)
	if !plan.VideoCopy {
		// Preserve frame order/count in both sample and final encodes, including VFR.
		args = append(args, "-fps_mode", "passthrough")
		args = append(args, plan.FFmpegArgs...)
	}
	return args
}

// ReferenceFilter is the software equivalent of the crop used for encoding.
// Metric comparison always decodes to software frames, including QSV encodes.
func ReferenceFilter(plan domain.EncodePlan) string {
	if noCropFilter(plan) {
		return ""
	}
	return plan.CropFilter
}

// ValidateEncoderArgs prevents extra options from changing what is searched,
// overriding the selected quality, or introducing additional inputs/outputs.
func ValidateEncoderArgs(args []string) error {
	for i := 0; i < len(args); i += 2 {
		key := args[i]
		if !strings.HasPrefix(key, "-") || key == "-" || i+1 == len(args) {
			return fmt.Errorf("ffmpeg_args must contain encoder option/value pairs; invalid option %q", key)
		}
		name := strings.SplitN(strings.TrimLeft(key, "-"), ":", 2)[0]
		if strings.HasPrefix(name, "filter") || strings.HasPrefix(name, "map") {
			return fmt.Errorf("ffmpeg_args option %q is managed by Anvil and cannot be used during CRF search", key)
		}
		switch name {
		case "i", "f", "y", "n", "stdin", "nostdin", "vf", "af", "lavfi", "s", "video_size", "r", "vsync", "fps_mode",
			"t", "to", "ss", "sseof", "fs", "frames", "vframes", "stream_loop", "shortest", "copyts", "start_at_zero", "itsoffset", "itsscale",
			"c", "codec", "vcodec", "an", "vn", "sn", "dn", "crf", "qp", "q", "qscale", "global_quality", "rc", "qp_i", "qp_p", "qp_b", "b", "vb", "bit_rate",
			"pix_fmt", "pixel_format", "threads", "preset", "pass", "passlogfile", "progress", "stats_period":
			return fmt.Errorf("ffmpeg_args option %q is managed by Anvil and cannot be used during CRF search", key)
		}
	}
	return nil
}

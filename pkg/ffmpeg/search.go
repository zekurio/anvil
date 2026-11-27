package ffmpeg

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/video"
)

// SampleArgs encodes a video-only reference clip using the final encode's crop,
// rate control, pixel format, preset, and encoder options. Samples use software
// decoding and cropping, like the reference frame count and quality metric.
func SampleArgs(plan domain.EncodePlan) []string {
	// Sample progress only feeds the per-process diagnostic log, so keep it but
	// match the final encode's period instead of a block every half second.
	args := []string{"-hide_banner", "-nostdin", "-n", "-xerror", "-nostats", "-stats_period", "5", "-progress", "pipe:1"}
	// Do not use inputArgs here: QSV and software decoders can emit different
	// frames at a copied GOP boundary. Decode the same packets with the same
	// default software decoder used by ffprobe and the metric, even for QSV.
	args = append(args, "-threads", strconv.Itoa(max(plan.Threads, 1)), "-i", plan.InputPath,
		"-map", "0:v:0")
	// Cutting a reordered GOP can leave gaps near the end of a sample. QSV
	// can emit backwards DTS when flushing those frames. Samples are compared
	// by frame order, so give them a continuous timeline without dropping frames.
	// Keep the reported frame rate, or use 25 when the input rate is unknown.
	filter := "setpts='N/(if(gt(FRAME_RATE,0),FRAME_RATE,25)*TB)'"
	if crop := ReferenceFilter(plan); crop != "" {
		filter += "," + crop
	}
	if video.EncoderAccelerator(plan.VideoCodec) == video.AcceleratorQSV {
		// QSV encoders upload software frames internally. Pin the input format
		// to the requested depth instead of letting negotiation choose 8-bit.
		filter += ",format=" + video.QSVVPPFormat(plan.BitDepth)
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

// encoderParamsOptions are the raw option names whose values libsvtav1,
// libx265, and libx264 apply after the FFmpeg-level options. A rate-control
// key inside one of them overrides the CRF Anvil searched for even though the
// FFmpeg-level options themselves pass validation.
var encoderParamsOptions = map[string]bool{
	"svtav1-params": true,
	"x265-params":   true,
	"x264-params":   true,
}

// encoderOptions is the small set of value-only encoder controls accepted
// across Anvil's software, QSV, VAAPI, and AMF encoders. None names a file,
// URL, device, stream selector, or filter graph.
var encoderOptions = map[string]bool{
	"tune": true, "profile": true, "level": true,
	"g": true, "keyint_min": true, "bf": true, "refs": true, "sc_threshold": true,
	"aq-mode": true, "aq-strength": true, "rc-lookahead": true, "b_strategy": true,
	"look_ahead": true, "look_ahead_depth": true, "extbrc": true, "low_power": true,
	"async_depth": true, "adaptive_i": true, "adaptive_b": true, "quality": true,
}

const acceptedEncoderArgs = "encoder parameter bundles (-svtav1-params, -x265-params, -x264-params) or value-only tuning options (-tune, -profile:v, -level, -g, -keyint_min, -bf, -refs, -sc_threshold, -aq-mode, -aq-strength, -rc-lookahead, -b_strategy, -look_ahead, -look_ahead_depth, -extbrc, -low_power, -async_depth, -adaptive_i, -adaptive_b, -quality)"

// managedParamsKeys are encoder-params keys that select quality, rate control,
// or speed, which Anvil owns for the search and final encode.
var managedParamsKeys = map[string]bool{
	"crf": true, "qp": true, "qpmin": true, "qpmax": true,
	"rc": true, "bitrate": true, "bit_rate": true, "tbr": true, "mbr": true,
	"preset": true,
}

// ValidateEncoderArgs prevents extra options from changing what is searched,
// overriding the selected quality, or introducing additional inputs/outputs.
func ValidateEncoderArgs(args []string) error {
	for i := 0; i < len(args); i += 2 {
		key := args[i]
		if !strings.HasPrefix(key, "-") || key == "-" || i+1 == len(args) {
			return fmt.Errorf("ffmpeg_args must contain option/value pairs; invalid option %q; accepted: %s", key, acceptedEncoderArgs)
		}
		name := strings.SplitN(strings.TrimLeft(key, "-"), ":", 2)[0]
		value := args[i+1]
		if strings.HasPrefix(value, "-") {
			return fmt.Errorf("ffmpeg_args value %q for option %q must not begin with \"-\"", value, key)
		}
		if encoderParamsOptions[name] {
			if err := validateEncoderParams(value); err != nil {
				return fmt.Errorf("ffmpeg_args option %q: %w", key, err)
			}
			continue
		}
		if !encoderOptions[name] {
			return fmt.Errorf("ffmpeg_args option %q is not accepted; accepted: %s", key, acceptedEncoderArgs)
		}
	}
	return nil
}

// validateEncoderParams rejects managed keys in a codec params string such as
// "tune=0:film-grain=8". Keys are colon separated.
func validateEncoderParams(value string) error {
	for _, entry := range strings.Split(value, ":") {
		key, _, found := strings.Cut(strings.TrimSpace(entry), "=")
		if !found {
			continue
		}
		if managedParamsKeys[strings.ToLower(strings.TrimSpace(key))] {
			return fmt.Errorf("encoder param %q overrides a setting managed by Anvil", strings.TrimSpace(key))
		}
	}
	return nil
}

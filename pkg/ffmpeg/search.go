package ffmpeg

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
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
	args = append(args, deviceArgs(plan)...)
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
	args = append(args, videoOutputArgs(plan, softwareInputFilter(plan, filter))...)
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

// encoderParams maps each encoder parameter bundle to the keys accepted
// inside it. libsvtav1, libx265, and libx264 apply a bundle after the
// FFmpeg-level options, and each of them has keys that name a file to read or
// write (stats, csv, analysis-save, dump-yuv, fgs-table, cqmfile, film-grain
// for x265, ...). So the bundles are an allow-list too: only keys whose value
// is a number, an enum, or a colour description are listed. Keys that take a
// path, select rate control or quality, change the frame geometry or timing,
// or are unclear are left out.
var encoderParams = map[string]map[string]bool{
	"svtav1-params": keySet(
		"tune", "film-grain", "film-grain-denoise", "adaptive-film-grain",
		"enable-qm", "qm-min", "qm-max", "chroma-qm-min", "chroma-qm-max",
		"aq-mode", "sharpness", "ac-bias", "luminance-qp-bias", "qp-scale-compress-strength",
		"enable-variance-boost", "variance-boost-strength", "variance-octile", "variance-boost-curve",
		"enable-overlays", "scd", "lookahead", "keyint", "irefresh-type", "hierarchical-levels",
		"enable-tf", "tf-strength", "scm", "fast-decode", "max-tx-size",
		"enable-restoration", "enable-cdef", "enable-dlf", "enable-mfmv", "enable-dg",
		"tile-rows", "tile-columns", "lp", "pin",
		"color-primaries", "transfer-characteristics", "matrix-coefficients", "color-range",
		"chroma-sample-position", "mastering-display", "content-light",
	),
	"x265-params": keySet(
		"aq-mode", "aq-strength", "aq-motion", "hevc-aq", "qg-size", "cutree",
		"psy-rd", "psy-rdoq", "rd", "rdoq-level", "rd-refine", "dynamic-rd", "ssim-rd",
		"rskip", "rskip-edge-threshold", "deblock", "sao", "sao-non-deblock", "limit-sao", "selective-sao",
		"strong-intra-smoothing", "constrained-intra", "b-intra", "fast-intra", "rdpenalty",
		"ctu", "min-cu-size", "max-tu-size", "tu-intra-depth", "tu-inter-depth", "limit-tu",
		"ref", "limit-refs", "limit-modes", "rect", "amp", "max-merge", "me", "subme", "merange",
		"temporal-mvp", "early-skip", "tskip", "tskip-fast", "signhide", "weightp", "weightb",
		"bframes", "b-adapt", "b-pyramid", "bframe-bias", "rc-lookahead", "lookahead-slices",
		"keyint", "min-keyint", "scenecut", "scenecut-bias", "hist-scenecut", "fades", "radl",
		"gop-lookahead", "open-gop", "rc-grain", "nr-intra", "nr-inter", "ipratio", "pbratio",
		"qcomp", "cbqpoffs", "crqpoffs", "frame-threads", "wpp", "info", "log-level",
		"hdr10", "hdr10-opt", "repeat-headers", "aud", "hrd", "high-tier", "level-idc",
		"colorprim", "transfer", "colormatrix", "range", "chromaloc", "master-display", "max-cll", "cll",
	),
	"x264-params": keySet(
		"aq-mode", "aq-strength", "psy-rd", "psy", "trellis", "deblock", "nr",
		"deadzone-inter", "deadzone-intra", "ipratio", "pbratio", "qcomp", "chroma-qp-offset", "mbtree",
		"bframes", "b-adapt", "b-bias", "b-pyramid", "open-gop", "ref", "me", "subme", "merange",
		"rc-lookahead", "keyint", "min-keyint", "scenecut", "cabac", "weightb", "weightp", "direct",
		"partitions", "8x8dct", "mixed-refs", "chroma-me", "fast-pskip", "dct-decimate",
		"constrained-intra", "aud",
		"colorprim", "transfer", "colormatrix", "chromaloc", "mastering-display", "cll",
	),
}

// negatableParams are the bundles whose parser reads "no-<key>" as <key>
// switched off, so no-sao=1 is the same key as sao=0.
var negatableParams = map[string]bool{"x265-params": true, "x264-params": true}

func keySet(keys ...string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}

// managedParamsKeys are encoder-params keys that select quality, rate control,
// or speed, which Anvil owns for the search and final encode. They are not in
// any allow-list; naming them only gives the refusal a clearer reason. Keys
// are compared lowercased with underscores as dashes, as x265 and x264 read
// them.
var managedParamsKeys = keySet(
	"crf", "crf-min", "crf-max", "qp", "qpmin", "qpmax", "min-qp", "max-qp",
	"rc", "bitrate", "bit-rate", "tbr", "mbr", "vbv-maxrate", "vbv-bufsize",
	"pass", "passes", "lossless", "preset",
)

// negativeNumber is the one shape of value that may begin with "-": ffmpeg
// takes the argument after an option as its value, so -g -1 is unambiguous.
var negativeNumber = regexp.MustCompile(`^-[0-9]+(\.[0-9]+)?$`)

// ValidateEncoderArgs prevents extra options from changing what is searched,
// overriding the selected quality, or introducing additional inputs/outputs.
func ValidateEncoderArgs(encoder string, args []string) error {
	settings, _ := video.EncoderByName(valueOr(encoder, "libsvtav1"))
	accepted := slices.Clone(settings.TuningOptions)
	if settings.ParamsOption != "" {
		accepted = append(accepted, settings.ParamsOption)
	}
	for i := range accepted {
		accepted[i] = "-" + accepted[i]
	}
	options := strings.Join(accepted, ", ")
	for i := 0; i < len(args); i += 2 {
		key := args[i]
		if !strings.HasPrefix(key, "-") || key == "-" || i+1 == len(args) {
			return fmt.Errorf("ffmpeg_args must contain option/value pairs; invalid option %q; accepted for %s: %s", key, settings.Name, options)
		}
		name := strings.SplitN(strings.TrimLeft(key, "-"), ":", 2)[0]
		value := args[i+1]
		if strings.HasPrefix(value, "-") && !negativeNumber.MatchString(value) {
			return fmt.Errorf("ffmpeg_args value %q for option %q must not begin with \"-\" unless it is a negative number", value, key)
		}
		if name == settings.ParamsOption && name != "" {
			if err := validateEncoderParams(name, value); err != nil {
				return fmt.Errorf("ffmpeg_args option %q: %w", key, err)
			}
			continue
		}
		if !slices.Contains(settings.TuningOptions, name) {
			return fmt.Errorf("ffmpeg_args option %q is not accepted by %s; accepted: %s", key, settings.Name, options)
		}
	}
	return nil
}

// validateEncoderParams checks a bundle such as "tune=0:film-grain=8" against
// the keys of its encoder. ffmpeg splits a bundle with av_dict_parse_string,
// which honours backslash escapes and quotes and trims whitespace, so c\rf=51
// or 'crf'=60 reaches the encoder as crf. Refusing every character but
// letters, digits, and . , + - _ ( ) makes the literal split on ":" and "="
// below see exactly the keys the encoder sees, and leaves no room for a path.
// ffmpeg refuses an entry without "=", so even a switch needs a value (sao=1,
// no-sao=1).
func validateEncoderParams(option, value string) error {
	keys := encoderParams[option]
	for _, entry := range strings.Split(value, ":") {
		key, setting, found := strings.Cut(entry, "=")
		normalized := strings.ReplaceAll(strings.ToLower(key), "_", "-")
		negated, isNegated := strings.CutPrefix(key, "no-")
		accepted := keys[key] || isNegated && negatableParams[option] && keys[negated]
		switch {
		case entry == "":
			return errors.New(`encoder params must not contain an empty entry; separate key=value entries with one ":"`)
		case !paramText(key) || !paramText(setting):
			return fmt.Errorf("encoder param %q may only contain letters, digits, and . , + - _ ( )", entry)
		case managedParamsKeys[normalized]:
			return fmt.Errorf("encoder param %q overrides a setting managed by Anvil", key)
		case !accepted:
			return fmt.Errorf("encoder param %q is not accepted in -%s; accepted: %s", key, option, strings.Join(slices.Sorted(maps.Keys(keys)), ", "))
		case !found || setting == "":
			return fmt.Errorf("encoder param %q needs a value, such as %s=1", key, key)
		}
	}
	return nil
}

func paramText(text string) bool {
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(".,+-_()", r):
		default:
			return false
		}
	}
	return true
}

package video

import "strings"

const (
	AcceleratorSoftware = "software"
	AcceleratorQSV      = "qsv"
	AcceleratorVAAPI    = "vaapi"
	AcceleratorAMF      = "amf"

	DefaultBitDepth = 10
)

func NormalizeCodec(codec string) string {
	codec = strings.ToLower(strings.TrimSpace(codec))
	codec = strings.ReplaceAll(codec, "_", "-")
	return codec
}

func NormalizeAccelerator(accelerator string) string {
	return strings.ToLower(strings.TrimSpace(accelerator))
}

func CanonicalCodec(codec string) string {
	switch NormalizeCodec(codec) {
	case "", "av1", "libsvtav1", "svt-av1", "svtav1", "libaom-av1", "librav1e", "rav1e", "av1-qsv", "av1-vaapi", "av1-amf", "av1-nvenc", "av1-videotoolbox":
		return "av1"
	case "hevc", "h265", "h.265", "libx265", "x265", "hevc-qsv", "hevc-vaapi", "hevc-amf", "hevc-nvenc", "hevc-videotoolbox":
		return "hevc"
	case "h264", "h.264", "avc", "libx264", "x264", "h264-qsv", "h264-vaapi", "h264-amf", "h264-nvenc", "h264-videotoolbox":
		return "h264"
	default:
		return NormalizeCodec(codec)
	}
}

func ResolveEncoder(codec string, accelerator string) string {
	canonical := CanonicalCodec(codec)
	accelerator = ResolveAccelerator(accelerator)
	if encoder, ok := encoderFor(canonical, accelerator); ok {
		return encoder
	}
	return "libsvtav1"
}

func ResolveAccelerator(accelerator string) string {
	if accelerator = NormalizeAccelerator(accelerator); accelerator == "" {
		return AcceleratorSoftware
	}
	return accelerator
}

func NormalizeBitDepth(bitDepth int) int {
	if bitDepth == 0 {
		return DefaultBitDepth
	}
	return bitDepth
}

func ValidBitDepth(bitDepth int) bool {
	switch bitDepth {
	case 8, 10:
		return true
	default:
		return false
	}
}

func SoftwarePixelFormat(bitDepth int) string {
	switch NormalizeBitDepth(bitDepth) {
	case 8:
		return "yuv420p"
	default:
		return "yuv420p10le"
	}
}

func QSVVPPFormat(bitDepth int) string {
	switch NormalizeBitDepth(bitDepth) {
	case 8:
		return "nv12"
	default:
		return "p010le"
	}
}

func EncoderAccelerator(encoder string) string {
	switch {
	case strings.HasSuffix(NormalizeCodec(encoder), "-qsv"):
		return AcceleratorQSV
	case strings.HasSuffix(NormalizeCodec(encoder), "-vaapi"):
		return AcceleratorVAAPI
	case strings.HasSuffix(NormalizeCodec(encoder), "-amf"):
		return AcceleratorAMF
	default:
		return ""
	}
}

func QSVDecoder(codec string) (string, bool) {
	switch CanonicalCodec(codec) {
	case "av1":
		return "av1_qsv", true
	case "hevc":
		return "hevc_qsv", true
	case "h264":
		return "h264_qsv", true
	case "vp9":
		return "vp9_qsv", true
	case "mpeg2video", "mpeg2":
		return "mpeg2_qsv", true
	default:
		return "", false
	}
}

// encoders maps an accelerator and canonical output codec to the FFmpeg
// encoder that produces it.
var encoders = map[string]map[string]string{
	AcceleratorSoftware: {"av1": "libsvtav1", "hevc": "libx265", "h264": "libx264"},
	AcceleratorQSV:      {"av1": "av1_qsv", "hevc": "hevc_qsv", "h264": "h264_qsv"},
	AcceleratorVAAPI:    {"av1": "av1_vaapi", "hevc": "hevc_vaapi", "h264": "h264_vaapi"},
	AcceleratorAMF:      {"av1": "av1_amf", "hevc": "hevc_amf", "h264": "h264_amf"},
}

func encoderFor(codec string, accelerator string) (string, bool) {
	encoder, ok := encoders[accelerator][codec]
	return encoder, ok
}

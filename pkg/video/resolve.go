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
	if encoder, ok := EncoderByName(codec); ok {
		return encoder.Codec
	}
	switch NormalizeCodec(codec) {
	case "", "av1", "libsvtav1", "svt-av1", "svtav1", "libaom-av1", "librav1e", "rav1e":
		return "av1"
	case "hevc", "h265", "h.265", "libx265", "x265":
		return "hevc"
	case "h264", "h.264", "avc", "libx264", "x264":
		return "h264"
	default:
		return NormalizeCodec(codec)
	}
}

func ResolveEncoder(codec string, accelerator string) string {
	if encoder, ok := LookupEncoder(codec, accelerator); ok {
		return encoder.Name
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

// QualityRange returns the range used by the encoder's quality control.
func QualityRange(encoder string) (int, int) {
	if encoder == "" {
		encoder = "libsvtav1"
	}
	e, _ := EncoderByName(encoder)
	return e.Quality.Min, e.Quality.Max
}

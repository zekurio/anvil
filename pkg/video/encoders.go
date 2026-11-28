package video

import (
	"slices"
	"strconv"
	"strings"
)

// SpeedControl describes the encoder's speed option. An empty Option means
// there is no speed control. Choices, when present, supersede Min and Max.
type SpeedControl struct {
	Option, Default string
	Choices         []string
	Min, Max        int
}

// Accepts reports whether a preset is valid. Empty selects the default.
func (s SpeedControl) Accepts(preset string) bool {
	if preset == "" {
		return true
	}
	if s.Option == "" {
		return false
	}
	if len(s.Choices) != 0 {
		return slices.Contains(s.Choices, preset)
	}
	n, err := strconv.Atoi(preset)
	return err == nil && n >= s.Min && n <= s.Max
}

// QualityControl describes constant-quality rate control. Each option in
// Options takes the same value, after any Setup options.
type QualityControl struct {
	Label                         string
	Setup, Options                []string
	Min, Max                      int
	Default, SearchMin, SearchMax int
}

// Encoder describes the available controls and frame requirements for one
// FFmpeg encoder. Device is empty when no explicit FFmpeg device is needed.
type Encoder struct {
	Name, Codec, Backend, Device string
	Speed                        SpeedControl
	Quality                      QualityControl
	BitDepths                    []int
	ParamsOption                 string
	TuningOptions                []string
}

// SurfaceFormat returns the software pixel format for upload to the encoder's
// device, or an empty string if the encoder accepts planar software frames.
func (e Encoder) SurfaceFormat(bitDepth int) string {
	if e.Device == "" && e.Backend != AcceleratorAMF {
		return ""
	}
	if NormalizeBitDepth(bitDepth) == 8 {
		return "nv12"
	}
	return "p010le"
}

var (
	x26xSpeed   = SpeedControl{Option: "-preset", Default: "medium", Choices: strings.Fields("ultrafast superfast veryfast faster fast medium slow slower veryslow placebo")}
	qsvSpeed    = SpeedControl{Option: "-preset", Default: "medium", Choices: strings.Fields("veryfast faster fast medium slow slower veryslow")}
	amfSpeed    = SpeedControl{Option: "-quality", Default: "balanced", Choices: strings.Fields("speed balanced quality")}
	amfAV1Speed = SpeedControl{Option: "-quality", Default: "balanced", Choices: strings.Fields("speed balanced quality high_quality")}
	depth8      = []int{8}
	depth10     = []int{8, 10}
	// These software limits reflect encoder behavior: SVT uses zero as
	// unset, x265 rejects values above 51 and x264 clamps them.
	svtQuality  = QualityControl{"CRF", nil, []string{"-crf"}, 1, 63, 28, 18, 40}
	x26xQuality = QualityControl{"CRF", nil, []string{"-crf"}, 0, 51, 23, 18, 36}
	// FFmpeg clamps ICQQuality to 1–51, including for AV1.
	qsvQuality      = QualityControl{"ICQ", nil, []string{"-global_quality"}, 1, 51, 23, 18, 36}
	vaapiQuality    = QualityControl{"QP", []string{"-rc_mode", "CQP"}, []string{"-global_quality"}, 1, 51, 23, 18, 36}
	vaapiAV1Quality = QualityControl{"QP", []string{"-rc_mode", "CQP"}, []string{"-global_quality"}, 1, 255, 110, 70, 170}
	amfQuality      = QualityControl{"QP", []string{"-rc", "cqp"}, []string{"-qp_i", "-qp_p"}, 0, 51, 23, 18, 36}
	amfH264Quality  = QualityControl{"QP", []string{"-rc", "cqp"}, []string{"-qp_i", "-qp_p", "-qp_b"}, 0, 51, 23, 18, 36}
	amfAV1Quality   = QualityControl{"QP", []string{"-rc", "cqp"}, []string{"-qp_i", "-qp_p"}, 0, 255, 110, 70, 170}
	qsvTuning       = strings.Fields("g bf refs profile low_power async_depth b_strategy extbrc look_ahead_depth adaptive_i adaptive_b")
	vaapiTuning     = strings.Fields("g bf refs profile level low_power async_depth")
	amfTuning       = strings.Fields("g profile level async_depth")
)

// Speed choices, pixel formats and private options were checked against
// ffmpeg -h encoder=<name>. SVT's negative presets are reserved or special.
var encoders = []Encoder{
	{"libsvtav1", "av1", AcceleratorSoftware, "", SpeedControl{Option: "-preset", Default: "6", Min: 0, Max: 13}, svtQuality, depth10, "svtav1-params", strings.Fields("g")},
	{"libx265", "hevc", AcceleratorSoftware, "", x26xSpeed, x26xQuality, depth10, "x265-params", strings.Fields("g bf refs tune profile")},
	{"libx264", "h264", AcceleratorSoftware, "", x26xSpeed, x26xQuality, depth10, "x264-params", strings.Fields("g keyint_min bf refs sc_threshold tune profile level aq-mode aq-strength rc-lookahead b_strategy")},
	{"av1_qsv", "av1", AcceleratorQSV, "qsv", qsvSpeed, qsvQuality, depth10, "", qsvTuning},
	{"hevc_qsv", "hevc", AcceleratorQSV, "qsv", qsvSpeed, qsvQuality, depth10, "", qsvTuning},
	{"h264_qsv", "h264", AcceleratorQSV, "qsv", qsvSpeed, qsvQuality, depth8, "", append(slices.Clone(qsvTuning), "look_ahead")},
	{"av1_vaapi", "av1", AcceleratorVAAPI, "vaapi", SpeedControl{}, vaapiAV1Quality, depth10, "", vaapiTuning},
	{"hevc_vaapi", "hevc", AcceleratorVAAPI, "vaapi", SpeedControl{}, vaapiQuality, depth10, "", vaapiTuning},
	{"h264_vaapi", "h264", AcceleratorVAAPI, "vaapi", SpeedControl{}, vaapiQuality, depth8, "", append(slices.Clone(vaapiTuning), "quality")},
	{"av1_amf", "av1", AcceleratorAMF, "", amfAV1Speed, amfAV1Quality, depth10, "", amfTuning},
	{"hevc_amf", "hevc", AcceleratorAMF, "", amfSpeed, amfQuality, depth10, "", amfTuning},
	{"h264_amf", "h264", AcceleratorAMF, "", amfSpeed, amfH264Quality, depth8, "", append(slices.Clone(amfTuning), "bf", "refs")},
}

func (e Encoder) clone() Encoder {
	e.Speed.Choices = slices.Clone(e.Speed.Choices)
	e.Quality.Setup = slices.Clone(e.Quality.Setup)
	e.Quality.Options = slices.Clone(e.Quality.Options)
	e.BitDepths = slices.Clone(e.BitDepths)
	e.TuningOptions = slices.Clone(e.TuningOptions)
	return e
}

// Encoders returns the encoder descriptions in registry order.
func Encoders() []Encoder {
	result := make([]Encoder, 0, len(encoders))
	for _, e := range encoders {
		result = append(result, e.clone())
	}
	return result
}

// EncoderByName looks up an FFmpeg encoder name.
func EncoderByName(name string) (Encoder, bool) {
	name = NormalizeCodec(name)
	for _, e := range encoders {
		if NormalizeCodec(e.Name) == name {
			return e.clone(), true
		}
	}
	return Encoder{}, false
}

// LookupEncoder resolves a codec alias and backend to an encoder description.
func LookupEncoder(codec, backend string) (Encoder, bool) {
	codec, backend = CanonicalCodec(codec), ResolveAccelerator(backend)
	for _, e := range encoders {
		if e.Codec == codec && e.Backend == backend {
			return e.clone(), true
		}
	}
	return Encoder{}, false
}

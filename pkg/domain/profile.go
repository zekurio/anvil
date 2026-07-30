package domain

type Flow struct {
	Name  FlowName
	Steps []FlowStep
}

type FlowStep struct {
	Name string
}

type Profile struct {
	Name        ProfileName
	Container   string
	Video       VideoProfile
	Audio       AudioProfile
	Subtitles   SubtitleProfile
	Validation  ValidationPolicy
	Metadata    MetadataPolicy
	Attachments AttachmentPolicy
	Chapters    ChapterPolicy
}

type VideoProfile struct {
	Codec              string
	Accelerator        string
	Preset             string
	BitDepth           int
	CRFMin             int
	CRFMax             int
	TargetVMAF         float64
	MinSavingsPercent  float64
	ForceEncodeOnNoFit bool
	SkipEncode         bool
	FFmpegArgs         []string
	ABAV1Args          []string
	Overrides          map[string]VideoOverride
	DolbyVision        DolbyVisionPolicy
}

// VideoOverrideDolbyVision is the reserved Overrides key applied when the
// Dolby Vision encoder is selected for a job. Every other key matches the
// canonical source video codec family (hevc, h264, av1, ...).
const VideoOverrideDolbyVision = "dolby_vision"

// VideoOverride adjusts the base video settings when its condition matches.
// Nil fields inherit the base value; set fields replace it, even when zero.
// FFmpegArgs and ABAV1Args append to the base args instead of replacing.
type VideoOverride struct {
	Codec              *string
	Accelerator        *string
	Preset             *string
	BitDepth           *int
	CRFMin             *int
	CRFMax             *int
	TargetVMAF         *float64
	MinSavingsPercent  *float64
	ForceEncodeOnNoFit *bool
	SkipEncode         *bool
	FFmpegArgs         []string
	ABAV1Args          []string
}

type DolbyVisionMode string

const (
	DolbyVisionModeAuto    DolbyVisionMode = "auto"
	DolbyVisionModeOff     DolbyVisionMode = "off"
	DolbyVisionModeRequire DolbyVisionMode = "require"
)

// DolbyVisionPolicy gates Dolby Vision handling. Encoder settings for
// Dolby Vision sources live in Overrides[VideoOverrideDolbyVision].
type DolbyVisionPolicy struct {
	Mode            DolbyVisionMode
	RemoveHDR10Plus bool
}

type StreamFallback string

const (
	StreamFallbackKeepAll   StreamFallback = "keep_all"
	StreamFallbackKeepFirst StreamFallback = "keep_first"
	StreamFallbackFailJob   StreamFallback = "fail_job"
)

type AudioProfile struct {
	LanguagesToKeep   []string
	KeepCommentary    bool
	Fallback          StreamFallback
	UnknownAsOriginal bool
}

type SubtitleProfile struct {
	LanguagesToKeep   []string
	KeepForced        bool
	KeepSDH           bool
	KeepCommentary    bool
	Fallback          StreamFallback
	UnknownAsOriginal bool
}

type ValidationPolicy struct {
	DurationToleranceSeconds float64
}

type MetadataMode string

const (
	MetadataModePreserve MetadataMode = "preserve"
	MetadataModeStrip    MetadataMode = "strip"
)

type TrackTitleMode string

const (
	TrackTitleModePreserve    TrackTitleMode = "preserve"
	TrackTitleModeStrip       TrackTitleMode = "strip"
	TrackTitleModeStandardize TrackTitleMode = "standardize"
)

type MetadataPolicy struct {
	Mode        MetadataMode
	TrackTitles TrackTitleMode
}

type AttachmentPolicy struct {
	Mode MetadataMode
}

type ChapterPolicy struct {
	Mode MetadataMode
}

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

type QualityMetric string

const (
	QualityMetricVMAF  QualityMetric = "vmaf"
	QualityMetricXPSNR QualityMetric = "xpsnr"
)

type VideoProfile struct {
	Codec              string
	Accelerator        string
	Preset             string
	BitDepth           int
	CRFMin             int
	CRFMax             int
	Metric             QualityMetric
	Target             float64
	MinSavingsPercent  float64
	ForceEncodeOnNoFit bool
	SkipEncode         bool
	FFmpegArgs         []string
	ABAV1Args          []string
	Overrides          map[string]VideoOverride
}

// VideoOverride adjusts the base video settings when its condition — the
// canonical source video codec family (hevc, h264, av1, ...) — matches.
// Nil fields inherit the base value; set fields replace it, even when zero.
// FFmpegArgs and ABAV1Args append to the base args instead of replacing.
type VideoOverride struct {
	Codec              *string
	Accelerator        *string
	Preset             *string
	BitDepth           *int
	CRFMin             *int
	CRFMax             *int
	Metric             *QualityMetric
	Target             *float64
	MinSavingsPercent  *float64
	ForceEncodeOnNoFit *bool
	SkipEncode         *bool
	FFmpegArgs         []string
	ABAV1Args          []string
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

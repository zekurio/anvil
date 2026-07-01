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
	FFmpegArgs         []string
	ABAV1Args          []string
	DolbyVision        DolbyVisionProfile
}

type DolbyVisionMode string

const (
	DolbyVisionModeAuto    DolbyVisionMode = "auto"
	DolbyVisionModeOff     DolbyVisionMode = "off"
	DolbyVisionModeRequire DolbyVisionMode = "require"
)

type DolbyVisionProfile struct {
	Mode            DolbyVisionMode
	Codec           string
	Accelerator     string
	Preset          string
	BitDepth        int
	FFmpegArgs      []string
	ABAV1Args       []string
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

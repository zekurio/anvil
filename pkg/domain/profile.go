package domain

import "time"

type Profile struct {
	Name        ProfileName
	Container   string
	Video       VideoProfile
	Crop        CropPolicy
	Audio       AudioProfile
	Subtitles   SubtitleProfile
	Validation  ValidationPolicy
	Metadata    MetadataPolicy
	Attachments AttachmentPolicy
	Chapters    ChapterPolicy
}

type CropPolicy struct {
	// Disabled skips crop detection entirely; the video keeps its full frame.
	Disabled               bool
	SeekOffsets            []time.Duration
	Samples                int
	FrameCount             int
	Limit                  int
	Round                  int
	ResetCount             int
	MinRetainedAreaPercent float64
	MinWidth               int
	MinHeight              int
	RequiredAlignment      int
}

type QualityMetric string

const (
	QualityMetricVMAF  QualityMetric = "vmaf"
	QualityMetricXPSNR QualityMetric = "xpsnr"
)

// VideoQualityMode selects how an encode picks its CRF.
type VideoQualityMode string

const (
	// VideoQualitySearch measures samples and picks the highest CRF that
	// meets the target. The zero value means search.
	VideoQualitySearch VideoQualityMode = "search"
	// VideoQualityFixed encodes at VideoProfile.CRF without searching.
	VideoQualityFixed VideoQualityMode = "fixed"
)

type VideoProfile struct {
	Codec       string
	Accelerator string
	Preset      string
	BitDepth    int
	// QualityMode is search unless set to fixed; CRF is only read in fixed
	// mode, where no samples are measured and no savings floor applies.
	QualityMode        VideoQualityMode
	CRF                int
	CRFMin             int
	CRFMax             int
	Samples            int
	SampleDuration     time.Duration
	Metric             QualityMetric
	Target             float64
	MinSavingsPercent  float64
	ForceEncodeOnNoFit bool
	SkipEncode         bool
	FFmpegArgs         []string
	Overrides          map[string]VideoOverride
}

// VideoOverride adjusts the base video settings when its condition — the
// canonical source video codec family (hevc, h264, av1, ...) — matches.
// FFmpegArgs append to the base arguments.
// Nil fields inherit the base value; set fields replace it, even when zero.
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
}

type StreamFallback string

const (
	StreamFallbackKeepAll   StreamFallback = "keep_all"
	StreamFallbackKeepFirst StreamFallback = "keep_first"
	StreamFallbackFailJob   StreamFallback = "fail_job"
)

// AudioProfile declares which audio tracks survive and how they are written.
// An empty LanguagesToKeep keeps every track. Kept tracks are stream-copied
// unless Encode or Downmix claims them.
type AudioProfile struct {
	LanguagesToKeep   []string
	KeepCommentary    bool
	Fallback          StreamFallback
	UnknownAsOriginal bool
	// Encode re-encodes kept tracks to one codec; nil copies them.
	Encode *AudioEncode
	// Downmix reduces tracks with more channels than a limit; nil keeps
	// every source layout.
	Downmix *AudioDownmix
}

// AudioEncode re-encodes kept audio tracks. A track already in Codec is
// copied unless Downmix also applies to it.
type AudioEncode struct {
	// Codec is the target: "opus", "aac", "ac3", "eac3", or "flac".
	Codec string
	// BitrateKbpsPerChannel sets the bitrate as a per-channel rate, so a 5.1
	// track gets six times the value. 0 uses the encoder default. Ignored
	// for flac.
	BitrateKbpsPerChannel int
	// SourceCodecs limits re-encoding to tracks whose source codec is
	// listed, using ffprobe names (truehd, dts, flac, pcm_s24le, ...). Empty
	// matches every track.
	SourceCodecs []string
}

// AudioDownmix re-encodes tracks that have more than MaxChannels channels
// down to MaxChannels. Tracks at or below the limit are left to Encode or
// copied.
type AudioDownmix struct {
	// MaxChannels is 2 (stereo) or 6 (5.1).
	MaxChannels int
	// Codec and BitrateKbpsPerChannel encode the downmixed track, with the
	// same meaning as in AudioEncode. When Encode also matches the track,
	// Encode's codec and bitrate win.
	Codec                 string
	BitrateKbpsPerChannel int
}

// SubtitleProfile declares which subtitle tracks survive. An empty
// LanguagesToKeep keeps every track. Subtitles are always stream-copied.
type SubtitleProfile struct {
	LanguagesToKeep   []string
	KeepForced        bool
	KeepSDH           bool
	KeepCommentary    bool
	Fallback          StreamFallback
	UnknownAsOriginal bool
	// RemoveAll drops every subtitle track and overrides every other field.
	RemoveAll bool
	// DropBitmap drops image-based tracks (hdmv_pgs_subtitle, dvd_subtitle,
	// dvb_subtitle) before the language rules run.
	DropBitmap bool
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

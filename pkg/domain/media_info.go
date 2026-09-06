package domain

import (
	"strings"
	"time"
)

type MediaStream struct {
	Index          int
	Type           string
	Codec          string
	Width          int
	Height         int
	PixelFormat    string
	BitRate        int64
	Channels       int
	ChannelLayout  string
	ColorRange     string
	ColorSpace     string
	ColorTransfer  string
	ColorPrimaries string
	Language       string
	Title          string
	Tags           map[string]string
	Disposition    map[string]bool
}

// AttachedPic reports whether the stream is an embedded image, such as cover
// art, exposed as a video stream with the attached_pic disposition.
func (s MediaStream) AttachedPic() bool {
	return s.Type == "video" && s.Disposition["attached_pic"]
}

// PrimaryVideoStream returns the first video stream that is not an attached
// picture. Embedded cover art shows up as an extra video stream and must not
// be treated as the main video for encoding, HDR detection, or validation.
func PrimaryVideoStream(streams []MediaStream) (MediaStream, bool) {
	for _, stream := range streams {
		if stream.Type == "video" && !stream.AttachedPic() {
			return stream, true
		}
	}
	return MediaStream{}, false
}

type ProbeResult struct {
	Path            string
	FormatName      string
	DurationSeconds float64
	SizeBytes       int64
	Streams         []MediaStream
}

type SearchResult struct {
	CRF                     int
	Metric                  QualityMetric
	VMAF                    float64
	XPSNR                   float64
	SkipVideoEncode         bool
	VideoEncodeSkipReason   string
	ForcedVideoEncodeReason string
	RawOutput               string
	RawCommand              []string
}

type AudioSelection struct {
	OriginalLanguage string
	LanguagesToKeep  []string
	StreamIndexes    []int
	Decision         *StreamSelectionDecision `json:"decision,omitempty"`
}

type SubtitleSelection struct {
	OriginalLanguage string
	LanguagesToKeep  []string
	StreamIndexes    []int
	Decision         *StreamSelectionDecision `json:"decision,omitempty"`
}

// CropSample records the picture bounds seen at one seek offset.
type CropSample struct {
	Offset       time.Duration `json:"offset"`
	Filter       string        `json:"filter,omitempty"`
	Observations int           `json:"observations"`
	Error        string        `json:"error,omitempty"`
}

type CropResult struct {
	Samples             []CropSample
	SelectionReason     string
	CandidateFilter     string
	Filter              string
	SourceWidth         int
	SourceHeight        int
	OutputWidth         int
	OutputHeight        int
	RetainedAreaPercent float64
	RejectionReason     string
	NoOp                bool
	RawOutput           string
	RawCommand          []string
}

type EncodePlan struct {
	InputPath                string
	OutputPath               string
	ProfileName              ProfileName
	VideoCodec               string
	InputVideoCodec          string
	InputWidth               int
	InputHeight              int
	Accelerator              string
	VideoCopy                bool
	VideoCopyReason          string
	Preset                   string
	BitDepth                 int
	PixelFormat              string
	CRF                      int
	CRFMin                   int
	CRFMax                   int
	SearchSamples            int
	Metric                   QualityMetric
	Target                   float64
	MinSavingsPercent        float64
	ForceEncodeOnNoFit       bool
	Threads                  int
	Container                string
	CropFilter               string
	CropPolicy               CropPolicy
	VideoSelectionApplied    bool
	VideoStreamIndex         int
	AudioSelectionApplied    bool
	AudioStreamIndexes       []int
	SubtitleSelectionApplied bool
	SubtitleStreamIndexes    []int
	MetadataMode             MetadataMode
	TrackTitleMode           TrackTitleMode
	TrackTitles              []TrackTitle
	AttachmentMode           MetadataMode
	ChapterMode              MetadataMode
	FFmpegArgs               []string
	ABAV1Args                []string
	HDR                      HDRMetadata
}

type TrackTitle struct {
	Type  string
	Index int
	Title string
}

type ValidationResult struct {
	OK                          bool
	SourceDurationSeconds       float64
	SourceSizeBytes             int64
	OutputDurationSeconds       float64
	OutputSizeBytes             int64
	SizeSavingsBytes            int64
	SizeSavingsPercent          float64
	OutputVideoStreamCount      int
	OutputAudioStreamCount      int
	OutputSubtitleStreamCount   int
	ExpectedVideoCodec          string
	OutputVideoCodec            string
	ExpectedVideoPixelFormat    string
	OutputVideoPixelFormat      string
	SourceAudioStreamCount      int
	ExpectedAudioStreamCount    int
	SourceSubtitleStreamCount   int
	ExpectedSubtitleStreamCount int
	AnvilProcessedMarkerPresent bool
	SourceHDRColorTransfer      string
	OutputHDRColorTransfer      string
	SourceHDRColorPrimaries     string
	OutputHDRColorPrimaries     string
	Errors                      []string
}

type JobMetadata struct {
	OriginalLanguage            string
	CropFilter                  string
	StreamCleanupDisabled       bool
	StreamCleanupDisabledReason string
	HDR                         HDRMetadata
}

type HDRMetadata struct {
	ColorRange     string
	ColorSpace     string
	ColorTransfer  string
	ColorPrimaries string
}

// EffectiveVideoProfile returns the video settings for this job after
// source-dependent overrides are applied: the base profile, then the
// override matching the source video codec family (hevc, h264, av1, ...).
func EffectiveVideoProfile(profile Profile, metadata JobMetadata, sourceVideoCodec string) VideoProfile {
	video := profile.Video
	codec := strings.ToLower(strings.TrimSpace(sourceVideoCodec))
	if codec != "" {
		if override, ok := profile.Video.Overrides[codec]; ok {
			video = applyVideoOverride(video, override)
		}
	}
	return video
}

func applyVideoOverride(video VideoProfile, override VideoOverride) VideoProfile {
	if override.Codec != nil {
		video.Codec = *override.Codec
	}
	if override.Accelerator != nil {
		video.Accelerator = *override.Accelerator
	}
	if override.Preset != nil {
		video.Preset = *override.Preset
	}
	if override.BitDepth != nil {
		video.BitDepth = *override.BitDepth
	}
	if override.CRFMin != nil {
		video.CRFMin = *override.CRFMin
	}
	if override.CRFMax != nil {
		video.CRFMax = *override.CRFMax
	}
	if override.Metric != nil {
		video.Metric = *override.Metric
	}
	if override.Target != nil {
		video.Target = *override.Target
	}
	if override.MinSavingsPercent != nil {
		video.MinSavingsPercent = *override.MinSavingsPercent
	}
	if override.ForceEncodeOnNoFit != nil {
		video.ForceEncodeOnNoFit = *override.ForceEncodeOnNoFit
	}
	if override.SkipEncode != nil {
		video.SkipEncode = *override.SkipEncode
	}
	video.FFmpegArgs = append(append([]string(nil), video.FFmpegArgs...), override.FFmpegArgs...)
	video.ABAV1Args = append(append([]string(nil), video.ABAV1Args...), override.ABAV1Args...)
	return video
}

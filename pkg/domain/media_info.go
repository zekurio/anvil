package domain

import (
	"strconv"
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
	// MasteringDisplay and ContentLightLevel report HDR10 static metadata
	// (SMPTE ST 2086 mastering display, MaxCLL/MaxFALL) declared by the
	// container or found on the first decoded frames.
	MasteringDisplay  bool
	ContentLightLevel bool
	// DolbyVision is nil when neither the container nor the first decoded
	// frames carry Dolby Vision signalling.
	DolbyVision *DolbyVision
	Language    string
	Title       string
	Tags        map[string]string
	Disposition map[string]bool
}

// DolbyVision is a stream's Dolby Vision signalling. Profile, Level, and
// BLCompatibilityID come from the container's configuration record. A stream
// with an RPU but no record gets them guessed from the RPU header and the
// stream's color tags.
type DolbyVision struct {
	Profile int
	Level   int
	// BLCompatibilityID is dv_bl_signal_compatibility_id: 1 HDR10, 2 SDR,
	// 4 HLG, 6 Blu-ray HDR10, and 0 for a base layer that only shows correct
	// colors with the RPU applied (profile 5, 10.0).
	BLCompatibilityID int
	EnhancementLayer  bool
	// ConfigurationRecord reports a container-level record (dvcC/dvvC or a
	// Matroska block addition mapping).
	ConfigurationRecord bool
	// RPU reports Dolby Vision metadata on the first decoded frames.
	RPU bool
}

// CompatibleBaseLayer reports whether the base layer is a correct HDR10,
// SDR, or HLG picture on its own, which is what remains when a re-encode
// drops the RPU.
func (d DolbyVision) CompatibleBaseLayer() bool {
	switch d.BLCompatibilityID {
	case 1, 2, 4, 6:
		return true
	default:
		return false
	}
}

// String names the profile the way players and media servers do: 5, 7,
// 8.1, 10.1, ...
func (d DolbyVision) String() string {
	name := strconv.Itoa(d.Profile)
	if d.Profile == 8 || d.Profile == 10 {
		name += "." + strconv.Itoa(d.BLCompatibilityID)
	}
	return name
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

// SearchCandidate records the measured mean quality and sampled video size ratio.
type SearchCandidate struct {
	CRF            int
	Score          float64
	EncodedPercent float64
}

type SearchResult struct {
	Candidates              []SearchCandidate
	CRF                     int
	Metric                  QualityMetric
	VMAF                    float64
	XPSNR                   float64
	SkipVideoEncode         bool
	VideoEncodeSkipReason   string
	ForcedVideoEncodeReason string
	RawOutput               string
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
	InputPixelFormat         string
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
	SearchSampleDuration     time.Duration
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
	Color                    VideoColor
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
	SourceDolbyVision           string
	OutputDolbyVision           string
	Errors                      []string
}

type JobMetadata struct {
	OriginalLanguage            string
	CropFilter                  string
	StreamCleanupDisabled       bool
	StreamCleanupDisabledReason string
}

// VideoColor is the color signalling an encode tags its video with, using
// FFmpeg's names (tv, bt2020nc, smpte2084, bt2020, ...).
type VideoColor struct {
	Range     string
	Space     string
	Transfer  string
	Primaries string
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
	return video
}

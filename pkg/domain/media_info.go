package domain

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
	DolbyVision    *DolbyVisionMetadata
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

type DolbyVisionMetadata struct {
	Profile                  int
	Level                    int
	RPUPresent               bool
	ELPresent                bool
	BLPresent                bool
	BLSignalCompatibilityID  int
	ConfigurationRecordFound bool
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
	VMAF                    float64
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

type CropResult struct {
	Filter     string
	RawOutput  string
	RawCommand []string
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
	VideoSource              string
	VideoCopy                bool
	VideoCopyReason          string
	Preset                   string
	BitDepth                 int
	PixelFormat              string
	CRF                      int
	CRFMin                   int
	CRFMax                   int
	TargetVMAF               float64
	MinSavingsPercent        float64
	ForceEncodeOnNoFit       bool
	Threads                  int
	Container                string
	CropFilter               string
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
	AnvilTags                map[string]string
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
	AnvilMarkerCompatible       bool
	AnvilProcessedMarkerPresent bool
	SourceHDRColorTransfer      string
	OutputHDRColorTransfer      string
	SourceHDRColorPrimaries     string
	OutputHDRColorPrimaries     string
	SourceDolbyVisionPresent    bool
	OutputDolbyVisionPresent    bool
	Errors                      []string
}

type JobMetadata struct {
	OriginalLanguage            string
	CropFilter                  string
	StreamCleanupDisabled       bool
	StreamCleanupDisabledReason string
	VideoAlreadyEncoded         bool
	AnvilTags                   map[string]string
	HDR                         HDRMetadata
}

type HDRMetadata struct {
	ColorRange                 string
	ColorSpace                 string
	ColorTransfer              string
	ColorPrimaries             string
	DolbyVision                *DolbyVisionMetadata
	DolbyVisionToolAvailable   bool
	DolbyVisionEncoderSelected bool
	DolbyVisionReason          string
}

// EffectiveVideoProfile returns the video settings for this job after
// source-dependent overrides, such as Dolby Vision handling, are applied.
func EffectiveVideoProfile(profile Profile, metadata JobMetadata) VideoProfile {
	video := profile.Video
	if !metadata.HDR.DolbyVisionEncoderSelected {
		return video
	}

	override := profile.Video.DolbyVision
	if override.Codec != "" {
		video.Codec = override.Codec
	}
	if override.Accelerator != "" {
		video.Accelerator = override.Accelerator
	}
	if override.Preset != "" {
		video.Preset = override.Preset
	}
	if override.BitDepth != 0 {
		video.BitDepth = override.BitDepth
	}
	video.FFmpegArgs = append(append([]string(nil), video.FFmpegArgs...), override.FFmpegArgs...)
	video.ABAV1Args = append(append([]string(nil), video.ABAV1Args...), override.ABAV1Args...)
	return video
}

func EffectiveVideoSource(metadata JobMetadata) string {
	if metadata.HDR.DolbyVisionEncoderSelected {
		return VideoSourceDolbyVision
	}
	return VideoSourceProfile
}

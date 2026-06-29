package domain

import "time"

const VideoSourceProfile = "profile"
const VideoSourceDolbyVision = "dolby_vision"

type LibraryName string
type FlowName string
type ProfileName string

type MediaSourceID int64
type MediaAssetID int64
type JobID int64
type AttemptID int64
type AttemptEventID int64

type LibraryKind string

const (
	LibraryKindMedia    LibraryKind = "media"
	LibraryKindDownload LibraryKind = "download"
)

type Library struct {
	Name             LibraryName
	Kind             LibraryKind
	Path             string
	Priority         int
	FlowName         FlowName
	ProfileName      ProfileName
	IncludeGlobs     []string
	ExcludeGlobs     []string
	ConcurrencyLimit int
	Metadata         MetadataProviderPolicy
	Media            MediaLibraryPolicy
	Download         DownloadLibraryPolicy
}

type MetadataProviderPolicy struct {
	Provider   MetadataProviderKind
	BaseURL    string
	APIKey     string
	APIKeyFile string
}

type MetadataProviderKind string

const (
	MetadataProviderNone   MetadataProviderKind = ""
	MetadataProviderRadarr MetadataProviderKind = "radarr"
	MetadataProviderSonarr MetadataProviderKind = "sonarr"
)

type MediaLibraryPolicy struct {
	ReplacementMode ReplacementMode
}

type ReplacementMode string

const (
	ReplacementModeReplace ReplacementMode = "replace"
	ReplacementModeCopy    ReplacementMode = "copy"
)

type DownloadLibraryPolicy struct {
	HandoffPath          string
	StableFor            time.Duration
	PackageMode          DownloadPackageMode
	HandoffMode          HandoffMode
	PreserveRelativePath bool
	CleanupSourceMedia   bool
	PruneEmptyDirs       bool
	IgnorableGlobs       []string
}

type DownloadPackageMode string

const (
	DownloadPackageModeAuto      DownloadPackageMode = "auto"
	DownloadPackageModeDirectory DownloadPackageMode = "directory"
	DownloadPackageModeFile      DownloadPackageMode = "file"
)

type HandoffMode string

const (
	HandoffModeMove HandoffMode = "move"
	HandoffModeCopy HandoffMode = "copy"
)

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

type StreamPolicyMode string

const (
	StreamPolicyPreserve StreamPolicyMode = "preserve"
	StreamPolicyPrefer   StreamPolicyMode = "prefer"
	StreamPolicyCleanup  StreamPolicyMode = "cleanup"
)

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
	Mode               StreamPolicyMode
	PreferredLanguages []string
	KeepForced         bool
	KeepSDH            bool
	KeepCommentary     bool
	KeepExternal       bool
	MaxTracks          int
	Fallback           StreamFallback
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

type SourceKind string

const (
	SourceKindFile    SourceKind = "file"
	SourceKindPackage SourceKind = "package"
)

type MediaSourceStatus string

const (
	MediaSourceActive  MediaSourceStatus = "active"
	MediaSourceMissing MediaSourceStatus = "missing"
	MediaSourceIgnored MediaSourceStatus = "ignored"
)

type MediaSource struct {
	ID           MediaSourceID
	LibraryName  LibraryName
	Kind         SourceKind
	RelativePath string
	Status       MediaSourceStatus
	Fingerprint  FileFingerprint
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
}

type MediaAssetRole string

const (
	MediaAssetRolePrimaryVideo MediaAssetRole = "primary_video"
	MediaAssetRoleVideo        MediaAssetRole = "video"
	MediaAssetRoleSample       MediaAssetRole = "sample"
	MediaAssetRoleSubtitle     MediaAssetRole = "subtitle"
	MediaAssetRoleMetadata     MediaAssetRole = "metadata"
	MediaAssetRoleExtra        MediaAssetRole = "extra"
	MediaAssetRoleUnknown      MediaAssetRole = "unknown"
)

type MediaAssetStatus string

const (
	MediaAssetActive    MediaAssetStatus = "active"
	MediaAssetProcessed MediaAssetStatus = "processed"
	MediaAssetMissing   MediaAssetStatus = "missing"
	MediaAssetIgnored   MediaAssetStatus = "ignored"
)

type MediaAsset struct {
	ID           MediaAssetID
	SourceID     MediaSourceID
	RelativePath string
	Role         MediaAssetRole
	Status       MediaAssetStatus
	Fingerprint  FileFingerprint
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
}

type FileFingerprint struct {
	SizeBytes     int64
	ModTime       time.Time
	HashAlgorithm string
	HashValue     string
}

type JobState string

const (
	JobStatePending    JobState = "pending"
	JobStateLeased     JobState = "leased"
	JobStateRunning    JobState = "running"
	JobStateValidating JobState = "validating"
	JobStateReplacing  JobState = "replacing"
	JobStateComplete   JobState = "complete"
	JobStateFailed     JobState = "failed"
	JobStateRetrying   JobState = "retrying"
	JobStateSkipped    JobState = "skipped"
)

type Job struct {
	ID              JobID
	SourceID        MediaSourceID
	AssetID         MediaAssetID
	LibraryName     LibraryName
	Priority        int
	State           JobState
	LeaseOwner      string
	LeaseDeadline   *time.Time
	HeartbeatAt     *time.Time
	AttemptCount    int
	LastError       string
	InputSizeBytes  int64
	OutputSizeBytes int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
}

func (s JobState) Terminal() bool {
	switch s {
	case JobStateComplete, JobStateFailed, JobStateSkipped:
		return true
	default:
		return false
	}
}

func CanTransitionJob(from, to JobState) bool {
	if from == to {
		return true
	}

	switch from {
	case JobStatePending:
		return to == JobStateLeased || to == JobStateSkipped
	case JobStateLeased:
		return to == JobStateRunning || to == JobStateFailed || to == JobStateRetrying
	case JobStateRunning:
		return to == JobStateValidating || to == JobStateFailed || to == JobStateRetrying
	case JobStateValidating:
		return to == JobStateReplacing || to == JobStateComplete || to == JobStateFailed || to == JobStateRetrying
	case JobStateReplacing:
		return to == JobStateComplete || to == JobStateFailed || to == JobStateRetrying
	case JobStateFailed:
		return to == JobStateRetrying
	case JobStateRetrying:
		return to == JobStatePending || to == JobStateFailed
	default:
		return false
	}
}

type AttemptState string

const (
	AttemptStateRunning   AttemptState = "running"
	AttemptStateSucceeded AttemptState = "succeeded"
	AttemptStateFailed    AttemptState = "failed"
	AttemptStateCanceled  AttemptState = "canceled"
)

type Attempt struct {
	ID              AttemptID
	JobID           JobID
	Number          int
	WorkerID        string
	State           AttemptState
	ResolvedLibrary []byte
	ResolvedFlow    []byte
	ResolvedProfile []byte
	StartedAt       time.Time
	FinishedAt      *time.Time
	Error           string
}

type ExecutionPlan struct {
	JobID           JobID
	AttemptID       AttemptID
	SourceID        MediaSourceID
	AssetID         MediaAssetID
	InputPath       string
	OutputPath      string
	ResolvedLibrary Library
	ResolvedFlow    Flow
	ResolvedProfile Profile
}

type ResourceAllocation struct {
	WorkerID string
	Threads  int
}

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
}

type CropResult struct {
	Filter     string
	RawOutput  string
	RawCommand []string
}

type EncodePlan struct {
	InputPath             string
	OutputPath            string
	ProfileName           ProfileName
	VideoCodec            string
	InputVideoCodec       string
	InputWidth            int
	InputHeight           int
	Accelerator           string
	VideoSource           string
	VideoCopy             bool
	VideoCopyReason       string
	Preset                string
	BitDepth              int
	PixelFormat           string
	CRF                   int
	CRFMin                int
	CRFMax                int
	TargetVMAF            float64
	MinSavingsPercent     float64
	ForceEncodeOnNoFit    bool
	Threads               int
	Container             string
	CropFilter            string
	AudioSelectionApplied bool
	AudioStreamIndexes    []int
	SubtitleMode          StreamPolicyMode
	MetadataMode          MetadataMode
	TrackTitleMode        TrackTitleMode
	TrackTitles           []TrackTitle
	AttachmentMode        MetadataMode
	ChapterMode           MetadataMode
	AnvilTags             map[string]string
	FFmpegArgs            []string
	ABAV1Args             []string
	HDR                   HDRMetadata
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

type AttemptEventType string

const (
	AttemptEventBlockStarted  AttemptEventType = "block_started"
	AttemptEventBlockFinished AttemptEventType = "block_finished"
	AttemptEventBlockFailed   AttemptEventType = "block_failed"
	AttemptEventArtifact      AttemptEventType = "artifact"
)

type AttemptEvent struct {
	ID        AttemptEventID
	AttemptID AttemptID
	Type      AttemptEventType
	Name      string
	Message   string
	Payload   []byte
	CreatedAt time.Time
}

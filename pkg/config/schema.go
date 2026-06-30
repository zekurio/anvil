package config

const (
	DefaultFlowName        = "av1-crf-search"
	DefaultProfileName     = "default-av1"
	DefaultLibraryKind     = "media"
	DefaultReplacementMode = "replace"
	DefaultScanInterval    = "30m"
	DefaultFSDebounce      = "2s"
	DefaultSchedulerTick   = "5s"
	DefaultLeaseDuration   = "30m"
	DefaultShutdownPolicy  = "drain"
	DefaultShutdownTimeout = "0s"
	DefaultStagingCleanup  = "0s"
	DefaultLogLevel        = "info"
	DefaultMaxAttempts     = 3
	DefaultStableFor       = "5m"
	DefaultPackageMode     = "auto"
	DefaultHandoffMode     = "copy"
	DefaultStreamMode      = "preserve"
	DefaultStreamFallback  = "keep_all"
	DefaultMetadataMode    = "preserve"
	DefaultTrackTitleMode  = "strip"
	DefaultDolbyVisionMode = "auto"
	DolbyVisionModeOff     = "off"
	DolbyVisionModeRequire = "require"
	DefaultMinSavingsPct   = 20
)

var DefaultIgnorableGlobs = []string{
	"**/samples/**",
	"**/sample*/**",
	"**/*sample*",
	"**/*.txt",
	"**/*.url",
	"**/*.sfv",
	"**/*.srr",
	"**/*.nzb",
	"**/__MACOSX/**",
	"**/.DS_Store",
	"**/.nfs*",
}

// Config is the top-level Anvil daemon configuration.
type Config struct {
	Daemon    DaemonConfig             `toml:"daemon"`
	Arrs      map[string]ArrConfig     `toml:"arrs"`
	Flows     map[string]FlowConfig    `toml:"flows"`
	Profiles  map[string]ProfileConfig `toml:"profiles"`
	Libraries map[string]LibraryConfig `toml:"libraries"`
}

// DaemonConfig contains process-wide runtime settings.
type DaemonConfig struct {
	TempDir           string `toml:"temp_dir"`
	StorePath         string `toml:"store_path"`
	WorkerCount       int    `toml:"worker_count"`
	TotalThreads      int    `toml:"total_threads"`
	MaxAttempts       int    `toml:"max_attempts"`
	ScanInterval      string `toml:"scan_interval"`
	FSDebounce        string `toml:"filesystem_event_debounce"`
	SchedulerInterval string `toml:"scheduler_interval"`
	LeaseDuration     string `toml:"lease_duration"`
	ShutdownPolicy    string `toml:"shutdown_policy"`
	ShutdownTimeout   string `toml:"shutdown_timeout"`
	StagingCleanupAge string `toml:"staging_cleanup_age"`
	LogLevel          string `toml:"log_level"`
}

// FlowConfig names an orchestration flow. The steps are declarative for now.
type FlowConfig struct {
	Name  string   `toml:"-"`
	Steps []string `toml:"steps"`
}

// ProfileConfig groups encode settings that libraries can reference.
type ProfileConfig struct {
	Name        string           `toml:"-"`
	Container   string           `toml:"container"`
	Video       VideoConfig      `toml:"video"`
	Audio       AudioConfig      `toml:"audio"`
	Subtitles   SubtitleConfig   `toml:"subtitles"`
	Validation  ValidationConfig `toml:"validation"`
	Metadata    MetadataConfig   `toml:"metadata"`
	Attachments MetadataConfig   `toml:"attachments"`
	Chapters    MetadataConfig   `toml:"chapters"`
}

// VideoConfig contains the initial video settings shape for AV1 search work.
type VideoConfig struct {
	Codec              string            `toml:"codec"`
	Accelerator        string            `toml:"accelerator"`
	Preset             string            `toml:"preset"`
	BitDepth           int               `toml:"bit_depth"`
	CRFMin             int               `toml:"crf_min"`
	CRFMax             int               `toml:"crf_max"`
	TargetVMAF         float64           `toml:"target_vmaf"`
	MinSavingsPercent  float64           `toml:"min_savings_percent"`
	ForceEncodeOnNoFit bool              `toml:"force_encode_on_no_fit"`
	FFmpegArgs         []string          `toml:"ffmpeg_args"`
	ABAV1Args          []string          `toml:"ab_av1_args"`
	DolbyVision        DolbyVisionConfig `toml:"dolby_vision"`
}

// DolbyVisionConfig overrides normal video settings for Dolby Vision sources.
type DolbyVisionConfig struct {
	Mode            string   `toml:"mode"`
	Codec           string   `toml:"codec"`
	Accelerator     string   `toml:"accelerator"`
	Preset          string   `toml:"preset"`
	BitDepth        int      `toml:"bit_depth"`
	FFmpegArgs      []string `toml:"ffmpeg_args"`
	ABAV1Args       []string `toml:"ab_av1_args"`
	RemoveHDR10Plus bool     `toml:"remove_hdr10plus"`
}

// AudioConfig declares track retention intent. It is conservative by default.
type AudioConfig struct {
	LanguagesToKeep   []string `toml:"languages_to_keep"`
	KeepCommentary    bool     `toml:"keep_commentary"`
	Fallback          string   `toml:"fallback"`
	UnknownAsOriginal bool     `toml:"unknown_as_original"`
}

// SubtitleConfig declares subtitle retention intent.
type SubtitleConfig struct {
	Mode               string   `toml:"mode"`
	PreferredLanguages []string `toml:"preferred_languages"`
	KeepForced         bool     `toml:"keep_forced"`
	KeepSDH            bool     `toml:"keep_sdh"`
	KeepCommentary     bool     `toml:"keep_commentary"`
	KeepExternal       bool     `toml:"keep_external"`
	MaxTracks          int      `toml:"max_tracks"`
	Fallback           string   `toml:"fallback"`
}

// ValidationConfig declares post-encode safety gates.
type ValidationConfig struct {
	DurationToleranceSeconds float64 `toml:"duration_tolerance_seconds"`
}

// MetadataConfig declares output metadata policy.
type MetadataConfig struct {
	Mode        string `toml:"mode"`
	TrackTitles string `toml:"track_titles"`
}

// LibraryConfig describes a user-defined media library.
type LibraryConfig struct {
	Name             string                `toml:"-"`
	Kind             string                `toml:"kind"`
	Path             string                `toml:"path"`
	Flow             string                `toml:"flow"`
	Profile          string                `toml:"profile"`
	ScanInterval     string                `toml:"scan_interval"`
	Priority         int                   `toml:"priority"`
	Include          []string              `toml:"include"`
	Exclude          []string              `toml:"exclude"`
	IgnoreRegex      []string              `toml:"ignore_regex"`
	ConcurrencyLimit int                   `toml:"concurrency_limit"`
	Arr              string                `toml:"arr"`
	Media            MediaLibraryConfig    `toml:"media"`
	Download         DownloadLibraryConfig `toml:"download"`
}

// ArrConfig controls external metadata lookup through an Arr instance.
type ArrConfig struct {
	Name       string `toml:"-"`
	Type       string `toml:"type"`
	BaseURL    string `toml:"base_url"`
	APIKey     string `toml:"api_key"`
	APIKeyFile string `toml:"api_key_file"`
}

// MediaLibraryConfig controls in-place media-library completion behavior.
type MediaLibraryConfig struct {
	ReplacementMode string `toml:"replacement_mode"`
}

// DownloadLibraryConfig controls intake and handoff behavior for completed downloads.
type DownloadLibraryConfig struct {
	HandoffPath          string   `toml:"handoff_path"`
	StableFor            string   `toml:"stable_for"`
	PackageMode          string   `toml:"package_mode"`
	HandoffMode          string   `toml:"handoff_mode"`
	PreserveRelativePath bool     `toml:"preserve_relative_path"`
	CleanupSourceMedia   bool     `toml:"cleanup_source_media"`
	PruneEmptyDirs       bool     `toml:"prune_empty_dirs"`
	IgnorableGlobs       []string `toml:"ignorable_globs"`
}

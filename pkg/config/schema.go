package config

import "time"

const (
	DefaultProfileName     = "default-av1"
	DefaultLibraryKind     = "media"
	DefaultReplacementMode = "replace"
	DefaultShutdownPolicy  = "drain"
	DefaultLogLevel        = "info"
	DefaultPackageMode     = "auto"
	DefaultHandoffMode     = "copy"
	DefaultStreamFallback  = "keep_all"
	DefaultMetadataMode    = "preserve"
	DefaultTrackTitleMode  = "strip"

	DefaultMaxAttempts            = 3
	DefaultMinSavingsPct          = 20
	DefaultCRFMin                 = 18
	DefaultCRFMax                 = 40
	DefaultTargetVMAF             = 95
	DefaultCropFrameCount         = 300
	DefaultCropDetectLimit        = 64
	DefaultCropDetectRound        = 16
	DefaultCropDetectResetCount   = 0
	DefaultCropMinRetainedAreaPct = 70
	DefaultCropMinWidth           = 128
	DefaultCropMinHeight          = 128
	DefaultCropRequiredAlignment  = 2
)

const (
	DefaultScanInterval    = 30 * time.Minute
	DefaultFSDebounce      = 2 * time.Second
	DefaultSchedulerTick   = 5 * time.Second
	DefaultLeaseDuration   = 30 * time.Minute
	DefaultShutdownTimeout = 0
	DefaultStagingCleanup  = 0
	DefaultStableFor       = 5 * time.Minute
)

// DefaultCropSeekOffsets sample the beginning and several points through
// typical episode- and movie-length content.
var DefaultCropSeekOffsets = []Duration{
	{Duration: 0},
	{Duration: 2 * time.Minute},
	{Duration: 5 * time.Minute},
	{Duration: 12 * time.Minute},
	{Duration: 20 * time.Minute},
	{Duration: 30 * time.Minute},
}

// DefaultIgnorableGlobs are excluded from download-package discovery and stability handling.
// External subtitle sidecars are intentionally preserved by default.
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

// Config is the resolved top-level daemon configuration. Load fills defaults
// and validates it before use.
type Config struct {
	Daemon    DaemonConfig             `toml:"daemon"`
	Arrs      map[string]ArrConfig     `toml:"arrs"`
	Profiles  map[string]ProfileConfig `toml:"profiles"`
	Libraries map[string]LibraryConfig `toml:"libraries"`
}

// DaemonConfig contains process-wide runtime settings.
type DaemonConfig struct {
	// Scratch directory for search samples and process logs. Encode artifacts
	// are written next to their publish destination as
	// <name>.job-<id>.anvil-part. store_path and control_socket default to
	// locations inside this directory.
	TempDir string `toml:"temp_dir"`
	// SQLite state path. Defaults to anvil.db inside temp_dir; any non-empty path.
	StorePath string `toml:"store_path"`
	// Daemon Unix socket. Defaults to anvild.sock inside temp_dir; must be absolute.
	ControlSocket string `toml:"control_socket"`
	// Simultaneous jobs. Defaults to the CPU count; integer >= 1.
	WorkerCount int `toml:"worker_count"`
	// Shared encode-thread budget. Defaults to the CPU count; integer >= 0.
	TotalThreads int `toml:"total_threads"`
	// Per-job thread cap. Zero shares the budget across worker slots; integer >= 0.
	MaxThreadsPerJob int `toml:"max_threads_per_job"`
	// Retries before a job stays failed; integer >= 1.
	MaxAttempts int `toml:"max_attempts"`
	// Full-library scan cadence; duration > 0.
	ScanInterval Duration `toml:"scan_interval"`
	// Coalesce filesystem events before scanning; duration >= 0.
	FSDebounce Duration `toml:"filesystem_event_debounce"`
	// Queue planning cadence; duration > 0.
	SchedulerInterval Duration `toml:"scheduler_interval"`
	// Time before an abandoned job lease is recoverable; duration > 0.
	LeaseDuration Duration `toml:"lease_duration"`
	// Let active jobs finish or cancel them; "drain" or "cancel".
	ShutdownPolicy string `toml:"shutdown_policy"`
	// Force cancellation after this graceful wait; "0s" waits indefinitely; duration >= 0.
	ShutdownTimeout Duration `toml:"shutdown_timeout"`
	// Remove abandoned staging older than this; "0s" disables cleanup; duration >= 0.
	StagingCleanupAge Duration `toml:"staging_cleanup_age"`
	// Keep logs useful without excessive detail; "debug", "info", "warn", or "error".
	LogLevel string `toml:"log_level"`
}

// ProfileConfig groups encode settings that libraries can reference. Custom
// profiles need only the values that differ from the defaults.
type ProfileConfig struct {
	Name string `toml:"-"`
	// Output container; only "mkv".
	Container   string           `toml:"container"`
	Video       VideoConfig      `toml:"video"`
	Crop        CropConfig       `toml:"crop"`
	Audio       AudioConfig      `toml:"audio"`
	Subtitles   SubtitleConfig   `toml:"subtitles"`
	Validation  ValidationConfig `toml:"validation"`
	Metadata    MetadataConfig   `toml:"metadata"`
	Attachments ModeConfig       `toml:"attachments"`
	Chapters    ModeConfig       `toml:"chapters"`
}

// CropConfig controls crop sampling and the safety policy applied to candidates.
type CropConfig struct {
	// Sample crop candidates at these input offsets; an empty list uses the
	// default; non-negative Go duration strings.
	SeekOffsets []Duration `toml:"seek_offsets"`
	// Frames analyzed at each seek offset; integer >= 1.
	FrameCount int `toml:"frame_count"`
	// ffmpeg cropdetect black threshold; integer from 1 through 255.
	Limit int `toml:"limit"`
	// Round crop width and height to this multiple during detection; integer >= 1.
	Round int `toml:"round"`
	// Reset cropdetect's largest-area history after this many frames; 0
	// disables reset; integer >= 0.
	ResetCount int `toml:"reset_count"`
	// Reject candidates retaining less source area and continue without
	// cropping; finite number > 0 through 100.
	MinRetainedAreaPct float64 `toml:"min_retained_area_percent"`
	// Reject a crop narrower than this encoder safety floor; integer >= 1.
	MinWidth int `toml:"min_width"`
	// Reject a crop shorter than this encoder safety floor; integer >= 1.
	MinHeight int `toml:"min_height"`
	// Require crop dimensions and offsets to be divisible by this encoder
	// alignment; integer >= 1.
	RequiredAlignment int `toml:"required_alignment"`
}

// VideoConfig contains the initial video settings shape for AV1 search work.
type VideoConfig struct {
	// Output video codec; "av1", "hevc"/"h265", or "h264"/"avc".
	Codec string `toml:"codec"`
	// Encoder backend; "software", "qsv", "vaapi", or "amf".
	Accelerator string `toml:"accelerator"`
	// Trade speed for compression to match the selected encoder; any
	// encoder-supported string.
	Preset string `toml:"preset"`
	// Preserve more tonal detail when the encoder supports it; 8 or 10.
	BitDepth int `toml:"bit_depth"`
	// Lowest search CRF; integer no greater than crf_max.
	CRFMin int `toml:"crf_min"`
	// Highest search CRF; integer no lower than crf_min.
	CRFMax int `toml:"crf_max"`
	// Fixed sample count for each CRF candidate; 0 uses ab-av1's
	// duration-based count; integer >= 0.
	Samples int `toml:"samples"`
	// Quality metric passed to ab-av1; "vmaf" or "xpsnr".
	Metric string `toml:"metric"`
	// Minimum score for metric; number from 0 through 100. Required when
	// metric is "xpsnr" (typical XPSNR target: 35-50).
	Target float64 `toml:"target"`
	// Skip a result that saves too little space; number from 0 through 100.
	MinSavingsPercent float64 `toml:"min_savings_percent"`
	// Encode at the lowest observed CRF instead of preserving video when
	// search finds no fit.
	ForceEncodeOnNoFit bool `toml:"force_encode_on_no_fit"`
	// Copy video instead of searching and encoding when policy requires it.
	SkipEncode bool `toml:"skip_encode"`
	// Extra ffmpeg arguments appended to Anvil's command.
	FFmpegArgs []string `toml:"ffmpeg_args"`
	// Extra ab-av1 arguments appended to the search command.
	ABAV1Args []string `toml:"ab_av1_args"`
	// Per-source-codec adjustments; see VideoOverrideConfig.
	Overrides map[string]VideoOverrideConfig `toml:"overrides"`
}

// VideoOverrideConfig adjusts video settings for canonical source codec
// family keys (hevc, h264, av1, ...). Absent fields inherit base video
// settings; ffmpeg_args and ab_av1_args append to the base args.
type VideoOverrideConfig struct {
	// Override output codec for matching sources; omit to inherit.
	Codec *string `toml:"codec"`
	// Override encoder backend for matching sources; omit to inherit.
	Accelerator *string `toml:"accelerator"`
	// Override encoder preset for matching sources; omit to inherit.
	Preset *string `toml:"preset"`
	// Override output bit depth for matching sources; omit to inherit.
	BitDepth *int `toml:"bit_depth"`
	// Override lowest CRF for matching sources; omit to inherit.
	CRFMin *int `toml:"crf_min"`
	// Override highest CRF for matching sources; omit to inherit.
	CRFMax *int `toml:"crf_max"`
	// Override search metric for matching sources; omit to inherit. Changing
	// the metric requires setting target too.
	Metric *string `toml:"metric"`
	// Override the metric target for matching sources; omit to inherit.
	Target *float64 `toml:"target"`
	// Override the savings floor for matching sources; omit to inherit.
	MinSavingsPercent *float64 `toml:"min_savings_percent"`
	// Override no-fit behavior for matching sources; omit to inherit.
	ForceEncodeOnNoFit *bool `toml:"force_encode_on_no_fit"`
	// Copy matching video instead of encoding only when explicitly needed;
	// omit to inherit.
	SkipEncode *bool `toml:"skip_encode"`
	// Append source-specific ffmpeg arguments.
	FFmpegArgs []string `toml:"ffmpeg_args"`
	// Append source-specific ab-av1 arguments.
	ABAV1Args []string `toml:"ab_av1_args"`
}

// AudioConfig declares track retention intent. It is conservative by default.
type AudioConfig struct {
	// Retain original language plus chosen tags; language tags and "orig".
	LanguagesToKeep []string `toml:"languages_to_keep"`
	// Keep commentary audio tracks.
	KeepCommentary bool `toml:"keep_commentary"`
	// Keep something when no requested audio matches; "keep_all",
	// "keep_first", or "fail_job".
	Fallback string `toml:"fallback"`
	// Treat unlabeled streams as original when metadata supplies one.
	UnknownAsOriginal bool `toml:"unknown_as_original"`
}

// SubtitleConfig declares subtitle retention intent.
type SubtitleConfig struct {
	// Retain original language plus chosen tags; language tags and "orig".
	LanguagesToKeep []string `toml:"languages_to_keep"`
	// Retain forced subtitles even when filtering.
	KeepForced bool `toml:"keep_forced"`
	// Retain SDH subtitles for accessibility.
	KeepSDH bool `toml:"keep_sdh"`
	// Keep commentary subtitle tracks.
	KeepCommentary bool `toml:"keep_commentary"`
	// Keep something when no requested subtitle matches; "keep_all",
	// "keep_first", or "fail_job".
	Fallback string `toml:"fallback"`
	// Treat unlabeled subtitles as original when metadata supplies one.
	UnknownAsOriginal bool `toml:"unknown_as_original"`
}

// ValidationConfig declares post-encode safety gates.
type ValidationConfig struct {
	// Flag duration drift beyond this tolerance without failing the job;
	// number >= 0.
	DurationToleranceSeconds float64 `toml:"duration_tolerance_seconds"`
}

// MetadataConfig declares output metadata policy.
type MetadataConfig struct {
	// Preserve or strip container metadata; "preserve" or "strip".
	Mode string `toml:"mode"`
	// Preserve, clear, or standardize stream titles; "preserve", "strip", or
	// "standardize".
	TrackTitles string `toml:"track_titles"`
}

// ModeConfig declares a preserve-or-strip policy for attachments and chapters,
// which have no per-track titles to manage.
type ModeConfig struct {
	// Preserve or strip; "preserve" or "strip".
	Mode string `toml:"mode"`
}

// LibraryConfig describes a user-defined media library.
type LibraryConfig struct {
	Name string `toml:"-"`
	// Replace media in place or process downloader output; "media" or "download".
	Kind string `toml:"kind"`
	// Root scanned for source files; non-empty path.
	Path string `toml:"path"`
	// Named encoding policy; existing profile name.
	Profile string `toml:"profile"`
	// Override daemon scan cadence for this library; omit to use
	// daemon.scan_interval; duration > 0 when set.
	ScanInterval Duration `toml:"scan_interval"`
	// Prefer higher-priority libraries when work competes; integer.
	Priority int `toml:"priority"`
	// Limit candidates to matching paths; empty matches all; doublestar glob
	// patterns.
	Include []string `toml:"include"`
	// Skip paths before candidate discovery; doublestar glob patterns.
	Exclude []string `toml:"exclude"`
	// Record matching media as ignored; valid Go regular expressions.
	IgnoreRegex []string `toml:"ignore_regex"`
	// Cap active jobs from this library; 0 means no per-library cap;
	// integer >= 0.
	ConcurrencyLimit int `toml:"concurrency_limit"`
	// Use this Arr instance for metadata; omit to disable lookup; existing
	// arr name.
	Arr      string                `toml:"arr"`
	Media    MediaLibraryConfig    `toml:"media"`
	Download DownloadLibraryConfig `toml:"download"`
}

// ArrConfig controls external metadata lookup through an Arr instance.
type ArrConfig struct {
	Name string `toml:"-"`
	// Metadata provider kind; "radarr" or "sonarr".
	Type string `toml:"type"`
	// Provider URL used for original-language lookup; non-empty URL.
	BaseURL string `toml:"base_url"`
	// Literal credential, redacted by check-config --show. Set this or
	// api_key_file.
	APIKey string `toml:"api_key"`
	// Secret-file credential, preferred over api_key. Set this or api_key.
	APIKeyFile string `toml:"api_key_file"`
}

// MediaLibraryConfig controls in-place media-library completion behavior.
type MediaLibraryConfig struct {
	// Replace source files or publish a copy beside them; "replace" or "copy".
	ReplacementMode string `toml:"replacement_mode"`
}

// DownloadLibraryConfig controls intake and handoff behavior for completed downloads.
type DownloadLibraryConfig struct {
	// Destination watched by the importer; non-empty path for download
	// libraries.
	HandoffPath string `toml:"handoff_path"`
	// Fallback quiet period when no completion event is observed; close-write
	// and moved-in transfers enqueue after debounce; duration >= 0.
	StableFor Duration `toml:"stable_for"`
	// Treat a download as a file or directory; "auto", "directory", or "file".
	PackageMode string `toml:"package_mode"`
	// Copy for safety or move when the downloader no longer needs it; "move"
	// or "copy".
	HandoffMode string `toml:"handoff_mode"`
	// Keep the package-relative directory structure below handoff_path.
	PreserveRelativePath bool `toml:"preserve_relative_path"`
	// Delete source media only after a successful handoff.
	CleanupSourceMedia bool `toml:"cleanup_source_media"`
	// Remove empty release directories only with source cleanup.
	PruneEmptyDirs bool `toml:"prune_empty_dirs"`
	// Excluded from download-package discovery and stability handling. During
	// successful handoff source cleanup, matching paths may be deleted when
	// cleanup_source_media and prune_empty_dirs are enabled. A non-empty list
	// replaces the built-in default; doublestar glob patterns.
	IgnorableGlobs []string `toml:"ignorable_globs"`
}

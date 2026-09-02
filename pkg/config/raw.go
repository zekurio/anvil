package config

// rawConfig mirrors the TOML schema with optional numeric and duration
// fields, so resolution can tell "not set" apart from an explicit zero.
// Strings, booleans, and string slices stay plain: empty and false are never
// values a user needs to distinguish from unset. Load decodes this shape and
// resolve turns it into the Config everything downstream uses. The toml tags
// here are the schema; keep them in sync with the resolved structs.
type rawConfig struct {
	Daemon    rawDaemonConfig             `toml:"daemon"`
	Arrs      map[string]ArrConfig        `toml:"arrs"`
	Profiles  map[string]rawProfileConfig `toml:"profiles"`
	Libraries map[string]rawLibraryConfig `toml:"libraries"`
}

type rawDaemonConfig struct {
	TempDir           string    `toml:"temp_dir"`
	StorePath         string    `toml:"store_path"`
	ControlSocket     string    `toml:"control_socket"`
	WorkerCount       *int      `toml:"worker_count"`
	TotalThreads      *int      `toml:"total_threads"`
	MaxAttempts       *int      `toml:"max_attempts"`
	ScanInterval      *Duration `toml:"scan_interval"`
	FSDebounce        *Duration `toml:"filesystem_event_debounce"`
	SchedulerInterval *Duration `toml:"scheduler_interval"`
	LeaseDuration     *Duration `toml:"lease_duration"`
	ShutdownPolicy    string    `toml:"shutdown_policy"`
	ShutdownTimeout   *Duration `toml:"shutdown_timeout"`
	StagingCleanupAge *Duration `toml:"staging_cleanup_age"`
	LogLevel          string    `toml:"log_level"`
}

type rawProfileConfig struct {
	Container   string           `toml:"container"`
	Video       rawVideoConfig   `toml:"video"`
	Crop        rawCropConfig    `toml:"crop"`
	Audio       AudioConfig      `toml:"audio"`
	Subtitles   SubtitleConfig   `toml:"subtitles"`
	Validation  ValidationConfig `toml:"validation"`
	Metadata    MetadataConfig   `toml:"metadata"`
	Attachments ModeConfig       `toml:"attachments"`
	Chapters    ModeConfig       `toml:"chapters"`
}

type rawVideoConfig struct {
	Codec              string                         `toml:"codec"`
	Accelerator        string                         `toml:"accelerator"`
	Preset             string                         `toml:"preset"`
	BitDepth           *int                           `toml:"bit_depth"`
	CRFMin             *int                           `toml:"crf_min"`
	CRFMax             *int                           `toml:"crf_max"`
	Samples            *int                           `toml:"samples"`
	Metric             string                         `toml:"metric"`
	Target             *float64                       `toml:"target"`
	MinSavingsPercent  *float64                       `toml:"min_savings_percent"`
	ForceEncodeOnNoFit bool                           `toml:"force_encode_on_no_fit"`
	SkipEncode         bool                           `toml:"skip_encode"`
	FFmpegArgs         []string                       `toml:"ffmpeg_args"`
	ABAV1Args          []string                       `toml:"ab_av1_args"`
	Overrides          map[string]VideoOverrideConfig `toml:"overrides"`
}

type rawCropConfig struct {
	SeekOffsets        []Duration `toml:"seek_offsets"`
	FrameCount         *int       `toml:"frame_count"`
	Limit              *int       `toml:"limit"`
	Round              *int       `toml:"round"`
	ResetCount         *int       `toml:"reset_count"`
	MinRetainedAreaPct *float64   `toml:"min_retained_area_percent"`
	MinWidth           *int       `toml:"min_width"`
	MinHeight          *int       `toml:"min_height"`
	RequiredAlignment  *int       `toml:"required_alignment"`
}

type rawLibraryConfig struct {
	Kind             string                   `toml:"kind"`
	Path             string                   `toml:"path"`
	Profile          string                   `toml:"profile"`
	ScanInterval     *Duration                `toml:"scan_interval"`
	Priority         int                      `toml:"priority"`
	Include          []string                 `toml:"include"`
	Exclude          []string                 `toml:"exclude"`
	IgnoreRegex      []string                 `toml:"ignore_regex"`
	ConcurrencyLimit int                      `toml:"concurrency_limit"`
	Arr              string                   `toml:"arr"`
	Media            MediaLibraryConfig       `toml:"media"`
	Download         rawDownloadLibraryConfig `toml:"download"`
}

type rawDownloadLibraryConfig struct {
	HandoffPath          string    `toml:"handoff_path"`
	StableFor            *Duration `toml:"stable_for"`
	PackageMode          string    `toml:"package_mode"`
	HandoffMode          string    `toml:"handoff_mode"`
	PreserveRelativePath bool      `toml:"preserve_relative_path"`
	CleanupSourceMedia   bool      `toml:"cleanup_source_media"`
	PruneEmptyDirs       bool      `toml:"prune_empty_dirs"`
	IgnorableGlobs       []string  `toml:"ignorable_globs"`
}

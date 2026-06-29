package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	DefaultFlowName        = "av1-crf-search"
	DefaultProfileName     = "default-av1"
	DefaultLibraryKind     = "media"
	DefaultReplacementMode = "replace"
	DefaultScanInterval    = "30m"
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
	Codec             string            `toml:"codec"`
	Preset            string            `toml:"preset"`
	PixelFormat       string            `toml:"pixel_format"`
	CRFMin            int               `toml:"crf_min"`
	CRFMax            int               `toml:"crf_max"`
	TargetVMAF        float64           `toml:"target_vmaf"`
	MinSavingsPercent float64           `toml:"min_savings_percent"`
	FFmpegArgs        []string          `toml:"ffmpeg_args"`
	ABAV1Args         []string          `toml:"ab_av1_args"`
	DolbyVision       DolbyVisionConfig `toml:"dolby_vision"`
}

// DolbyVisionConfig overrides normal video settings for Dolby Vision sources.
type DolbyVisionConfig struct {
	Mode            string   `toml:"mode"`
	Codec           string   `toml:"codec"`
	Preset          string   `toml:"preset"`
	PixelFormat     string   `toml:"pixel_format"`
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

// Load reads a TOML configuration file, applies defaults, and validates it.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return Config{}, fmt.Errorf("load config %q: %w", path, err)
		}
	}

	applyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Default returns a valid minimal configuration.
func Default() Config {
	tempDir := filepath.Join(os.TempDir(), "anvil")

	return Config{
		Daemon: DaemonConfig{
			TempDir:           tempDir,
			StorePath:         filepath.Join(tempDir, "anvil.db"),
			WorkerCount:       max(runtime.NumCPU(), 1),
			TotalThreads:      max(runtime.NumCPU(), 1),
			MaxAttempts:       DefaultMaxAttempts,
			ScanInterval:      DefaultScanInterval,
			SchedulerInterval: DefaultSchedulerTick,
			LeaseDuration:     DefaultLeaseDuration,
			ShutdownPolicy:    DefaultShutdownPolicy,
			ShutdownTimeout:   DefaultShutdownTimeout,
			StagingCleanupAge: DefaultStagingCleanup,
			LogLevel:          DefaultLogLevel,
		},
		Flows: map[string]FlowConfig{
			DefaultFlowName: {
				Steps: []string{"probe", "crop-detect", "audio-cleanup", "stage", "crf-search", "encode", "dovi-fix", "validate", "replace", "cleanup"},
			},
		},
		Profiles: map[string]ProfileConfig{
			DefaultProfileName: {
				Container: "mkv",
				Video: VideoConfig{
					Codec:             "libsvtav1",
					Preset:            "6",
					PixelFormat:       "yuv420p10le",
					CRFMin:            18,
					CRFMax:            40,
					TargetVMAF:        95,
					MinSavingsPercent: DefaultMinSavingsPct,
					DolbyVision: DolbyVisionConfig{
						Mode: DefaultDolbyVisionMode,
					},
				},
				Audio: AudioConfig{
					Fallback: DefaultStreamFallback,
				},
				Subtitles: SubtitleConfig{
					Mode:     DefaultStreamMode,
					Fallback: DefaultStreamFallback,
				},
				Metadata: MetadataConfig{
					Mode:        DefaultMetadataMode,
					TrackTitles: DefaultTrackTitleMode,
				},
				Attachments: MetadataConfig{
					Mode: DefaultMetadataMode,
				},
				Chapters: MetadataConfig{
					Mode: DefaultMetadataMode,
				},
			},
		},
	}
}

// Validate checks that the configuration is internally consistent.
func (c Config) Validate() error {
	var problems []string

	if strings.TrimSpace(c.Daemon.TempDir) == "" {
		problems = append(problems, "daemon.temp_dir is required")
	}
	if strings.TrimSpace(c.Daemon.StorePath) == "" {
		problems = append(problems, "daemon.store_path is required")
	}
	if c.Daemon.WorkerCount < 1 {
		problems = append(problems, "daemon.worker_count must be at least 1")
	}
	if c.Daemon.TotalThreads < 0 {
		problems = append(problems, "daemon.total_threads must be non-negative")
	}
	if c.Daemon.MaxAttempts < 1 {
		problems = append(problems, "daemon.max_attempts must be at least 1")
	}
	validatePositiveDuration(&problems, "daemon.scan_interval", c.Daemon.ScanInterval)
	validatePositiveDuration(&problems, "daemon.scheduler_interval", c.Daemon.SchedulerInterval)
	validatePositiveDuration(&problems, "daemon.lease_duration", c.Daemon.LeaseDuration)
	validateNonNegativeDuration(&problems, "daemon.shutdown_timeout", c.Daemon.ShutdownTimeout)
	validateNonNegativeDuration(&problems, "daemon.staging_cleanup_age", c.Daemon.StagingCleanupAge)
	if !validShutdownPolicy(c.Daemon.ShutdownPolicy) {
		problems = append(problems, fmt.Sprintf("daemon.shutdown_policy %q is invalid", c.Daemon.ShutdownPolicy))
	}
	if _, ok := NormalizeLogLevel(c.Daemon.LogLevel); !ok {
		problems = append(problems, fmt.Sprintf("daemon.log_level %q is invalid (must be debug, info, warn, or error)", c.Daemon.LogLevel))
	}

	flows := make(map[string]struct{}, len(c.Flows))
	for _, name := range sortedKeys(c.Flows) {
		flow := c.Flows[name]
		name = strings.TrimSpace(name)
		if name == "" {
			problems = append(problems, "flow name is required")
			continue
		}
		flows[name] = struct{}{}
		if len(flow.Steps) == 0 {
			problems = append(problems, fmt.Sprintf("flow %q must have at least one step", name))
		}
	}

	profiles := make(map[string]struct{}, len(c.Profiles))
	for _, name := range sortedKeys(c.Profiles) {
		profile := c.Profiles[name]
		name = strings.TrimSpace(name)
		if name == "" {
			problems = append(problems, "profile name is required")
			continue
		}
		profiles[name] = struct{}{}

		if !validContainer(profile.Container) {
			problems = append(problems, fmt.Sprintf("profile %q container %q is invalid; Anvil outputs MKV only", name, profile.Container))
		}
		if profile.Video.CRFMin < 0 || profile.Video.CRFMax < 0 {
			problems = append(problems, fmt.Sprintf("profile %q CRF values must be non-negative", name))
		}
		if profile.Video.CRFMin > profile.Video.CRFMax {
			problems = append(problems, fmt.Sprintf("profile %q crf_min must be less than or equal to crf_max", name))
		}
		if profile.Video.TargetVMAF < 0 || profile.Video.TargetVMAF > 100 {
			problems = append(problems, fmt.Sprintf("profile %q target_vmaf must be between 0 and 100", name))
		}
		if profile.Video.MinSavingsPercent < 0 || profile.Video.MinSavingsPercent > 100 {
			problems = append(problems, fmt.Sprintf("profile %q min_savings_percent must be between 0 and 100", name))
		}
		if !validDolbyVisionMode(profile.Video.DolbyVision.Mode) {
			problems = append(problems, fmt.Sprintf("profile %q video.dolby_vision.mode %q is invalid", name, profile.Video.DolbyVision.Mode))
		}
		if profile.Video.DolbyVision.Mode == DolbyVisionModeRequire && strings.TrimSpace(profile.Video.DolbyVision.Codec) == "" {
			problems = append(problems, fmt.Sprintf("profile %q video.dolby_vision.codec is required when mode is require", name))
		}
		if !validStreamFallback(profile.Audio.Fallback) {
			problems = append(problems, fmt.Sprintf("profile %q audio.fallback %q is invalid", name, profile.Audio.Fallback))
		}
		if !validStreamMode(profile.Subtitles.Mode) {
			problems = append(problems, fmt.Sprintf("profile %q subtitles.mode %q is invalid", name, profile.Subtitles.Mode))
		}
		if !validStreamFallback(profile.Subtitles.Fallback) {
			problems = append(problems, fmt.Sprintf("profile %q subtitles.fallback %q is invalid", name, profile.Subtitles.Fallback))
		}
		if profile.Subtitles.MaxTracks < 0 {
			problems = append(problems, fmt.Sprintf("profile %q subtitles.max_tracks must be non-negative", name))
		}
		if profile.Validation.DurationToleranceSeconds < 0 {
			problems = append(problems, fmt.Sprintf("profile %q validation.duration_tolerance_seconds must be non-negative", name))
		}
		if !validMetadataMode(profile.Metadata.Mode) {
			problems = append(problems, fmt.Sprintf("profile %q metadata.mode %q is invalid", name, profile.Metadata.Mode))
		}
		if !validTrackTitleMode(profile.Metadata.TrackTitles) {
			problems = append(problems, fmt.Sprintf("profile %q metadata.track_titles %q is invalid", name, profile.Metadata.TrackTitles))
		}
		if !validMetadataMode(profile.Attachments.Mode) {
			problems = append(problems, fmt.Sprintf("profile %q attachments.mode %q is invalid", name, profile.Attachments.Mode))
		}
		if !validMetadataMode(profile.Chapters.Mode) {
			problems = append(problems, fmt.Sprintf("profile %q chapters.mode %q is invalid", name, profile.Chapters.Mode))
		}
	}

	arrs := make(map[string]struct{}, len(c.Arrs))
	for _, name := range sortedKeys(c.Arrs) {
		arr := c.Arrs[name]
		name = strings.TrimSpace(name)
		if name == "" {
			problems = append(problems, "arr name is required")
			continue
		}
		arrs[name] = struct{}{}
		provider := arrProvider(arr)
		if !validArrProvider(provider) {
			problems = append(problems, fmt.Sprintf("arr %q type %q is invalid", name, provider))
		}
		if strings.TrimSpace(arr.BaseURL) == "" {
			problems = append(problems, fmt.Sprintf("arr %q base_url is required", name))
		}
		if strings.TrimSpace(arr.APIKey) == "" && strings.TrimSpace(arr.APIKeyFile) == "" {
			problems = append(problems, fmt.Sprintf("arr %q api_key or api_key_file is required", name))
		}
	}

	for _, name := range sortedKeys(c.Libraries) {
		library := c.Libraries[name]
		name = strings.TrimSpace(name)
		if name == "" {
			problems = append(problems, "library name is required")
			continue
		}
		if !validLibraryKind(library.Kind) {
			problems = append(problems, fmt.Sprintf("library %q kind %q is invalid", name, library.Kind))
		}
		if strings.TrimSpace(library.Path) == "" {
			problems = append(problems, fmt.Sprintf("library %q path is required", name))
		}
		if _, exists := flows[library.Flow]; !exists {
			problems = append(problems, fmt.Sprintf("library %q references unknown flow %q", name, library.Flow))
		}
		if _, exists := profiles[library.Profile]; !exists {
			problems = append(problems, fmt.Sprintf("library %q references unknown profile %q", name, library.Profile))
		}
		if library.ConcurrencyLimit < 0 {
			problems = append(problems, fmt.Sprintf("library %q concurrency_limit must be non-negative", name))
		}
		if strings.TrimSpace(library.ScanInterval) != "" {
			validatePositiveDuration(&problems, fmt.Sprintf("library %q scan_interval", name), library.ScanInterval)
		}
		if strings.TrimSpace(library.Arr) != "" {
			if _, exists := arrs[library.Arr]; !exists {
				problems = append(problems, fmt.Sprintf("library %q references unknown arr %q", name, library.Arr))
			}
		}
		if !validReplacementMode(library.Media.ReplacementMode) {
			problems = append(problems, fmt.Sprintf("library %q media.replacement_mode %q is invalid", name, library.Media.ReplacementMode))
		}
		if library.Kind == "download" {
			if strings.TrimSpace(library.Download.HandoffPath) == "" {
				problems = append(problems, fmt.Sprintf("download library %q download.handoff_path is required", name))
			}
			if stableFor, err := time.ParseDuration(library.Download.StableFor); err != nil {
				problems = append(problems, fmt.Sprintf("download library %q download.stable_for is invalid: %v", name, err))
			} else if stableFor < 0 {
				problems = append(problems, fmt.Sprintf("download library %q download.stable_for must be non-negative", name))
			}
			if !validPackageMode(library.Download.PackageMode) {
				problems = append(problems, fmt.Sprintf("download library %q download.package_mode %q is invalid", name, library.Download.PackageMode))
			}
			if !validHandoffMode(library.Download.HandoffMode) {
				problems = append(problems, fmt.Sprintf("download library %q download.handoff_mode %q is invalid", name, library.Download.HandoffMode))
			}
		}
	}

	if len(problems) > 0 {
		return errors.New("invalid config: " + strings.Join(problems, "; "))
	}

	return nil
}

func applyDefaults(c *Config) {
	defaults := Default()

	if strings.TrimSpace(c.Daemon.TempDir) == "" {
		c.Daemon.TempDir = defaults.Daemon.TempDir
	}
	if strings.TrimSpace(c.Daemon.StorePath) == "" {
		c.Daemon.StorePath = filepath.Join(c.Daemon.TempDir, "anvil.db")
	}
	if c.Daemon.WorkerCount == 0 {
		c.Daemon.WorkerCount = defaults.Daemon.WorkerCount
	}
	if c.Daemon.TotalThreads == 0 {
		c.Daemon.TotalThreads = defaults.Daemon.TotalThreads
	}
	if c.Daemon.MaxAttempts == 0 {
		c.Daemon.MaxAttempts = defaults.Daemon.MaxAttempts
	}
	if strings.TrimSpace(c.Daemon.ScanInterval) == "" {
		c.Daemon.ScanInterval = defaults.Daemon.ScanInterval
	}
	if strings.TrimSpace(c.Daemon.SchedulerInterval) == "" {
		c.Daemon.SchedulerInterval = defaults.Daemon.SchedulerInterval
	}
	if strings.TrimSpace(c.Daemon.LeaseDuration) == "" {
		c.Daemon.LeaseDuration = defaults.Daemon.LeaseDuration
	}
	if strings.TrimSpace(c.Daemon.ShutdownPolicy) == "" {
		c.Daemon.ShutdownPolicy = defaults.Daemon.ShutdownPolicy
	}
	if strings.TrimSpace(c.Daemon.ShutdownTimeout) == "" {
		c.Daemon.ShutdownTimeout = defaults.Daemon.ShutdownTimeout
	}
	if strings.TrimSpace(c.Daemon.StagingCleanupAge) == "" {
		c.Daemon.StagingCleanupAge = defaults.Daemon.StagingCleanupAge
	}
	if strings.TrimSpace(c.Daemon.LogLevel) == "" {
		c.Daemon.LogLevel = defaults.Daemon.LogLevel
	}
	if logLevel, ok := NormalizeLogLevel(c.Daemon.LogLevel); ok {
		c.Daemon.LogLevel = logLevel
	}
	if len(c.Flows) == 0 {
		c.Flows = defaults.Flows
	}
	if len(c.Profiles) == 0 {
		c.Profiles = defaults.Profiles
	}
	if c.Arrs == nil {
		c.Arrs = make(map[string]ArrConfig)
	}
	if c.Libraries == nil {
		c.Libraries = make(map[string]LibraryConfig)
	}

	for name, arr := range c.Arrs {
		arr.Name = name
		c.Arrs[name] = arr
	}

	for name, flow := range c.Flows {
		flow.Name = name
		c.Flows[name] = flow
	}

	for name, profile := range c.Profiles {
		profile.Name = name
		applyProfileDefaults(&profile)
		c.Profiles[name] = profile
	}

	for name, library := range c.Libraries {
		library.Name = name
		if strings.TrimSpace(library.Kind) == "" {
			library.Kind = DefaultLibraryKind
		}
		if strings.TrimSpace(library.Flow) == "" {
			library.Flow = DefaultFlowName
		}
		if strings.TrimSpace(library.Profile) == "" {
			library.Profile = DefaultProfileName
		}
		applyLibraryPolicyDefaults(&library)
		c.Libraries[name] = library
	}
}

func applyProfileDefaults(profile *ProfileConfig) {
	if strings.TrimSpace(profile.Container) == "" {
		profile.Container = "mkv"
	} else {
		profile.Container = normalizeContainer(profile.Container)
	}
	if strings.TrimSpace(profile.Video.DolbyVision.Mode) == "" {
		profile.Video.DolbyVision.Mode = DefaultDolbyVisionMode
	}
	profile.Video.DolbyVision.Mode = strings.ToLower(strings.TrimSpace(profile.Video.DolbyVision.Mode))
	if strings.TrimSpace(profile.Audio.Fallback) == "" {
		profile.Audio.Fallback = DefaultStreamFallback
	}
	if strings.TrimSpace(profile.Subtitles.Mode) == "" {
		profile.Subtitles.Mode = DefaultStreamMode
	}
	if strings.TrimSpace(profile.Subtitles.Fallback) == "" {
		profile.Subtitles.Fallback = DefaultStreamFallback
	}
	if strings.TrimSpace(profile.Metadata.Mode) == "" {
		profile.Metadata.Mode = DefaultMetadataMode
	}
	if strings.TrimSpace(profile.Metadata.TrackTitles) == "" {
		profile.Metadata.TrackTitles = DefaultTrackTitleMode
	} else {
		profile.Metadata.TrackTitles = strings.ToLower(strings.TrimSpace(profile.Metadata.TrackTitles))
	}
	if strings.TrimSpace(profile.Attachments.Mode) == "" {
		profile.Attachments.Mode = DefaultMetadataMode
	}
	if strings.TrimSpace(profile.Chapters.Mode) == "" {
		profile.Chapters.Mode = DefaultMetadataMode
	}
}

func applyLibraryPolicyDefaults(library *LibraryConfig) {
	if strings.TrimSpace(library.Media.ReplacementMode) == "" {
		library.Media.ReplacementMode = DefaultReplacementMode
	}
	if strings.TrimSpace(library.Download.StableFor) == "" {
		library.Download.StableFor = DefaultStableFor
	}
	if strings.TrimSpace(library.Download.PackageMode) == "" {
		library.Download.PackageMode = DefaultPackageMode
	}
	if strings.TrimSpace(library.Download.HandoffMode) == "" {
		library.Download.HandoffMode = DefaultHandoffMode
	}
	if len(library.Download.IgnorableGlobs) == 0 {
		library.Download.IgnorableGlobs = append([]string(nil), DefaultIgnorableGlobs...)
	}
}

func validLibraryKind(kind string) bool {
	return kind == "media" || kind == "download"
}

func validatePositiveDuration(problems *[]string, name string, value string) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s is invalid: %v", name, err))
		return
	}
	if duration <= 0 {
		*problems = append(*problems, fmt.Sprintf("%s must be greater than zero", name))
	}
}

func validateNonNegativeDuration(problems *[]string, name string, value string) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s is invalid: %v", name, err))
		return
	}
	if duration < 0 {
		*problems = append(*problems, fmt.Sprintf("%s must be non-negative", name))
	}
}

func validShutdownPolicy(policy string) bool {
	return policy == "drain" || policy == "cancel"
}

// NormalizeLogLevel trims and canonicalizes a configured daemon log level.
func NormalizeLogLevel(level string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(level))
	switch normalized {
	case "debug", "info", "warn", "error":
		return normalized, true
	default:
		return "", false
	}
}

func validReplacementMode(mode string) bool {
	return mode == "replace" || mode == "copy"
}

func validPackageMode(mode string) bool {
	return mode == "auto" || mode == "directory" || mode == "file"
}

func validHandoffMode(mode string) bool {
	return mode == "move" || mode == "copy"
}

func validArrProvider(provider string) bool {
	return provider == "radarr" || provider == "sonarr"
}

func arrProvider(arr ArrConfig) string {
	return arr.Type
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validStreamMode(mode string) bool {
	return mode == "preserve" || mode == "prefer" || mode == "cleanup"
}

func validDolbyVisionMode(mode string) bool {
	return mode == DefaultDolbyVisionMode || mode == DolbyVisionModeOff || mode == DolbyVisionModeRequire
}

func normalizeContainer(container string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(container), "."))
}

func validContainer(container string) bool {
	return normalizeContainer(container) == "mkv"
}

func validStreamFallback(fallback string) bool {
	return fallback == "keep_all" || fallback == "keep_first" || fallback == "fail_job"
}

func validMetadataMode(mode string) bool {
	return mode == "preserve" || mode == "strip"
}

func validTrackTitleMode(mode string) bool {
	return mode == "preserve" || mode == "strip" || mode == "standardize"
}

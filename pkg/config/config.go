package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	DefaultMaxAttempts     = 3
	DefaultStableFor       = "5m"
	DefaultPackageMode     = "auto"
	DefaultHandoffMode     = "copy"
	DefaultStreamMode      = "preserve"
	DefaultStreamFallback  = "keep_all"
	DefaultMetadataMode    = "preserve"
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
	Daemon    DaemonConfig    `toml:"daemon"`
	Flows     []FlowConfig    `toml:"flows"`
	Profiles  []ProfileConfig `toml:"profiles"`
	Libraries []LibraryConfig `toml:"libraries"`
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
	LogLevel          string `toml:"log_level"`
}

// FlowConfig names an orchestration flow. The steps are declarative for now.
type FlowConfig struct {
	Name  string   `toml:"name"`
	Steps []string `toml:"steps"`
}

// ProfileConfig groups encode settings that libraries can reference.
type ProfileConfig struct {
	Name        string         `toml:"name"`
	Container   string         `toml:"container"`
	Video       VideoConfig    `toml:"video"`
	Audio       AudioConfig    `toml:"audio"`
	Subtitles   SubtitleConfig `toml:"subtitles"`
	Metadata    MetadataConfig `toml:"metadata"`
	Attachments MetadataConfig `toml:"attachments"`
	Chapters    MetadataConfig `toml:"chapters"`
}

// VideoConfig contains the initial video settings shape for AV1 search work.
type VideoConfig struct {
	Codec       string  `toml:"codec"`
	Preset      string  `toml:"preset"`
	PixelFormat string  `toml:"pixel_format"`
	CRFMin      int     `toml:"crf_min"`
	CRFMax      int     `toml:"crf_max"`
	TargetVMAF  float64 `toml:"target_vmaf"`
}

// AudioConfig declares track retention intent. It is conservative by default.
type AudioConfig struct {
	Mode                 string   `toml:"mode"`
	PreferredLanguages   []string `toml:"preferred_languages"`
	LanguagesToKeep      []string `toml:"languages_to_keep"`
	KeepOriginalLanguage bool     `toml:"keep_original_language"`
	KeepCommentary       bool     `toml:"keep_commentary"`
	KeepOtherTracks      bool     `toml:"keep_other_tracks"`
	KeepDescriptiveAudio bool     `toml:"keep_descriptive_audio"`
	KeepLossless         bool     `toml:"keep_lossless"`
	MaxTracks            int      `toml:"max_tracks"`
	Fallback             string   `toml:"fallback"`
	TranscodeUnsupported bool     `toml:"transcode_unsupported"`
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

// MetadataConfig is shared by metadata, attachments, and chapters.
type MetadataConfig struct {
	Mode string `toml:"mode"`
}

// LibraryConfig describes a user-defined media library.
type LibraryConfig struct {
	Name             string                `toml:"name"`
	Kind             string                `toml:"kind"`
	Path             string                `toml:"path"`
	OriginalLanguage string                `toml:"original_language"`
	Flow             string                `toml:"flow"`
	Profile          string                `toml:"profile"`
	Priority         int                   `toml:"priority"`
	Include          []string              `toml:"include"`
	Exclude          []string              `toml:"exclude"`
	ConcurrencyLimit int                   `toml:"concurrency_limit"`
	Media            MediaLibraryConfig    `toml:"media"`
	Download         DownloadLibraryConfig `toml:"download"`
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
			LogLevel:          "info",
		},
		Flows: []FlowConfig{
			{
				Name:  DefaultFlowName,
				Steps: []string{"probe", "crop-detect", "audio-cleanup", "stage", "crf-search", "encode", "validate", "replace", "cleanup"},
			},
		},
		Profiles: []ProfileConfig{
			{
				Name:      DefaultProfileName,
				Container: "mkv",
				Video: VideoConfig{
					Codec:       "libsvtav1",
					Preset:      "6",
					PixelFormat: "yuv420p10le",
					CRFMin:      18,
					CRFMax:      40,
					TargetVMAF:  95,
				},
				Audio: AudioConfig{
					Mode:     DefaultStreamMode,
					Fallback: DefaultStreamFallback,
				},
				Subtitles: SubtitleConfig{
					Mode:     DefaultStreamMode,
					Fallback: DefaultStreamFallback,
				},
				Metadata: MetadataConfig{
					Mode: DefaultMetadataMode,
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

	flows := make(map[string]struct{}, len(c.Flows))
	for i, flow := range c.Flows {
		name := strings.TrimSpace(flow.Name)
		if name == "" {
			problems = append(problems, fmt.Sprintf("flows[%d].name is required", i))
			continue
		}
		if _, exists := flows[name]; exists {
			problems = append(problems, fmt.Sprintf("duplicate flow %q", name))
			continue
		}
		flows[name] = struct{}{}
		if len(flow.Steps) == 0 {
			problems = append(problems, fmt.Sprintf("flow %q must have at least one step", name))
		}
	}

	profiles := make(map[string]struct{}, len(c.Profiles))
	for i, profile := range c.Profiles {
		name := strings.TrimSpace(profile.Name)
		if name == "" {
			problems = append(problems, fmt.Sprintf("profiles[%d].name is required", i))
			continue
		}
		if _, exists := profiles[name]; exists {
			problems = append(problems, fmt.Sprintf("duplicate profile %q", name))
			continue
		}
		profiles[name] = struct{}{}

		if profile.Video.CRFMin < 0 || profile.Video.CRFMax < 0 {
			problems = append(problems, fmt.Sprintf("profile %q CRF values must be non-negative", name))
		}
		if profile.Video.CRFMin > profile.Video.CRFMax {
			problems = append(problems, fmt.Sprintf("profile %q crf_min must be less than or equal to crf_max", name))
		}
		if profile.Video.TargetVMAF < 0 || profile.Video.TargetVMAF > 100 {
			problems = append(problems, fmt.Sprintf("profile %q target_vmaf must be between 0 and 100", name))
		}
		if !validStreamMode(profile.Audio.Mode) {
			problems = append(problems, fmt.Sprintf("profile %q audio.mode %q is invalid", name, profile.Audio.Mode))
		}
		if !validStreamFallback(profile.Audio.Fallback) {
			problems = append(problems, fmt.Sprintf("profile %q audio.fallback %q is invalid", name, profile.Audio.Fallback))
		}
		if profile.Audio.MaxTracks < 0 {
			problems = append(problems, fmt.Sprintf("profile %q audio.max_tracks must be non-negative", name))
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
		if !validMetadataMode(profile.Metadata.Mode) {
			problems = append(problems, fmt.Sprintf("profile %q metadata.mode %q is invalid", name, profile.Metadata.Mode))
		}
		if !validMetadataMode(profile.Attachments.Mode) {
			problems = append(problems, fmt.Sprintf("profile %q attachments.mode %q is invalid", name, profile.Attachments.Mode))
		}
		if !validMetadataMode(profile.Chapters.Mode) {
			problems = append(problems, fmt.Sprintf("profile %q chapters.mode %q is invalid", name, profile.Chapters.Mode))
		}
	}

	libraries := make(map[string]struct{}, len(c.Libraries))
	for i, library := range c.Libraries {
		name := strings.TrimSpace(library.Name)
		if name == "" {
			problems = append(problems, fmt.Sprintf("libraries[%d].name is required", i))
			continue
		}
		if _, exists := libraries[name]; exists {
			problems = append(problems, fmt.Sprintf("duplicate library %q", name))
			continue
		}
		libraries[name] = struct{}{}

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
	if strings.TrimSpace(c.Daemon.LogLevel) == "" {
		c.Daemon.LogLevel = defaults.Daemon.LogLevel
	}
	if len(c.Flows) == 0 {
		c.Flows = defaults.Flows
	}
	if len(c.Profiles) == 0 {
		c.Profiles = defaults.Profiles
	}

	for i := range c.Profiles {
		applyProfileDefaults(&c.Profiles[i])
	}

	for i := range c.Libraries {
		if strings.TrimSpace(c.Libraries[i].Kind) == "" {
			c.Libraries[i].Kind = DefaultLibraryKind
		}
		if strings.TrimSpace(c.Libraries[i].Flow) == "" {
			c.Libraries[i].Flow = DefaultFlowName
		}
		if strings.TrimSpace(c.Libraries[i].Profile) == "" {
			c.Libraries[i].Profile = DefaultProfileName
		}
		applyLibraryPolicyDefaults(&c.Libraries[i])
	}
}

func applyProfileDefaults(profile *ProfileConfig) {
	if strings.TrimSpace(profile.Container) == "" {
		profile.Container = "mkv"
	}
	if strings.TrimSpace(profile.Audio.Mode) == "" {
		profile.Audio.Mode = DefaultStreamMode
	}
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

func validReplacementMode(mode string) bool {
	return mode == "replace" || mode == "sidecar"
}

func validPackageMode(mode string) bool {
	return mode == "auto" || mode == "directory" || mode == "file"
}

func validHandoffMode(mode string) bool {
	return mode == "move" || mode == "copy"
}

func validStreamMode(mode string) bool {
	return mode == "preserve" || mode == "prefer" || mode == "cleanup"
}

func validStreamFallback(fallback string) bool {
	return fallback == "keep_all" || fallback == "keep_first" || fallback == "fail_job"
}

func validMetadataMode(mode string) bool {
	return mode == "preserve" || mode == "strip"
}

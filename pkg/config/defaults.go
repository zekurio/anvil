package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zekurio/anvil/pkg/video"
)

// Default returns a valid minimal configuration.
func Default() Config {
	tempDir := filepath.Join(os.TempDir(), "anvil")

	return Config{
		Daemon: DaemonConfig{
			TempDir:           tempDir,
			StorePath:         filepath.Join(tempDir, "anvil.db"),
			ControlSocket:     filepath.Join(tempDir, "anvild.sock"),
			WorkerCount:       max(runtime.NumCPU(), 1),
			TotalThreads:      max(runtime.NumCPU(), 1),
			MaxAttempts:       DefaultMaxAttempts,
			ScanInterval:      DefaultScanInterval,
			FSDebounce:        DefaultFSDebounce,
			SchedulerInterval: DefaultSchedulerTick,
			LeaseDuration:     DefaultLeaseDuration,
			ShutdownPolicy:    DefaultShutdownPolicy,
			ShutdownTimeout:   DefaultShutdownTimeout,
			StagingCleanupAge: DefaultStagingCleanup,
			LogLevel:          DefaultLogLevel,
		},
		Flows: map[string]FlowConfig{
			DefaultFlowName: {
				Steps: []string{"probe", "crop-detect", "audio-cleanup", "subtitle-cleanup", "stage", "crf-search", "encode", "dovi-fix", "validate", "replace", "cleanup"},
			},
			DefaultDownloadFlowName: {
				Steps: []string{"probe", "crop-detect", "audio-cleanup", "subtitle-cleanup", "stage", "crf-search", "encode", "dovi-fix", "validate", "handoff", "cleanup"},
			},
		},
		Profiles: map[string]ProfileConfig{
			DefaultProfileName: {
				Container: "mkv",
				Video: VideoConfig{
					Codec:             "av1",
					Accelerator:       "software",
					Preset:            "6",
					BitDepth:          video.DefaultBitDepth,
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

func applyDefaults(c *Config) {
	defaults := Default()

	if strings.TrimSpace(c.Daemon.TempDir) == "" {
		c.Daemon.TempDir = defaults.Daemon.TempDir
	}
	if strings.TrimSpace(c.Daemon.StorePath) == "" {
		c.Daemon.StorePath = filepath.Join(c.Daemon.TempDir, "anvil.db")
	}
	if strings.TrimSpace(c.Daemon.ControlSocket) == "" {
		c.Daemon.ControlSocket = filepath.Join(c.Daemon.TempDir, "anvild.sock")
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
	if strings.TrimSpace(c.Daemon.FSDebounce) == "" {
		c.Daemon.FSDebounce = defaults.Daemon.FSDebounce
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
			if library.Kind == "download" {
				library.Flow = DefaultDownloadFlowName
			}
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
	if strings.TrimSpace(profile.Video.Codec) == "" {
		profile.Video.Codec = "av1"
	}
	profile.Video.Codec = normalizeConfigVideoCodec(profile.Video.Codec)
	if strings.TrimSpace(profile.Video.Accelerator) == "" {
		profile.Video.Accelerator = "software"
	}
	profile.Video.Accelerator = video.NormalizeAccelerator(profile.Video.Accelerator)
	if profile.Video.BitDepth == 0 {
		profile.Video.BitDepth = video.DefaultBitDepth
	}
	if strings.TrimSpace(profile.Video.DolbyVision.Mode) == "" {
		profile.Video.DolbyVision.Mode = DefaultDolbyVisionMode
	}
	profile.Video.DolbyVision.Mode = strings.ToLower(strings.TrimSpace(profile.Video.DolbyVision.Mode))
	if profile.Video.Overrides != nil {
		overrides := make(map[string]VideoOverrideConfig, len(profile.Video.Overrides))
		for _, key := range sortedKeys(profile.Video.Overrides) {
			override := profile.Video.Overrides[key]
			if override.Codec != nil && strings.TrimSpace(*override.Codec) != "" {
				codec := normalizeConfigVideoCodec(*override.Codec)
				override.Codec = &codec
			}
			if override.Accelerator != nil {
				accelerator := video.NormalizeAccelerator(*override.Accelerator)
				override.Accelerator = &accelerator
			}

			key = canonicalVideoOverrideKey(key)
			if _, exists := overrides[key]; exists {
				continue
			}
			overrides[key] = override
		}
		profile.Video.Overrides = overrides
	}
	if strings.TrimSpace(profile.Audio.Fallback) == "" {
		profile.Audio.Fallback = DefaultStreamFallback
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

func canonicalVideoOverrideKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" || key == "dolby_vision" {
		return key
	}
	return video.CanonicalCodec(key)
}

func applyLibraryPolicyDefaults(library *LibraryConfig) {
	if library.Kind != "download" {
		if strings.TrimSpace(library.Media.ReplacementMode) == "" {
			library.Media.ReplacementMode = DefaultReplacementMode
		}
		return
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

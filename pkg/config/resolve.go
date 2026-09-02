package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zekurio/anvil/pkg/video"
)

// Default returns a valid minimal configuration.
func Default() Config {
	cfg, err := resolve(rawConfig{})
	if err != nil {
		panic(fmt.Sprintf("config: default configuration does not resolve: %v", err))
	}
	return cfg
}

// resolve turns a decoded rawConfig into an effective Config by filling
// defaults. Because raw numeric and duration fields are pointers, an explicit
// zero in the file survives while an omitted key gets the default.
func resolve(raw rawConfig) (Config, error) {
	cfg := Config{
		Daemon: resolveDaemon(raw.Daemon),
		Arrs:   make(map[string]ArrConfig, len(raw.Arrs)),
	}

	for name, arr := range raw.Arrs {
		arr.Name = name
		cfg.Arrs[name] = arr
	}

	if raw.Profiles == nil {
		raw.Profiles = make(map[string]rawProfileConfig)
	}
	if _, exists := raw.Profiles[DefaultProfileName]; !exists {
		raw.Profiles[DefaultProfileName] = rawProfileConfig{}
	}
	cfg.Profiles = make(map[string]ProfileConfig, len(raw.Profiles))
	for name, profile := range raw.Profiles {
		resolved, err := resolveProfile(name, profile)
		if err != nil {
			return Config{}, err
		}
		cfg.Profiles[name] = resolved
	}

	cfg.Libraries = make(map[string]LibraryConfig, len(raw.Libraries))
	for name, library := range raw.Libraries {
		resolved, err := resolveLibrary(name, library)
		if err != nil {
			return Config{}, err
		}
		cfg.Libraries[name] = resolved
	}

	return cfg, nil
}

func resolveDaemon(raw rawDaemonConfig) DaemonConfig {
	tempDir := strings.TrimSpace(raw.TempDir)
	if tempDir == "" {
		tempDir = filepath.Join(os.TempDir(), "anvil")
	}
	storePath := strings.TrimSpace(raw.StorePath)
	if storePath == "" {
		storePath = filepath.Join(tempDir, "anvil.db")
	}
	controlSocket := strings.TrimSpace(raw.ControlSocket)
	if controlSocket == "" {
		controlSocket = filepath.Join(tempDir, "anvild.sock")
	}
	logLevel := orDefault(raw.LogLevel, DefaultLogLevel)
	if normalized, ok := NormalizeLogLevel(logLevel); ok {
		logLevel = normalized
	}
	cpus := max(runtime.NumCPU(), 1)
	return DaemonConfig{
		TempDir:           tempDir,
		StorePath:         storePath,
		ControlSocket:     controlSocket,
		WorkerCount:       valueOr(raw.WorkerCount, cpus),
		TotalThreads:      valueOr(raw.TotalThreads, cpus),
		MaxAttempts:       valueOr(raw.MaxAttempts, DefaultMaxAttempts),
		ScanInterval:      valueOr(raw.ScanInterval, Duration{Duration: DefaultScanInterval}),
		FSDebounce:        valueOr(raw.FSDebounce, Duration{Duration: DefaultFSDebounce}),
		SchedulerInterval: valueOr(raw.SchedulerInterval, Duration{Duration: DefaultSchedulerTick}),
		LeaseDuration:     valueOr(raw.LeaseDuration, Duration{Duration: DefaultLeaseDuration}),
		ShutdownPolicy:    orDefault(raw.ShutdownPolicy, DefaultShutdownPolicy),
		ShutdownTimeout:   valueOr(raw.ShutdownTimeout, Duration{Duration: DefaultShutdownTimeout}),
		StagingCleanupAge: valueOr(raw.StagingCleanupAge, Duration{Duration: DefaultStagingCleanup}),
		LogLevel:          logLevel,
	}
}

func resolveProfile(name string, raw rawProfileConfig) (ProfileConfig, error) {
	overrides, err := resolveOverrides(name, raw.Video.Overrides)
	if err != nil {
		return ProfileConfig{}, err
	}

	metric := strings.ToLower(orDefault(raw.Video.Metric, "vmaf"))
	// An unset VMAF target means ab-av1's own 95 default anyway; making it
	// explicit keeps the effective config honest. An unset XPSNR target stays
	// zero so validation can demand one instead of silently searching at the
	// VMAF default.
	target := float64(0)
	if raw.Video.Target != nil {
		target = *raw.Video.Target
	} else if metric == "vmaf" {
		target = DefaultTargetVMAF
	}

	return ProfileConfig{
		Name:      name,
		Container: normalizeContainer(orDefault(raw.Container, "mkv")),
		Crop: CropConfig{
			SeekOffsets:        orDefaultSlice(raw.Crop.SeekOffsets, DefaultCropSeekOffsets),
			FrameCount:         valueOr(raw.Crop.FrameCount, DefaultCropFrameCount),
			Limit:              valueOr(raw.Crop.Limit, DefaultCropDetectLimit),
			Round:              valueOr(raw.Crop.Round, DefaultCropDetectRound),
			ResetCount:         valueOr(raw.Crop.ResetCount, DefaultCropDetectResetCount),
			MinRetainedAreaPct: valueOr(raw.Crop.MinRetainedAreaPct, float64(DefaultCropMinRetainedAreaPct)),
			MinWidth:           valueOr(raw.Crop.MinWidth, DefaultCropMinWidth),
			MinHeight:          valueOr(raw.Crop.MinHeight, DefaultCropMinHeight),
			RequiredAlignment:  valueOr(raw.Crop.RequiredAlignment, DefaultCropRequiredAlignment),
		},
		Video: VideoConfig{
			Codec:              normalizeConfigVideoCodec(orDefault(raw.Video.Codec, "av1")),
			Accelerator:        video.NormalizeAccelerator(orDefault(raw.Video.Accelerator, "software")),
			Preset:             orDefault(raw.Video.Preset, "6"),
			BitDepth:           valueOr(raw.Video.BitDepth, video.DefaultBitDepth),
			CRFMin:             valueOr(raw.Video.CRFMin, DefaultCRFMin),
			CRFMax:             valueOr(raw.Video.CRFMax, DefaultCRFMax),
			Samples:            valueOr(raw.Video.Samples, 0),
			Metric:             metric,
			Target:             target,
			MinSavingsPercent:  valueOr(raw.Video.MinSavingsPercent, float64(DefaultMinSavingsPct)),
			ForceEncodeOnNoFit: raw.Video.ForceEncodeOnNoFit,
			SkipEncode:         raw.Video.SkipEncode,
			FFmpegArgs:         raw.Video.FFmpegArgs,
			ABAV1Args:          raw.Video.ABAV1Args,
			Overrides:          overrides,
		},
		Audio: AudioConfig{
			LanguagesToKeep:   raw.Audio.LanguagesToKeep,
			KeepCommentary:    raw.Audio.KeepCommentary,
			Fallback:          orDefault(raw.Audio.Fallback, DefaultStreamFallback),
			UnknownAsOriginal: raw.Audio.UnknownAsOriginal,
		},
		Subtitles: SubtitleConfig{
			LanguagesToKeep:   raw.Subtitles.LanguagesToKeep,
			KeepForced:        raw.Subtitles.KeepForced,
			KeepSDH:           raw.Subtitles.KeepSDH,
			KeepCommentary:    raw.Subtitles.KeepCommentary,
			Fallback:          orDefault(raw.Subtitles.Fallback, DefaultStreamFallback),
			UnknownAsOriginal: raw.Subtitles.UnknownAsOriginal,
		},
		Validation: ValidationConfig{
			DurationToleranceSeconds: raw.Validation.DurationToleranceSeconds,
		},
		Metadata: MetadataConfig{
			Mode:        orDefault(raw.Metadata.Mode, DefaultMetadataMode),
			TrackTitles: strings.ToLower(orDefault(raw.Metadata.TrackTitles, DefaultTrackTitleMode)),
		},
		Attachments: ModeConfig{
			Mode: orDefault(raw.Attachments.Mode, DefaultMetadataMode),
		},
		Chapters: ModeConfig{
			Mode: orDefault(raw.Chapters.Mode, DefaultMetadataMode),
		},
	}, nil
}

func resolveOverrides(profileName string, raw map[string]VideoOverrideConfig) (map[string]VideoOverrideConfig, error) {
	if raw == nil {
		return nil, nil
	}
	if problems := videoOverrideKeyProblems(profileName, raw); len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "\n- "))
	}
	overrides := make(map[string]VideoOverrideConfig, len(raw))
	for key, override := range raw {
		if override.Codec != nil && strings.TrimSpace(*override.Codec) != "" {
			codec := normalizeConfigVideoCodec(*override.Codec)
			override.Codec = &codec
		}
		if override.Accelerator != nil {
			accelerator := video.NormalizeAccelerator(*override.Accelerator)
			override.Accelerator = &accelerator
		}
		if override.Metric != nil {
			metric := strings.ToLower(strings.TrimSpace(*override.Metric))
			override.Metric = &metric
		}
		overrides[canonicalVideoOverrideKey(key)] = override
	}
	return overrides, nil
}

func resolveLibrary(name string, raw rawLibraryConfig) (LibraryConfig, error) {
	if raw.ScanInterval != nil && raw.ScanInterval.Duration <= 0 {
		return LibraryConfig{}, fmt.Errorf("library %q scan_interval must be greater than zero", name)
	}
	kind := orDefault(raw.Kind, DefaultLibraryKind)
	library := LibraryConfig{
		Name:             name,
		Kind:             kind,
		Path:             raw.Path,
		Profile:          orDefault(raw.Profile, DefaultProfileName),
		ScanInterval:     valueOr(raw.ScanInterval, Duration{}),
		Priority:         raw.Priority,
		Include:          raw.Include,
		Exclude:          raw.Exclude,
		IgnoreRegex:      raw.IgnoreRegex,
		ConcurrencyLimit: raw.ConcurrencyLimit,
		Arr:              raw.Arr,
		Media:            raw.Media,
	}
	if kind != "download" {
		library.Media.ReplacementMode = orDefault(library.Media.ReplacementMode, DefaultReplacementMode)
		return library, nil
	}

	ignorableGlobs := raw.Download.IgnorableGlobs
	if len(ignorableGlobs) == 0 {
		ignorableGlobs = append([]string(nil), DefaultIgnorableGlobs...)
	}
	library.Download = DownloadLibraryConfig{
		HandoffPath:          raw.Download.HandoffPath,
		StableFor:            valueOr(raw.Download.StableFor, Duration{Duration: DefaultStableFor}),
		PackageMode:          orDefault(raw.Download.PackageMode, DefaultPackageMode),
		HandoffMode:          orDefault(raw.Download.HandoffMode, DefaultHandoffMode),
		PreserveRelativePath: raw.Download.PreserveRelativePath,
		CleanupSourceMedia:   raw.Download.CleanupSourceMedia,
		PruneEmptyDirs:       raw.Download.PruneEmptyDirs,
		IgnorableGlobs:       ignorableGlobs,
	}
	return library, nil
}

func valueOr[T any](value *T, fallback T) T {
	if value == nil {
		return fallback
	}
	return *value
}

func orDefault(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func orDefaultSlice[T any](value []T, fallback []T) []T {
	if len(value) == 0 {
		return append([]T(nil), fallback...)
	}
	return value
}

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
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
		Daemon:        resolveDaemon(raw.Daemon),
		DaemonSources: fileDaemonSources(raw.Daemon),
		Web: WebConfig{
			Enabled:              valueOr(raw.Web.Enabled, true),
			Listen:               orDefault(raw.Web.Listen, DefaultWebListen),
			PasswordFile:         strings.TrimSpace(raw.Web.PasswordFile),
			Password:             raw.Web.Password,
			AllowUnauthenticated: raw.Web.AllowUnauthenticated,
		},
		Arrs:      map[string]ArrConfig{},
		Libraries: map[string]LibraryConfig{},
		Legacy: LegacyConfig{
			Arrs: make(map[string]ArrConfig, len(raw.Arrs)),
		},
	}
	for section, present := range map[string]bool{"arrs": raw.Arrs != nil, "profiles": raw.Profiles != nil, "libraries": raw.Libraries != nil} {
		if present {
			cfg.Legacy.Sections = append(cfg.Legacy.Sections, section)
		}
	}
	sort.Strings(cfg.Legacy.Sections)

	for name, arr := range raw.Arrs {
		arr.Name = name
		cfg.Legacy.Arrs[name] = arr
	}

	if raw.Profiles == nil {
		raw.Profiles = make(map[string]rawProfileConfig)
	}
	if _, exists := raw.Profiles[DefaultProfileName]; !exists {
		raw.Profiles[DefaultProfileName] = rawProfileConfig{}
	}
	cfg.Legacy.Profiles = make(map[string]ProfileConfig, len(raw.Profiles))
	for name, profile := range raw.Profiles {
		resolved, err := resolveProfile(name, profile)
		if err != nil {
			return Config{}, err
		}
		cfg.Legacy.Profiles[name] = resolved
	}

	cfg.Legacy.Libraries = make(map[string]LegacyLibraryConfig, len(raw.Libraries))
	for name, library := range raw.Libraries {
		resolved, err := resolveLibrary(name, library)
		if err != nil {
			return Config{}, err
		}
		cfg.Legacy.Libraries[name] = resolved
	}

	return cfg, nil
}

// StoreOnScratch reports whether the store lies under the system's temporary
// directory or inside temp_dir, which is where it ends up when neither
// store_path nor temp_dir is set. Either may be cleared at boot or by a
// cleanup job, and the store is the only copy of the libraries, flows, Arr
// connections, and daemon settings, so the daemon warns about it.
func (d DaemonConfig) StoreOnScratch() bool {
	store, err := filepath.Abs(d.StorePath)
	if err != nil {
		return false
	}
	for _, dir := range []string{os.TempDir(), d.TempDir} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		dir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(dir, store); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// StoreOnScratchWarning is the warning the daemon and check-config give when
// StoreOnScratch holds.
const StoreOnScratchWarning = "the store holds the libraries, flows, Arr connections, and daemon settings, but lies in a temporary directory; set daemon.store_path to a file on persistent storage"

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
		MediaRoots:        slices.Clone(raw.MediaRoots),
		TempDir:           tempDir,
		StorePath:         storePath,
		ControlSocket:     controlSocket,
		WorkerCount:       valueOr(raw.WorkerCount, cpus),
		TotalThreads:      valueOr(raw.TotalThreads, cpus),
		MaxThreadsPerJob:  valueOr(raw.MaxThreadsPerJob, 0),
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
	// XPSNR has no default target; validation requires an explicit choice.
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
			SeekOffsets:        raw.Crop.SeekOffsets,
			Samples:            raw.Crop.Samples, // 0 infers the window count from the runtime.
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
			SampleDuration:     valueOr(raw.Video.SampleDuration, Duration{Duration: 20 * time.Second}),
			Metric:             metric,
			Target:             target,
			MinSavingsPercent:  valueOr(raw.Video.MinSavingsPercent, float64(DefaultMinSavingsPct)),
			ForceEncodeOnNoFit: raw.Video.ForceEncodeOnNoFit,
			SkipEncode:         raw.Video.SkipEncode,
			FFmpegArgs:         raw.Video.FFmpegArgs,
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

func resolveLibrary(name string, raw rawLibraryConfig) (LegacyLibraryConfig, error) {
	if raw.ScanInterval != nil && raw.ScanInterval.Duration <= 0 {
		return LegacyLibraryConfig{}, fmt.Errorf("library %q scan_interval must be greater than zero", name)
	}
	kind := orDefault(raw.Kind, DefaultLibraryKind)
	library := LegacyLibraryConfig{
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
	if kind != string(domain.LibraryKindDownload) {
		library.Media.ReplacementMode = orDefault(library.Media.ReplacementMode, DefaultReplacementMode)
		return library, nil
	}

	library.Download = DownloadLibraryConfig{
		HandoffPath:          raw.Download.HandoffPath,
		StableFor:            valueOr(raw.Download.StableFor, Duration{Duration: DefaultStableFor}),
		PackageMode:          orDefault(raw.Download.PackageMode, DefaultPackageMode),
		HandoffMode:          orDefault(raw.Download.HandoffMode, DefaultHandoffMode),
		PreserveRelativePath: raw.Download.PreserveRelativePath,
		CleanupSourceMedia:   raw.Download.CleanupSourceMedia,
		PruneEmptyDirs:       raw.Download.PruneEmptyDirs,
		IgnorableGlobs:       EffectiveIgnorableGlobs(raw.Download.IgnorableGlobs),
	}
	return library, nil
}

// EffectiveIgnorableGlobs returns the residue globs of a download library:
// the given list, or the built-in default when it is empty.
func EffectiveIgnorableGlobs(globs []string) []string {
	if len(globs) == 0 {
		return append([]string(nil), DefaultIgnorableGlobs...)
	}
	return globs
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

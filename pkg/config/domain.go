package config

import (
	"fmt"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func (c Config) FindLibrary(name domain.LibraryName) (LibraryConfig, bool) {
	library, ok := c.Libraries[string(name)]
	if ok {
		library.Name = string(name)
		return library, true
	}
	return LibraryConfig{}, false
}

func (c Config) FindProfile(name string) (ProfileConfig, bool) {
	profile, ok := c.Profiles[name]
	if ok {
		profile.Name = name
		return profile, true
	}
	return ProfileConfig{}, false
}

func (c Config) FindArr(name string) (ArrConfig, bool) {
	arr, ok := c.Arrs[name]
	if ok {
		arr.Name = name
	}
	return arr, ok
}

func (c Config) ResolveForLibrary(name domain.LibraryName) (domain.Library, domain.Profile, error) {
	library, ok := c.FindLibrary(name)
	if !ok {
		return domain.Library{}, domain.Profile{}, fmt.Errorf("library %q not found", name)
	}
	profile, ok := c.FindProfile(library.Profile)
	if !ok {
		return domain.Library{}, domain.Profile{}, fmt.Errorf("profile %q not found", library.Profile)
	}
	var arr ArrConfig
	if library.Arr != "" {
		var ok bool
		arr, ok = c.FindArr(library.Arr)
		if !ok {
			return domain.Library{}, domain.Profile{}, fmt.Errorf("arr %q not found", library.Arr)
		}
	}
	return library.ToDomain(arr), profile.ToDomain(), nil
}

func (c Config) ScanInterval() time.Duration {
	return c.Daemon.ScanInterval.Duration
}

func (c Config) FilesystemEventDebounce() time.Duration {
	return c.Daemon.FSDebounce.Duration
}

func (c Config) ScanIntervalForLibrary(name domain.LibraryName) time.Duration {
	library, ok := c.FindLibrary(name)
	if ok && library.ScanInterval.Duration > 0 {
		return library.ScanInterval.Duration
	}
	return c.Daemon.ScanInterval.Duration
}

func (c Config) SchedulerInterval() time.Duration {
	return c.Daemon.SchedulerInterval.Duration
}

func (c Config) LeaseDuration() time.Duration {
	return c.Daemon.LeaseDuration.Duration
}

func (c Config) ShutdownTimeout() time.Duration {
	return c.Daemon.ShutdownTimeout.Duration
}

func (c Config) StagingCleanupAge() time.Duration {
	return c.Daemon.StagingCleanupAge.Duration
}

func (l LibraryConfig) ToDomain(arr ArrConfig) domain.Library {
	return domain.Library{
		Name:             domain.LibraryName(l.Name),
		Kind:             domain.LibraryKind(l.Kind),
		Path:             l.Path,
		Priority:         l.Priority,
		ProfileName:      domain.ProfileName(l.Profile),
		IncludeGlobs:     append([]string(nil), l.Include...),
		ExcludeGlobs:     append([]string(nil), l.Exclude...),
		ConcurrencyLimit: l.ConcurrencyLimit,
		Metadata: domain.MetadataProviderPolicy{
			Provider:   domain.MetadataProviderKind(arrProvider(arr)),
			BaseURL:    arr.BaseURL,
			APIKey:     arr.APIKey,
			APIKeyFile: arr.APIKeyFile,
		},
		Media: domain.MediaLibraryPolicy{
			ReplacementMode: domain.ReplacementMode(l.Media.ReplacementMode),
		},
		Download: domain.DownloadLibraryPolicy{
			HandoffPath:          l.Download.HandoffPath,
			StableFor:            l.Download.StableFor.Duration,
			PackageMode:          domain.DownloadPackageMode(l.Download.PackageMode),
			HandoffMode:          domain.HandoffMode(l.Download.HandoffMode),
			PreserveRelativePath: l.Download.PreserveRelativePath,
			CleanupSourceMedia:   l.Download.CleanupSourceMedia,
			PruneEmptyDirs:       l.Download.PruneEmptyDirs,
			IgnorableGlobs:       append([]string(nil), l.Download.IgnorableGlobs...),
		},
	}
}

func (p ProfileConfig) ToDomain() domain.Profile {
	var videoOverrides map[string]domain.VideoOverride
	if p.Video.Overrides != nil {
		videoOverrides = make(map[string]domain.VideoOverride, len(p.Video.Overrides))
		for key, override := range p.Video.Overrides {
			videoOverrides[key] = domain.VideoOverride{
				Codec:              clonePointer(override.Codec),
				Accelerator:        clonePointer(override.Accelerator),
				Preset:             clonePointer(override.Preset),
				BitDepth:           clonePointer(override.BitDepth),
				CRFMin:             clonePointer(override.CRFMin),
				CRFMax:             clonePointer(override.CRFMax),
				Metric:             qualityMetricPointer(override.Metric),
				Target:             clonePointer(override.Target),
				MinSavingsPercent:  clonePointer(override.MinSavingsPercent),
				ForceEncodeOnNoFit: clonePointer(override.ForceEncodeOnNoFit),
				SkipEncode:         clonePointer(override.SkipEncode),
				FFmpegArgs:         append([]string(nil), override.FFmpegArgs...),
				ABAV1Args:          append([]string(nil), override.ABAV1Args...),
			}
		}
	}

	return domain.Profile{
		Name:      domain.ProfileName(p.Name),
		Container: p.Container,
		Crop: domain.CropPolicy{
			SeekOffsets:            stdDurations(p.Crop.SeekOffsets),
			FrameCount:             p.Crop.FrameCount,
			Limit:                  p.Crop.Limit,
			Round:                  p.Crop.Round,
			ResetCount:             p.Crop.ResetCount,
			MinRetainedAreaPercent: p.Crop.MinRetainedAreaPct,
			MinWidth:               p.Crop.MinWidth,
			MinHeight:              p.Crop.MinHeight,
			RequiredAlignment:      p.Crop.RequiredAlignment,
		},
		Video: domain.VideoProfile{
			Codec:              p.Video.Codec,
			Accelerator:        p.Video.Accelerator,
			Preset:             p.Video.Preset,
			BitDepth:           p.Video.BitDepth,
			CRFMin:             p.Video.CRFMin,
			CRFMax:             p.Video.CRFMax,
			Samples:            p.Video.Samples,
			Metric:             domain.QualityMetric(p.Video.Metric),
			Target:             p.Video.Target,
			MinSavingsPercent:  p.Video.MinSavingsPercent,
			ForceEncodeOnNoFit: p.Video.ForceEncodeOnNoFit,
			SkipEncode:         p.Video.SkipEncode,
			FFmpegArgs:         append([]string(nil), p.Video.FFmpegArgs...),
			ABAV1Args:          append([]string(nil), p.Video.ABAV1Args...),
			Overrides:          videoOverrides,
		},
		Audio: domain.AudioProfile{
			LanguagesToKeep:   append([]string(nil), p.Audio.LanguagesToKeep...),
			KeepCommentary:    p.Audio.KeepCommentary,
			Fallback:          domain.StreamFallback(p.Audio.Fallback),
			UnknownAsOriginal: p.Audio.UnknownAsOriginal,
		},
		Subtitles: domain.SubtitleProfile{
			LanguagesToKeep:   append([]string(nil), p.Subtitles.LanguagesToKeep...),
			KeepForced:        p.Subtitles.KeepForced,
			KeepSDH:           p.Subtitles.KeepSDH,
			KeepCommentary:    p.Subtitles.KeepCommentary,
			Fallback:          domain.StreamFallback(p.Subtitles.Fallback),
			UnknownAsOriginal: p.Subtitles.UnknownAsOriginal,
		},
		Validation: domain.ValidationPolicy{
			DurationToleranceSeconds: p.Validation.DurationToleranceSeconds,
		},
		Metadata: domain.MetadataPolicy{
			Mode:        domain.MetadataMode(p.Metadata.Mode),
			TrackTitles: domain.TrackTitleMode(p.Metadata.TrackTitles),
		},
		Attachments: domain.AttachmentPolicy{
			Mode: domain.MetadataMode(p.Attachments.Mode),
		},
		Chapters: domain.ChapterPolicy{
			Mode: domain.MetadataMode(p.Chapters.Mode),
		},
	}
}

func qualityMetricPointer(value *string) *domain.QualityMetric {
	if value == nil {
		return nil
	}
	metric := domain.QualityMetric(*value)
	return &metric
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func stdDurations(values []Duration) []time.Duration {
	result := make([]time.Duration, 0, len(values))
	for _, value := range values {
		result = append(result, value.Duration)
	}
	return result
}

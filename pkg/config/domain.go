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

// FindProfile looks a profile up among the legacy TOML profiles. The built-in
// default profile is always there.
func (c Config) FindProfile(name string) (ProfileConfig, bool) {
	profile, ok := c.Legacy.Profiles[name]
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

// ResolveLibrary returns a library of the runtime snapshot together with the
// Arr connection it names, in the shape the pipeline works with.
func (c Config) ResolveLibrary(name domain.LibraryName) (domain.Library, error) {
	library, ok := c.FindLibrary(name)
	if !ok {
		return domain.Library{}, fmt.Errorf("library %q not found", name)
	}
	var arr ArrConfig
	if library.Arr != "" {
		var ok bool
		arr, ok = c.FindArr(library.Arr)
		if !ok {
			return domain.Library{}, fmt.Errorf("arr %q not found", library.Arr)
		}
	}
	return library.ToDomain(arr), nil
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

// Runtime converts a TOML library into the shape the daemon runs libraries
// in. include, exclude, and ignore_regex become the equivalent rule list; the
// flow is left for the importer to assign from the profile name. A download
// library keeps its intake settings and reprocesses what it finds again; its
// handoff settings have no place on a library any more (a flow's move output
// says where a result goes), so the importer pauses it.
func (l LegacyLibraryConfig) Runtime() LibraryConfig {
	rules, fallback := LegacyRules(l.Include, l.Exclude, l.IgnoreRegex)
	library := LibraryConfig{
		Name:             l.Name,
		Path:             l.Path,
		ScanInterval:     l.ScanInterval,
		Priority:         l.Priority,
		ConcurrencyLimit: l.ConcurrencyLimit,
		Arr:              l.Arr,
		Rules:            rules,
		RuleFallback:     fallback,
	}
	if l.IsDownload() {
		library.Reprocess = true
		library.StableFor = l.Download.StableFor
		library.PackageMode = l.Download.PackageMode
		library.IgnorableGlobs = l.Download.IgnorableGlobs
	}
	return library.WithDefaults()
}

// IsDownload reports whether the TOML library processed downloader output
// and handed it off instead of replacing media in place.
func (l LegacyLibraryConfig) IsDownload() bool {
	return l.Kind == "download"
}

func (l LibraryConfig) ToDomain(arr ArrConfig) domain.Library {
	return domain.Library{
		Name:   domain.LibraryName(l.Name),
		Path:   l.Path,
		FlowID: l.FlowID,
		Metadata: domain.MetadataProviderPolicy{
			Provider:   domain.MetadataProviderKind(arr.Type),
			BaseURL:    arr.BaseURL,
			APIKey:     arr.APIKey,
			APIKeyFile: arr.APIKeyFile,
		},
		Reprocess:      l.Reprocess,
		IgnorableGlobs: append([]string(nil), l.IgnorableGlobs...),
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
			}
		}
	}

	return domain.Profile{
		Name:      domain.ProfileName(p.Name),
		Container: p.Container,
		Crop: domain.CropPolicy{
			SeekOffsets:            stdDurations(p.Crop.SeekOffsets),
			Samples:                p.Crop.Samples,
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
			SampleDuration:     p.Video.SampleDuration.Duration,
			Metric:             domain.QualityMetric(p.Video.Metric),
			Target:             p.Video.Target,
			MinSavingsPercent:  p.Video.MinSavingsPercent,
			ForceEncodeOnNoFit: p.Video.ForceEncodeOnNoFit,
			SkipEncode:         p.Video.SkipEncode,
			FFmpegArgs:         append([]string(nil), p.Video.FFmpegArgs...),
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
		// A TOML profile has no say in publication; the flow it becomes
		// replaces in place.
		Publish: domain.PublishPolicy{Mode: domain.PublishReplace},
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

package config

import (
	"fmt"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func (c Config) FindLibrary(name domain.LibraryName) (LibraryConfig, bool) {
	for _, library := range c.Libraries {
		if library.Name == string(name) {
			return library, true
		}
	}
	return LibraryConfig{}, false
}

func (c Config) FindFlow(name string) (FlowConfig, bool) {
	for _, flow := range c.Flows {
		if flow.Name == name {
			return flow, true
		}
	}
	return FlowConfig{}, false
}

func (c Config) FindProfile(name string) (ProfileConfig, bool) {
	for _, profile := range c.Profiles {
		if profile.Name == name {
			return profile, true
		}
	}
	return ProfileConfig{}, false
}

func (c Config) ResolveForLibrary(name domain.LibraryName) (domain.Library, domain.Flow, domain.Profile, error) {
	library, ok := c.FindLibrary(name)
	if !ok {
		return domain.Library{}, domain.Flow{}, domain.Profile{}, fmt.Errorf("library %q not found", name)
	}
	flow, ok := c.FindFlow(library.Flow)
	if !ok {
		return domain.Library{}, domain.Flow{}, domain.Profile{}, fmt.Errorf("flow %q not found", library.Flow)
	}
	profile, ok := c.FindProfile(library.Profile)
	if !ok {
		return domain.Library{}, domain.Flow{}, domain.Profile{}, fmt.Errorf("profile %q not found", library.Profile)
	}
	return library.ToDomain(), flow.ToDomain(), profile.ToDomain(), nil
}

func (c Config) ScanInterval() time.Duration {
	return mustDuration(c.Daemon.ScanInterval)
}

func (c Config) SchedulerInterval() time.Duration {
	return mustDuration(c.Daemon.SchedulerInterval)
}

func (c Config) LeaseDuration() time.Duration {
	return mustDuration(c.Daemon.LeaseDuration)
}

func (l LibraryConfig) ToDomain() domain.Library {
	stableFor, _ := time.ParseDuration(l.Download.StableFor)
	return domain.Library{
		Name:             domain.LibraryName(l.Name),
		Kind:             domain.LibraryKind(l.Kind),
		Path:             l.Path,
		Priority:         l.Priority,
		FlowName:         domain.FlowName(l.Flow),
		ProfileName:      domain.ProfileName(l.Profile),
		IncludeGlobs:     append([]string(nil), l.Include...),
		ExcludeGlobs:     append([]string(nil), l.Exclude...),
		ConcurrencyLimit: l.ConcurrencyLimit,
		Metadata: domain.MetadataProviderPolicy{
			Provider: domain.MetadataProviderKind(l.Metadata.Provider),
			BaseURL:  l.Metadata.BaseURL,
			APIKey:   l.Metadata.APIKey,
		},
		Media: domain.MediaLibraryPolicy{
			ReplacementMode: domain.ReplacementMode(l.Media.ReplacementMode),
		},
		Download: domain.DownloadLibraryPolicy{
			HandoffPath:          l.Download.HandoffPath,
			StableFor:            stableFor,
			PackageMode:          domain.DownloadPackageMode(l.Download.PackageMode),
			HandoffMode:          domain.HandoffMode(l.Download.HandoffMode),
			PreserveRelativePath: l.Download.PreserveRelativePath,
			CleanupSourceMedia:   l.Download.CleanupSourceMedia,
			PruneEmptyDirs:       l.Download.PruneEmptyDirs,
			IgnorableGlobs:       append([]string(nil), l.Download.IgnorableGlobs...),
		},
	}
}

func (f FlowConfig) ToDomain() domain.Flow {
	steps := make([]domain.FlowStep, 0, len(f.Steps))
	for _, step := range f.Steps {
		steps = append(steps, domain.FlowStep{Name: step})
	}
	return domain.Flow{
		Name:  domain.FlowName(f.Name),
		Steps: steps,
	}
}

func (p ProfileConfig) ToDomain() domain.Profile {
	return domain.Profile{
		Name:      domain.ProfileName(p.Name),
		Container: p.Container,
		Video: domain.VideoProfile{
			Codec:       p.Video.Codec,
			Preset:      p.Video.Preset,
			PixelFormat: p.Video.PixelFormat,
			CRFMin:      p.Video.CRFMin,
			CRFMax:      p.Video.CRFMax,
			TargetVMAF:  p.Video.TargetVMAF,
		},
		Audio: domain.AudioProfile{
			LanguagesToKeep:   append([]string(nil), p.Audio.LanguagesToKeep...),
			KeepCommentary:    p.Audio.KeepCommentary,
			Fallback:          domain.StreamFallback(p.Audio.Fallback),
			UnknownAsOriginal: p.Audio.UnknownAsOriginal,
		},
		Subtitles: domain.SubtitleProfile{
			Mode:               domain.StreamPolicyMode(p.Subtitles.Mode),
			PreferredLanguages: append([]string(nil), p.Subtitles.PreferredLanguages...),
			KeepForced:         p.Subtitles.KeepForced,
			KeepSDH:            p.Subtitles.KeepSDH,
			KeepCommentary:     p.Subtitles.KeepCommentary,
			KeepExternal:       p.Subtitles.KeepExternal,
			MaxTracks:          p.Subtitles.MaxTracks,
			Fallback:           domain.StreamFallback(p.Subtitles.Fallback),
		},
		Metadata: domain.MetadataPolicy{
			Mode: domain.MetadataMode(p.Metadata.Mode),
		},
		Attachments: domain.AttachmentPolicy{
			Mode: domain.MetadataMode(p.Attachments.Mode),
		},
		Chapters: domain.ChapterPolicy{
			Mode: domain.MetadataMode(p.Chapters.Mode),
		},
	}
}

func mustDuration(value string) time.Duration {
	duration, err := time.ParseDuration(value)
	if err != nil {
		panic(err)
	}
	return duration
}

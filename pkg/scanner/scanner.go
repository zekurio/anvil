package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/store"
)

type Store interface {
	UpsertMediaSource(context.Context, domain.MediaSource) (domain.MediaSource, error)
	UpsertMediaAsset(context.Context, domain.MediaAsset) (domain.MediaAsset, error)
	EnqueueJob(context.Context, store.EnqueueJobInput) (domain.Job, bool, error)
}

type Scanner struct {
	Store Store
	Now   func() time.Time
}

type ScanResult struct {
	Libraries       int
	Sources         int
	Assets          int
	EnqueuedJobs    int
	ExistingJobs    int
	SkippedIgnored  int
	SkippedUnstable int
}

func (s Scanner) Scan(ctx context.Context, cfg config.Config) (ScanResult, error) {
	var result ScanResult
	for _, library := range cfg.Libraries {
		libraryResult, err := s.ScanLibrary(ctx, library)
		if err != nil {
			return result, fmt.Errorf("scan library %q: %w", library.Name, err)
		}
		result.add(libraryResult)
	}
	return result, nil
}

func (s Scanner) ScanLibrary(ctx context.Context, library config.LibraryConfig) (ScanResult, error) {
	if s.Store == nil {
		return ScanResult{}, fmt.Errorf("scanner store is required")
	}
	root := strings.TrimSpace(library.Path)
	if root == "" {
		return ScanResult{}, fmt.Errorf("library path is required")
	}

	now := s.now()
	candidates, sourceStats, skipped, err := discoverCandidates(ctx, root, library)
	if err != nil {
		return ScanResult{}, err
	}

	discovered := groupCandidates(library, candidates, sourceStats)
	result := ScanResult{
		Libraries:      1,
		SkippedIgnored: skipped,
	}

	stableFor, err := parseStableFor(library)
	if err != nil {
		return result, err
	}

	for _, source := range discovered {
		if source.isDownload && !source.stable(now, stableFor) {
			result.SkippedUnstable += len(source.assets)
			continue
		}

		storedSource, err := s.Store.UpsertMediaSource(ctx, domain.MediaSource{
			LibraryName:  domain.LibraryName(library.Name),
			Kind:         source.kind,
			RelativePath: source.relativePath,
			Status:       domain.MediaSourceActive,
			Fingerprint: domain.FileFingerprint{
				SizeBytes: source.sizeBytes,
				ModTime:   source.modTime,
			},
			LastSeenAt: now,
		})
		if err != nil {
			return result, fmt.Errorf("upsert source %q: %w", source.relativePath, err)
		}
		result.Sources++

		for _, asset := range source.assets {
			storedAsset, err := s.Store.UpsertMediaAsset(ctx, domain.MediaAsset{
				SourceID:     storedSource.ID,
				RelativePath: asset.assetRelativePath,
				Role:         asset.role,
				Status:       domain.MediaAssetActive,
				Fingerprint: domain.FileFingerprint{
					SizeBytes: asset.info.Size(),
					ModTime:   asset.info.ModTime(),
				},
				LastSeenAt: now,
			})
			if err != nil {
				return result, fmt.Errorf("upsert asset %q: %w", asset.libraryRelativePath, err)
			}
			result.Assets++

			if !enqueueableRole(asset.role) {
				result.SkippedIgnored++
				continue
			}

			_, inserted, err := s.Store.EnqueueJob(ctx, store.EnqueueJobInput{
				SourceID:    storedSource.ID,
				AssetID:     storedAsset.ID,
				LibraryName: storedSource.LibraryName,
				Priority:    library.Priority,
				Now:         now,
			})
			if err != nil {
				return result, fmt.Errorf("enqueue asset %q: %w", asset.libraryRelativePath, err)
			}
			if inserted {
				result.EnqueuedJobs++
			} else {
				result.ExistingJobs++
			}
		}
	}

	return result, nil
}

func (s Scanner) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *ScanResult) add(other ScanResult) {
	r.Libraries += other.Libraries
	r.Sources += other.Sources
	r.Assets += other.Assets
	r.EnqueuedJobs += other.EnqueuedJobs
	r.ExistingJobs += other.ExistingJobs
	r.SkippedIgnored += other.SkippedIgnored
	r.SkippedUnstable += other.SkippedUnstable
}

type candidate struct {
	libraryRelativePath string
	assetRelativePath   string
	sourceRelativePath  string
	sourceKind          domain.SourceKind
	role                domain.MediaAssetRole
	info                fs.FileInfo
}

type discoveredSource struct {
	relativePath string
	kind         domain.SourceKind
	isDownload   bool
	sizeBytes    int64
	modTime      time.Time
	assets       []candidate
}

type sourceStat struct {
	sizeBytes int64
	modTime   time.Time
}

func discoverCandidates(ctx context.Context, root string, library config.LibraryConfig) ([]candidate, map[string]sourceStat, int, error) {
	var candidates []candidate
	sourceStats := make(map[string]sourceStat)
	var skipped int
	excludePatterns := effectiveExcludeGlobs(library)

	err := filepath.WalkDir(root, func(absPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		if absPath == root {
			return nil
		}

		rel, err := filepath.Rel(root, absPath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		excluded, err := matchesAny(excludePatterns, rel)
		if err != nil {
			return err
		}
		if excluded {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			skipped++
			return nil
		}

		if entry.IsDir() {
			excludedDir, err := matchesAny(excludePatterns, rel+"/")
			if err != nil {
				return err
			}
			if excludedDir {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeType != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		recordSourceStat(sourceStats, library, rel, info)

		if !likelyMediaFile(rel) {
			return nil
		}

		included, err := includedBy(library.Include, rel)
		if err != nil {
			return err
		}
		if !included {
			skipped++
			return nil
		}

		role := classifyMediaAsset(rel)
		if role == domain.MediaAssetRoleSample {
			skipped++
			return nil
		}

		sourceRel, assetRel, sourceKind := sourceFor(library, rel)
		candidates = append(candidates, candidate{
			libraryRelativePath: rel,
			assetRelativePath:   assetRel,
			sourceRelativePath:  sourceRel,
			sourceKind:          sourceKind,
			role:                role,
			info:                info,
		})
		return nil
	})
	if err != nil {
		return nil, nil, skipped, err
	}

	return candidates, sourceStats, skipped, nil
}

func effectiveExcludeGlobs(library config.LibraryConfig) []string {
	exclude := append([]string(nil), library.Exclude...)
	if library.Kind == "download" {
		exclude = append(exclude, library.Download.IgnorableGlobs...)
	}
	return exclude
}

func groupCandidates(library config.LibraryConfig, candidates []candidate, sourceStats map[string]sourceStat) []discoveredSource {
	bySource := make(map[string]*discoveredSource)
	var order []string

	for _, asset := range candidates {
		key := string(asset.sourceKind) + "\x00" + asset.sourceRelativePath
		source, exists := bySource[key]
		if !exists {
			source = &discoveredSource{
				relativePath: asset.sourceRelativePath,
				kind:         asset.sourceKind,
				isDownload:   library.Kind == "download",
			}
			if stat, ok := sourceStats[key]; ok {
				source.sizeBytes = stat.sizeBytes
				source.modTime = stat.modTime
			}
			bySource[key] = source
			order = append(order, key)
		}

		if _, ok := sourceStats[key]; !ok {
			source.sizeBytes += asset.info.Size()
			if asset.info.ModTime().After(source.modTime) {
				source.modTime = asset.info.ModTime()
			}
		}
		source.assets = append(source.assets, asset)
	}

	sources := make([]discoveredSource, 0, len(order))
	for _, key := range order {
		sources = append(sources, *bySource[key])
	}
	return sources
}

func recordSourceStat(stats map[string]sourceStat, library config.LibraryConfig, rel string, info fs.FileInfo) {
	sourceRel, _, sourceKind := sourceFor(library, rel)
	key := string(sourceKind) + "\x00" + sourceRel
	stat := stats[key]
	stat.sizeBytes += info.Size()
	if info.ModTime().After(stat.modTime) {
		stat.modTime = info.ModTime()
	}
	stats[key] = stat
}

func (s discoveredSource) stable(now time.Time, stableFor time.Duration) bool {
	if stableFor <= 0 {
		return true
	}
	return !s.modTime.After(now.Add(-stableFor))
}

func sourceFor(library config.LibraryConfig, rel string) (string, string, domain.SourceKind) {
	if library.Kind != "download" || library.Download.PackageMode == "file" {
		return rel, rel, domain.SourceKindFile
	}

	first, rest, nested := strings.Cut(rel, "/")
	if !nested {
		return rel, rel, domain.SourceKindFile
	}
	return first, rest, domain.SourceKindPackage
}

func parseStableFor(library config.LibraryConfig) (time.Duration, error) {
	if library.Kind != "download" {
		return 0, nil
	}
	stableFor := strings.TrimSpace(library.Download.StableFor)
	if stableFor == "" {
		stableFor = config.DefaultStableFor
	}
	duration, err := time.ParseDuration(stableFor)
	if err != nil {
		return 0, fmt.Errorf("parse download.stable_for: %w", err)
	}
	return duration, nil
}

func includedBy(patterns []string, rel string) (bool, error) {
	if len(patterns) == 0 {
		return true, nil
	}
	return matchesAny(patterns, rel)
}

func matchesAny(patterns []string, rel string) (bool, error) {
	rel = path.Clean(filepath.ToSlash(rel))
	base := path.Base(rel)

	for _, pattern := range patterns {
		pattern = strings.TrimSpace(filepath.ToSlash(pattern))
		if pattern == "" {
			continue
		}

		matched, err := doublestar.PathMatch(pattern, rel)
		if err != nil {
			return false, fmt.Errorf("invalid glob %q: %w", pattern, err)
		}
		if matched {
			return true, nil
		}

		if !strings.Contains(pattern, "/") {
			matched, err = doublestar.PathMatch(pattern, base)
			if err != nil {
				return false, fmt.Errorf("invalid glob %q: %w", pattern, err)
			}
			if matched {
				return true, nil
			}
		}
	}

	return false, nil
}

func likelyMediaFile(rel string) bool {
	switch strings.ToLower(path.Ext(rel)) {
	case ".mkv", ".mp4", ".m4v", ".mov", ".avi", ".webm", ".ts", ".m2ts":
		return true
	default:
		return false
	}
}

func classifyMediaAsset(rel string) domain.MediaAssetRole {
	lower := strings.ToLower(rel)
	base := strings.ToLower(path.Base(rel))
	if strings.Contains(lower, "/sample") || strings.Contains(base, "sample") {
		return domain.MediaAssetRoleSample
	}
	return domain.MediaAssetRolePrimaryVideo
}

func enqueueableRole(role domain.MediaAssetRole) bool {
	return role == domain.MediaAssetRolePrimaryVideo || role == domain.MediaAssetRoleVideo
}

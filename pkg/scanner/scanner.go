package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
	"github.com/zekurio/anvil/pkg/store"
)

type Store interface {
	BeginLibraryScan(context.Context, domain.LibraryName) (store.ScanToken, error)
	ApplyLibraryScan(context.Context, store.ScanToken, store.ApplyScanInput) (store.ApplyScanResult, error)
}

type Scanner struct {
	Store      Store
	Now        func() time.Time
	Completion *CompletionTracker
}

type ScanResult struct {
	Libraries       int
	Sources         int
	Assets          int
	EnqueuedJobs    int
	ExistingJobs    int
	SkippedIgnored  int
	SkippedUnstable int
	NextStableAt    time.Time
}

type LibraryPlan struct {
	ScanToken       store.ScanToken
	Library         config.LibraryConfig
	Candidates      []CandidatePlan
	SkippedIgnored  int
	SkippedUnstable int
	NextStableAt    time.Time
}

type CandidatePlan struct {
	LibraryName         domain.LibraryName
	LibraryKind         string
	LibraryRoot         string
	LibraryRelativePath string
	SourceRelativePath  string
	AssetRelativePath   string
	SourceKind          domain.SourceKind
	Role                domain.MediaAssetRole
	SizeBytes           int64
	ModTime             time.Time
	SourceSizeBytes     int64
	SourceModTime       time.Time
	Ignored             bool
	IgnoreReason        string
	Unstable            bool
	Enqueueable         bool
}

func (s Scanner) Scan(ctx context.Context, cfg config.Config) (ScanResult, error) {
	var result ScanResult
	for name, library := range cfg.Libraries {
		if library.Name == "" {
			library.Name = name
		}
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

	libraryName := domain.LibraryName(library.Name)
	token, err := s.Store.BeginLibraryScan(ctx, libraryName)
	if err != nil {
		return ScanResult{}, fmt.Errorf("begin library scan: %w", err)
	}
	plan, err := s.PlanLibrary(ctx, library)
	if err != nil {
		return ScanResult{}, err
	}
	plan.ScanToken = token
	return s.applyPlan(ctx, plan)
}

func (s Scanner) PlanLibrary(ctx context.Context, library config.LibraryConfig) (LibraryPlan, error) {
	root := strings.TrimSpace(library.Path)
	if root == "" {
		return LibraryPlan{}, fmt.Errorf("library path is required")
	}

	now := s.now()
	candidates, sourceStats, skipped, err := discoverCandidates(ctx, root, library, s.Completion)
	if err != nil {
		return LibraryPlan{}, err
	}

	stableFor, err := parseStableFor(library)
	if err != nil {
		return LibraryPlan{}, err
	}

	plan := LibraryPlan{
		Library:        library,
		SkippedIgnored: skipped,
	}
	for _, candidate := range candidates {
		stat := sourceStats[sourceKey(candidate.sourceKind, candidate.sourceRelativePath)]
		if stat.modTime.IsZero() {
			stat.sizeBytes = candidate.info.Size()
			stat.modTime = candidate.info.ModTime()
			if !s.Completion.CompletedSince(candidate.absolutePath, candidate.info.ModTime()) {
				stat.pendingModTime = candidate.info.ModTime()
			}
		}
		unstable := library.Kind == "download" && !stable(stat.pendingModTime, now, stableFor)
		if candidate.ignored {
			unstable = false
		}
		enqueueable := !candidate.ignored && !unstable && enqueueableRole(candidate.role)
		if unstable {
			plan.SkippedUnstable++
			stableAt := stat.pendingModTime.Add(stableFor).UTC()
			if plan.NextStableAt.IsZero() || stableAt.Before(plan.NextStableAt) {
				plan.NextStableAt = stableAt
			}
		}
		plan.Candidates = append(plan.Candidates, CandidatePlan{
			LibraryName:         domain.LibraryName(library.Name),
			LibraryKind:         library.Kind,
			LibraryRoot:         root,
			LibraryRelativePath: candidate.libraryRelativePath,
			SourceRelativePath:  candidate.sourceRelativePath,
			AssetRelativePath:   candidate.assetRelativePath,
			SourceKind:          candidate.sourceKind,
			Role:                candidate.role,
			SizeBytes:           candidate.info.Size(),
			ModTime:             candidate.info.ModTime().UTC(),
			SourceSizeBytes:     stat.sizeBytes,
			SourceModTime:       stat.modTime.UTC(),
			Ignored:             candidate.ignored,
			IgnoreReason:        candidate.ignoreReason,
			Unstable:            unstable,
			Enqueueable:         enqueueable,
		})
	}
	return plan, nil
}

func (s Scanner) applyPlan(ctx context.Context, plan LibraryPlan) (ScanResult, error) {
	result := ScanResult{
		Libraries:       1,
		SkippedIgnored:  plan.SkippedIgnored,
		SkippedUnstable: plan.SkippedUnstable,
		NextStableAt:    plan.NextStableAt,
	}
	now := s.now()
	entries := make([]store.ScanEntry, 0, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		entries = append(entries, store.ScanEntry{
			SourceKind: candidate.SourceKind, SourceRelativePath: candidate.SourceRelativePath,
			SourceFingerprint: domain.FileFingerprint{SizeBytes: candidate.SourceSizeBytes, ModTime: candidate.SourceModTime},
			AssetRelativePath: candidate.AssetRelativePath, AssetRole: candidate.Role,
			AssetFingerprint: domain.FileFingerprint{SizeBytes: candidate.SizeBytes, ModTime: candidate.ModTime},
			Persist:          !candidate.Ignored && !candidate.Unstable,
			Enqueue:          candidate.Enqueueable,
		})
	}
	applied, err := s.Store.ApplyLibraryScan(ctx, plan.ScanToken, store.ApplyScanInput{
		LibraryName: domain.LibraryName(plan.Library.Name), Priority: plan.Library.Priority,
		RequeueExisting: plan.Library.Kind == string(domain.LibraryKindDownload), Entries: entries, CompletedAt: now,
	})
	if err != nil {
		return result, fmt.Errorf("apply library scan: %w", err)
	}
	result.Sources = applied.Sources
	result.Assets = applied.Assets
	result.EnqueuedJobs = applied.EnqueuedJobs
	result.ExistingJobs = applied.ExistingJobs

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
	if !other.NextStableAt.IsZero() && (r.NextStableAt.IsZero() || other.NextStableAt.Before(r.NextStableAt)) {
		r.NextStableAt = other.NextStableAt
	}
}

type candidate struct {
	absolutePath        string
	libraryRelativePath string
	assetRelativePath   string
	sourceRelativePath  string
	sourceKind          domain.SourceKind
	role                domain.MediaAssetRole
	info                fs.FileInfo
	ignored             bool
	ignoreReason        string
}

type sourceStat struct {
	sizeBytes      int64
	modTime        time.Time
	pendingModTime time.Time
}

func discoverCandidates(ctx context.Context, root string, library config.LibraryConfig, completion *CompletionTracker) ([]candidate, map[string]sourceStat, int, error) {
	var candidates []candidate
	sourceStats := make(map[string]sourceStat)
	var skipped int
	excludePatterns := effectiveExcludeGlobs(library)
	ignoreRegexps, err := compileIgnoreRegexps(library.IgnoreRegex)
	if err != nil {
		return nil, nil, skipped, err
	}

	err = filepath.WalkDir(root, func(absPath string, entry fs.DirEntry, walkErr error) error {
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

		if ignoredByRegex(ignoreRegexps, rel, entry.IsDir()) {
			skipped++
			if entry.IsDir() {
				return filepath.SkipDir
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if likelyMediaFile(rel) {
				candidates = append(candidates, buildCandidate(library, absPath, rel, info, true, "ignore_regex"))
			}
			return nil
		}

		excluded, err := matchesAny(excludePatterns, rel)
		if err != nil {
			return err
		}
		if excluded {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if likelyMediaFile(rel) {
				candidates = append(candidates, buildCandidate(library, absPath, rel, info, true, "excluded"))
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

		if !likelyMediaFile(rel) {
			recordSourceStat(sourceStats, library, absPath, rel, info, completion)
			return nil
		}
		if replacepkg.IsAnvilCopyOutputPath(rel) {
			candidates = append(candidates, buildCandidate(library, absPath, rel, info, true, "anvil_output"))
			skipped++
			return nil
		}
		recordSourceStat(sourceStats, library, absPath, rel, info, completion)

		included, err := includedBy(library.Include, rel)
		if err != nil {
			return err
		}
		if !included {
			candidates = append(candidates, buildCandidate(library, absPath, rel, info, true, "not_included"))
			skipped++
			return nil
		}

		role := classifyMediaAsset(rel)
		if role == domain.MediaAssetRoleSample {
			candidates = append(candidates, buildCandidate(library, absPath, rel, info, true, "sample"))
			skipped++
			return nil
		}

		candidates = append(candidates, buildCandidate(library, absPath, rel, info, false, ""))
		return nil
	})
	if err != nil {
		return nil, nil, skipped, err
	}

	return candidates, sourceStats, skipped, nil
}

func compileIgnoreRegexps(patterns []string) ([]*regexp.Regexp, error) {
	if len(patterns) == 0 {
		return nil, nil
	}

	regexps := make([]*regexp.Regexp, 0, len(patterns))
	for i, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			return nil, fmt.Errorf("ignore_regex[%d] must not be empty", i)
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("ignore_regex[%d] is invalid: %w", i, err)
		}
		regexps = append(regexps, compiled)
	}
	return regexps, nil
}

func ignoredByRegex(patterns []*regexp.Regexp, rel string, isDir bool) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(rel) {
			return true
		}
		if isDir && pattern.MatchString(rel+"/") {
			return true
		}
	}
	return false
}

func buildCandidate(library config.LibraryConfig, absPath string, rel string, info fs.FileInfo, ignored bool, ignoreReason string) candidate {
	sourceRel, assetRel, sourceKind := sourceFor(library, rel)
	return candidate{
		absolutePath:        absPath,
		libraryRelativePath: rel,
		assetRelativePath:   assetRel,
		sourceRelativePath:  sourceRel,
		sourceKind:          sourceKind,
		role:                classifyMediaAsset(rel),
		info:                info,
		ignored:             ignored,
		ignoreReason:        ignoreReason,
	}
}

func effectiveExcludeGlobs(library config.LibraryConfig) []string {
	exclude := append([]string(nil), library.Exclude...)
	if library.Kind == "download" {
		exclude = append(exclude, library.Download.IgnorableGlobs...)
	}
	return exclude
}

func recordSourceStat(stats map[string]sourceStat, library config.LibraryConfig, absPath string, rel string, info fs.FileInfo, completion *CompletionTracker) {
	sourceRel, _, sourceKind := sourceFor(library, rel)
	key := sourceKey(sourceKind, sourceRel)
	stat := stats[key]
	stat.sizeBytes += info.Size()
	if info.ModTime().After(stat.modTime) {
		stat.modTime = info.ModTime()
	}
	if !completion.CompletedSince(absPath, info.ModTime()) && info.ModTime().After(stat.pendingModTime) {
		stat.pendingModTime = info.ModTime()
	}
	stats[key] = stat
}

func stable(modTime time.Time, now time.Time, stableFor time.Duration) bool {
	if stableFor <= 0 {
		return true
	}
	return !modTime.After(now.Add(-stableFor))
}

func sourceKey(kind domain.SourceKind, relativePath string) string {
	return string(kind) + "\x00" + relativePath
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

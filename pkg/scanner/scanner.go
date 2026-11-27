package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

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
	SourcePaths     []string
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
	// Denied is set on an ignored candidate the library's rules keep out, as
	// opposed to one the scanner leaves alone on its own account: a sample,
	// download residue, or one of Anvil's own outputs.
	Denied      bool
	Unstable    bool
	Enqueueable bool
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
	root := strings.TrimSpace(library.Path)
	if root == "" {
		return ScanResult{}, fmt.Errorf("library path is required")
	}
	return s.scan(ctx, library, root, nil)
}

// PlanLibrary observes a whole library without applying the result.
func (s Scanner) PlanLibrary(ctx context.Context, library config.LibraryConfig) (LibraryPlan, error) {
	root := strings.TrimSpace(library.Path)
	if root == "" {
		return LibraryPlan{}, fmt.Errorf("library path is required")
	}
	return s.planPaths(ctx, library, root, nil)
}

// scan takes the scan token before observing the filesystem, so the store can
// reject this result if a later scan of the same sources has already applied.
// A nil paths slice scans the whole library.
func (s Scanner) scan(ctx context.Context, library config.LibraryConfig, root string, paths []string) (ScanResult, error) {
	if s.Store == nil {
		return ScanResult{}, fmt.Errorf("scanner store is required")
	}
	token, err := s.Store.BeginLibraryScan(ctx, domain.LibraryName(library.Name))
	if err != nil {
		return ScanResult{}, fmt.Errorf("begin library scan: %w", err)
	}
	plan, err := s.planPaths(ctx, library, root, paths)
	if err != nil {
		return ScanResult{}, err
	}
	plan.ScanToken = token
	return s.applyPlan(ctx, plan)
}

func (s Scanner) planPaths(ctx context.Context, library config.LibraryConfig, root string, paths []string) (LibraryPlan, error) {
	now := s.now()
	candidates, sourceStats, skipped, err := discoverPaths(ctx, root, library, s.Completion, paths)
	if err != nil {
		return LibraryPlan{}, err
	}

	stableFor := downloadStableFor(library)

	plan := LibraryPlan{
		Library:        library,
		SourcePaths:    paths,
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
		unstable := library.IsDownload() && !stable(stat.pendingModTime, now, stableFor)
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
			Denied:              candidate.denied,
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
		SourcePaths: plan.SourcePaths, LibraryName: domain.LibraryName(plan.Library.Name), Priority: plan.Library.Priority,
		RequeueExisting: plan.Library.IsDownload(), Entries: entries, CompletedAt: now,
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
	denied              bool
}

type sourceStat struct {
	sizeBytes      int64
	modTime        time.Time
	pendingModTime time.Time
}

func discoverCandidates(ctx context.Context, root string, library config.LibraryConfig, completion *CompletionTracker) ([]candidate, map[string]sourceStat, int, error) {
	return discoverPaths(ctx, root, library, completion, nil)
}

// discovery is one walk over a library tree. It classifies every file it
// visits as a candidate, an ignored candidate, or a source-size contribution.
type discovery struct {
	ctx        context.Context
	root       string
	library    config.LibraryConfig
	completion *CompletionTracker
	rules      config.RuleSet
	// ignorable holds the residue globs of a download library, which are
	// never part of a package whatever the rules say.
	ignorable []string

	candidates []candidate
	stats      map[string]sourceStat
	skipped    int
}

func discoverPaths(ctx context.Context, root string, library config.LibraryConfig, completion *CompletionTracker, paths []string) ([]candidate, map[string]sourceStat, int, error) {
	rules, err := config.CompileRules(library.Rules, library.RuleFallback)
	if err != nil {
		return nil, nil, 0, err
	}
	d := &discovery{
		ctx:        ctx,
		root:       root,
		library:    library,
		completion: completion,
		rules:      rules,
		ignorable:  ignorableGlobs(library),
		stats:      make(map[string]sourceStat),
	}
	if paths == nil {
		if err := filepath.WalkDir(root, d.visit); err != nil {
			return nil, nil, d.skipped, err
		}
		return d.candidates, d.stats, d.skipped, nil
	}
	for _, rel := range paths {
		walkRoot := filepath.Join(root, filepath.FromSlash(rel))
		skip, err := d.skipScopedRoot(walkRoot)
		if err != nil {
			return nil, nil, d.skipped, err
		}
		if skip {
			continue
		}
		if err := filepath.WalkDir(walkRoot, d.visit); err != nil {
			return nil, nil, d.skipped, err
		}
	}
	return d.candidates, d.stats, d.skipped, nil
}

// skipScopedRoot reports whether a scoped walk root must not be walked: it
// sits below a pruned directory a full walk would never have entered, or it
// no longer exists.
func (d *discovery) skipScopedRoot(walkRoot string) (bool, error) {
	if parent := filepath.Dir(walkRoot); parent != d.root && parent != filepath.Dir(parent) {
		rel, err := filepath.Rel(d.root, parent)
		if err != nil {
			return false, err
		}
		pruned, err := d.pruned(filepath.ToSlash(rel), true)
		if pruned || err != nil {
			return pruned, err
		}
	}
	if _, err := os.Lstat(walkRoot); errors.Is(err, fs.ErrNotExist) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	return false, nil
}

// pruned reports whether a path, or a directory above it, is out of the
// library: the rules prune it or it is ignorable download residue.
func (d *discovery) pruned(rel string, dir bool) (bool, error) {
	if d.rules.Prunes(rel, dir) {
		return true, nil
	}
	return ignorablePath(d.ignorable, rel, dir)
}

func (d *discovery) visit(absPath string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if err := d.ctx.Err(); err != nil {
		return err
	}
	if absPath == d.root {
		return nil
	}
	rel, err := filepath.Rel(d.root, absPath)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	if entry.IsDir() {
		return d.visitDir(rel)
	}
	return d.visitFile(absPath, rel, entry)
}

func (d *discovery) visitDir(rel string) error {
	if d.rules.Prunes(rel, true) {
		return filepath.SkipDir
	}
	ignorable, err := matchesExcludedPath(d.ignorable, rel, true)
	if err != nil {
		return err
	}
	if ignorable {
		return filepath.SkipDir
	}
	return nil
}

func (d *discovery) visitFile(absPath, rel string, entry fs.DirEntry) error {
	media := LikelyMediaFile(rel)
	// A pruned file is not part of the library: it is neither a candidate's
	// residue nor counted towards a package. Pruned media is still reported,
	// so a scan says what the rules kept out.
	if d.rules.Prunes(rel, false) {
		if !media {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		d.deny(absPath, rel, info, d.rules.Decide(config.RuleFile{Path: rel}).Reason)
		return nil
	}
	ignorable, err := matchesAny(d.ignorable, rel)
	if err != nil {
		return err
	}
	if ignorable {
		if !media {
			return nil
		}
		return d.skipEntry(absPath, rel, entry, "ignorable")
	}
	if entry.Type()&fs.ModeType != 0 {
		return nil
	}
	// Non-media files only matter as package residue that contributes to a
	// package's size and stability.
	if !media && (!d.library.GroupsPackages() || !strings.Contains(rel, "/")) {
		return nil
	}
	if strings.HasSuffix(rel, replacepkg.PartSuffix) {
		return nil
	}
	info, err := entry.Info()
	if err != nil {
		return err
	}
	if !media {
		recordSourceStat(d.stats, d.library, absPath, rel, info, d.completion)
		return nil
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
		d.skipped++
		return nil
	}
	if replacepkg.IsAnvilCopyOutputPath(rel) {
		d.skip(absPath, rel, info, "anvil_output")
		return nil
	}
	recordSourceStat(d.stats, d.library, absPath, rel, info, d.completion)
	if decision := d.rules.Decide(config.RuleFile{Path: rel, SizeBytes: info.Size()}); !decision.Allowed {
		d.deny(absPath, rel, info, decision.Reason)
		return nil
	}
	if classifyMediaAsset(rel) == domain.MediaAssetRoleSample {
		d.skip(absPath, rel, info, "sample")
		return nil
	}
	d.candidates = append(d.candidates, buildCandidate(d.library, absPath, rel, info, false, ""))
	return nil
}

func (d *discovery) skipEntry(absPath, rel string, entry fs.DirEntry, reason string) error {
	info, err := entry.Info()
	if err != nil {
		return err
	}
	d.skip(absPath, rel, info, reason)
	return nil
}

// skip records an ignored media file. It stays a candidate so the store can
// still track the occurrence, but it is never enqueued.
func (d *discovery) skip(absPath, rel string, info fs.FileInfo, reason string) {
	d.candidates = append(d.candidates, buildCandidate(d.library, absPath, rel, info, true, reason))
	d.skipped++
}

// deny records a media file the library's rules keep out.
func (d *discovery) deny(absPath, rel string, info fs.FileInfo, reason string) {
	d.skip(absPath, rel, info, reason)
	d.candidates[len(d.candidates)-1].denied = true
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

// ignorableGlobs are the residue patterns of a download library. Media
// libraries have none.
func ignorableGlobs(library config.LibraryConfig) []string {
	if !library.IsDownload() {
		return nil
	}
	return library.Download.IgnorableGlobs
}

// ignorablePath reports whether a path, or a directory above it, matches an
// ignorable glob.
func ignorablePath(patterns []string, rel string, dir bool) (bool, error) {
	if len(patterns) == 0 {
		return false, nil
	}
	rel = path.Clean(rel)
	for current := rel; current != "." && current != "/"; current = path.Dir(current) {
		matched, err := matchesExcludedPath(patterns, current, dir || current != rel)
		if matched || err != nil {
			return matched, err
		}
	}
	return false, nil
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
	if !library.GroupsPackages() {
		return rel, rel, domain.SourceKindFile
	}

	first, rest, nested := strings.Cut(rel, "/")
	if !nested {
		return rel, rel, domain.SourceKindFile
	}
	return first, rest, domain.SourceKindPackage
}

func matchesAny(patterns []string, rel string) (bool, error) {
	for _, pattern := range patterns {
		matched, err := config.GlobMatches(pattern, rel)
		if matched || err != nil {
			return matched, err
		}
	}
	return false, nil
}

// LikelyMediaFile reports whether a path has one of the extensions the
// scanner treats as media. It looks at the name only.
func LikelyMediaFile(rel string) bool {
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

// ScanPaths replaces only the sources touched by these absolute event paths.
// Directory events in file mode require a full scan to find removed descendants.
func (s Scanner) ScanPaths(ctx context.Context, library config.LibraryConfig, paths []string) (ScanResult, error) {
	root, err := filepath.Abs(library.Path)
	if err != nil {
		return ScanResult{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return ScanResult{}, fmt.Errorf("stat library root: %w", err)
	}
	if !info.IsDir() {
		return ScanResult{}, fmt.Errorf("library path is not a directory")
	}
	sources := make(map[string]struct{})
	for _, eventPath := range paths {
		rel, err := filepath.Rel(root, eventPath)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return s.ScanLibrary(ctx, library)
		}
		rel = filepath.ToSlash(rel)
		if !library.GroupsPackages() && !LikelyMediaFile(rel) {
			return s.ScanLibrary(ctx, library)
		}
		source, _, _ := sourceFor(library, rel)
		sources[source] = struct{}{}
	}
	if len(sources) == 0 {
		return s.ScanLibrary(ctx, library)
	}
	selected := make([]string, 0, len(sources))
	for source := range sources {
		selected = append(selected, source)
	}
	sort.Strings(selected)
	return s.scan(ctx, library, root, selected)
}

func matchesExcludedPath(patterns []string, rel string, directory bool) (bool, error) {
	matched, err := matchesAny(patterns, rel)
	if matched || err != nil || !directory {
		return matched, err
	}
	return matchesAny(patterns, rel+"/")
}

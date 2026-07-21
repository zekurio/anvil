package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/store"
)

func TestScanMediaLibraryEnqueuesRecursiveMediaFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := testNow()

	writeTestFile(t, root, "Movie.mkv", now.Add(-time.Hour))
	writeTestFile(t, root, "Nested/Episode.mp4", now.Add(-time.Hour))
	writeTestFile(t, root, "Nested/sample.mkv", now.Add(-time.Hour))
	writeTestFile(t, root, ".staging/Ignore.mkv", now.Add(-time.Hour))

	fake := newFakeStore()
	result, err := Scanner{
		Store: fake,
		Now: func() time.Time {
			return now
		},
	}.ScanLibrary(ctx, config.LibraryConfig{
		Name:    "movies",
		Kind:    "media",
		Path:    root,
		Include: []string{"*.mkv", "*.mp4"},
		Exclude: []string{"**/.staging/**"},
	})
	if err != nil {
		t.Fatalf("ScanLibrary() error = %v", err)
	}

	if result.Sources != 2 {
		t.Fatalf("sources = %d, want 2", result.Sources)
	}
	if result.Assets != 2 {
		t.Fatalf("assets = %d, want 2", result.Assets)
	}
	if result.EnqueuedJobs != 2 {
		t.Fatalf("enqueued jobs = %d, want 2", result.EnqueuedJobs)
	}
	if _, exists := fake.sourceByPath["movies\x00Nested/Episode.mp4"]; !exists {
		t.Fatal("nested media file was not discovered")
	}
}

func TestScanMediaLibraryIgnoresAnvilCopyOutputs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := testNow()

	writeTestFile(t, root, "Movie.mkv", now.Add(-time.Hour))
	writeTestFile(t, root, "Movie.anvil.mkv", now.Add(-time.Hour))
	writeTestFile(t, root, "Other.ANVIL.mkv", now.Add(-time.Hour))
	writeTestFile(t, root, "The.Anvil.2020.mkv", now.Add(-time.Hour))

	library := config.LibraryConfig{
		Name:    "movies",
		Kind:    "media",
		Path:    root,
		Include: []string{"*.mkv"},
	}
	plan, err := (Scanner{Now: func() time.Time { return now }}).PlanLibrary(ctx, library)
	if err != nil {
		t.Fatalf("PlanLibrary() error = %v", err)
	}

	byPath := make(map[string]CandidatePlan)
	for _, candidate := range plan.Candidates {
		byPath[candidate.LibraryRelativePath] = candidate
	}
	for _, path := range []string{"Movie.anvil.mkv", "Other.ANVIL.mkv"} {
		candidate := byPath[path]
		if !candidate.Ignored || candidate.IgnoreReason != "anvil_output" || candidate.Enqueueable {
			t.Fatalf("%s plan = %+v, want ignored anvil_output", path, candidate)
		}
	}
	for _, path := range []string{"Movie.mkv", "The.Anvil.2020.mkv"} {
		candidate := byPath[path]
		if candidate.Ignored || !candidate.Enqueueable {
			t.Fatalf("%s plan = %+v, want enqueueable", path, candidate)
		}
	}

	fake := newFakeStore()
	result, err := (Scanner{
		Store: fake,
		Now:   func() time.Time { return now },
	}).ScanLibrary(ctx, library)
	if err != nil {
		t.Fatalf("ScanLibrary() error = %v", err)
	}
	if result.EnqueuedJobs != 2 {
		t.Fatalf("enqueued jobs = %d, want 2", result.EnqueuedJobs)
	}
	if len(fake.jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(fake.jobs))
	}
}

func TestScanDownloadLibraryGroupsNestedPackageAssets(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := testNow()

	writeTestFile(t, root, "SomeShowS01/SomeShowS01E01/episode_1.mkv", now.Add(-time.Hour))
	writeTestFile(t, root, "SomeShowS01/SomeShowS01E02/episode_2.mkv", now.Add(-time.Hour))

	fake := newFakeStore()
	result, err := Scanner{
		Store: fake,
		Now: func() time.Time {
			return now
		},
	}.ScanLibrary(ctx, config.LibraryConfig{
		Name: "usenet-tv",
		Kind: "download",
		Path: root,
		Download: config.DownloadLibraryConfig{
			HandoffPath: "/imports/tv",
			StableFor:   "5m",
			PackageMode: "auto",
		},
	})
	if err != nil {
		t.Fatalf("ScanLibrary() error = %v", err)
	}

	if result.Sources != 1 {
		t.Fatalf("sources = %d, want 1", result.Sources)
	}
	if result.Assets != 2 {
		t.Fatalf("assets = %d, want 2", result.Assets)
	}
	if result.EnqueuedJobs != 2 {
		t.Fatalf("enqueued jobs = %d, want 2", result.EnqueuedJobs)
	}

	source, exists := fake.sourceByPath["usenet-tv\x00SomeShowS01"]
	if !exists {
		t.Fatal("package source SomeShowS01 was not discovered")
	}
	if source.Kind != domain.SourceKindPackage {
		t.Fatalf("source kind = %q, want %q", source.Kind, domain.SourceKindPackage)
	}
	if source.Fingerprint.SizeBytes == 0 {
		t.Fatal("package source size was not aggregated")
	}

	for _, job := range fake.jobs {
		if job.SourceID != source.ID {
			t.Fatalf("job source ID = %d, want package source ID %d", job.SourceID, source.ID)
		}
		if job.AssetID == 0 {
			t.Fatal("download package job has no asset ID")
		}
	}
	if _, exists := fake.assetByPath[sourceAssetKey(source.ID, "SomeShowS01E01/episode_1.mkv")]; !exists {
		t.Fatal("episode 1 asset path was not relative to package root")
	}
}

func TestScanDownloadLibrarySkipsUnstablePackage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := testNow()

	writeTestFile(t, root, "SomeShowS01/SomeShowS01E01/episode_1.mkv", now.Add(-time.Minute))

	fake := newFakeStore()
	result, err := Scanner{
		Store: fake,
		Now: func() time.Time {
			return now
		},
	}.ScanLibrary(ctx, config.LibraryConfig{
		Name: "usenet-tv",
		Kind: "download",
		Path: root,
		Download: config.DownloadLibraryConfig{
			HandoffPath: "/imports/tv",
			StableFor:   "5m",
			PackageMode: "auto",
		},
	})
	if err != nil {
		t.Fatalf("ScanLibrary() error = %v", err)
	}

	if result.Sources != 0 {
		t.Fatalf("sources = %d, want 0", result.Sources)
	}
	if result.EnqueuedJobs != 0 {
		t.Fatalf("enqueued jobs = %d, want 0", result.EnqueuedJobs)
	}
	if result.SkippedUnstable != 1 {
		t.Fatalf("skipped unstable = %d, want 1", result.SkippedUnstable)
	}
	if got, want := result.NextStableAt, now.Add(4*time.Minute).UTC(); !got.Equal(want) {
		t.Fatalf("next stable at = %s, want %s", got, want)
	}
}

func TestRealSQLiteScanDoesNotPersistUnstableEntry(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := testNow()
	writeTestFile(t, root, "Movie.mkv", now)
	state := openScannerTestStore(t, ctx)

	result, err := (Scanner{Store: state, Now: func() time.Time { return now }}).ScanLibrary(ctx, config.LibraryConfig{
		Name: "downloads", Kind: "download", Path: root,
		Download: config.DownloadLibraryConfig{StableFor: "1h", PackageMode: "file"},
	})
	if err != nil {
		t.Fatalf("ScanLibrary() error = %v", err)
	}
	if result.SkippedUnstable != 1 || result.EnqueuedJobs != 0 {
		t.Fatalf("unstable scan result = %+v", result)
	}
	if _, ok, err := state.FindMediaSourceByPath(ctx, "downloads", "Movie.mkv"); err != nil || ok {
		t.Fatalf("unstable source exists=%t err=%v, want absent", ok, err)
	}
}

func TestFailedDiscoveryDoesNotReconcileMissingEntries(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	root := filepath.Join(parent, "downloads")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	now := testNow()
	writeTestFile(t, root, "Movie.mkv", now.Add(-time.Hour))
	state := openScannerTestStore(t, ctx)
	library := config.LibraryConfig{
		Name: "downloads", Kind: "download", Path: root,
		Download: config.DownloadLibraryConfig{StableFor: "1m", PackageMode: "file"},
	}
	scanner := Scanner{Store: state, Now: func() time.Time { return now }}
	if _, err := scanner.ScanLibrary(ctx, library); err != nil {
		t.Fatalf("initial ScanLibrary() error = %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove scan root: %v", err)
	}
	if _, err := scanner.ScanLibrary(ctx, library); err == nil {
		t.Fatal("failed ScanLibrary() error = nil, want discovery failure")
	}
	source, ok, err := state.FindMediaSourceByPath(ctx, "downloads", "Movie.mkv")
	if err != nil || !ok || !source.Current || source.Status != domain.MediaSourceActive {
		t.Fatalf("source after failed discovery = %+v, ok=%t err=%v", source, ok, err)
	}
}

func TestScanDownloadLibraryStabilityIncludesCompanionFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := testNow()

	writeTestFile(t, root, "SomeShowS01/SomeShowS01E01/episode_1.mkv", now.Add(-time.Hour))
	writeTestFile(t, root, "SomeShowS01/release.nfo", now.Add(-time.Minute))

	fake := newFakeStore()
	result, err := Scanner{
		Store: fake,
		Now: func() time.Time {
			return now
		},
	}.ScanLibrary(ctx, config.LibraryConfig{
		Name: "usenet-tv",
		Kind: "download",
		Path: root,
		Download: config.DownloadLibraryConfig{
			HandoffPath: "/imports/tv",
			StableFor:   "5m",
			PackageMode: "auto",
		},
	})
	if err != nil {
		t.Fatalf("ScanLibrary() error = %v", err)
	}

	if result.EnqueuedJobs != 0 {
		t.Fatalf("enqueued jobs = %d, want 0", result.EnqueuedJobs)
	}
	if result.SkippedUnstable != 1 {
		t.Fatalf("skipped unstable = %d, want 1", result.SkippedUnstable)
	}
}

func TestScanDownloadLibraryIgnoresConfiguredIgnorableFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := testNow()

	writeTestFile(t, root, "SomeShowS01/SomeShowS01E01/episode_1.mkv", now.Add(-time.Hour))
	writeTestFile(t, root, "SomeShowS01/release.nzb", now.Add(-time.Minute))

	fake := newFakeStore()
	result, err := Scanner{
		Store: fake,
		Now: func() time.Time {
			return now
		},
	}.ScanLibrary(ctx, config.LibraryConfig{
		Name: "usenet-tv",
		Kind: "download",
		Path: root,
		Download: config.DownloadLibraryConfig{
			HandoffPath:    "/imports/tv",
			StableFor:      "5m",
			PackageMode:    "auto",
			IgnorableGlobs: []string{"**/*.nzb"},
		},
	})
	if err != nil {
		t.Fatalf("ScanLibrary() error = %v", err)
	}

	if result.EnqueuedJobs != 1 {
		t.Fatalf("enqueued jobs = %d, want 1", result.EnqueuedJobs)
	}
	if result.SkippedUnstable != 0 {
		t.Fatalf("skipped unstable = %d, want 0", result.SkippedUnstable)
	}
}

func TestScanDownloadLibraryIgnoresRegexMatchedPackageDirs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := testNow()

	writeTestFile(t, root, "_UNPACK_SomeShowS01/episode_1.mkv", now.Add(-time.Minute))
	writeTestFile(t, root, "SomeShowS01/episode_1.mkv", now.Add(-time.Hour))

	fake := newFakeStore()
	result, err := Scanner{
		Store: fake,
		Now: func() time.Time {
			return now
		},
	}.ScanLibrary(ctx, config.LibraryConfig{
		Name:        "usenet-tv",
		Kind:        "download",
		Path:        root,
		IgnoreRegex: []string{`(^|/)_UNPACK[^/]*(/|$)`},
		Download: config.DownloadLibraryConfig{
			HandoffPath: "/imports/tv",
			StableFor:   "5m",
			PackageMode: "auto",
		},
	})
	if err != nil {
		t.Fatalf("ScanLibrary() error = %v", err)
	}

	if result.EnqueuedJobs != 1 {
		t.Fatalf("enqueued jobs = %d, want 1", result.EnqueuedJobs)
	}
	if result.SkippedIgnored != 1 {
		t.Fatalf("skipped ignored = %d, want 1", result.SkippedIgnored)
	}
	if result.SkippedUnstable != 0 {
		t.Fatalf("skipped unstable = %d, want 0", result.SkippedUnstable)
	}
	if _, exists := fake.sourceByPath["usenet-tv\x00_UNPACK_SomeShowS01"]; exists {
		t.Fatal("_UNPACK package source was discovered")
	}
	if _, exists := fake.sourceByPath["usenet-tv\x00SomeShowS01"]; !exists {
		t.Fatal("stable package source was not discovered")
	}
}

func TestPlanLibraryReportsIgnoredAndEnqueueableCandidates(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := testNow()

	writeTestFile(t, root, "Movie.mkv", now.Add(-time.Hour))
	writeTestFile(t, root, "sample.mkv", now.Add(-time.Hour))
	writeTestFile(t, root, "Trailer.avi", now.Add(-time.Hour))
	writeTestFile(t, root, "Ignored.mkv", now.Add(-time.Hour))
	writeTestFile(t, root, "RegexIgnored.mkv", now.Add(-time.Hour))

	plan, err := (Scanner{
		Now: func() time.Time {
			return now
		},
	}).PlanLibrary(ctx, config.LibraryConfig{
		Name:    "movies",
		Kind:    "media",
		Path:    root,
		Include: []string{"*.mkv"},
		Exclude: []string{"Ignored.mkv"},
		IgnoreRegex: []string{
			`^RegexIgnored\.mkv$`,
		},
	})
	if err != nil {
		t.Fatalf("PlanLibrary() error = %v", err)
	}
	if len(plan.Candidates) != 5 {
		t.Fatalf("candidates len = %d, want 5", len(plan.Candidates))
	}

	byPath := make(map[string]CandidatePlan)
	for _, candidate := range plan.Candidates {
		byPath[candidate.LibraryRelativePath] = candidate
	}
	if !byPath["Movie.mkv"].Enqueueable || byPath["Movie.mkv"].Ignored {
		t.Fatalf("Movie.mkv plan = %+v, want enqueueable", byPath["Movie.mkv"])
	}
	for path, reason := range map[string]string{
		"sample.mkv":       "sample",
		"Trailer.avi":      "not_included",
		"Ignored.mkv":      "excluded",
		"RegexIgnored.mkv": "ignore_regex",
	} {
		candidate := byPath[path]
		if !candidate.Ignored || candidate.IgnoreReason != reason || candidate.Enqueueable {
			t.Fatalf("%s plan = %+v, want ignored reason %q", path, candidate, reason)
		}
	}
	if plan.SkippedIgnored != 4 {
		t.Fatalf("skipped ignored = %d, want 4", plan.SkippedIgnored)
	}
}

type fakeStore struct {
	nextSourceID domain.MediaSourceID
	nextAssetID  domain.MediaAssetID
	nextJobID    domain.JobID
	sourceByPath map[string]domain.MediaSource
	assetByPath  map[string]domain.MediaAsset
	jobs         []store.EnqueueJobInput
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		nextSourceID: 1,
		nextAssetID:  1,
		nextJobID:    1,
		sourceByPath: make(map[string]domain.MediaSource),
		assetByPath:  make(map[string]domain.MediaAsset),
	}
}

func (f *fakeStore) BeginLibraryScan(_ context.Context, libraryName domain.LibraryName) (store.ScanToken, error) {
	return store.ScanToken{LibraryName: libraryName, Sequence: 1}, nil
}

func (f *fakeStore) ApplyLibraryScan(_ context.Context, _ store.ScanToken, input store.ApplyScanInput) (store.ApplyScanResult, error) {
	result := store.ApplyScanResult{Applied: true}
	for _, entry := range input.Entries {
		if !entry.Persist {
			continue
		}
		sourceKey := string(input.LibraryName) + "\x00" + entry.SourceRelativePath
		source, exists := f.sourceByPath[sourceKey]
		if !exists {
			source = domain.MediaSource{ID: f.nextSourceID, LibraryName: input.LibraryName, Kind: entry.SourceKind, RelativePath: entry.SourceRelativePath, Generation: 1, Current: true, Status: domain.MediaSourceActive, Fingerprint: entry.SourceFingerprint}
			f.nextSourceID++
			f.sourceByPath[sourceKey] = source
			result.Sources++
		}
		assetKey := sourceAssetKey(source.ID, entry.AssetRelativePath)
		asset, exists := f.assetByPath[assetKey]
		if !exists {
			asset = domain.MediaAsset{ID: f.nextAssetID, SourceID: source.ID, RelativePath: entry.AssetRelativePath, Generation: 1, Current: true, Role: entry.AssetRole, Status: domain.MediaAssetActive, Fingerprint: entry.AssetFingerprint}
			f.nextAssetID++
			f.assetByPath[assetKey] = asset
			result.Assets++
		}
		if !entry.Enqueue {
			continue
		}
		duplicate := false
		for _, job := range f.jobs {
			if job.SourceID == source.ID && job.AssetID == asset.ID {
				duplicate = true
				break
			}
		}
		if duplicate {
			result.ExistingJobs++
			continue
		}
		f.jobs = append(f.jobs, store.EnqueueJobInput{SourceID: source.ID, AssetID: asset.ID, LibraryName: input.LibraryName, Priority: input.Priority, Now: input.CompletedAt})
		f.nextJobID++
		result.EnqueuedJobs++
	}
	return result, nil
}

func writeTestFile(t *testing.T, root, rel string, modTime time.Time) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("media"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
}

func openScannerTestStore(t *testing.T, ctx context.Context) *store.SQLiteStore {
	t.Helper()
	state, err := store.Open(ctx, filepath.Join(t.TempDir(), "anvil.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Fatalf("store.Close() error = %v", err)
		}
	})
	return state
}

func sourceAssetKey(sourceID domain.MediaSourceID, rel string) string {
	return fmt.Sprintf("%d\x00%s", sourceID, rel)
}

func testNow() time.Time {
	return time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
}

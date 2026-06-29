package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
)

func TestBuildPreflightReportShowsPlansWithoutExistingStore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	writePreflightFile(t, root, "Movie.mkv", now)

	cfg := testPreflightConfig(t, root)
	report, err := buildPreflightReport(ctx, cfg, options{
		command:        commandPreflight,
		libraryName:    "movies",
		preflightLimit: 1,
	}, nil, false)
	if err != nil {
		t.Fatalf("buildPreflightReport() error = %v", err)
	}
	if report.Summary.Candidates != 1 || report.Summary.Shown != 1 {
		t.Fatalf("summary = %+v, want one shown candidate", report.Summary)
	}
	if report.Summary.WouldEnqueue != 1 {
		t.Fatalf("would enqueue = %d, want 1", report.Summary.WouldEnqueue)
	}
	item := report.Candidates[0]
	if !strings.Contains(item.Paths.StagingDir, "job-<new>-attempt-<new>") {
		t.Fatalf("staging dir = %q, want new placeholders", item.Paths.StagingDir)
	}
	if item.Publish.Action != "copy" || !strings.HasSuffix(item.Publish.CopyPath, "Movie.anvil.mkv") {
		t.Fatalf("publish = %+v, want copy plan", item.Publish)
	}
	if !strings.Contains(item.Search.SavingsPolicy, "ab-av1/search") {
		t.Fatalf("savings policy = %q, want ab-av1/search language", item.Search.SavingsPolicy)
	}
	if !strings.Contains(item.Search.NoFitBehavior, "video-copy/remux") {
		t.Fatalf("no-fit behavior = %q, want remux fallback language", item.Search.NoFitBehavior)
	}
	if !item.Encode.Enabled || !strings.Contains(item.Encode.NoFitAction, "skip AV1 CRF encode") {
		t.Fatalf("encode plan = %+v, want AV1 no-fit fallback", item.Encode)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if bytes.Contains(encoded, []byte("larger-than-source")) {
		t.Fatalf("JSON output mentioned larger-than-source: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte(`"search_policy"`)) {
		t.Fatalf("JSON output missing search_policy: %s", encoded)
	}
}

func TestBuildPreflightReportShowsForcedNoFitEncodePolicy(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	writePreflightFile(t, root, "Movie.mkv", now)

	cfg := testPreflightConfig(t, root)
	profile := cfg.Profiles[config.DefaultProfileName]
	profile.Video.ForceEncodeOnNoFit = true
	cfg.Profiles[config.DefaultProfileName] = profile

	report, err := buildPreflightReport(ctx, cfg, options{
		command:        commandPreflight,
		libraryName:    "movies",
		preflightLimit: 1,
	}, nil, false)
	if err != nil {
		t.Fatalf("buildPreflightReport() error = %v", err)
	}
	item := report.Candidates[0]
	if !item.Search.ForceEncodeOnNoFit || item.Search.FlowCanFallbackToRemux {
		t.Fatalf("search policy = %+v, want forced no-fit encode without remux fallback", item.Search)
	}
	if !strings.Contains(item.Search.NoFitBehavior, "force an encode") {
		t.Fatalf("no-fit behavior = %q, want forced encode language", item.Search.NoFitBehavior)
	}
	if !strings.Contains(item.Encode.NoFitAction, "lowest tested CRF") {
		t.Fatalf("encode no-fit action = %q, want lowest tested CRF language", item.Encode.NoFitAction)
	}
	if !item.Profile.ForceEncodeOnNoFit {
		t.Fatalf("profile = %+v, want force_encode_on_no_fit", item.Profile)
	}
}

func TestBuildPreflightReportFindsExistingJobReadOnly(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	writePreflightFile(t, root, "Movie.mkv", now)

	cfg := testPreflightConfig(t, root)
	state := &fakePreflightStore{
		source: domain.MediaSource{ID: 12, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv"},
		asset:  domain.MediaAsset{ID: 34, SourceID: 12, RelativePath: "Movie.mkv", Role: domain.MediaAssetRolePrimaryVideo},
		job:    domain.Job{ID: 56, SourceID: 12, AssetID: 34, LibraryName: "movies", State: domain.JobStatePending},
	}
	report, err := buildPreflightReport(ctx, cfg, options{command: commandPreflight, libraryName: "movies"}, state, true)
	if err != nil {
		t.Fatalf("buildPreflightReport() error = %v", err)
	}
	item := report.Candidates[0]
	if !item.Status.AlreadyHasJob || item.Status.WouldEnqueueNewJob {
		t.Fatalf("status = %+v, want existing job and no new enqueue", item.Status)
	}
	if !strings.Contains(item.Paths.StagingDir, "job-56-attempt-<new>") {
		t.Fatalf("staging dir = %q, want existing job placeholder attempt", item.Paths.StagingDir)
	}
	if state.sourceLookups == 0 || state.assetLookups == 0 || state.jobLookups == 0 {
		t.Fatalf("lookups = source %d asset %d job %d, want read lookups", state.sourceLookups, state.assetLookups, state.jobLookups)
	}
}

func TestPrintPreflightReportHumanBasics(t *testing.T) {
	report := preflightReport{
		Summary: preflightSummary{Libraries: 1, Candidates: 1, Shown: 1, WouldEnqueue: 1, StoreReadOnly: true},
		Candidates: []preflightCandidate{{
			Library:     preflightLibrary{Name: "movies", Kind: "media", Root: "/media"},
			Source:      preflightSource{RelativePath: "Movie.mkv", Kind: domain.SourceKindFile},
			Asset:       preflightAsset{LibraryRelativePath: "Movie.mkv", RelativePath: "Movie.mkv", Role: domain.MediaAssetRolePrimaryVideo, ModTime: time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)},
			Status:      preflightStatus{Enqueueable: true, WouldEnqueueNewJob: true},
			Flow:        preflightFlow{Name: "flow", Steps: []string{"probe", "crf-search", "encode", "replace"}},
			Profile:     preflightProfile{Name: "profile", Container: "mkv", VideoCodec: "libsvtav1"},
			Search:      preflightSearchPolicy{Enabled: true, Tool: "ab-av1 crf-search", CRFMin: 18, CRFMax: 40, TargetVMAF: "95", SavingsPolicy: "ab-av1/search policy; explicit min-savings is not configured", NoFitBehavior: "if search decides AV1 fitting is not worthwhile, continue remaining configured actions as video-copy/remux/metadata processing without applying an AV1 CRF encode"},
			Encode:      preflightEncode{Enabled: true, VideoAction: "AV1 encode using CRF selected by search", Output: "/tmp/staging/job-<new>-attempt-<new>/output.mkv", NoFitAction: "if search policy decides AV1 fitting is not worthwhile, skip AV1 CRF encode and continue remaining configured actions as video-copy/remux/metadata processing"},
			Paths:       preflightPaths{Input: "/media/Movie.mkv", StagingDir: "/tmp/staging/job-<new>-attempt-<new>", Output: "/tmp/staging/job-<new>-attempt-<new>/output.mkv"},
			Publish:     preflightPublish{Action: "copy", CopyPath: "/media/Movie.anvil.mkv"},
			Cleanup:     preflightCleanup{StagingCleanupAction: "remove staging dir after configured cleanup step"},
			Description: "would enqueue new job",
		}},
	}
	output := captureStdout(t, func() {
		printPreflightReport(report)
	})
	for _, want := range []string{"preflight libraries=1", "savings_policy=ab-av1/search", "no-fit:", "video-copy/remux", "encode: enabled=true", "publish: copy"} {
		if !strings.Contains(output, want) {
			t.Fatalf("human output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "larger-than-source") {
		t.Fatalf("human output mentioned larger-than-source:\n%s", output)
	}
}

type fakePreflightStore struct {
	source        domain.MediaSource
	asset         domain.MediaAsset
	job           domain.Job
	sourceLookups int
	assetLookups  int
	jobLookups    int
}

func (f *fakePreflightStore) FindMediaSourceByPath(_ context.Context, libraryName domain.LibraryName, relativePath string) (domain.MediaSource, bool, error) {
	f.sourceLookups++
	return f.source, f.source.LibraryName == libraryName && f.source.RelativePath == relativePath, nil
}

func (f *fakePreflightStore) FindMediaAssetByPath(_ context.Context, sourceID domain.MediaSourceID, relativePath string) (domain.MediaAsset, bool, error) {
	f.assetLookups++
	return f.asset, f.asset.SourceID == sourceID && f.asset.RelativePath == relativePath, nil
}

func (f *fakePreflightStore) FindJobForTarget(_ context.Context, sourceID domain.MediaSourceID, assetID domain.MediaAssetID) (domain.Job, bool, error) {
	f.jobLookups++
	return f.job, f.job.SourceID == sourceID && f.job.AssetID == assetID, nil
}

func testPreflightConfig(t *testing.T, root string) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Daemon.TempDir = t.TempDir()
	cfg.Daemon.StorePath = filepath.Join(t.TempDir(), "missing.db")
	cfg.Libraries = map[string]config.LibraryConfig{
		"movies": {
			Name:    "movies",
			Kind:    "media",
			Path:    root,
			Flow:    config.DefaultFlowName,
			Profile: config.DefaultProfileName,
			Media: config.MediaLibraryConfig{
				ReplacementMode: string(domain.ReplacementModeCopy),
			},
		},
	}
	return cfg
}

func writePreflightFile(t *testing.T, root string, rel string, modTime time.Time) {
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

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	os.Stdout = write
	defer func() {
		os.Stdout = old
	}()
	fn()
	if err := write.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(read); err != nil {
		t.Fatalf("ReadFrom() error = %v", err)
	}
	return buf.String()
}

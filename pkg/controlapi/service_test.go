package controlapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
	"github.com/zekurio/anvil/pkg/store"
)

func TestListJobsMatchesExactSourceAssetAndDestinationPaths(t *testing.T) {
	ctx := context.Background()
	service, state, cfg, job := testService(t, ctx)
	sourceRoot := cfg.Libraries["downloads"].Path
	handoffRoot := cfg.Libraries["downloads"].Download.HandoffPath

	tests := []struct {
		name  string
		query JobQuery
	}{
		{name: "relative package", query: JobQuery{Library: "downloads", Path: "Release", CurrentOnly: true}},
		{name: "relative asset", query: JobQuery{Library: "downloads", Path: "Release/Season/Episode.mkv", CurrentOnly: true}},
		{name: "absolute package", query: JobQuery{AbsolutePath: filepath.Join(sourceRoot, "Release"), CurrentOnly: true}},
		{name: "absolute asset", query: JobQuery{AbsolutePath: filepath.Join(sourceRoot, "Release", "Season", "Episode.mkv"), CurrentOnly: true}},
		{name: "planned destination", query: JobQuery{AbsolutePath: filepath.Join(handoffRoot, "Release", "Season", "Episode.mkv"), CurrentOnly: true}},
		{name: "asset destination directory", query: JobQuery{AbsolutePath: filepath.Join(handoffRoot, "Release", "Season"), CurrentOnly: true}},
		{name: "package destination directory", query: JobQuery{AbsolutePath: filepath.Join(handoffRoot, "Release"), CurrentOnly: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := service.ListJobs(ctx, tt.query)
			if err != nil {
				t.Fatalf("ListJobs() error = %v", err)
			}
			if response.Matched != 1 || len(response.Jobs) != 1 || response.Jobs[0].ID != int64(job.ID) {
				t.Fatalf("ListJobs() = %+v, want only job %d", response, job.ID)
			}
		})
	}

	response, err := service.ListJobs(ctx, JobQuery{AbsolutePath: filepath.Join(sourceRoot, "Release", "Episode"), CurrentOnly: true})
	if err != nil {
		t.Fatalf("partial ListJobs() error = %v", err)
	}
	if response.Matched != 0 || len(response.Jobs) != 0 {
		t.Fatalf("partial ListJobs() = %+v, want no fuzzy match", response)
	}

	operation := replacepkg.PublishOperation{
		JobID: job.ID, Stage: replacepkg.PublishStagePrepared,
		DestinationPath: filepath.Join(handoffRoot, "journaled", "Episode.mkv"),
		CreatedAt:       time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := state.CreatePublishOperation(ctx, operation); err != nil {
		t.Fatalf("CreatePublishOperation() error = %v", err)
	}
	response, err = service.ListJobs(ctx, JobQuery{AbsolutePath: operation.DestinationPath})
	if err != nil {
		t.Fatalf("journaled ListJobs() error = %v", err)
	}
	if response.Matched != 1 || response.Jobs[0].DestinationPath != operation.DestinationPath || response.Jobs[0].PublishStage != string(operation.Stage) {
		t.Fatalf("journaled ListJobs() = %+v", response)
	}
	response, err = service.ListJobs(ctx, JobQuery{AbsolutePath: filepath.Join(handoffRoot, "Release", "Season", "Episode.mkv")})
	if err != nil {
		t.Fatalf("superseded plan ListJobs() error = %v", err)
	}
	if response.Matched != 0 {
		t.Fatalf("superseded planned destination matched after journal: %+v", response)
	}
	response, err = service.ListJobs(ctx, JobQuery{AbsolutePath: filepath.Join(handoffRoot, "Release")})
	if err != nil {
		t.Fatalf("superseded package directory ListJobs() error = %v", err)
	}
	if response.Matched != 0 {
		t.Fatalf("superseded package directory matched after journal: %+v", response)
	}
}

func TestListJobsCurrentOnlyUsesOccurrenceCurrentness(t *testing.T) {
	ctx := context.Background()
	service, state, _, first := testService(t, ctx)
	now := time.Now().UTC()
	leased, err := state.LeaseNextJob(ctx, "worker-1", now.Add(time.Minute), now)
	if err != nil || leased == nil {
		t.Fatalf("LeaseNextJob() = %v, %v", leased, err)
	}
	if _, err := state.TransitionJob(ctx, leased.ID, domain.JobStateRunning, now, ""); err != nil {
		t.Fatalf("TransitionJob(running) error = %v", err)
	}
	if _, err := state.TransitionJob(ctx, leased.ID, domain.JobStateFailed, now, "failed"); err != nil {
		t.Fatalf("TransitionJob(failed) error = %v", err)
	}
	second, err := state.ForceOccurrence(ctx, store.ForceOccurrenceInput{
		LibraryName: "downloads", SourceKind: domain.SourceKindPackage,
		SourceRelativePath: "Release", AssetRelativePath: "Season/Episode.mkv",
		AssetRole:         domain.MediaAssetRolePrimaryVideo,
		SourceFingerprint: domain.FileFingerprint{SizeBytes: 2, ModTime: now.Add(time.Second)},
		AssetFingerprint:  domain.FileFingerprint{SizeBytes: 2, ModTime: now.Add(time.Second)},
		Now:               now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("ForceOccurrence(second) error = %v", err)
	}

	all, err := service.ListJobs(ctx, JobQuery{Library: "downloads", Path: "Release/Season/Episode.mkv"})
	if err != nil {
		t.Fatalf("ListJobs(all) error = %v", err)
	}
	if all.Matched != 2 {
		t.Fatalf("ListJobs(all).Matched = %d, want 2", all.Matched)
	}
	current, err := service.ListJobs(ctx, JobQuery{Library: "downloads", Path: "Release/Season/Episode.mkv", CurrentOnly: true})
	if err != nil {
		t.Fatalf("ListJobs(current) error = %v", err)
	}
	if current.Matched != 1 || current.Jobs[0].ID != int64(second.Job.ID) || current.Jobs[0].ID == int64(first.ID) {
		t.Fatalf("ListJobs(current) = %+v, want job %d", current, second.Job.ID)
	}
}

func TestPackageDirectoryLookupPreservesAmbiguity(t *testing.T) {
	ctx := context.Background()
	service, state, cfg, _ := testService(t, ctx)
	now := time.Now().UTC()
	if _, err := state.ForceOccurrence(ctx, store.ForceOccurrenceInput{
		LibraryName: "downloads", SourceKind: domain.SourceKindPackage,
		SourceRelativePath: "Release", AssetRelativePath: "Season/Episode-2.mkv",
		AssetRole:         domain.MediaAssetRolePrimaryVideo,
		SourceFingerprint: domain.FileFingerprint{SizeBytes: 2, ModTime: now},
		AssetFingerprint:  domain.FileFingerprint{SizeBytes: 2, ModTime: now},
		Now:               now,
	}); err != nil {
		t.Fatalf("ForceOccurrence(second asset) error = %v", err)
	}
	packageDirectory := filepath.Join(cfg.Libraries["downloads"].Download.HandoffPath, "Release")
	response, err := service.ListJobs(ctx, JobQuery{AbsolutePath: packageDirectory, CurrentOnly: true, Limit: 1})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if response.Matched != 2 || len(response.Jobs) != 1 || !response.Truncated {
		t.Fatalf("ListJobs() = %+v, want two preserved matches with one-item truncation", response)
	}
}

func TestStatusReportsDaemonFactsAndQueueCounts(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	startedAt := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	now := startedAt.Add(time.Hour)
	service.StartedAt = startedAt
	service.DaemonVersion = "test-version"
	service.ActiveWorkers = func() int { return 1 }
	service.Now = func() time.Time { return now }

	response, err := service.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if response.APIVersion != Version || response.ServerTime != now || response.Daemon.State != "ready" || response.Daemon.Version != "test-version" {
		t.Fatalf("Status() = %+v", response)
	}
	if response.Workers.Configured != 2 || response.Workers.Active != 1 || response.Queue[string(domain.JobStatePending)] != 1 {
		t.Fatalf("Status() workers/queue = %+v %+v", response.Workers, response.Queue)
	}
	if _, ok := response.Queue[string(domain.JobStateComplete)]; !ok {
		t.Fatal("Status() omitted zero-valued complete queue count")
	}
}

func TestHTTPRejectsInexactOrUnscopedJobQueries(t *testing.T) {
	service := Service{}
	tests := []string{
		"/v1/jobs?path=Release",
		"/v1/jobs?absolute_path=relative/path",
		"/v1/jobs?path=Release&absolute_path=/downloads/Release&library=downloads",
		"/v1/jobs?unknown=value",
	}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, target, nil)
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), `"code":"invalid_argument"`) {
				t.Fatalf("body = %s", response.Body.String())
			}
		})
	}
}

func testService(t *testing.T, ctx context.Context) (Service, *store.SQLiteStore, config.Config, domain.Job) {
	t.Helper()
	root := t.TempDir()
	state, err := store.Open(ctx, filepath.Join(root, "anvil.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Fatalf("state.Close() error = %v", err)
		}
	})
	cfg := config.Default()
	cfg.Daemon.WorkerCount = 2
	cfg.Libraries = map[string]config.LibraryConfig{
		"downloads": {
			Name: "downloads", Kind: string(domain.LibraryKindDownload),
			Path: filepath.Join(root, "complete"), Flow: config.DefaultDownloadFlowName,
			Profile: config.DefaultProfileName,
			Download: config.DownloadLibraryConfig{
				HandoffPath: filepath.Join(root, "converted"), HandoffMode: config.DefaultHandoffMode,
				PreserveRelativePath: true, PackageMode: config.DefaultPackageMode,
			},
		},
	}
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	forced, err := state.ForceOccurrence(ctx, store.ForceOccurrenceInput{
		LibraryName: "downloads", SourceKind: domain.SourceKindPackage,
		SourceRelativePath: "Release", AssetRelativePath: "Season/Episode.mkv",
		AssetRole:         domain.MediaAssetRolePrimaryVideo,
		SourceFingerprint: domain.FileFingerprint{SizeBytes: 1, ModTime: now},
		AssetFingerprint:  domain.FileFingerprint{SizeBytes: 1, ModTime: now},
		Now:               now,
	})
	if err != nil {
		t.Fatalf("ForceOccurrence() error = %v", err)
	}
	service := Service{Store: state, Config: func() config.Config { return cfg }, Now: func() time.Time { return now }}
	return service, state, cfg, forced.Job
}

package controlapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
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

func TestCancelJobsRequiresAnExplicitSelector(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	if _, err := service.CancelJobs(ctx, JobCancelRequest{}); err == nil {
		t.Fatal("CancelJobs() with no selector error = nil, want rejection")
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/jobs/cancel", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"code":"invalid_argument"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
	remaining, err := service.ListJobs(ctx, JobQuery{Library: "downloads"})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if remaining.Jobs[0].State != string(domain.JobStatePending) {
		t.Fatalf("rejected cancel changed job state to %q", remaining.Jobs[0].State)
	}
}

func TestCancelJobsUsesTheJobListSelectorAndStaysIdempotent(t *testing.T) {
	ctx := context.Background()
	service, _, cfg, job := testService(t, ctx)
	signaled := make([]domain.JobID, 0, 1)
	service.CancelRunningJob = func(jobID domain.JobID) bool {
		signaled = append(signaled, jobID)
		return false
	}

	absolutePath := filepath.Join(cfg.Libraries["downloads"].Path, "Release", "Season", "Episode.mkv")
	response, err := service.CancelJobs(ctx, JobCancelRequest{AbsolutePath: absolutePath, Reason: "queued by mistake"})
	if err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	if response.Matched != 1 || response.Canceled != 1 || len(response.Jobs) != 1 {
		t.Fatalf("CancelJobs() = %+v", response)
	}
	if response.Jobs[0].ID != int64(job.ID) || response.Jobs[0].PreviousState != string(domain.JobStatePending) || response.Jobs[0].State != string(domain.JobStateCanceled) {
		t.Fatalf("CancelJobs() job = %+v", response.Jobs[0])
	}
	if len(signaled) != 1 || signaled[0] != job.ID {
		t.Fatalf("signaled workers = %v, want job %d", signaled, job.ID)
	}

	listed, err := service.ListJobs(ctx, JobQuery{States: []string{string(domain.JobStateCanceled)}})
	if err != nil {
		t.Fatalf("ListJobs(canceled) error = %v", err)
	}
	if listed.Matched != 1 || listed.Jobs[0].ID != int64(job.ID) {
		t.Fatalf("ListJobs(canceled) = %+v", listed)
	}
	skipped, err := service.ListJobs(ctx, JobQuery{States: []string{string(domain.JobStateSkipped)}})
	if err != nil {
		t.Fatalf("ListJobs(skipped) error = %v", err)
	}
	if skipped.Matched != 0 {
		t.Fatalf("canceled job is indistinguishable from skipped: %+v", skipped)
	}

	repeat, err := service.CancelJobs(ctx, JobCancelRequest{AbsolutePath: absolutePath})
	if err != nil {
		t.Fatalf("repeat CancelJobs() error = %v", err)
	}
	if repeat.Matched != 1 || repeat.Canceled != 0 || repeat.Jobs[0].Canceled {
		t.Fatalf("repeat CancelJobs() = %+v, want an idempotent no-op", repeat)
	}
	if len(signaled) != 1 {
		t.Fatalf("terminal job signaled a worker again: %v", signaled)
	}
}

func TestCancelJobsNeverTargetsMoreThanTheEquivalentJobList(t *testing.T) {
	ctx := context.Background()
	service, _, _, job := testService(t, ctx)

	if _, err := service.CancelJobs(ctx, JobCancelRequest{Library: "other", IDs: []int64{int64(job.ID)}}); err == nil {
		t.Fatal("CancelJobs() for an id outside the selector error = nil, want rejection")
	}
	listed, err := service.ListJobs(ctx, JobQuery{})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if listed.Jobs[0].State != string(domain.JobStatePending) {
		t.Fatalf("rejected cancel changed job state to %q", listed.Jobs[0].State)
	}

	response, err := service.CancelJobs(ctx, JobCancelRequest{Library: "downloads", IDs: []int64{int64(job.ID)}})
	if err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	if response.Canceled != 1 || response.Jobs[0].ID != int64(job.ID) {
		t.Fatalf("CancelJobs() = %+v", response)
	}
}

func TestJobCancelEndpointRejectsNonPostAndUnknownFields(t *testing.T) {
	service := Service{}
	tests := []struct {
		name    string
		request *http.Request
		want    int
	}{
		{name: "get", request: httptest.NewRequest(http.MethodGet, "/v1/jobs/cancel", nil), want: http.StatusMethodNotAllowed},
		{name: "query parameters", request: httptest.NewRequest(http.MethodPost, "/v1/jobs/cancel?library=downloads", strings.NewReader(`{}`)), want: http.StatusBadRequest},
		{name: "unknown field", request: httptest.NewRequest(http.MethodPost, "/v1/jobs/cancel", strings.NewReader(`{"all":true}`)), want: http.StatusBadRequest},
		{name: "trailing json", request: httptest.NewRequest(http.MethodPost, "/v1/jobs/cancel", strings.NewReader(`{"ids":[1]}{"ids":[2]}`)), want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, tt.request)
			if response.Code != tt.want {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, tt.want, response.Body.String())
			}
		})
	}
}

// TestCancelJobsRejectsCurrentOnlyAsTheOnlySelector pins that the refinement
// flag cannot stand in for a selector: on its own it matches every job in every
// library and state, which is exactly what the selector guard exists to stop.
func TestCancelJobsRejectsCurrentOnlyAsTheOnlySelector(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)

	if _, err := service.CancelJobs(ctx, JobCancelRequest{CurrentOnly: true}); err == nil {
		t.Fatal("CancelJobs(--current-only) error = nil, want rejection")
	}
	listed, err := service.ListJobs(ctx, JobQuery{})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if listed.Jobs[0].State != string(domain.JobStatePending) {
		t.Fatalf("rejected cancel changed job state to %q", listed.Jobs[0].State)
	}
	if _, err := service.CancelJobs(ctx, JobCancelRequest{Library: "downloads", CurrentOnly: true}); err != nil {
		t.Fatalf("CancelJobs(--library --current-only) error = %v", err)
	}
}

// TestCancelJobsReportsWhyAJobWasNotCanceled covers the operator-visible half of
// the publish guard: the refusal is reported per job with a machine-readable
// reason instead of erroring the batch.
func TestCancelJobsReportsWhyAJobWasNotCanceled(t *testing.T) {
	ctx := context.Background()
	service, state, _, job := testService(t, ctx)
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	if err := state.CreatePublishOperation(ctx, replacepkg.PublishOperation{
		JobID: job.ID, Kind: "handoff", Mode: "move", Stage: replacepkg.PublishStagePrepared,
		ArtifactPath: "/staging/output.mkv", DestinationPath: "/converted/Release/Season/Episode.mkv",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePublishOperation() error = %v", err)
	}
	signaled := 0
	service.CancelRunningJob = func(domain.JobID) bool {
		signaled++
		return true
	}

	response, err := service.CancelJobs(ctx, JobCancelRequest{Library: "downloads"})
	if err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	if response.Matched != 1 || response.Canceled != 0 {
		t.Fatalf("CancelJobs() = %+v, want the publish refused", response)
	}
	if response.Jobs[0].Canceled || response.Jobs[0].SkipReason != string(store.CancelSkipPublishInFlight) {
		t.Fatalf("CancelJobs() job = %+v", response.Jobs[0])
	}
	if signaled != 0 {
		t.Fatalf("signaled workers = %d, want the worker left alone", signaled)
	}
	listed, err := service.ListJobs(ctx, JobQuery{Library: "downloads"})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if listed.Jobs[0].State != string(domain.JobStatePending) {
		t.Fatalf("refused cancel changed job state to %q", listed.Jobs[0].State)
	}
}

// TestCancelJobsReportsASignaledWorker asserts the worker_signaled path end to
// end, not just the "no worker was running" case.
func TestCancelJobsReportsASignaledWorker(t *testing.T) {
	ctx := context.Background()
	service, _, _, job := testService(t, ctx)
	signaled := make([]domain.JobID, 0, 1)
	service.CancelRunningJob = func(jobID domain.JobID) bool {
		signaled = append(signaled, jobID)
		return true
	}

	response, err := service.CancelJobs(ctx, JobCancelRequest{Library: "downloads"})
	if err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	if response.Canceled != 1 || !response.Jobs[0].WorkerSignaled {
		t.Fatalf("CancelJobs() = %+v, want a signaled worker", response)
	}
	if len(signaled) != 1 || signaled[0] != job.ID {
		t.Fatalf("signaled workers = %v, want job %d", signaled, job.ID)
	}
}

// recordingCancelStore captures the input the service builds for the store so a
// test can assert the selector survives the hop, then delegates to the real one.
type recordingCancelStore struct {
	Store
	input store.CancelJobsInput
}

func (r *recordingCancelStore) CancelJobs(ctx context.Context, input store.CancelJobsInput) ([]store.CancelJobResult, error) {
	r.input = input
	return r.Store.CancelJobs(ctx, input)
}

// TestCancelJobsForwardsTheSelectorStatesToTheStore pins the control-API half of
// the state guard. The store re-checks the state inside the cancel transaction,
// but only if the service actually hands the selector's states over: without
// that, a job that changed state between the listing and the cancel is canceled
// anyway, which is how `--state pending` could kill a running encode.
func TestCancelJobsForwardsTheSelectorStatesToTheStore(t *testing.T) {
	ctx := context.Background()
	service, _, _, job := testService(t, ctx)
	recorder := &recordingCancelStore{Store: service.Store}
	service.Store = recorder

	response, err := service.CancelJobs(ctx, JobCancelRequest{Library: "downloads", States: []string{"pending", "retrying"}})
	if err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	if len(response.Jobs) != 1 || !response.Jobs[0].Canceled {
		t.Fatalf("cancel results = %+v, want job %d canceled", response.Jobs, job.ID)
	}
	want := []domain.JobState{domain.JobStatePending, domain.JobStateRetrying}
	if !slices.Equal(recorder.input.States, want) {
		t.Fatalf("CancelJobsInput.States = %v, want %v", recorder.input.States, want)
	}
}

// TestCancelJobsWithoutAStateSelectorForwardsNoStates keeps the guard from
// silently narrowing a cancel that never asked for a state.
func TestCancelJobsWithoutAStateSelectorForwardsNoStates(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	recorder := &recordingCancelStore{Store: service.Store}
	service.Store = recorder

	if _, err := service.CancelJobs(ctx, JobCancelRequest{Library: "downloads"}); err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	if len(recorder.input.States) != 0 {
		t.Fatalf("CancelJobsInput.States = %v, want none", recorder.input.States)
	}
}

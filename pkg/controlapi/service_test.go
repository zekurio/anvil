package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/store"
)

func TestListJobsMatchesExactSourceAssetAndDestinationPaths(t *testing.T) {
	ctx := context.Background()
	service, state, cfg, job := testService(t, ctx)
	sourceRoot := cfg.Libraries["downloads"].Path
	handoffRoot := cfg.Libraries["downloads"].Download.HandoffPath

	tests := []struct {
		name      string
		query     JobQuery
		matchedOn []PathMatchSide
	}{
		{name: "relative package", query: JobQuery{Library: "downloads", Path: "Release", CurrentOnly: true}},
		{name: "relative asset", query: JobQuery{Library: "downloads", Path: "Release/Season/Episode.mkv", CurrentOnly: true}},
		{name: "absolute package", query: JobQuery{AbsolutePath: filepath.Join(sourceRoot, "Release"), CurrentOnly: true}, matchedOn: []PathMatchSide{PathMatchSource}},
		{name: "absolute asset", query: JobQuery{AbsolutePath: filepath.Join(sourceRoot, "Release", "Season", "Episode.mkv"), CurrentOnly: true}, matchedOn: []PathMatchSide{PathMatchAsset}},
		{name: "planned destination", query: JobQuery{AbsolutePath: filepath.Join(handoffRoot, "Release", "Season", "Episode.mkv"), CurrentOnly: true}, matchedOn: []PathMatchSide{PathMatchDestination}},
		{name: "asset destination directory", query: JobQuery{AbsolutePath: filepath.Join(handoffRoot, "Release", "Season"), CurrentOnly: true}, matchedOn: []PathMatchSide{PathMatchDestinationDirectory}},
		{name: "package destination directory", query: JobQuery{AbsolutePath: filepath.Join(handoffRoot, "Release"), CurrentOnly: true}, matchedOn: []PathMatchSide{PathMatchDestinationDirectory}},
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
			// A relative-path query reports no side, because it did not ask
			// about absolute paths at all.
			if !slices.Equal(response.Jobs[0].MatchedOn, tt.matchedOn) {
				t.Fatalf("MatchedOn = %v, want %v", response.Jobs[0].MatchedOn, tt.matchedOn)
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

// TestListJobsRejectsInexactOrUnscopedQueries keeps path lookups exact. A
// library-relative path without its library, or an "absolute" path that is not
// absolute, cannot identify a job, and answering them approximately is how a
// caller ends up acting on the wrong file.
func TestListJobsRejectsInexactOrUnscopedJobQueries(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	tests := []struct {
		name  string
		query JobQuery
	}{
		{name: "relative path without library", query: JobQuery{Path: "Release"}},
		{name: "absolute path that is relative", query: JobQuery{AbsolutePath: "relative/path"}},
		{name: "both path forms", query: JobQuery{Library: "downloads", Path: "Release", AbsolutePath: "/downloads/Release"}},
		{name: "negative limit", query: JobQuery{Limit: -1}},
		{name: "unknown state", query: JobQuery{States: []string{"almost-done"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.ListJobs(ctx, tt.query)
			var controlErr *Error
			if !errors.As(err, &controlErr) || controlErr.Code != CodeInvalidArgument {
				t.Fatalf("ListJobs() error = %v, want invalid_argument", err)
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
	service := Service{
		Store:   state,
		Scanner: scanner.Scanner{Store: state},
		Config:  func() config.Config { return cfg },
		Now:     func() time.Time { return now },
	}
	return service, state, cfg, forced.Job
}

func TestCancelJobsRequiresAnExplicitSelector(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	_, err := service.CancelJobs(ctx, JobCancelRequest{})
	var controlErr *Error
	if !errors.As(err, &controlErr) || controlErr.Code != CodeInvalidArgument {
		t.Fatalf("CancelJobs() with no selector error = %v, want invalid_argument", err)
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

// recordStreamSelection stores a decision the way the pipeline runner does, as
// an artifact attempt event, so the test exercises the real payload contract.
func recordStreamSelection(t *testing.T, ctx context.Context, state *store.SQLiteStore, jobID domain.JobID, decision domain.StreamSelectionDecision) domain.Attempt {
	t.Helper()
	attempt := startTestAttempt(t, ctx, state, jobID)
	payload, err := json.Marshal(decision)
	if err != nil {
		t.Fatalf("marshal decision: %v", err)
	}
	if _, err := state.RecordAttemptEvent(ctx, domain.AttemptEvent{
		AttemptID: attempt.ID, Type: domain.AttemptEventArtifact,
		Name: pipeline.StreamSelectionArtifact, Message: "audio language_filter", Payload: payload,
	}); err != nil {
		t.Fatalf("RecordAttemptEvent() error = %v", err)
	}
	return attempt
}

// startTestAttempt leases the job first, because StartAttempt only accepts the
// worker that currently holds the lease.
func startTestAttempt(t *testing.T, ctx context.Context, state *store.SQLiteStore, jobID domain.JobID) domain.Attempt {
	t.Helper()
	now := time.Now().UTC()
	for {
		leased, err := state.LeaseNextJobForLibraries(ctx, "worker-1", now.Add(time.Minute), now, nil)
		if err != nil {
			t.Fatalf("LeaseNextJobForLibraries() error = %v", err)
		}
		if leased == nil {
			t.Fatalf("no pending job to lease for job %d", jobID)
		}
		if leased.ID != jobID {
			continue
		}
		attempt, err := state.StartAttempt(ctx, jobID, "worker-1", nil, nil, nil, now)
		if err != nil {
			t.Fatalf("StartAttempt() error = %v", err)
		}
		return attempt
	}
}

func germanMissingDecision() domain.StreamSelectionDecision {
	return domain.StreamSelectionDecision{
		Kind: domain.StreamKindAudio, Rule: domain.StreamSelectionRuleLanguageFilter,
		OriginalLanguage:   "jpn",
		RequestedLanguages: []string{"orig", "deu"},
		ResolvedLanguages:  []string{"jpn", "deu"},
		MissingLanguages:   []string{"deu"},
		Streams: []domain.StreamDecision{
			{Index: 0, Codec: "aac", Language: "jpn", Kept: true, Reason: domain.StreamKeptOriginalLanguage},
			{Index: 1, Codec: "aac", Language: "eng", Reason: domain.StreamDroppedLanguage},
		},
	}
}

// TestListJobsExposesStreamSelectionAfterTheSourceIsGone is the incident this
// feature exists for: the source file has been deleted by cleanup, and the only
// remaining evidence of why German is absent has to come back over the socket.
func TestListJobsExposesStreamSelectionAfterTheSourceIsGone(t *testing.T) {
	ctx := context.Background()
	service, state, cfg, job := testService(t, ctx)
	attempt := recordStreamSelection(t, ctx, state, job.ID, germanMissingDecision())

	sourcePath := filepath.Join(cfg.Libraries["downloads"].Path, "Release")
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Fatalf("test fixture unexpectedly has a real source at %s", sourcePath)
	}

	response, err := service.ListJobs(ctx, JobQuery{Library: "downloads", WithSelection: true})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(response.Jobs) != 1 || len(response.Jobs[0].StreamSelection) != 1 {
		t.Fatalf("ListJobs() = %+v, want one job carrying one selection", response)
	}
	selection := response.Jobs[0].StreamSelection[0]
	if selection.AttemptID != int64(attempt.ID) {
		t.Fatalf("AttemptID = %d, want %d", selection.AttemptID, attempt.ID)
	}
	if selection.DecisionError != "" {
		t.Fatalf("DecisionError = %q, want the decision to decode", selection.DecisionError)
	}
	if !slices.Equal(selection.Decision.MissingLanguages, []string{"deu"}) {
		t.Fatalf("MissingLanguages = %v, want [deu]", selection.Decision.MissingLanguages)
	}
	kept := selection.Decision.Streams[0]
	if !kept.Kept || kept.Reason != domain.StreamKeptOriginalLanguage {
		t.Fatalf("kept stream = %+v, want jpn kept as the original language", kept)
	}
	dropped := selection.Decision.Streams[1]
	if dropped.Kept || dropped.Reason != domain.StreamDroppedLanguage {
		t.Fatalf("dropped stream = %+v, want eng dropped as not requested", dropped)
	}
}

// TestListJobsStreamSelectionIsOptIn keeps listings small by default, and keeps
// "nothing recorded" distinguishable from "recorded, and it kept everything".
func TestListJobsStreamSelectionIsOptIn(t *testing.T) {
	ctx := context.Background()
	service, state, _, job := testService(t, ctx)

	t.Run("absent without the flag", func(t *testing.T) {
		recordStreamSelection(t, ctx, state, job.ID, germanMissingDecision())
		response, err := service.ListJobs(ctx, JobQuery{Library: "downloads"})
		if err != nil {
			t.Fatalf("ListJobs() error = %v", err)
		}
		if len(response.Jobs) != 1 || response.Jobs[0].StreamSelection != nil {
			t.Fatalf("StreamSelection = %+v, want it omitted without WithSelection", response.Jobs[0].StreamSelection)
		}
	})

	t.Run("a job that recorded nothing carries no selection", func(t *testing.T) {
		other, err := state.ForceOccurrence(ctx, store.ForceOccurrenceInput{
			LibraryName: "downloads", SourceKind: domain.SourceKindPackage,
			SourceRelativePath: "Other", AssetRelativePath: "Season/Other.mkv",
			AssetRole:         domain.MediaAssetRolePrimaryVideo,
			SourceFingerprint: domain.FileFingerprint{SizeBytes: 2},
			AssetFingerprint:  domain.FileFingerprint{SizeBytes: 2},
		})
		if err != nil {
			t.Fatalf("ForceOccurrence() error = %v", err)
		}
		response, err := service.ListJobs(ctx, JobQuery{Library: "downloads", WithSelection: true})
		if err != nil {
			t.Fatalf("ListJobs() error = %v", err)
		}
		var checked bool
		for _, item := range response.Jobs {
			if item.ID != int64(other.Job.ID) {
				continue
			}
			checked = true
			if item.StreamSelection != nil {
				t.Fatalf("StreamSelection = %+v, want none for a job that recorded no decision", item.StreamSelection)
			}
		}
		if !checked {
			t.Fatalf("job %d missing from the listing", other.Job.ID)
		}
	})
}

// TestListJobsReportsAnUnreadableStreamSelection pins that a decision Anvil
// cannot decode is reported as unreadable rather than silently omitted, so a
// consumer never reads a corrupt record as "no streams were dropped".
func TestListJobsReportsAnUnreadableStreamSelection(t *testing.T) {
	ctx := context.Background()
	service, state, _, job := testService(t, ctx)
	attempt := startTestAttempt(t, ctx, state, job.ID)
	if _, err := state.RecordAttemptEvent(ctx, domain.AttemptEvent{
		AttemptID: attempt.ID, Type: domain.AttemptEventArtifact,
		Name: pipeline.StreamSelectionArtifact, Payload: []byte("{not json"),
	}); err != nil {
		t.Fatalf("RecordAttemptEvent() error = %v", err)
	}

	response, err := service.ListJobs(ctx, JobQuery{Library: "downloads", WithSelection: true})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(response.Jobs[0].StreamSelection) != 1 {
		t.Fatalf("StreamSelection = %+v, want the unreadable record reported", response.Jobs[0].StreamSelection)
	}
	if response.Jobs[0].StreamSelection[0].DecisionError == "" {
		t.Fatal("DecisionError = \"\", want the decode failure reported")
	}
}

// TestListJobsReturnsBothAudioAndSubtitleDecisions pins that one attempt's two
// decisions both come back. The feature is half useless with only one kind, and
// a query narrowed to a single row per job would pass every other test here.
func TestListJobsReturnsBothAudioAndSubtitleDecisions(t *testing.T) {
	ctx := context.Background()
	service, state, _, job := testService(t, ctx)
	attempt := startTestAttempt(t, ctx, state, job.ID)

	for _, decision := range []domain.StreamSelectionDecision{
		germanMissingDecision(),
		{Kind: domain.StreamKindSubtitle, Rule: domain.StreamSelectionRuleLanguageFilter},
	} {
		payload, err := json.Marshal(decision)
		if err != nil {
			t.Fatalf("marshal decision: %v", err)
		}
		if _, err := state.RecordAttemptEvent(ctx, domain.AttemptEvent{
			AttemptID: attempt.ID, Type: domain.AttemptEventArtifact,
			Name: pipeline.StreamSelectionArtifact, Payload: payload,
		}); err != nil {
			t.Fatalf("RecordAttemptEvent() error = %v", err)
		}
	}

	response, err := service.ListJobs(ctx, JobQuery{Library: "downloads", WithSelection: true})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	kinds := make([]domain.StreamKind, 0, 2)
	for _, selection := range response.Jobs[0].StreamSelection {
		if selection.Decision == nil {
			t.Fatalf("selection %+v has no decision", selection)
		}
		kinds = append(kinds, selection.Decision.Kind)
	}
	if !slices.Equal(kinds, []domain.StreamKind{domain.StreamKindAudio, domain.StreamKindSubtitle}) {
		t.Fatalf("decision kinds = %v, want both audio and subtitle", kinds)
	}
}

// TestListJobsReportsAPathOutsideEveryLibrary closes the other half of the
// incident: zero results for a path Anvil could never match must not look like
// zero results for a path that simply has no job.
func TestListJobsReportsAPathOutsideEveryLibrary(t *testing.T) {
	ctx := context.Background()
	service, _, cfg, _ := testService(t, ctx)

	tests := []struct {
		name    string
		path    string
		outside bool
	}{
		{name: "outside every configured library", path: filepath.Join(t.TempDir(), "elsewhere", "Episode.mkv"), outside: true},
		{name: "under the library but unknown", path: filepath.Join(cfg.Libraries["downloads"].Path, "Unknown", "Episode.mkv")},
		{name: "under the handoff root but unknown", path: filepath.Join(cfg.Libraries["downloads"].Download.HandoffPath, "Unknown.mkv")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := service.ListJobs(ctx, JobQuery{AbsolutePath: tt.path})
			if err != nil {
				t.Fatalf("ListJobs() error = %v", err)
			}
			if response.Matched != 0 {
				t.Fatalf("Matched = %d, want no match", response.Matched)
			}
			if response.PathOutsideLibraries != tt.outside {
				t.Fatalf("PathOutsideLibraries = %t, want %t", response.PathOutsideLibraries, tt.outside)
			}
		})
	}

	// A path that did match must never be reported as unmatchable.
	response, err := service.ListJobs(ctx, JobQuery{AbsolutePath: filepath.Join(cfg.Libraries["downloads"].Path, "Release")})
	if err != nil {
		t.Fatalf("matching ListJobs() error = %v", err)
	}
	if response.Matched != 1 || response.PathOutsideLibraries {
		t.Fatalf("matching listing = %+v, want a match and no outside-library flag", response)
	}
}

// TestListJobsReportsEverySideAPathMatched covers an in-place replacement, where
// the converted file is written back over its own source. Reporting one side
// would tell a consumer the output is not a destination.
func TestListJobsReportsEverySideAPathMatched(t *testing.T) {
	ctx := context.Background()
	service, state, cfg, job := testService(t, ctx)

	// Journal a publish whose destination is the asset's own path.
	assetPath := filepath.Join(cfg.Libraries["downloads"].Path, "Release", "Season", "Episode.mkv")
	now := time.Now().UTC()
	if err := state.CreatePublishOperation(ctx, replacepkg.PublishOperation{
		JobID: job.ID, Kind: "replace", Mode: "replace", Stage: replacepkg.PublishStagePrepared,
		ArtifactPath: "/staging/output.mkv", DestinationPath: assetPath,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePublishOperation() error = %v", err)
	}

	response, err := service.ListJobs(ctx, JobQuery{AbsolutePath: assetPath})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if response.Matched != 1 {
		t.Fatalf("Matched = %d, want the in-place job", response.Matched)
	}
	want := []PathMatchSide{PathMatchAsset, PathMatchDestination}
	if !slices.Equal(response.Jobs[0].MatchedOn, want) {
		t.Fatalf("MatchedOn = %v, want %v so the output is not reported as merely a source", response.Jobs[0].MatchedOn, want)
	}
}

// TestServiceOnlyReturnsStreamSelectionWhenAsked keeps the opt-in honest: the
// decisions dwarf a listing, and a caller that did not ask must not be made to
// pay for them. The wire hop for the same flag is pinned in server_test.go.
func TestServiceOnlyReturnsStreamSelectionWhenAsked(t *testing.T) {
	ctx := context.Background()
	service, state, _, job := testService(t, ctx)
	recordStreamSelection(t, ctx, state, job.ID, germanMissingDecision())

	for _, withSelection := range []bool{true, false} {
		response, err := service.ListJobs(ctx, JobQuery{Library: "downloads", WithSelection: withSelection})
		if err != nil {
			t.Fatalf("ListJobs(with_selection=%t) error = %v", withSelection, err)
		}
		if got := len(response.Jobs[0].StreamSelection) > 0; got != withSelection {
			t.Fatalf("stream selection present = %t, want %t", got, withSelection)
		}
	}
}

// TestUniqueAbsoluteKeysNormalizesAndDeduplicates pins the normalization that
// keeps a traversal-equivalent path from being treated as a different key.
func TestUniqueAbsoluteKeysNormalizesAndDeduplicates(t *testing.T) {
	keys := uniqueAbsoluteKeys(
		absolutePathKey{path: "/media/Movie.mkv", side: PathMatchSource},
		absolutePathKey{path: "/media/Season/../Movie.mkv", side: PathMatchAsset},
		absolutePathKey{path: "  ", side: PathMatchDestination},
		absolutePathKey{path: "/media/Movie.mkv", side: PathMatchSource},
		absolutePathKey{path: "/media/Movie.mkv/", side: PathMatchDestination},
	)
	want := []absolutePathKey{
		{path: "/media/Movie.mkv", side: PathMatchSource},
		{path: "/media/Movie.mkv", side: PathMatchAsset},
		{path: "/media/Movie.mkv", side: PathMatchDestination},
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("uniqueAbsoluteKeys() = %+v, want %+v", keys, want)
	}
}

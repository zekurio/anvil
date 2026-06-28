package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestEnqueueJobIsIdempotentForActiveTarget(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	source := upsertTestSource(t, ctx, store, "movies", "Movie.mkv")
	asset := upsertTestAsset(t, ctx, store, source.ID, "Movie.mkv")

	first, inserted, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    source.ID,
		AssetID:     asset.ID,
		LibraryName: source.LibraryName,
		Priority:    1,
		Now:         testNow(),
	})
	if err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}
	if !inserted {
		t.Fatal("first EnqueueJob() inserted = false, want true")
	}

	second, inserted, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    source.ID,
		AssetID:     asset.ID,
		LibraryName: source.LibraryName,
		Priority:    1,
		Now:         testNow(),
	})
	if err != nil {
		t.Fatalf("second EnqueueJob() error = %v", err)
	}
	if inserted {
		t.Fatal("second EnqueueJob() inserted = true, want false")
	}
	if second.ID != first.ID {
		t.Fatalf("second job ID = %d, want %d", second.ID, first.ID)
	}
}

func TestEnqueueJobDoesNotRecreateTerminalTarget(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := testNow()

	source := upsertTestSource(t, ctx, store, "movies", "Movie.mkv")
	job, inserted, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    source.ID,
		LibraryName: source.LibraryName,
		Now:         now,
	})
	if err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}
	if !inserted {
		t.Fatal("first EnqueueJob() inserted = false, want true")
	}
	if _, err := store.LeaseNextJob(ctx, "worker-1", now.Add(time.Minute), now); err != nil {
		t.Fatalf("LeaseNextJob() error = %v", err)
	}
	if _, err := store.TransitionJob(ctx, job.ID, domain.JobStateRunning, now, ""); err != nil {
		t.Fatalf("TransitionJob(running) error = %v", err)
	}
	if _, err := store.TransitionJob(ctx, job.ID, domain.JobStateValidating, now, ""); err != nil {
		t.Fatalf("TransitionJob(validating) error = %v", err)
	}
	if _, err := store.TransitionJob(ctx, job.ID, domain.JobStateComplete, now, ""); err != nil {
		t.Fatalf("TransitionJob(complete) error = %v", err)
	}

	second, inserted, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    source.ID,
		LibraryName: source.LibraryName,
		Now:         now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("second EnqueueJob() error = %v", err)
	}
	if inserted {
		t.Fatal("second EnqueueJob() inserted = true, want false")
	}
	if second.ID != job.ID {
		t.Fatalf("second job ID = %d, want original job ID %d", second.ID, job.ID)
	}
	if second.State != domain.JobStateComplete {
		t.Fatalf("second job state = %q, want %q", second.State, domain.JobStateComplete)
	}
}

func TestLeaseNextJobUsesPriorityThenCreatedOrder(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := testNow()

	low := upsertTestSource(t, ctx, store, "movies", "Low.mkv")
	high := upsertTestSource(t, ctx, store, "movies", "High.mkv")

	if _, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    low.ID,
		LibraryName: low.LibraryName,
		Priority:    1,
		Now:         now,
	}); err != nil {
		t.Fatalf("enqueue low priority job: %v", err)
	}
	if _, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    high.ID,
		LibraryName: high.LibraryName,
		Priority:    10,
		Now:         now.Add(time.Second),
	}); err != nil {
		t.Fatalf("enqueue high priority job: %v", err)
	}

	job, err := store.LeaseNextJob(ctx, "worker-1", now.Add(time.Minute), now)
	if err != nil {
		t.Fatalf("LeaseNextJob() error = %v", err)
	}
	if job == nil {
		t.Fatal("LeaseNextJob() = nil, want job")
	}
	if job.SourceID != high.ID {
		t.Fatalf("leased source ID = %d, want high priority source %d", job.SourceID, high.ID)
	}
	if job.State != domain.JobStateLeased {
		t.Fatalf("leased job state = %q, want %q", job.State, domain.JobStateLeased)
	}
	if job.LeaseOwner != "worker-1" {
		t.Fatalf("lease owner = %q, want worker-1", job.LeaseOwner)
	}
}

func TestLeaseNextJobForLibraries(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := testNow()

	movies := upsertTestSource(t, ctx, store, "movies", "Movie.mkv")
	tv := upsertTestSource(t, ctx, store, "tv", "Show.mkv")

	if _, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    movies.ID,
		LibraryName: movies.LibraryName,
		Priority:    100,
		Now:         now,
	}); err != nil {
		t.Fatalf("enqueue movies job: %v", err)
	}
	if _, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    tv.ID,
		LibraryName: tv.LibraryName,
		Priority:    1,
		Now:         now,
	}); err != nil {
		t.Fatalf("enqueue tv job: %v", err)
	}

	job, err := store.LeaseNextJobForLibraries(ctx, "worker-1", now.Add(time.Minute), now, []domain.LibraryName{"tv"})
	if err != nil {
		t.Fatalf("LeaseNextJobForLibraries() error = %v", err)
	}
	if job == nil {
		t.Fatal("LeaseNextJobForLibraries() = nil, want job")
	}
	if job.LibraryName != "tv" {
		t.Fatalf("leased library = %q, want tv", job.LibraryName)
	}
}

func TestListJobsFiltersByLibraryStateAndLimit(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := testNow()

	movie := upsertTestSource(t, ctx, store, "movies", "Movie.mkv")
	movieAsset := upsertTestAsset(t, ctx, store, movie.ID, "Movie.mkv")
	show := upsertTestSource(t, ctx, store, "tv", "Show.mkv")

	if _, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    movie.ID,
		AssetID:     movieAsset.ID,
		LibraryName: movie.LibraryName,
		Now:         now,
	}); err != nil {
		t.Fatalf("enqueue movie: %v", err)
	}
	tvJob, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    show.ID,
		LibraryName: show.LibraryName,
		Now:         now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("enqueue tv: %v", err)
	}
	if _, err := store.TransitionJob(ctx, tvJob.ID, domain.JobStateSkipped, now.Add(2*time.Second), "manual skip"); err != nil {
		t.Fatalf("skip tv job: %v", err)
	}

	jobs, err := store.ListJobs(ctx, JobListFilter{
		LibraryName: "tv",
		States:      []domain.JobState{domain.JobStateSkipped},
		Limit:       1,
	})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(jobs))
	}
	if jobs[0].Job.ID != tvJob.ID {
		t.Fatalf("job id = %d, want %d", jobs[0].Job.ID, tvJob.ID)
	}
	if got, want := jobs[0].SourcePath, "Show.mkv"; got != want {
		t.Fatalf("source path = %q, want %q", got, want)
	}
	if got := jobs[0].AssetPath; got != "" {
		t.Fatalf("asset path = %q, want empty", got)
	}
}

func TestGetJobSummaryAndListAttemptsForJob(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := testNow()

	source := upsertTestSource(t, ctx, store, "movies", "Movie.mkv")
	asset := upsertTestAsset(t, ctx, store, source.ID, "Movie.mkv")
	job, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    source.ID,
		AssetID:     asset.ID,
		LibraryName: source.LibraryName,
		Now:         now,
	})
	if err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}

	_, first := leaseAndStartAttempt(t, ctx, store, now, "worker-1")
	if _, err := store.FinishAttempt(ctx, first.ID, domain.AttemptStateFailed, "first failed", now.Add(time.Second)); err != nil {
		t.Fatalf("FinishAttempt(first) error = %v", err)
	}
	if _, err := store.TransitionJob(ctx, job.ID, domain.JobStateRetrying, now.Add(2*time.Second), "retrying"); err != nil {
		t.Fatalf("TransitionJob(retrying) error = %v", err)
	}
	if _, err := store.TransitionJob(ctx, job.ID, domain.JobStatePending, now.Add(3*time.Second), "pending"); err != nil {
		t.Fatalf("TransitionJob(pending) error = %v", err)
	}
	_, second := leaseAndStartAttempt(t, ctx, store, now.Add(4*time.Second), "worker-2")

	attempts, err := store.ListAttemptsForJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("ListAttemptsForJob() error = %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts len = %d, want 2", len(attempts))
	}
	if attempts[0].ID != first.ID || attempts[0].Number != 1 || attempts[0].WorkerID != "worker-1" {
		t.Fatalf("first attempt = %+v, want id=%d number=1 worker-1", attempts[0], first.ID)
	}
	if attempts[1].ID != second.ID || attempts[1].Number != 2 || attempts[1].WorkerID != "worker-2" {
		t.Fatalf("second attempt = %+v, want id=%d number=2 worker-2", attempts[1], second.ID)
	}

	summary, err := store.GetJobSummary(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJobSummary() error = %v", err)
	}
	if summary.Job.ID != job.ID {
		t.Fatalf("summary job id = %d, want %d", summary.Job.ID, job.ID)
	}
	if summary.Job.AttemptCount != 2 {
		t.Fatalf("summary attempt count = %d, want 2", summary.Job.AttemptCount)
	}
	if summary.SourcePath != "Movie.mkv" {
		t.Fatalf("summary source path = %q, want Movie.mkv", summary.SourcePath)
	}
	if summary.AssetPath != "Movie.mkv" {
		t.Fatalf("summary asset path = %q, want Movie.mkv", summary.AssetPath)
	}
	if summary.AssetRole != domain.MediaAssetRolePrimaryVideo {
		t.Fatalf("summary asset role = %q, want %q", summary.AssetRole, domain.MediaAssetRolePrimaryVideo)
	}
}

func TestRetryJobReturnsFailedJobToPending(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := testNow()

	source := upsertTestSource(t, ctx, store, "movies", "Movie.mkv")
	if _, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    source.ID,
		LibraryName: source.LibraryName,
		Now:         now,
	}); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	job, err := store.LeaseNextJob(ctx, "worker-1", now.Add(time.Minute), now)
	if err != nil {
		t.Fatalf("LeaseNextJob() error = %v", err)
	}
	if _, err := store.TransitionJob(ctx, job.ID, domain.JobStateRunning, now, ""); err != nil {
		t.Fatalf("running transition: %v", err)
	}
	if _, err := store.TransitionJob(ctx, job.ID, domain.JobStateFailed, now.Add(time.Second), "encode failed"); err != nil {
		t.Fatalf("failed transition: %v", err)
	}

	retried, err := store.RetryJob(ctx, job.ID, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("RetryJob() error = %v", err)
	}
	if retried.State != domain.JobStatePending {
		t.Fatalf("state = %q, want pending", retried.State)
	}
	if retried.LastError != "" {
		t.Fatalf("last error = %q, want empty", retried.LastError)
	}
	if retried.CompletedAt != nil {
		t.Fatalf("completed at = %v, want nil", retried.CompletedAt)
	}
}

func TestRetryFailedJobsCanFilterByLibrary(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := testNow()

	movieJob := enqueueFailedJob(t, ctx, store, "movies", "Movie.mkv", now)
	tvJob := enqueueFailedJob(t, ctx, store, "tv", "Show.mkv", now.Add(time.Minute))

	retried, err := store.RetryFailedJobs(ctx, "movies", now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("RetryFailedJobs() error = %v", err)
	}
	if retried != 1 {
		t.Fatalf("retried = %d, want 1", retried)
	}

	movieJob, err = store.GetJob(ctx, movieJob.ID)
	if err != nil {
		t.Fatalf("get movie job: %v", err)
	}
	tvJob, err = store.GetJob(ctx, tvJob.ID)
	if err != nil {
		t.Fatalf("get tv job: %v", err)
	}
	if movieJob.State != domain.JobStatePending {
		t.Fatalf("movie state = %q, want pending", movieJob.State)
	}
	if tvJob.State != domain.JobStateFailed {
		t.Fatalf("tv state = %q, want failed", tvJob.State)
	}
}

func TestHeartbeatRequiresLeaseOwner(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := testNow()

	source := upsertTestSource(t, ctx, store, "movies", "Movie.mkv")
	if _, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    source.ID,
		LibraryName: source.LibraryName,
		Now:         now,
	}); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	job, err := store.LeaseNextJob(ctx, "worker-1", now.Add(time.Minute), now)
	if err != nil {
		t.Fatalf("LeaseNextJob() error = %v", err)
	}

	if _, err := store.HeartbeatJob(ctx, job.ID, "worker-2", now.Add(2*time.Minute), now.Add(time.Second)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("HeartbeatJob() error = %v, want ErrNotFound", err)
	}
	updated, err := store.HeartbeatJob(ctx, job.ID, "worker-1", now.Add(2*time.Minute), now.Add(time.Second))
	if err != nil {
		t.Fatalf("HeartbeatJob() error = %v", err)
	}
	if updated.LeaseDeadline == nil || !updated.LeaseDeadline.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("lease deadline = %v, want %v", updated.LeaseDeadline, now.Add(2*time.Minute))
	}
}

func TestStartAttemptRequiresLeaseOwner(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := testNow()

	source := upsertTestSource(t, ctx, store, "movies", "Movie.mkv")
	if _, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    source.ID,
		LibraryName: source.LibraryName,
		Now:         now,
	}); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	job, err := store.LeaseNextJob(ctx, "worker-1", now.Add(time.Minute), now)
	if err != nil {
		t.Fatalf("LeaseNextJob() error = %v", err)
	}

	_, err = store.StartAttempt(ctx, job.ID, "worker-2", nil, nil, nil, now)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("StartAttempt() error = %v, want ErrNotFound", err)
	}
	if _, err := store.StartAttempt(ctx, job.ID, "worker-1", nil, nil, nil, now.Add(2*time.Minute)); err == nil {
		t.Fatal("StartAttempt() error = nil, want expired lease error")
	}
	if _, err := store.StartAttempt(ctx, job.ID, "worker-1", nil, nil, nil, now); err != nil {
		t.Fatalf("StartAttempt() with lease owner error = %v", err)
	}
}

func TestStartAttemptRequiresLeaseDeadline(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := testNow()

	source := upsertTestSource(t, ctx, store, "movies", "Movie.mkv")
	job, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    source.ID,
		LibraryName: source.LibraryName,
		Now:         now,
	})
	if err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE jobs
SET state = ?, lease_owner = ?, lease_deadline = NULL
WHERE id = ?
`, string(domain.JobStateLeased), "worker-1", int64(job.ID)); err != nil {
		t.Fatalf("force malformed lease: %v", err)
	}

	_, err = store.StartAttempt(ctx, job.ID, "worker-1", nil, nil, nil, now)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("StartAttempt() error = %v, want ErrNotFound", err)
	}
}

func TestRecordAttemptEvent(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := testNow()

	source := upsertTestSource(t, ctx, store, "movies", "Movie.mkv")
	if _, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    source.ID,
		LibraryName: source.LibraryName,
		Now:         now,
	}); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	_, attempt := leaseAndStartAttempt(t, ctx, store, now, "worker-1")

	event, err := store.RecordAttemptEvent(ctx, domain.AttemptEvent{
		AttemptID: attempt.ID,
		Type:      domain.AttemptEventBlockStarted,
		Name:      "probe",
		Message:   "started",
		Payload:   []byte(`{"ok":true}`),
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("RecordAttemptEvent() error = %v", err)
	}
	if event.ID == 0 {
		t.Fatal("event ID = 0, want stored event")
	}

	events, err := store.ListAttemptEvents(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("ListAttemptEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	if events[0].Name != "probe" {
		t.Fatalf("event name = %q, want probe", events[0].Name)
	}
}

func TestRecoverStaleJobsRequeuesThenFailsAtRetryLimit(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := testNow()

	source := upsertTestSource(t, ctx, store, "movies", "Movie.mkv")
	if _, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    source.ID,
		LibraryName: source.LibraryName,
		Now:         now,
	}); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	job, attempt := leaseAndStartAttempt(t, ctx, store, now, "worker-1")
	recovered, err := store.RecoverStaleJobs(ctx, 2, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("RecoverStaleJobs() error = %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}
	job, err = store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if job.State != domain.JobStatePending {
		t.Fatalf("job state = %q, want %q", job.State, domain.JobStatePending)
	}
	attempt, err = store.GetAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("GetAttempt() error = %v", err)
	}
	if attempt.State != domain.AttemptStateCanceled {
		t.Fatalf("attempt state = %q, want %q", attempt.State, domain.AttemptStateCanceled)
	}

	job, _ = leaseAndStartAttempt(t, ctx, store, now.Add(3*time.Minute), "worker-2")
	recovered, err = store.RecoverStaleJobs(ctx, 2, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("second RecoverStaleJobs() error = %v", err)
	}
	if recovered != 1 {
		t.Fatalf("second recovered = %d, want 1", recovered)
	}
	job, err = store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if job.State != domain.JobStateFailed {
		t.Fatalf("job state = %q, want %q", job.State, domain.JobStateFailed)
	}
}

func openTestStore(t *testing.T) *SQLiteStore {
	t.Helper()

	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "anvil.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	return store
}

func upsertTestSource(t *testing.T, ctx context.Context, store *SQLiteStore, libraryName, relativePath string) domain.MediaSource {
	t.Helper()

	source, err := store.UpsertMediaSource(ctx, domain.MediaSource{
		LibraryName:  domain.LibraryName(libraryName),
		Kind:         domain.SourceKindFile,
		RelativePath: relativePath,
		Status:       domain.MediaSourceActive,
		Fingerprint: domain.FileFingerprint{
			SizeBytes: 100,
			ModTime:   testNow(),
		},
		LastSeenAt: testNow(),
	})
	if err != nil {
		t.Fatalf("UpsertMediaSource() error = %v", err)
	}
	return source
}

func upsertTestAsset(t *testing.T, ctx context.Context, store *SQLiteStore, sourceID domain.MediaSourceID, relativePath string) domain.MediaAsset {
	t.Helper()

	asset, err := store.UpsertMediaAsset(ctx, domain.MediaAsset{
		SourceID:     sourceID,
		RelativePath: relativePath,
		Role:         domain.MediaAssetRolePrimaryVideo,
		Status:       domain.MediaAssetActive,
		Fingerprint: domain.FileFingerprint{
			SizeBytes: 100,
			ModTime:   testNow(),
		},
		LastSeenAt: testNow(),
	})
	if err != nil {
		t.Fatalf("UpsertMediaAsset() error = %v", err)
	}
	return asset
}

func enqueueFailedJob(t *testing.T, ctx context.Context, store *SQLiteStore, libraryName, relativePath string, now time.Time) domain.Job {
	t.Helper()

	source := upsertTestSource(t, ctx, store, libraryName, relativePath)
	if _, _, err := store.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    source.ID,
		LibraryName: source.LibraryName,
		Now:         now,
	}); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	job, err := store.LeaseNextJob(ctx, "worker-"+libraryName, now.Add(time.Minute), now)
	if err != nil {
		t.Fatalf("LeaseNextJob() error = %v", err)
	}
	if _, err := store.TransitionJob(ctx, job.ID, domain.JobStateRunning, now, ""); err != nil {
		t.Fatalf("running transition: %v", err)
	}
	failed, err := store.TransitionJob(ctx, job.ID, domain.JobStateFailed, now.Add(time.Second), "failed")
	if err != nil {
		t.Fatalf("failed transition: %v", err)
	}
	return failed
}

func leaseAndStartAttempt(t *testing.T, ctx context.Context, store *SQLiteStore, now time.Time, workerID string) (domain.Job, domain.Attempt) {
	t.Helper()

	job, err := store.LeaseNextJob(ctx, workerID, now.Add(time.Minute), now)
	if err != nil {
		t.Fatalf("LeaseNextJob() error = %v", err)
	}
	if job == nil {
		t.Fatal("LeaseNextJob() = nil, want job")
	}
	attempt, err := store.StartAttempt(ctx, job.ID, workerID, []byte(`{"name":"movies"}`), []byte(`{"name":"flow"}`), []byte(`{"name":"profile"}`), now)
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	return *job, attempt
}

func testNow() time.Time {
	return time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
}

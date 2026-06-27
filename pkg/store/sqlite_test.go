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

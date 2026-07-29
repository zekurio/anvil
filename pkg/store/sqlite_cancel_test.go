package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestCancelPendingJobIsNeverLeased(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	source := upsertTestSource(t, ctx, state, "movies", "Movie.mkv")
	job, _, err := state.EnqueueJob(ctx, EnqueueJobInput{SourceID: source.ID, LibraryName: source.LibraryName, Now: now})
	if err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}

	results, err := state.CancelJobs(ctx, CancelJobsInput{IDs: []domain.JobID{job.ID}, Now: now})
	if err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	if len(results) != 1 || !results[0].Canceled || results[0].PreviousState != domain.JobStatePending || results[0].State != domain.JobStateCanceled {
		t.Fatalf("CancelJobs() = %+v", results)
	}

	canceled, err := state.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if canceled.State != domain.JobStateCanceled || canceled.CompletedAt == nil || canceled.LeaseOwner != "" || canceled.LeaseDeadline != nil {
		t.Fatalf("canceled job = %+v", canceled)
	}
	leased, err := state.LeaseNextJob(ctx, "worker-1", now.Add(time.Minute), now)
	if err != nil {
		t.Fatalf("LeaseNextJob() error = %v", err)
	}
	if leased != nil {
		t.Fatalf("LeaseNextJob() leased canceled job %+v", leased)
	}
}

func TestCancelRunningJobCancelsAttemptAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	source := upsertTestSource(t, ctx, state, "movies", "Movie.mkv")
	if _, _, err := state.EnqueueJob(ctx, EnqueueJobInput{SourceID: source.ID, LibraryName: source.LibraryName, Now: now}); err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}
	leased, err := state.LeaseNextJob(ctx, "worker-1", now.Add(time.Minute), now)
	if err != nil || leased == nil {
		t.Fatalf("LeaseNextJob() = %v, %v", leased, err)
	}
	attempt, err := state.StartAttempt(ctx, leased.ID, "worker-1", nil, nil, nil, now)
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}

	results, err := state.CancelJobs(ctx, CancelJobsInput{IDs: []domain.JobID{leased.ID}, Reason: "operator abort", Now: now})
	if err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	if len(results) != 1 || !results[0].Canceled || results[0].PreviousState != domain.JobStateRunning {
		t.Fatalf("CancelJobs() = %+v", results)
	}
	canceledAttempt, err := state.GetAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("GetAttempt() error = %v", err)
	}
	if canceledAttempt.State != domain.AttemptStateCanceled || canceledAttempt.FinishedAt == nil || canceledAttempt.Error != "operator abort" {
		t.Fatalf("attempt = %+v", canceledAttempt)
	}

	repeat, err := state.CancelJobs(ctx, CancelJobsInput{IDs: []domain.JobID{leased.ID}, Now: now})
	if err != nil {
		t.Fatalf("repeat CancelJobs() error = %v", err)
	}
	if len(repeat) != 1 || repeat[0].Canceled || repeat[0].State != domain.JobStateCanceled {
		t.Fatalf("repeat CancelJobs() = %+v, want idempotent no-op", repeat)
	}
	job, err := state.GetJob(ctx, leased.ID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if job.LastError != "operator abort" {
		t.Fatalf("repeat cancel overwrote the recorded reason: %q", job.LastError)
	}
}

// TestTransitionToCanceledPreservesTheFirstRecordedOutcome covers the live
// daemon ordering: the control API cancels the row with the operator reason,
// then the unwinding worker re-asserts the same terminal state.
func TestTransitionToCanceledPreservesTheFirstRecordedOutcome(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	source := upsertTestSource(t, ctx, state, "movies", "Movie.mkv")
	job, _, err := state.EnqueueJob(ctx, EnqueueJobInput{SourceID: source.ID, LibraryName: source.LibraryName, Now: now})
	if err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}
	if _, err := state.CancelJobs(ctx, CancelJobsInput{IDs: []domain.JobID{job.ID}, Reason: "queued by mistake", Now: now}); err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	canceled, err := state.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}

	again, err := state.TransitionJob(ctx, job.ID, domain.JobStateCanceled, now.Add(time.Minute), "job canceled by operator")
	if err != nil {
		t.Fatalf("TransitionJob(canceled) error = %v", err)
	}
	if again.LastError != "queued by mistake" {
		t.Fatalf("last error = %q, want the original cancel reason", again.LastError)
	}
	if again.CompletedAt == nil || !again.CompletedAt.Equal(*canceled.CompletedAt) {
		t.Fatalf("completed at = %v, want %v", again.CompletedAt, canceled.CompletedAt)
	}
}

func TestCancelJobsRejectsEmptySelectionAndUnknownJobs(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	if _, err := state.CancelJobs(ctx, CancelJobsInput{Now: testNow()}); err == nil {
		t.Fatal("CancelJobs() with no ids error = nil, want failure")
	}
	if _, err := state.CancelJobs(ctx, CancelJobsInput{IDs: []domain.JobID{404}, Now: testNow()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CancelJobs(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestCanceledJobCanBeRetried(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	source := upsertTestSource(t, ctx, state, "movies", "Movie.mkv")
	job, _, err := state.EnqueueJob(ctx, EnqueueJobInput{SourceID: source.ID, LibraryName: source.LibraryName, Now: now})
	if err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}
	if _, err := state.CancelJobs(ctx, CancelJobsInput{IDs: []domain.JobID{job.ID}, Now: now}); err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	retried, err := state.RetryJob(ctx, job.ID, now)
	if err != nil {
		t.Fatalf("RetryJob() error = %v", err)
	}
	if retried.State != domain.JobStatePending || retried.CompletedAt != nil {
		t.Fatalf("RetryJob() = %+v, want pending", retried)
	}
}

func TestCancelJobsCountsAreReportedSeparatelyFromSkipped(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	source := upsertTestSource(t, ctx, state, "movies", "Movie.mkv")
	job, _, err := state.EnqueueJob(ctx, EnqueueJobInput{SourceID: source.ID, LibraryName: source.LibraryName, Now: now})
	if err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}
	if _, err := state.CancelJobs(ctx, CancelJobsInput{IDs: []domain.JobID{job.ID}, Now: now}); err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	counts, err := state.CountJobsByState(ctx)
	if err != nil {
		t.Fatalf("CountJobsByState() error = %v", err)
	}
	if counts[domain.JobStateCanceled] != 1 || counts[domain.JobStateSkipped] != 0 {
		t.Fatalf("counts = %+v, want one canceled and no skipped", counts)
	}
	listed, err := state.ListJobs(ctx, JobListFilter{States: []domain.JobState{domain.JobStateCanceled}})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if len(listed) != 1 || listed[0].Job.ID != job.ID {
		t.Fatalf("ListJobs(canceled) = %+v", listed)
	}
}

// TestUpgradeFromSchemaVersion5PreservesJobs proves an existing database is
// migrated in place instead of requiring an operator reset, and that the
// rebuilt jobs table keeps its rows, indexes, and child records.
func TestUpgradeFromSchemaVersion5PreservesJobs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "anvil.db")
	legacy := legacySchemaVersion5(t)

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, legacy); err != nil {
		t.Fatalf("create version 5 schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO schema_migrations (version, applied_at) VALUES (5, '2026-07-21T00:00:00Z');
INSERT INTO media_sources (id, library_name, kind, relative_path, generation, is_current, status, first_seen_at, last_seen_at, updated_at)
VALUES (1, 'movies', 'file', 'Movie.mkv', 1, 1, 'active', '2026-07-21T00:00:00Z', '2026-07-21T00:00:00Z', '2026-07-21T00:00:00Z');
INSERT INTO jobs (id, slug, source_id, library_name, state, created_at, updated_at)
VALUES (7, 'kind-pink-heron', 1, 'movies', 'running', '2026-07-21T00:00:00Z', '2026-07-21T00:00:00Z');
INSERT INTO attempts (id, job_id, number, worker_id, state, started_at)
VALUES (3, 7, 1, 'worker-1', 'running', '2026-07-21T00:00:00Z');
`); err != nil {
		t.Fatalf("seed version 5 rows: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 5 database: %v", err)
	}

	state, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	version, exists, err := state.schemaVersion(ctx)
	if err != nil || !exists || version != currentSchemaVersion {
		t.Fatalf("schemaVersion() = %d, %t, %v; want %d", version, exists, err, currentSchemaVersion)
	}
	job, err := state.GetJob(ctx, 7)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if job.Slug != "kind-pink-heron" || job.State != domain.JobStateRunning {
		t.Fatalf("migrated job = %+v", job)
	}
	attempts, err := state.ListAttemptsForJob(ctx, 7)
	if err != nil {
		t.Fatalf("ListAttemptsForJob() error = %v", err)
	}
	if len(attempts) != 1 || attempts[0].ID != 3 {
		t.Fatalf("migrated attempts = %+v, want the seeded attempt to survive the rebuild", attempts)
	}
	if _, err := state.CancelJobs(ctx, CancelJobsInput{IDs: []domain.JobID{7}, Now: testNow()}); err != nil {
		t.Fatalf("CancelJobs() after migration error = %v", err)
	}
	var indexes int
	if err := state.db.QueryRowContext(ctx, `
SELECT count(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'jobs' AND name LIKE 'jobs_%'
`).Scan(&indexes); err != nil {
		t.Fatalf("count jobs indexes: %v", err)
	}
	if indexes != 3 {
		t.Fatalf("jobs indexes = %d, want 3 after the rebuild", indexes)
	}
}

func TestExistingSchemaWithoutCoreTablesStillRequiresReset(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "anvil.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
INSERT INTO schema_migrations (version, applied_at) VALUES (5, '2026-07-21T00:00:00Z');
`); err != nil {
		t.Fatalf("create partial schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close partial schema: %v", err)
	}
	if _, err := Open(ctx, path); !errors.Is(err, ErrIncompatibleSchema) {
		t.Fatalf("Open() error = %v, want ErrIncompatibleSchema", err)
	}
}

// legacySchemaVersion5 is the released version 5 schema: identical to the
// current one except that jobs.state does not accept 'canceled'.
func legacySchemaVersion5(t *testing.T) string {
	t.Helper()
	const canceled = ", 'canceled')"
	if !strings.Contains(currentSchema, canceled) {
		t.Fatal("current schema no longer contains the canceled job state")
	}
	return strings.Replace(currentSchema, canceled, ")", 1)
}

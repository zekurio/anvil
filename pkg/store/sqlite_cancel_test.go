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
	replacepkg "github.com/zekurio/anvil/pkg/replace"
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

// TestCancelJobsRejectsEmptySelectionAndReportsUnknownJobs pins that one job
// pruned between listing and cancelling never fails the whole batch.
func TestCancelJobsRejectsEmptySelectionAndReportsUnknownJobs(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	if _, err := state.CancelJobs(ctx, CancelJobsInput{Now: now}); err == nil {
		t.Fatal("CancelJobs() with no ids error = nil, want failure")
	}
	source := upsertTestSource(t, ctx, state, "movies", "Movie.mkv")
	job, _, err := state.EnqueueJob(ctx, EnqueueJobInput{SourceID: source.ID, LibraryName: source.LibraryName, Now: now})
	if err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}

	results, err := state.CancelJobs(ctx, CancelJobsInput{IDs: []domain.JobID{404, job.ID}, Now: now})
	if err != nil {
		t.Fatalf("CancelJobs() error = %v, want the missing job reported per job", err)
	}
	if len(results) != 2 {
		t.Fatalf("CancelJobs() = %+v, want one result per requested id", results)
	}
	if results[0].JobID != 404 || results[0].Canceled || results[0].SkipReason != CancelSkipMissing {
		t.Fatalf("missing job result = %+v", results[0])
	}
	if !results[1].Canceled || results[1].State != domain.JobStateCanceled {
		t.Fatalf("present job result = %+v, want it canceled anyway", results[1])
	}
}

// TestCancelJobsRefusesAJobWithAJournaledPublish is the data-safety case from
// the publish journal: cancelling between prepare and commit would clear the
// lease of a job that can never be re-leased, recovered, or rescanned, leaving
// the destination file, the backup, and the journal row stranded.
func TestCancelJobsRefusesAJobWithAJournaledPublish(t *testing.T) {
	stages := []replacepkg.PublishStage{
		replacepkg.PublishStagePrepared,
		replacepkg.PublishStagePublished,
		replacepkg.PublishStageSourceCleaned,
		replacepkg.PublishStageCommitted,
	}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			ctx := context.Background()
			state := openTestStore(t)
			now := testNow()
			job := runningJobWithPublishStage(t, ctx, state, stage)

			results, err := state.CancelJobs(ctx, CancelJobsInput{IDs: []domain.JobID{job.ID}, Now: now})
			if err != nil {
				t.Fatalf("CancelJobs() error = %v", err)
			}
			if len(results) != 1 || results[0].Canceled || results[0].SkipReason != CancelSkipPublishInFlight {
				t.Fatalf("CancelJobs() = %+v, want a refused cancel", results)
			}
			stored, err := state.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatalf("GetJob() error = %v", err)
			}
			if stored.State != domain.JobStateRunning || stored.LeaseOwner != "worker-1" {
				t.Fatalf("job = %+v, want the running lease untouched", stored)
			}
		})
	}
}

func TestCancelJobsAllowsAConflictedPublish(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	job := runningJobWithPublishStage(t, ctx, state, replacepkg.PublishStageConflict)

	results, err := state.CancelJobs(ctx, CancelJobsInput{IDs: []domain.JobID{job.ID}, Now: now})
	if err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	if len(results) != 1 || !results[0].Canceled {
		t.Fatalf("CancelJobs() = %+v, want a conflicted publish to stay cancelable", results)
	}
}

// TestCreatePublishOperationRefusesACanceledJob is the other half of the mutual
// exclusion: a publish that starts after the cancel committed must not journal
// itself, or it would mutate the destination of a terminally canceled job.
func TestCreatePublishOperationRefusesACanceledJob(t *testing.T) {
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

	err = state.CreatePublishOperation(ctx, replacepkg.PublishOperation{
		JobID: job.ID, Kind: "replacement", Mode: "replace", Stage: replacepkg.PublishStagePrepared,
		ArtifactPath: "/staging/output.mkv", DestinationPath: "/movies/Movie.mkv",
		CreatedAt: now, UpdatedAt: now,
	})
	if !errors.Is(err, ErrJobCanceled) {
		t.Fatalf("CreatePublishOperation() error = %v, want ErrJobCanceled", err)
	}
	if _, ok, err := state.GetPublishOperation(ctx, job.ID); err != nil || ok {
		t.Fatalf("GetPublishOperation() = %t, %v, want no journaled publish", ok, err)
	}
}

// TestCancelJobsRechecksTheSelectorStateFilter covers a job that left the state
// the selector asked for between listing and cancelling.
func TestCancelJobsRechecksTheSelectorStateFilter(t *testing.T) {
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

	results, err := state.CancelJobs(ctx, CancelJobsInput{
		IDs: []domain.JobID{leased.ID}, States: []domain.JobState{domain.JobStatePending}, Now: now,
	})
	if err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	if len(results) != 1 || results[0].Canceled || results[0].SkipReason != CancelSkipStateChanged {
		t.Fatalf("CancelJobs() = %+v, want the state filter re-checked", results)
	}
	stored, err := state.GetJob(ctx, leased.ID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if stored.State != domain.JobStateLeased {
		t.Fatalf("job state = %q, want the leased job untouched", stored.State)
	}

	matching, err := state.CancelJobs(ctx, CancelJobsInput{
		IDs: []domain.JobID{leased.ID}, States: []domain.JobState{domain.JobStateLeased}, Now: now,
	})
	if err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	if len(matching) != 1 || !matching[0].Canceled {
		t.Fatalf("CancelJobs() = %+v, want a matching state to cancel", matching)
	}
}

func TestCancelJobsReportsAnAlreadyTerminalJob(t *testing.T) {
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
	results, err := state.CancelJobs(ctx, CancelJobsInput{IDs: []domain.JobID{job.ID}, Now: now})
	if err != nil {
		t.Fatalf("repeat CancelJobs() error = %v", err)
	}
	if len(results) != 1 || results[0].Canceled || results[0].SkipReason != CancelSkipAlreadyTerminal {
		t.Fatalf("repeat CancelJobs() = %+v", results)
	}
}

func runningJobWithPublishStage(t *testing.T, ctx context.Context, state *SQLiteStore, stage replacepkg.PublishStage) domain.Job {
	t.Helper()
	now := testNow()
	source := upsertTestSource(t, ctx, state, "movies", "Movie.mkv")
	if _, _, err := state.EnqueueJob(ctx, EnqueueJobInput{SourceID: source.ID, LibraryName: source.LibraryName, Now: now}); err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}
	leased, err := state.LeaseNextJob(ctx, "worker-1", now.Add(time.Minute), now)
	if err != nil || leased == nil {
		t.Fatalf("LeaseNextJob() = %v, %v", leased, err)
	}
	if _, err := state.StartAttempt(ctx, leased.ID, "worker-1", nil, nil, nil, now); err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	operation := replacepkg.PublishOperation{
		JobID: leased.ID, Kind: "replacement", Mode: "replace", Stage: replacepkg.PublishStagePrepared,
		ArtifactPath: "/staging/output.mkv", DestinationPath: "/movies/Movie.mkv",
		BackupPath: "/movies/Movie.mkv.anvil-backup", CreatedAt: now, UpdatedAt: now,
	}
	if err := state.CreatePublishOperation(ctx, operation); err != nil {
		t.Fatalf("CreatePublishOperation() error = %v", err)
	}
	if stage != replacepkg.PublishStagePrepared {
		previous := operation.Stage
		operation.Stage = stage
		if err := state.UpdatePublishOperation(ctx, operation, previous); err != nil {
			t.Fatalf("UpdatePublishOperation() error = %v", err)
		}
	}
	job, err := state.GetJob(ctx, leased.ID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	return job
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

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, legacySchemaVersion5); err != nil {
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

// TestUpgradeFromSchemaVersion5KeepsForeignKeysEnforced guards the table
// rebuild: the migration disables foreign keys outside its transaction, and the
// inbound REFERENCES clauses would be silently rewritten to the temporary table
// name if that pragma were dropped or scoped wrong.
func TestUpgradeFromSchemaVersion5KeepsForeignKeysEnforced(t *testing.T) {
	ctx := context.Background()
	state := openMigratedVersion5Store(t, ctx)

	var foreignKeys int
	if err := state.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1 after the migration", foreignKeys)
	}
	for _, table := range []string{"attempts", "publish_operations"} {
		var ddl string
		if err := state.db.QueryRowContext(ctx, `
SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?
`, table).Scan(&ddl); err != nil {
			t.Fatalf("read %s ddl: %v", table, err)
		}
		if !strings.Contains(ddl, "REFERENCES jobs(id) ON DELETE CASCADE") {
			t.Fatalf("%s ddl lost its inbound foreign key: %s", table, ddl)
		}
	}
	if _, err := state.db.ExecContext(ctx, `
INSERT INTO attempts (job_id, number, worker_id, state, started_at)
VALUES (404, 1, 'worker-1', 'running', '2026-07-21T00:00:00Z')
`); err == nil {
		t.Fatal("insert attempt for an unknown job error = nil, want a foreign key violation")
	}
	for _, table := range []string{"attempts", "attempt_events", "publish_operations"} {
		var rows int
		if err := state.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&rows); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if rows != 1 {
			t.Fatalf("%s rows = %d, want the seeded child row to survive the rebuild", table, rows)
		}
	}
	if _, err := state.db.ExecContext(ctx, `DELETE FROM jobs WHERE id = 7`); err != nil {
		t.Fatalf("delete migrated job: %v", err)
	}
	for _, table := range []string{"attempts", "attempt_events", "publish_operations"} {
		var rows int
		if err := state.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&rows); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if rows != 0 {
			t.Fatalf("%s rows = %d, want the delete to cascade after the rebuild", table, rows)
		}
	}
}

// TestUpgradeFromSchemaVersion5PreservesAutoincrement proves the rebuild keeps
// sqlite_sequence, so a job id is never reused after a deletion.
func TestUpgradeFromSchemaVersion5PreservesAutoincrement(t *testing.T) {
	ctx := context.Background()
	state := openMigratedVersion5Store(t, ctx)

	if _, err := state.db.ExecContext(ctx, `DELETE FROM jobs WHERE id = 7`); err != nil {
		t.Fatalf("delete migrated job: %v", err)
	}
	if _, err := state.db.ExecContext(ctx, `
INSERT INTO jobs (slug, source_id, library_name, state, created_at, updated_at)
VALUES ('brave-teal-otter', 1, 'movies', 'pending', '2026-07-21T00:00:00Z', '2026-07-21T00:00:00Z')
`); err != nil {
		t.Fatalf("insert job after the rebuild: %v", err)
	}
	var id int64
	if err := state.db.QueryRowContext(ctx, `SELECT id FROM jobs WHERE slug = 'brave-teal-otter'`).Scan(&id); err != nil {
		t.Fatalf("read new job id: %v", err)
	}
	if id != 8 {
		t.Fatalf("new job id = %d, want 8 from the preserved autoincrement counter", id)
	}
}

func TestSchemaVersionBelowTheMigrationChainIsRejected(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "anvil.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, legacySchemaVersion5); err != nil {
		t.Fatalf("create version 5 schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO schema_migrations (version, applied_at) VALUES (3, '2026-07-21T00:00:00Z')
`); err != nil {
		t.Fatalf("stamp pre-migration-chain version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	if _, err := Open(ctx, path); !errors.Is(err, ErrIncompatibleSchema) {
		t.Fatalf("Open() error = %v, want ErrIncompatibleSchema for a pre-v%d schema", err, minUpgradableSchemaVersion)
	}
}

func openMigratedVersion5Store(t *testing.T, ctx context.Context) *SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "anvil.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, legacySchemaVersion5); err != nil {
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
INSERT INTO attempt_events (id, attempt_id, type, name, created_at)
VALUES (11, 3, 'block_started', 'encode', '2026-07-21T00:00:00Z');
INSERT INTO publish_operations (job_id, stage, operation_json, created_at, updated_at)
VALUES (7, 'prepared', x'', '2026-07-21T00:00:00Z', '2026-07-21T00:00:00Z');
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
	return state
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

// legacySchemaVersion5 is the literal released schema version 5 DDL. It is
// frozen on purpose: deriving it from currentSchema would silently retarget
// this migration test at whatever the newest schema happens to be.
const legacySchemaVersion5 = `
CREATE TABLE schema_migrations (
	version INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL
);

CREATE TABLE library_scans (
	library_name TEXT PRIMARY KEY,
	next_token INTEGER NOT NULL DEFAULT 0,
	applied_token INTEGER NOT NULL DEFAULT 0,
	applied_at TEXT
);

CREATE TABLE media_sources (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	library_name TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('file', 'package')),
	relative_path TEXT NOT NULL,
	generation INTEGER NOT NULL CHECK (generation >= 1),
	is_current INTEGER NOT NULL DEFAULT 1 CHECK (is_current IN (0, 1)),
	status TEXT NOT NULL CHECK (status IN ('active', 'processed', 'missing')),
	size_bytes INTEGER NOT NULL DEFAULT 0,
	mod_time TEXT,
	hash_algorithm TEXT NOT NULL DEFAULT '',
	hash_value TEXT NOT NULL DEFAULT '',
	first_seen_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (library_name, relative_path, generation)
);

CREATE UNIQUE INDEX media_sources_current_idx
ON media_sources(library_name, relative_path)
WHERE is_current = 1;

CREATE INDEX media_sources_library_path_idx
ON media_sources(library_name, relative_path, generation DESC);

CREATE TABLE media_assets (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	source_id INTEGER NOT NULL REFERENCES media_sources(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	generation INTEGER NOT NULL CHECK (generation >= 1),
	is_current INTEGER NOT NULL DEFAULT 1 CHECK (is_current IN (0, 1)),
	role TEXT NOT NULL CHECK (role IN ('primary_video', 'video', 'sample', 'subtitle', 'metadata', 'extra', 'unknown')),
	status TEXT NOT NULL CHECK (status IN ('active', 'processed', 'missing')),
	size_bytes INTEGER NOT NULL DEFAULT 0,
	mod_time TEXT,
	hash_algorithm TEXT NOT NULL DEFAULT '',
	hash_value TEXT NOT NULL DEFAULT '',
	first_seen_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (source_id, relative_path, generation)
);

CREATE UNIQUE INDEX media_assets_current_idx
ON media_assets(source_id, relative_path)
WHERE is_current = 1;

CREATE INDEX media_assets_source_path_idx
ON media_assets(source_id, relative_path, generation DESC);

CREATE TABLE jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	slug TEXT NOT NULL UNIQUE,
	source_id INTEGER NOT NULL REFERENCES media_sources(id) ON DELETE CASCADE,
	asset_id INTEGER REFERENCES media_assets(id) ON DELETE CASCADE,
	library_name TEXT NOT NULL,
	priority INTEGER NOT NULL DEFAULT 0,
	state TEXT NOT NULL CHECK (state IN ('pending', 'leased', 'running', 'validating', 'replacing', 'complete', 'failed', 'retrying', 'skipped')),
	lease_owner TEXT NOT NULL DEFAULT '',
	lease_deadline TEXT,
	heartbeat_at TEXT,
	attempt_count INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	input_size_bytes INTEGER NOT NULL DEFAULT 0,
	output_size_bytes INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	completed_at TEXT,
	pipeline_context_json BLOB NOT NULL DEFAULT x''
);

CREATE UNIQUE INDEX jobs_target_idx
ON jobs(source_id, ifnull(asset_id, 0));

CREATE INDEX jobs_state_priority_idx
ON jobs(state, priority DESC, created_at ASC, id ASC);

CREATE INDEX jobs_lease_deadline_idx
ON jobs(lease_deadline)
WHERE lease_deadline IS NOT NULL;

CREATE TABLE attempts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	number INTEGER NOT NULL,
	worker_id TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('running', 'succeeded', 'failed', 'canceled')),
	resolved_library_json BLOB NOT NULL DEFAULT x'',
	resolved_flow_json BLOB NOT NULL DEFAULT x'',
	resolved_profile_json BLOB NOT NULL DEFAULT x'',
	started_at TEXT NOT NULL,
	finished_at TEXT,
	error TEXT NOT NULL DEFAULT '',
	UNIQUE (job_id, number)
);

CREATE TABLE attempt_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	attempt_id INTEGER NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
	type TEXT NOT NULL CHECK (type IN ('block_started', 'block_finished', 'block_failed', 'artifact')),
	name TEXT NOT NULL,
	message TEXT NOT NULL DEFAULT '',
	payload BLOB NOT NULL DEFAULT x'',
	created_at TEXT NOT NULL
);

CREATE INDEX attempt_events_attempt_idx
ON attempt_events(attempt_id, id);

CREATE TABLE publish_operations (
	job_id INTEGER PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
	stage TEXT NOT NULL CHECK (stage IN ('prepared', 'published', 'source_cleaned', 'committed', 'conflict')),
	operation_json BLOB NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
`

// TestCompleteJobOccurrenceRefusesACanceledJob is the guard that keeps the
// worker's "a finished pipeline is authoritative" rule honest: completion runs
// on a context detached from the cancellation, so the state machine, not the
// context, has to be what stops a canceled job from being completed.
func TestCompleteJobOccurrenceRefusesACanceledJob(t *testing.T) {
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
	if _, err := state.CancelJobs(ctx, CancelJobsInput{IDs: []domain.JobID{leased.ID}, Now: now}); err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}

	if _, err := state.CompleteJobOccurrence(ctx, CompleteJobOccurrenceInput{
		JobID: leased.ID, AttemptID: attempt.ID, CompletedAt: now,
	}); err == nil {
		t.Fatal("CompleteJobOccurrence() error = nil, want a canceled job to stay canceled")
	}
	job, err := state.GetJob(ctx, leased.ID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if job.State != domain.JobStateCanceled {
		t.Fatalf("job state = %q, want canceled", job.State)
	}
}

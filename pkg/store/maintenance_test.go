package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestBackupIncludesCommittedWALStateAndPassesIntegrityCheck(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	storePath := filepath.Join(directory, "anvil.db")
	state, err := Open(ctx, storePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	if _, err := state.db.ExecContext(ctx, `PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatalf("disable WAL autocheckpoint: %v", err)
	}
	source := upsertTestSource(t, ctx, state, "movies", "WAL Movie.mkv")
	if _, _, err := state.EnqueueJob(ctx, EnqueueJobInput{
		SourceID: source.ID, LibraryName: source.LibraryName, Now: testNow(),
	}); err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}
	walInfo, err := os.Stat(storePath + "-wal")
	if err != nil {
		t.Fatalf("stat live WAL: %v", err)
	}
	if walInfo.Size() == 0 {
		t.Fatal("live WAL is empty, want committed state in WAL")
	}

	backupPath := filepath.Join(directory, "backup.db")
	result, err := state.Backup(ctx, backupPath)
	if err != nil {
		t.Fatalf("Backup() error = %v", err)
	}
	if result.Path != backupPath || result.SizeBytes == 0 || result.Integrity != "ok" {
		t.Fatalf("Backup() result = %+v, want installed integral backup", result)
	}

	backup, err := sql.Open("sqlite", readOnlyDSN(backupPath))
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backup.Close() //nolint:errcheck
	var sources int
	var jobs int
	if err := backup.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_sources`).Scan(&sources); err != nil {
		t.Fatalf("count backed-up sources: %v", err)
	}
	if err := backup.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs`).Scan(&jobs); err != nil {
		t.Fatalf("count backed-up jobs: %v", err)
	}
	if sources != 1 || jobs != 1 {
		t.Fatalf("backup counts = sources %d jobs %d, want 1 and 1", sources, jobs)
	}
}

func TestBackupNeverOverwritesDestination(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	destination := filepath.Join(t.TempDir(), "existing.db")
	const sentinel = "operator data"
	if err := os.WriteFile(destination, []byte(sentinel), 0o600); err != nil {
		t.Fatalf("write destination sentinel: %v", err)
	}
	if _, err := state.Backup(ctx, destination); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Backup() error = %v, want existing destination refusal", err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination sentinel: %v", err)
	}
	if string(contents) != sentinel {
		t.Fatalf("destination contents = %q, want untouched sentinel", contents)
	}
}

func TestBackupRefusesUnsafeDestination(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	for _, destination := range []string{"", ":memory:", "file:backup.db", filepath.Join(t.TempDir(), "missing", "backup.db")} {
		if _, err := state.Backup(ctx, destination); err == nil {
			t.Fatalf("Backup(%q) error = nil, want unsafe destination refusal", destination)
		}
	}
}

func TestPruneMissingSourceJobsDryRunPreservesEverything(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	missingComplete := createPruneTestJob(t, ctx, state, "movies", "missing-complete.mkv", domain.JobStateComplete, true)
	presentComplete := createPruneTestJob(t, ctx, state, "movies", "present-complete.mkv", domain.JobStateComplete, false)
	missingPending := createPruneTestJob(t, ctx, state, "movies", "missing-pending.mkv", domain.JobStatePending, true)

	result, err := state.PruneMissingSourceJobs(ctx, PruneMissingSourceJobsOptions{})
	if err != nil {
		t.Fatalf("PruneMissingSourceJobs() error = %v", err)
	}
	if !result.DryRun || result.MatchedJobs != 1 || result.AffectedSources != 1 || result.DeletedJobs != 0 {
		t.Fatalf("dry-run result = %+v, want one match and no deletion", result)
	}
	for _, id := range []domain.JobID{missingComplete, presentComplete, missingPending} {
		if _, err := state.GetJob(ctx, id); err != nil {
			t.Fatalf("GetJob(%d) after dry run error = %v", id, err)
		}
	}
}

func TestPruneMissingSourceJobsAppliesLibraryAndStateFilters(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	movieComplete := createPruneTestJob(t, ctx, state, "movies", "complete.mkv", domain.JobStateComplete, true)
	movieFailed := createPruneTestJob(t, ctx, state, "movies", "failed.mkv", domain.JobStateFailed, true)
	tvComplete := createPruneTestJob(t, ctx, state, "tv", "episode.mkv", domain.JobStateComplete, true)
	presentComplete := createPruneTestJob(t, ctx, state, "movies", "present.mkv", domain.JobStateComplete, false)
	if _, err := state.db.ExecContext(ctx, `
INSERT INTO attempts (job_id, number, worker_id, state, started_at)
VALUES (?, 1, 'worker-history', ?, ?)
`, int64(movieComplete), string(domain.AttemptStateSucceeded), encodeTime(testNow())); err != nil {
		t.Fatalf("insert attempt history: %v", err)
	}

	result, err := state.PruneMissingSourceJobs(ctx, PruneMissingSourceJobsOptions{
		LibraryName: "movies",
		States:      []domain.JobState{domain.JobStateComplete},
		Apply:       true,
	})
	if err != nil {
		t.Fatalf("PruneMissingSourceJobs() error = %v", err)
	}
	if result.DryRun || result.MatchedJobs != 1 || result.DeletedJobs != 1 || result.ByState[domain.JobStateComplete] != 1 {
		t.Fatalf("apply result = %+v, want one completed movie deletion", result)
	}
	if _, err := state.GetJob(ctx, movieComplete); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetJob(deleted) error = %v, want ErrNotFound", err)
	}
	var attempts int
	if err := state.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE job_id = ?`, int64(movieComplete)).Scan(&attempts); err != nil {
		t.Fatalf("count attempt history after prune: %v", err)
	}
	if attempts != 0 {
		t.Fatalf("attempt history count = %d, want cascade delete", attempts)
	}
	for _, id := range []domain.JobID{movieFailed, tvComplete, presentComplete} {
		if _, err := state.GetJob(ctx, id); err != nil {
			t.Fatalf("GetJob(preserved %d) error = %v", id, err)
		}
	}
}

func TestPruneMissingSourceJobsRejectsActiveStates(t *testing.T) {
	state := openTestStore(t)
	_, err := state.PruneMissingSourceJobs(context.Background(), PruneMissingSourceJobsOptions{
		States: []domain.JobState{domain.JobStateRunning},
	})
	if err == nil {
		t.Fatal("PruneMissingSourceJobs() error = nil, want active-state refusal")
	}
}

func TestHasViableQueueWorkForLibraryMirrorsOccurrenceEligibility(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	source := upsertTestSource(t, ctx, state, "movies", "Movie.mkv")
	asset := upsertTestAsset(t, ctx, state, source.ID, "Movie.mkv")
	job, _, err := state.EnqueueJob(ctx, EnqueueJobInput{
		SourceID: source.ID, AssetID: asset.ID, LibraryName: source.LibraryName, Now: testNow(),
	})
	if err != nil {
		t.Fatalf("EnqueueJob() error = %v", err)
	}

	for _, jobState := range []domain.JobState{domain.JobStatePending, domain.JobStateLeased, domain.JobStateRetrying} {
		if _, err := state.db.ExecContext(ctx, `UPDATE jobs SET state = ? WHERE id = ?`, string(jobState), int64(job.ID)); err != nil {
			t.Fatalf("set job state %q: %v", jobState, err)
		}
		viable, err := state.HasViableQueueWorkForLibrary(ctx, "movies")
		if err != nil {
			t.Fatalf("HasViableQueueWorkForLibrary(%q) error = %v", jobState, err)
		}
		if !viable {
			t.Fatalf("HasViableQueueWorkForLibrary(%q) = false, want true", jobState)
		}
	}

	if _, err := state.db.ExecContext(ctx, `UPDATE jobs SET state = ? WHERE id = ?`, string(domain.JobStateFailed), int64(job.ID)); err != nil {
		t.Fatalf("set terminal job state: %v", err)
	}
	viable, err := state.HasViableQueueWorkForLibrary(ctx, "movies")
	if err != nil {
		t.Fatalf("HasViableQueueWorkForLibrary(failed) error = %v", err)
	}
	if viable {
		t.Fatal("HasViableQueueWorkForLibrary(failed) = true, want false")
	}

	if _, err := state.db.ExecContext(ctx, `UPDATE jobs SET state = ? WHERE id = ?`, string(domain.JobStatePending), int64(job.ID)); err != nil {
		t.Fatalf("restore pending job state: %v", err)
	}
	if _, err := state.db.ExecContext(ctx, `UPDATE media_sources SET is_current = 0, status = ? WHERE id = ?`, string(domain.MediaSourceMissing), int64(source.ID)); err != nil {
		t.Fatalf("retire source occurrence: %v", err)
	}
	viable, err = state.HasViableQueueWorkForLibrary(ctx, "movies")
	if err != nil {
		t.Fatalf("HasViableQueueWorkForLibrary(retired) error = %v", err)
	}
	if viable {
		t.Fatal("HasViableQueueWorkForLibrary(retired) = true, want unleaseable ghost to be ignored")
	}
}

func createPruneTestJob(t *testing.T, ctx context.Context, state *SQLiteStore, library string, relativePath string, jobState domain.JobState, missing bool) domain.JobID {
	t.Helper()
	source := upsertTestSource(t, ctx, state, library, relativePath)
	job, _, err := state.EnqueueJob(ctx, EnqueueJobInput{
		SourceID: source.ID, LibraryName: source.LibraryName, Now: testNow(),
	})
	if err != nil {
		t.Fatalf("enqueue prune test job: %v", err)
	}
	if _, err := state.db.ExecContext(ctx, `UPDATE jobs SET state = ? WHERE id = ?`, string(jobState), int64(job.ID)); err != nil {
		t.Fatalf("set prune test job state: %v", err)
	}
	if missing {
		if _, err := state.db.ExecContext(ctx, `UPDATE media_sources SET status = ? WHERE id = ?`, string(domain.MediaSourceMissing), int64(source.ID)); err != nil {
			t.Fatalf("mark prune test source missing: %v", err)
		}
	}
	return job.ID
}

package controlapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/domain"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/staging"
	"github.com/zekurio/anvil/pkg/store"
)

func TestRetryJobsAcceptsIDsAndSlugs(t *testing.T) {
	ctx := context.Background()
	service, state, _, job := testService(t, ctx)
	failJob(t, ctx, state, job.ID)

	response, err := service.RetryJobs(ctx, control.JobRetryRequest{References: []string{job.Label()}})
	if err != nil {
		t.Fatalf("RetryJobs(slug) error = %v", err)
	}
	if len(response.Jobs) != 1 || response.Jobs[0].ID != int64(job.ID) || response.Jobs[0].State != string(domain.JobStatePending) {
		t.Fatalf("RetryJobs() = %+v", response)
	}

	// The same job by numeric id, which is the form that shows up in logs.
	failJob(t, ctx, state, job.ID)
	response, err = service.RetryJobs(ctx, control.JobRetryRequest{References: []string{itoa(int64(job.ID))}})
	if err != nil {
		t.Fatalf("RetryJobs(id) error = %v", err)
	}
	if len(response.Jobs) != 1 || response.Jobs[0].ID != int64(job.ID) {
		t.Fatalf("RetryJobs(id) = %+v", response)
	}

	// The bulk form counts separately, so an operator can tell a broad retry
	// from a targeted one in the same response.
	failJob(t, ctx, state, job.ID)
	bulk, err := service.RetryJobs(ctx, control.JobRetryRequest{Failed: true, Library: "downloads"})
	if err != nil {
		t.Fatalf("RetryJobs(failed) error = %v", err)
	}
	if bulk.RetriedFailed != 1 || len(bulk.Jobs) != 0 {
		t.Fatalf("RetryJobs(failed) = %+v", bulk)
	}
}

// failJob drives a job to the failed state the way a worker would.
func failJob(t *testing.T, ctx context.Context, state *store.SQLiteStore, jobID domain.JobID) {
	t.Helper()
	now := time.Now().UTC()
	for {
		leased, err := state.LeaseNextJob(ctx, "worker-1", now.Add(time.Minute), now)
		if err != nil || leased == nil {
			t.Fatalf("LeaseNextJob() = %v, %v", leased, err)
		}
		if leased.ID != jobID {
			continue
		}
		if _, err := state.TransitionJob(ctx, jobID, domain.JobStateRunning, now, ""); err != nil {
			t.Fatalf("TransitionJob(running) error = %v", err)
		}
		if _, err := state.TransitionJob(ctx, jobID, domain.JobStateFailed, now, "boom"); err != nil {
			t.Fatalf("TransitionJob(failed) error = %v", err)
		}
		return
	}
}

// TestRetryJobsAppliesNothingWhenAnyPartFails is the data-safety rule the bulk
// form used to break: --failed was committed before the explicit references
// were even resolved, so a typo'd job name reported a failed command while the
// queue had already been requeued behind the operator's back. The response is
// the only record they get of what the daemon did, so it has to be true.
func TestRetryJobsAppliesNothingWhenAnyPartFails(t *testing.T) {
	ctx := context.Background()
	service, state, _, job := testService(t, ctx)
	// The job is failed exactly once. Every case below must leave it that way,
	// which is the whole assertion: a refused retry changes nothing.
	failJob(t, ctx, state, job.ID)

	tests := []struct {
		name    string
		request control.JobRetryRequest
		want    control.ErrorCode
	}{
		{
			name:    "bulk retry with an unresolvable reference",
			request: control.JobRetryRequest{Failed: true, References: []string{"no-such-job"}},
			want:    control.CodeNotFound,
		},
		{
			name:    "bulk retry narrowed to a library that is not configured",
			request: control.JobRetryRequest{Failed: true, Library: "nope"},
			want:    control.CodeNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.RetryJobs(ctx, tt.request)
			var controlErr *control.Error
			if !errors.As(err, &controlErr) || controlErr.Code != tt.want {
				t.Fatalf("RetryJobs() error = %v, want %q", err, tt.want)
			}
			current, err := state.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatalf("GetJob() error = %v", err)
			}
			if current.State != domain.JobStateFailed {
				t.Fatalf("job state = %q, want the failed job untouched by a refused retry", current.State)
			}
		})
	}

	// The same rule inside one request: the first reference is retryable and the
	// second is not, because the first already moved it to pending. Neither may
	// survive, or the operator is told the command failed while half of it ran.
	if _, err := service.RetryJobs(ctx, control.JobRetryRequest{References: []string{job.Label(), job.Label()}}); err == nil {
		t.Fatal("RetryJobs() error = nil, want the second retry of an already pending job refused")
	}
	current, err := state.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if current.State != domain.JobStateFailed {
		t.Fatalf("job state = %q, want the first retry rolled back with the second", current.State)
	}
}

// TestRetryJobsRefusesAnEmptyBulkRequest keeps a mistyped retry from being read
// as "retry nothing" or, worse, as a queue-wide action.
func TestRetryJobsRefusesAnEmptyBulkRequest(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)

	_, err := service.RetryJobs(ctx, control.JobRetryRequest{})
	var controlErr *control.Error
	if !errors.As(err, &controlErr) || controlErr.Code != control.CodeInvalidArgument {
		t.Fatalf("RetryJobs() error = %v, want invalid_argument", err)
	}
	if _, err := service.RetryJobs(ctx, control.JobRetryRequest{Library: "downloads"}); err == nil {
		t.Fatal("RetryJobs(library only) error = nil, want a refusal")
	}
	if _, err := service.RetryJobs(ctx, control.JobRetryRequest{Failed: true, Library: "nope"}); err == nil {
		t.Fatal("RetryJobs(unknown library) error = nil, want not found")
	}
}

func TestShowJobReportsAttemptHistory(t *testing.T) {
	ctx := context.Background()
	service, state, _, job := testService(t, ctx)
	attempt := recordStreamSelection(t, ctx, state, job.ID, germanMissingDecision())

	response, err := service.ShowJob(ctx, control.JobShowRequest{Reference: job.Label()})
	if err != nil {
		t.Fatalf("ShowJob() error = %v", err)
	}
	if response.Job.ID != int64(job.ID) || response.Job.Path != "Release/Season/Episode.mkv" {
		t.Fatalf("ShowJob() job = %+v", response.Job)
	}
	if len(response.Attempts) != 1 || response.Attempts[0].ID != int64(attempt.ID) {
		t.Fatalf("ShowJob() attempts = %+v", response.Attempts)
	}
	if len(response.StreamSelection) != 1 || response.StreamSelection[0].Decision == nil {
		t.Fatalf("ShowJob() stream selection = %+v", response.StreamSelection)
	}
	if _, err := service.ShowJob(ctx, control.JobShowRequest{Reference: "  "}); err == nil {
		t.Fatal("ShowJob(blank) error = nil, want a rejection")
	}
}

// TestPruneJobsRefusesJobsHoldingAPublishJournal is a data-safety rule: the job
// row is the only thing that points at a staged artifact, a published
// destination, and an .anvil-backup. Deleting it cascades the journal away and
// leaves those files with nothing that knows how to resolve them.
func TestPruneJobsRefusesJobsHoldingAPublishJournal(t *testing.T) {
	ctx := context.Background()
	service, state, _, job := testService(t, ctx)
	now := time.Now().UTC()
	if _, err := state.TransitionJob(ctx, job.ID, domain.JobStateSkipped, now, ""); err != nil {
		t.Fatalf("TransitionJob(skipped) error = %v", err)
	}
	if err := state.CreatePublishOperation(ctx, replacepkg.PublishOperation{
		JobID: job.ID, Kind: "handoff", Mode: "move", Stage: replacepkg.PublishStagePublished,
		ArtifactPath: "/staging/output.mkv", DestinationPath: "/converted/Episode.mkv",
		BackupPath: "/converted/Episode.mkv.anvil-backup",
		CreatedAt:  now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePublishOperation() error = %v", err)
	}
	markSourceMissing(t, ctx, state)

	response, err := service.PruneJobs(ctx, control.JobPruneRequest{Apply: true})
	if err != nil {
		t.Fatalf("PruneJobs() error = %v", err)
	}
	if response.MatchedJobs != 0 || response.DeletedJobs != 0 {
		t.Fatalf("PruneJobs() = %+v, want the journaled job left alone", response)
	}
	if len(response.ProtectedJobs) != 1 || response.ProtectedJobs[0].ID != int64(job.ID) {
		t.Fatalf("ProtectedJobs = %+v, want the journaled job reported", response.ProtectedJobs)
	}
	if response.ProtectedJobs[0].Reason != string(store.JobProtectedPublishJournal) {
		t.Fatalf("reason = %q", response.ProtectedJobs[0].Reason)
	}
	if _, err := state.GetJob(ctx, job.ID); err != nil {
		t.Fatalf("job was deleted despite its publish journal: %v", err)
	}
}

// TestPruneJobsDeletesResolvedJobs keeps the guard from being a blanket refusal:
// a committed publish is finished work, and its job is prunable.
func TestPruneJobsDeletesResolvedJobs(t *testing.T) {
	ctx := context.Background()
	service, state, _, job := testService(t, ctx)
	now := time.Now().UTC()
	if _, err := state.TransitionJob(ctx, job.ID, domain.JobStateSkipped, now, ""); err != nil {
		t.Fatalf("TransitionJob(skipped) error = %v", err)
	}
	operation := replacepkg.PublishOperation{
		JobID: job.ID, Kind: "handoff", Mode: "move", Stage: replacepkg.PublishStagePrepared,
		ArtifactPath: "/staging/output.mkv", DestinationPath: "/converted/Episode.mkv",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := state.CreatePublishOperation(ctx, operation); err != nil {
		t.Fatalf("CreatePublishOperation() error = %v", err)
	}
	committed := operation
	committed.Stage = replacepkg.PublishStageCommitted
	if err := state.UpdatePublishOperation(ctx, committed, replacepkg.PublishStagePrepared); err != nil {
		t.Fatalf("UpdatePublishOperation() error = %v", err)
	}
	markSourceMissing(t, ctx, state)

	dryRun, err := service.PruneJobs(ctx, control.JobPruneRequest{})
	if err != nil {
		t.Fatalf("PruneJobs(dry run) error = %v", err)
	}
	if !dryRun.DryRun || dryRun.MatchedJobs != 1 || dryRun.DeletedJobs != 0 {
		t.Fatalf("PruneJobs(dry run) = %+v, want a preview", dryRun)
	}
	applied, err := service.PruneJobs(ctx, control.JobPruneRequest{Apply: true})
	if err != nil {
		t.Fatalf("PruneJobs(apply) error = %v", err)
	}
	if applied.DeletedJobs != 1 {
		t.Fatalf("PruneJobs(apply) = %+v, want the resolved job deleted", applied)
	}
}

func TestPruneJobsRejectsNonTerminalStates(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	_, err := service.PruneJobs(ctx, control.JobPruneRequest{States: []string{"running"}})
	var controlErr *control.Error
	if !errors.As(err, &controlErr) || controlErr.Code != control.CodeInvalidArgument {
		t.Fatalf("PruneJobs() error = %v, want invalid_argument", err)
	}
}

// TestCleanupStagingProtectsLiveWork is the incident this guard exists for: a
// staging directory's mtime stops moving once the output file exists, so a
// multi-hour encode looks exactly as stale as an abandoned attempt.
func TestCleanupStagingProtectsLiveWork(t *testing.T) {
	ctx := context.Background()
	service, state, cfg, job := testService(t, ctx)
	tempDir := t.TempDir()
	cfg.Daemon.TempDir = tempDir
	service.Config = func() config.Config { return cfg }
	now := time.Now().UTC()
	service.Now = func() time.Time { return now }

	root := staging.Root(tempDir)
	live := filepath.Join(root, "job-"+itoa(int64(job.ID))+"-attempt-1")
	abandoned := filepath.Join(root, "job-9999-attempt-1")
	for _, dir := range []string{live, abandoned} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
		old := now.Add(-48 * time.Hour)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatalf("Chtimes(%s) error = %v", dir, err)
		}
	}
	// The fixture job is pending, which is exactly the state a queued encode is
	// in before a worker picks it up again.
	if _, err := state.GetJob(ctx, job.ID); err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}

	response, err := service.CleanupStaging(ctx, control.StagingCleanupRequest{OlderThan: "24h"})
	if err != nil {
		t.Fatalf("CleanupStaging() error = %v", err)
	}
	if response.Protected != 1 || len(response.ProtectedJobs) != 1 || response.ProtectedJobs[0].ID != int64(job.ID) {
		t.Fatalf("CleanupStaging() = %+v, want the live job protected", response)
	}
	if response.Removed != 1 {
		t.Fatalf("CleanupStaging() removed = %d, want only the abandoned directory", response.Removed)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live staging directory was removed: %v", err)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatalf("abandoned staging directory stat = %v, want removed", err)
	}
}

func TestCleanupStagingRejectsAnUnparsableAge(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	_, err := service.CleanupStaging(ctx, control.StagingCleanupRequest{OlderThan: "sometimes"})
	var controlErr *control.Error
	if !errors.As(err, &controlErr) || controlErr.Code != control.CodeInvalidArgument {
		t.Fatalf("CleanupStaging() error = %v, want invalid_argument", err)
	}
	if _, err := service.CleanupStaging(ctx, control.StagingCleanupRequest{OlderThan: "-1h"}); err == nil {
		t.Fatal("CleanupStaging(negative) error = nil, want a refusal")
	}
}

// TestCleanupStagingRefusesAZeroAge closes a gap between the daemon and the
// control command: daemon.staging_cleanup_age of 0s disables the daemon's own
// sweep, and pkg/staging refuses to sweep without a cutoff, so inheriting it
// here produced a successful-looking report of zero candidates for a directory
// that was never examined. Both spellings of zero are refused, with different
// wording, because "you inherited this" and "you asked for this" are different
// mistakes.
func TestCleanupStagingRefusesAZeroAge(t *testing.T) {
	ctx := context.Background()
	service, _, cfg, _ := testService(t, ctx)
	tempDir := t.TempDir()
	cfg.Daemon.TempDir = tempDir
	cfg.Daemon.StagingCleanupAge = "0s"
	service.Config = func() config.Config { return cfg }
	now := time.Now().UTC()
	service.Now = func() time.Time { return now }

	abandoned := filepath.Join(staging.Root(tempDir), "job-9999-attempt-1")
	if err := os.MkdirAll(abandoned, 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(abandoned, old, old); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	_, err := service.CleanupStaging(ctx, control.StagingCleanupRequest{})
	var controlErr *control.Error
	if !errors.As(err, &controlErr) || controlErr.Code != control.CodeInvalidArgument {
		t.Fatalf("CleanupStaging() error = %v, want invalid_argument", err)
	}
	if _, err := os.Stat(abandoned); err != nil {
		t.Fatalf("a refused cleanup removed %s: %v", abandoned, err)
	}

	_, err = service.CleanupStaging(ctx, control.StagingCleanupRequest{OlderThan: "0s"})
	if !errors.As(err, &controlErr) || controlErr.Code != control.CodeInvalidArgument {
		t.Fatalf("CleanupStaging(explicit 0s) error = %v, want invalid_argument", err)
	}
	if !strings.Contains(controlErr.Message, "greater than zero") {
		t.Fatalf("message = %q, want the explicit-zero wording", controlErr.Message)
	}

	// A real age still works, and still protects live work.
	response, err := service.CleanupStaging(ctx, control.StagingCleanupRequest{OlderThan: "24h"})
	if err != nil {
		t.Fatalf("CleanupStaging(24h) error = %v", err)
	}
	if response.Removed != 1 {
		t.Fatalf("CleanupStaging(24h) removed = %d, want the abandoned directory", response.Removed)
	}
}

func TestScanAndStatsUseTheDaemonConfig(t *testing.T) {
	ctx := context.Background()
	service, _, cfg, _ := testService(t, ctx)
	root := cfg.Libraries["downloads"].Path
	if err := os.MkdirAll(filepath.Join(root, "Release", "Season"), 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "Release", "Season", "Episode.mkv"), []byte("media"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// The fixture library is a download library, so the scanner clock is moved
	// past the stability window instead of sleeping through it.
	service.Scanner = scanner.Scanner{Store: service.Store.(*store.SQLiteStore), Now: func() time.Time { return time.Now().Add(time.Hour) }}
	scan, err := service.ScanLibraries(ctx, control.LibraryScanRequest{Library: "downloads"})
	if err != nil {
		t.Fatalf("ScanLibraries() error = %v", err)
	}
	if scan.Libraries != 1 || scan.Sources != 1 {
		t.Fatalf("ScanLibraries() = %+v", scan)
	}
	// Stats only describe finished work, so a job has to complete with recorded
	// sizes before it can appear.
	completeJobWithSizes(t, ctx, service.Store.(*store.SQLiteStore))
	stats, err := service.LibraryStats(ctx, control.LibraryStatsRequest{})
	if err != nil {
		t.Fatalf("LibraryStats() error = %v", err)
	}
	if len(stats.Libraries) != 1 || stats.Libraries[0].Library != "downloads" || stats.Libraries[0].SavedBytes != 400 {
		t.Fatalf("LibraryStats() = %+v", stats)
	}
}

// completeJobWithSizes finishes the next pending job the way a worker does,
// including the recorded sizes stats are computed from.
func completeJobWithSizes(t *testing.T, ctx context.Context, state *store.SQLiteStore) {
	t.Helper()
	now := time.Now().UTC()
	leased, err := state.LeaseNextJob(ctx, "worker-1", now.Add(time.Minute), now)
	if err != nil || leased == nil {
		t.Fatalf("LeaseNextJob() = %v, %v", leased, err)
	}
	if _, err := state.TransitionJob(ctx, leased.ID, domain.JobStateRunning, now, ""); err != nil {
		t.Fatalf("TransitionJob(running) error = %v", err)
	}
	if _, err := state.TransitionJob(ctx, leased.ID, domain.JobStateValidating, now, ""); err != nil {
		t.Fatalf("TransitionJob(validating) error = %v", err)
	}
	if _, err := state.RecordJobFileSizes(ctx, leased.ID, 1000, 600, now); err != nil {
		t.Fatalf("RecordJobFileSizes() error = %v", err)
	}
	if _, err := state.TransitionJob(ctx, leased.ID, domain.JobStateComplete, now, ""); err != nil {
		t.Fatalf("TransitionJob(complete) error = %v", err)
	}
}

// TestForceOccurrenceRefusesUnsafeTargets keeps the explicit path from
// bypassing the scan rules it is meant to work alongside.
func TestForceOccurrenceRefusesUnsafeTargets(t *testing.T) {
	ctx := context.Background()
	service, _, cfg, _ := testService(t, ctx)
	root := cfg.Libraries["downloads"].Path
	if err := os.MkdirAll(filepath.Join(root, "Release", "Season"), 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "Release", "Season", "Episode.mkv"), []byte("media"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tests := []struct {
		name    string
		request control.ForceOccurrenceRequest
		want    control.ErrorCode
	}{
		{name: "no library", request: control.ForceOccurrenceRequest{Path: "Release"}, want: control.CodeInvalidArgument},
		{name: "unknown library", request: control.ForceOccurrenceRequest{Library: "nope", Path: "Release"}, want: control.CodeNotFound},
		{name: "absolute path", request: control.ForceOccurrenceRequest{Library: "downloads", Path: "/etc/passwd"}, want: control.CodeInvalidArgument},
		{name: "escaping path", request: control.ForceOccurrenceRequest{Library: "downloads", Path: "../outside"}, want: control.CodeInvalidArgument},
		{name: "missing target", request: control.ForceOccurrenceRequest{Library: "downloads", Path: "Nothing/Here.mkv"}, want: control.CodeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.ForceOccurrence(ctx, tt.request)
			var controlErr *control.Error
			if !errors.As(err, &controlErr) || controlErr.Code != tt.want {
				t.Fatalf("ForceOccurrence() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestBackupStoreRequiresAnAbsoluteDestination(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	for _, destination := range []string{"", "  ", "relative/anvil.db"} {
		_, err := service.BackupStore(ctx, control.StoreBackupRequest{Destination: destination})
		var controlErr *control.Error
		if !errors.As(err, &controlErr) || controlErr.Code != control.CodeInvalidArgument {
			t.Fatalf("BackupStore(%q) error = %v, want invalid_argument", destination, err)
		}
	}
}

func TestBackupStoreWritesAVerifiedSnapshot(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	destination := filepath.Join(t.TempDir(), "anvil-backup.db")

	response, err := service.BackupStore(ctx, control.StoreBackupRequest{Destination: destination})
	if err != nil {
		t.Fatalf("BackupStore() error = %v", err)
	}
	if response.Path != destination || response.Integrity != "ok" || response.SizeBytes <= 0 {
		t.Fatalf("BackupStore() = %+v", response)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("backup file stat error = %v", err)
	}
	if _, err := service.BackupStore(ctx, control.StoreBackupRequest{Destination: destination}); err == nil {
		t.Fatal("BackupStore() over an existing file error = nil, want a refusal")
	}
}

func TestRecoverJobsUsesTheDaemonRetryPolicy(t *testing.T) {
	ctx := context.Background()
	service, state, _, _ := testService(t, ctx)
	// The lease expires before the service's clock, which is what makes it
	// recoverable rather than merely old.
	now := service.Now()
	leased, err := state.LeaseNextJob(ctx, "worker-1", now.Add(-time.Hour), now.Add(-2*time.Hour))
	if err != nil || leased == nil {
		t.Fatalf("LeaseNextJob() = %v, %v", leased, err)
	}
	response, err := service.RecoverJobs(ctx)
	if err != nil {
		t.Fatalf("RecoverJobs() error = %v", err)
	}
	if response.RecoveredJobs != 1 {
		t.Fatalf("RecoverJobs() = %+v, want the expired lease recovered", response)
	}
}

// markSourceMissing simulates the scan result that makes a job prunable: the
// source occurrence is gone from disk.
func markSourceMissing(t *testing.T, ctx context.Context, state *store.SQLiteStore) {
	t.Helper()
	token, err := state.BeginLibraryScan(ctx, "downloads")
	if err != nil {
		t.Fatalf("BeginLibraryScan() error = %v", err)
	}
	if _, err := state.ApplyLibraryScan(ctx, token, store.ApplyScanInput{
		LibraryName: "downloads", CompletedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("ApplyLibraryScan() error = %v", err)
	}
}

func itoa(value int64) string { // strconv.FormatInt without the import churn in tests
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

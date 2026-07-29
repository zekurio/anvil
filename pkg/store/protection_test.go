package store

import (
	"context"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
)

// TestProtectedJobsReportsActiveWorkAndUnresolvedJournals pins the input every
// maintenance path depends on. A wrong answer here deletes a staging directory
// out from under a running encode, or a job row that is the last record of a
// half-published destination.
func TestProtectedJobsReportsActiveWorkAndUnresolvedJournals(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := time.Now().UTC()

	active := enqueueProtectionJob(t, ctx, state, "Active.mkv", now)
	journaled := enqueueProtectionJob(t, ctx, state, "Journaled.mkv", now)
	finished := enqueueProtectionJob(t, ctx, state, "Finished.mkv", now)

	// The journaled job is terminal, so only its publish journal protects it.
	transitionToState(t, ctx, state, journaled, domain.JobStateSkipped, now)
	transitionToState(t, ctx, state, finished, domain.JobStateSkipped, now)
	if err := state.CreatePublishOperation(ctx, replacepkg.PublishOperation{
		JobID: journaled, Kind: "handoff", Mode: "move", Stage: replacepkg.PublishStagePublished,
		ArtifactPath: "/staging/output.mkv", DestinationPath: "/converted/Journaled.mkv",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePublishOperation() error = %v", err)
	}
	committed := replacepkg.PublishOperation{
		JobID: finished, Kind: "handoff", Mode: "move", Stage: replacepkg.PublishStagePrepared,
		ArtifactPath: "/staging/output.mkv", DestinationPath: "/converted/Finished.mkv",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := state.CreatePublishOperation(ctx, committed); err != nil {
		t.Fatalf("CreatePublishOperation(finished) error = %v", err)
	}
	committed.Stage = replacepkg.PublishStageCommitted
	if err := state.UpdatePublishOperation(ctx, committed, replacepkg.PublishStagePrepared); err != nil {
		t.Fatalf("UpdatePublishOperation() error = %v", err)
	}

	protected, err := state.ProtectedJobs(ctx)
	if err != nil {
		t.Fatalf("ProtectedJobs() error = %v", err)
	}
	reasons := make(map[domain.JobID]JobProtectionReason, len(protected))
	for _, job := range protected {
		reasons[job.JobID] = job.Reason
	}
	if reasons[active] != JobProtectedActive {
		t.Fatalf("active job reason = %q, want %q", reasons[active], JobProtectedActive)
	}
	if reasons[journaled] != JobProtectedPublishJournal {
		t.Fatalf("journaled job reason = %q, want %q", reasons[journaled], JobProtectedPublishJournal)
	}
	if _, ok := reasons[finished]; ok {
		t.Fatalf("committed publish protected job %d, want it releasable", finished)
	}
}

// TestPruneMissingSourceJobsKeepsJobsHoldingAPublishJournal is the delete-side
// half of the same rule: pruning cascades the journal row away, so a job that
// still owns one must never match.
func TestPruneMissingSourceJobsKeepsJobsHoldingAPublishJournal(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := time.Now().UTC()

	journaled := enqueueProtectionJob(t, ctx, state, "Journaled.mkv", now)
	plain := enqueueProtectionJob(t, ctx, state, "Plain.mkv", now)
	transitionToState(t, ctx, state, journaled, domain.JobStateSkipped, now)
	transitionToState(t, ctx, state, plain, domain.JobStateSkipped, now)
	if err := state.CreatePublishOperation(ctx, replacepkg.PublishOperation{
		JobID: journaled, Kind: "handoff", Mode: "move", Stage: replacepkg.PublishStageConflict,
		ArtifactPath: "/staging/output.mkv", DestinationPath: "/converted/Journaled.mkv",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePublishOperation() error = %v", err)
	}

	// A scan that finds nothing marks every source missing, which is what makes
	// these jobs prunable in the first place.
	token, err := state.BeginLibraryScan(ctx, "movies")
	if err != nil {
		t.Fatalf("BeginLibraryScan() error = %v", err)
	}
	if _, err := state.ApplyLibraryScan(ctx, token, ApplyScanInput{LibraryName: "movies", CompletedAt: now}); err != nil {
		t.Fatalf("ApplyLibraryScan() error = %v", err)
	}

	result, err := state.PruneMissingSourceJobs(ctx, PruneMissingSourceJobsOptions{Apply: true})
	if err != nil {
		t.Fatalf("PruneMissingSourceJobs() error = %v", err)
	}
	if result.MatchedJobs != 1 || result.DeletedJobs != 1 {
		t.Fatalf("result = %+v, want only the unjournaled job deleted", result)
	}
	if len(result.ProtectedJobs) != 1 || result.ProtectedJobs[0].JobID != journaled {
		t.Fatalf("ProtectedJobs = %+v, want the journaled job reported", result.ProtectedJobs)
	}
	if _, err := state.GetJob(ctx, journaled); err != nil {
		t.Fatalf("journaled job was deleted: %v", err)
	}
	if _, err := state.GetJob(ctx, plain); err == nil {
		t.Fatal("prunable job survived the apply")
	}
}

func enqueueProtectionJob(t *testing.T, ctx context.Context, state *SQLiteStore, path string, now time.Time) domain.JobID {
	t.Helper()
	result, err := state.ForceOccurrence(ctx, ForceOccurrenceInput{
		LibraryName: "movies", SourceKind: domain.SourceKindFile,
		SourceRelativePath: path, AssetRelativePath: path,
		AssetRole:         domain.MediaAssetRolePrimaryVideo,
		SourceFingerprint: domain.FileFingerprint{SizeBytes: 1, ModTime: now},
		AssetFingerprint:  domain.FileFingerprint{SizeBytes: 1, ModTime: now},
		Now:               now,
	})
	if err != nil {
		t.Fatalf("ForceOccurrence(%s) error = %v", path, err)
	}
	return result.Job.ID
}

func transitionToState(t *testing.T, ctx context.Context, state *SQLiteStore, jobID domain.JobID, to domain.JobState, now time.Time) {
	t.Helper()
	if _, err := state.TransitionJob(ctx, jobID, to, now, ""); err != nil {
		t.Fatalf("TransitionJob(%s) error = %v", to, err)
	}
}

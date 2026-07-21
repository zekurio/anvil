package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
)

func TestOccurrenceOnlySchemaRequiresResetForPublishJournal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "anvil.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
INSERT INTO schema_migrations (version, applied_at) VALUES (5, '2026-07-21T00:00:00Z');
`); err != nil {
		t.Fatalf("create occurrence-only schema marker: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close occurrence-only database: %v", err)
	}
	if _, err := Open(ctx, path); !errors.Is(err, ErrIncompatibleSchema) {
		t.Fatalf("Open() error = %v, want ErrIncompatibleSchema", err)
	}
}

func TestPublishOperationStageTransitionsRoundTrip(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	source := upsertTestSource(t, ctx, state, "downloads", "Show/Episode.mkv")
	job, _, err := state.EnqueueJob(ctx, EnqueueJobInput{
		SourceID:    source.ID,
		LibraryName: source.LibraryName,
		Now:         now,
	})
	if err != nil {
		t.Fatal(err)
	}

	operation := replacepkg.PublishOperation{
		JobID:            job.ID,
		Kind:             "handoff",
		Mode:             "move",
		Stage:            replacepkg.PublishStagePrepared,
		ArtifactPath:     "/staging/output.mkv",
		DestinationPath:  "/imports/Show/Episode.mkv",
		ArtifactIdentity: replacepkg.FileIdentity{SizeBytes: 1234, Device: 5, Inode: 9},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := state.CreatePublishOperation(ctx, operation); err != nil {
		t.Fatalf("CreatePublishOperation() error = %v", err)
	}
	operation.Stage = replacepkg.PublishStagePublished
	operation.DigestAlgorithm = "sha256"
	operation.DigestValue = "abc123"
	operation.UpdatedAt = now.Add(1)
	if err := state.UpdatePublishOperation(ctx, operation, replacepkg.PublishStagePrepared); err != nil {
		t.Fatalf("UpdatePublishOperation() error = %v", err)
	}
	if err := state.UpdatePublishOperation(ctx, operation, replacepkg.PublishStagePrepared); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale UpdatePublishOperation() error = %v, want ErrNotFound", err)
	}

	got, ok, err := state.GetPublishOperation(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetPublishOperation() error = %v", err)
	}
	if !ok {
		t.Fatal("GetPublishOperation() ok = false, want true")
	}
	if got.Stage != replacepkg.PublishStagePublished || got.DigestValue != "abc123" || got.ArtifactIdentity != operation.ArtifactIdentity {
		t.Fatalf("operation = %+v, want published operation", got)
	}

	if _, err := state.db.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, int64(job.ID)); err != nil {
		t.Fatalf("delete job: %v", err)
	}
	if _, ok, err := state.GetPublishOperation(ctx, domain.JobID(job.ID)); err != nil || ok {
		t.Fatalf("operation after job delete: ok=%t err=%v, want false nil", ok, err)
	}
}

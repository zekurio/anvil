package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
)

func TestPublishArtifactProtectedByUnresolvedStage(t *testing.T) {
	stages := []replacepkg.PublishStage{
		replacepkg.PublishStagePrepared,
		replacepkg.PublishStagePublished,
		replacepkg.PublishStageSourceCleaned,
		replacepkg.PublishStageConflict,
		replacepkg.PublishStageCommitted,
	}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			ctx := context.Background()
			store, jobID := openPublishTestStore(t, ctx)
			artifact := filepath.Join(t.TempDir(), "movie.mkv.anvil-part")
			createPublishTestOperation(t, ctx, store, jobID, stage, artifact)

			cleanEquivalent := filepath.Join(filepath.Dir(artifact), ".", filepath.Base(artifact))
			protected, err := store.PublishArtifactProtected(ctx, cleanEquivalent)
			if err != nil {
				t.Fatalf("PublishArtifactProtected: %v", err)
			}
			want := stage != replacepkg.PublishStageCommitted
			if protected != want {
				t.Fatalf("PublishArtifactProtected = %t, want %t", protected, want)
			}
			protected, err = store.PublishArtifactProtected(ctx, artifact+".other")
			if err != nil {
				t.Fatalf("PublishArtifactProtected unrelated: %v", err)
			}
			if protected {
				t.Fatal("unrelated artifact was protected")
			}
		})
	}
}

func TestPublishArtifactProtectedFailsClosedOnInvalidUnresolvedJournal(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(t *testing.T, operation replacepkg.PublishOperation) []byte
		wantError string
	}{
		{
			name: "unreadable JSON",
			mutate: func(*testing.T, replacepkg.PublishOperation) []byte {
				return []byte("{")
			},
			wantError: "decode publish operation",
		},
		{
			name: "job mismatch",
			mutate: func(t *testing.T, operation replacepkg.PublishOperation) []byte {
				operation.JobID++
				return marshalPublishTestOperation(t, operation)
			},
			wantError: "publish operation job mismatch",
		},
		{
			name: "stage mismatch",
			mutate: func(t *testing.T, operation replacepkg.PublishOperation) []byte {
				operation.Stage = replacepkg.PublishStagePublished
				return marshalPublishTestOperation(t, operation)
			},
			wantError: "publish operation stage mismatch",
		},
		{
			name: "missing artifact path",
			mutate: func(t *testing.T, operation replacepkg.PublishOperation) []byte {
				operation.ArtifactPath = ""
				return marshalPublishTestOperation(t, operation)
			},
			wantError: "has no artifact path",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, jobID := openPublishTestStore(t, ctx)
			artifact := "/media/movie.mkv.anvil-part"
			operation := createPublishTestOperation(t, ctx, store, jobID, replacepkg.PublishStagePrepared, artifact)
			data := test.mutate(t, operation)
			if _, err := store.db.ExecContext(ctx, `UPDATE publish_operations SET operation_json = ? WHERE job_id = ?`, data, int64(jobID)); err != nil {
				t.Fatalf("mutate publish operation: %v", err)
			}

			_, err := store.PublishArtifactProtected(ctx, artifact)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("PublishArtifactProtected error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestPublishArtifactProtectedIgnoresInvalidCommittedJournal(t *testing.T) {
	ctx := context.Background()
	store, jobID := openPublishTestStore(t, ctx)
	artifact := "/media/movie.mkv.anvil-part"
	createPublishTestOperation(t, ctx, store, jobID, replacepkg.PublishStageCommitted, artifact)
	if _, err := store.db.ExecContext(ctx, `UPDATE publish_operations SET operation_json = ? WHERE job_id = ?`, []byte("{"), int64(jobID)); err != nil {
		t.Fatalf("corrupt committed publish operation: %v", err)
	}

	protected, err := store.PublishArtifactProtected(ctx, artifact)
	if err != nil {
		t.Fatalf("PublishArtifactProtected: %v", err)
	}
	if protected {
		t.Fatal("invalid committed journal protected artifact")
	}
}

func createPublishTestOperation(t *testing.T, ctx context.Context, store *SQLiteStore, jobID domain.JobID, stage replacepkg.PublishStage, artifact string) replacepkg.PublishOperation {
	t.Helper()
	now := time.Now().UTC()
	operation := replacepkg.PublishOperation{
		JobID: jobID, Stage: stage, ArtifactPath: artifact,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreatePublishOperation(ctx, operation); err != nil {
		t.Fatalf("CreatePublishOperation: %v", err)
	}
	return operation
}

func marshalPublishTestOperation(t *testing.T, operation replacepkg.PublishOperation) []byte {
	t.Helper()
	data, err := json.Marshal(operation)
	if err != nil {
		t.Fatalf("marshal publish operation: %v", err)
	}
	return data
}

func openPublishTestStore(t *testing.T, ctx context.Context) (*SQLiteStore, domain.JobID) {
	t.Helper()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "anvil.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	now := encodeTime(time.Now().UTC())
	result, err := store.db.ExecContext(ctx, `
INSERT INTO media_sources (
	library_name, kind, relative_path, generation, is_current, status,
	first_seen_at, last_seen_at, updated_at
) VALUES (?, ?, ?, 1, 1, ?, ?, ?, ?)
`, "media", domain.SourceKindFile, "movie.mkv", domain.MediaSourceActive, now, now, now)
	if err != nil {
		t.Fatalf("insert media source: %v", err)
	}
	sourceID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("media source id: %v", err)
	}
	result, err = store.db.ExecContext(ctx, `
INSERT INTO jobs (slug, source_id, library_name, state, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
`, "test-job", sourceID, "media", domain.JobStatePending, now, now)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	jobID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("job id: %v", err)
	}
	return store, domain.JobID(jobID)
}

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestApplyLibraryScanRequeuesOnlyWhenAllowed(t *testing.T) {
	for _, test := range []struct {
		name              string
		requeueExisting   bool
		wantSecondEnqueue int
		wantStatus        domain.MediaSourceStatus
	}{
		{name: "media", wantSecondEnqueue: 0, wantStatus: domain.MediaSourceProcessed},
		{name: "download", requeueExisting: true, wantSecondEnqueue: 1, wantStatus: domain.MediaSourceActive},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			state, err := Open(ctx, filepath.Join(t.TempDir(), "anvil.db"))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() {
				if err := state.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			})

			library := domain.LibraryName(test.name)
			first := scanTestEntry(1)
			result := applyScanForTest(t, ctx, state, library, test.requeueExisting, first)
			if result.EnqueuedJobs != 1 {
				t.Fatalf("first EnqueuedJobs = %d, want 1", result.EnqueuedJobs)
			}

			second := scanTestEntry(2)
			result = applyScanForTest(t, ctx, state, library, test.requeueExisting, second)
			if result.EnqueuedJobs != test.wantSecondEnqueue {
				t.Fatalf("second EnqueuedJobs = %d, want %d", result.EnqueuedJobs, test.wantSecondEnqueue)
			}
			source, err := state.GetMediaSourceByPath(ctx, library, "movie.mkv")
			if err != nil {
				t.Fatalf("GetMediaSourceByPath: %v", err)
			}
			if source.Status != test.wantStatus {
				t.Fatalf("source status = %q, want %q", source.Status, test.wantStatus)
			}
		})
	}
}

func scanTestEntry(size int64) ScanEntry {
	return ScanEntry{
		SourceKind: domain.SourceKindFile, SourceRelativePath: "movie.mkv",
		SourceFingerprint: domain.FileFingerprint{SizeBytes: size},
		AssetRelativePath: "movie.mkv", AssetRole: domain.MediaAssetRolePrimaryVideo,
		AssetFingerprint: domain.FileFingerprint{SizeBytes: size}, Persist: true, Enqueue: true,
	}
}

func applyScanForTest(t *testing.T, ctx context.Context, state *SQLiteStore, library domain.LibraryName, requeueExisting bool, entry ScanEntry) ApplyScanResult {
	t.Helper()
	token, err := state.BeginLibraryScan(ctx, library)
	if err != nil {
		t.Fatalf("BeginLibraryScan: %v", err)
	}
	result, err := state.ApplyLibraryScan(ctx, token, ApplyScanInput{
		LibraryName: library, RequeueExisting: requeueExisting, Entries: []ScanEntry{entry}, CompletedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("ApplyLibraryScan: %v", err)
	}
	return result
}

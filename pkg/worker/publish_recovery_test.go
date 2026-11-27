package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/pipeline"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
	"github.com/zekurio/anvil/pkg/scheduler"
	"github.com/zekurio/anvil/pkg/store"
)

// A publish that was interrupted is finished by the next attempt from its
// journal. Whether the source media is gone afterwards was decided when the
// operation was journaled, and the library may have been edited since: the
// completion has to record what the operation did, not what the library says
// now.
func TestRecoveredPublishCompletesFromTheJournaledOperation(t *testing.T) {
	tests := []struct {
		name            string
		journaledRemove bool
		libraryRemoves  bool
	}{
		{"the source was removed, and the library no longer removes sources", true, false},
		{"the source was kept, and the library now removes sources", false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			downloads, handoff := filepath.Join(root, "downloads"), filepath.Join(root, "handoff")
			for _, dir := range []string{downloads, handoff} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			state, err := store.Open(ctx, filepath.Join(root, "anvil.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := state.Close(); err != nil {
					t.Error(err)
				}
			})
			graph := flow.Graph{
				Nodes: []flow.Node{{ID: "in", Type: "input"}, {ID: "out", Type: "output.publish"}},
				Edges: []flow.Edge{{From: "in", To: "out"}},
			}
			created, err := state.CreateFlow(ctx, store.FlowInput{Name: "Remux", Graph: graph}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			library := config.LibraryConfig{
				Name: "downloads", Kind: "download", Path: downloads, FlowID: created.ID,
				Download: config.DownloadLibraryConfig{HandoffPath: handoff, PackageMode: "file", HandoffMode: "move", CleanupSourceMedia: test.journaledRemove},
			}
			if _, err := state.CreateLibrary(ctx, library, time.Now()); err != nil {
				t.Fatal(err)
			}

			// The file is scanned and its job leased, and the publish got as
			// far as cleaning up the source before the daemon went down.
			token, err := state.BeginLibraryScan(ctx, "downloads")
			if err != nil {
				t.Fatal(err)
			}
			entry := store.ScanEntry{
				SourceKind: domain.SourceKindFile, SourceRelativePath: "film.mkv", SourceFingerprint: domain.FileFingerprint{SizeBytes: 10},
				AssetRelativePath: "film.mkv", AssetRole: domain.MediaAssetRolePrimaryVideo, AssetFingerprint: domain.FileFingerprint{SizeBytes: 10},
				Persist: true, Enqueue: true,
			}
			if _, err := state.ApplyLibraryScan(ctx, token, store.ApplyScanInput{LibraryName: "downloads", SourcePaths: []string{"film.mkv"}, Entries: []store.ScanEntry{entry}}); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			leased, err := state.LeaseNextJob(ctx, "w1", now.Add(time.Hour), now)
			if err != nil || leased == nil {
				t.Fatalf("LeaseNextJob = %v, %v", leased, err)
			}
			destination := filepath.Join(handoff, "film.mkv")
			if err := os.WriteFile(destination, []byte("encoded"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := state.CreatePublishOperation(ctx, replacepkg.PublishOperation{
				JobID: leased.ID, Kind: "handoff", Mode: "move", Stage: replacepkg.PublishStageSourceCleaned,
				ArtifactPath:    filepath.Join(handoff, ".anvil-work", "film.job-1.anvil-part"),
				DestinationPath: destination, CleanupSourcePath: filepath.Join(downloads, "film.mkv"),
				CleanupArtifact: true, CleanupSource: test.journaledRemove, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}

			// The operator edits the library before the daemon is back.
			library.Download.CleanupSourceMedia = test.libraryRemoves
			cfg := config.Default()
			cfg.Libraries = map[string]config.LibraryConfig{"downloads": library}
			runner := Runner{
				Store:          state,
				ConfigProvider: func() config.Config { return cfg },
				TempDir:        t.TempDir(),
				Pipeline:       pipeline.Runner{Blocks: []pipeline.Block{replacepkg.PublishBlock{Manager: replacepkg.Manager{Journal: state}}}},
			}
			if err := runner.Run(ctx, scheduler.Assignment{Job: *leased, WorkerID: "w1"}); err != nil {
				t.Fatalf("Run = %v", err)
			}

			job, err := state.GetJob(ctx, leased.ID)
			if err != nil || job.State != domain.JobStateComplete {
				t.Fatalf("job = %+v, %v; want it complete", job, err)
			}
			operation, found, err := state.GetPublishOperation(ctx, leased.ID)
			if err != nil || !found || operation.Stage != replacepkg.PublishStageCommitted {
				t.Fatalf("publish operation = %+v, %t, %v; want it committed", operation, found, err)
			}
			source, err := state.GetMediaSource(ctx, leased.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			if removed := !source.Current && source.Status == domain.MediaSourceMissing; removed != test.journaledRemove {
				t.Errorf("source after completion = current %t, status %q; the journaled operation had cleanup_source = %t", source.Current, source.Status, test.journaledRemove)
			}
		})
	}
}

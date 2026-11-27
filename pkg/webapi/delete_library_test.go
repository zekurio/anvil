package webapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
)

// stagedJob is a job of a download library whose attempt staged an artifact
// the way the pipeline does, up to the point where it would publish.
type stagedJob struct {
	job     *pipeline.JobContext
	source  string
	handoff string
}

// stageDownload creates a download library holding one file, leases its job,
// starts an attempt with the snapshots the worker records, and writes the
// artifact where the stage step puts it.
func (f *fixture) stageDownload(name string) stagedJob {
	f.t.Helper()
	ctx := context.Background()
	handoff, err := filepath.EvalSymlinks(f.t.TempDir())
	if err != nil {
		f.t.Fatal(err)
	}
	source := f.writeMedia("film.mp4")
	want[Library](f.t, f.send(http.MethodPost, "/api/v1/libraries", LibraryWrite{
		Name: name, Kind: "download", Path: f.media, FlowID: f.defaultFlow().ID,
		Download: DownloadOptions{HandoffPath: handoff},
	}), http.StatusCreated)
	if scan := want[ScanResult](f.t, f.send(http.MethodPost, "/api/v1/libraries/"+name+"/scan", nil), http.StatusOK); scan.EnqueuedJobs != 1 {
		f.t.Fatalf("scan = %+v, want one job", scan)
	}

	now := time.Now().UTC()
	leased, err := f.state.LeaseNextJob(ctx, "worker-1", now.Add(time.Hour), now)
	if err != nil || leased == nil {
		f.t.Fatalf("LeaseNextJob = %v, %v", leased, err)
	}
	library, err := f.config().ResolveLibrary(leased.LibraryName)
	if err != nil {
		f.t.Fatal(err)
	}
	snapshot, err := json.Marshal(library)
	if err != nil {
		f.t.Fatal(err)
	}
	attempt, err := f.state.StartAttempt(ctx, leased.ID, "worker-1", snapshot, nil, []byte(`{"Name":"flow"}`), now)
	if err != nil {
		f.t.Fatal(err)
	}
	mediaSource, err := f.state.GetMediaSource(ctx, leased.SourceID)
	if err != nil {
		f.t.Fatal(err)
	}
	asset, err := f.state.GetMediaAsset(ctx, leased.AssetID)
	if err != nil {
		f.t.Fatal(err)
	}
	job := pipeline.NewJobContext(*leased, mediaSource, asset, library)
	job.Attempt = attempt
	if err := replacepkg.PlanArtifactPaths(job); err != nil {
		f.t.Fatal(err)
	}
	if err := replacepkg.PrepareDestination(job); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(job.OutputPath, []byte("the encode"), 0o640); err != nil {
		f.t.Fatal(err)
	}
	return stagedJob{job: job, source: source, handoff: handoff}
}

// failAttempt ends the attempt the way the worker does when its publish does
// not go through: the attempt fails and the job goes back to the queue.
func (f *fixture) failAttempt(staged stagedJob, cause error) {
	f.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := f.state.FinishAttempt(ctx, staged.job.Attempt.ID, domain.AttemptStateFailed, cause.Error(), now); err != nil {
		f.t.Fatal(err)
	}
	for _, state := range []domain.JobState{domain.JobStateRetrying, domain.JobStatePending} {
		if _, err := f.state.TransitionJob(ctx, staged.job.Job.ID, state, now, cause.Error()); err != nil {
			f.t.Fatal(err)
		}
	}
}

// A publish that stops in conflict stays in conflict: no attempt can move it.
// The library it belongs to used to be undeletable for good, with a message
// that advised a retry.
func TestDeleteLibraryWithAConflictedPublish(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "", nil)
	staged := f.stageDownload("inbox")

	// Something else already sits at the destination, as when the same file
	// name is handed off twice.
	taken := []byte("somebody else's file")
	if err := os.WriteFile(staged.job.DestinationPath, taken, 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := replacepkg.Manager{Journal: f.state}.Handoff(ctx, staged.job)
	if !errors.Is(err, replacepkg.ErrPublishConflict) {
		t.Fatalf("Handoff = %v, want a publish conflict", err)
	}
	f.failAttempt(staged, err)
	if operation, found, err := f.state.GetPublishOperation(ctx, staged.job.Job.ID); err != nil || !found || operation.Stage != replacepkg.PublishStageConflict {
		t.Fatalf("publish journal = %+v, %t, %v; want it in conflict", operation, found, err)
	}

	wantNoContent(t, f.send(http.MethodDelete, "/api/v1/libraries/inbox", nil))
	if list := want[LibraryList](t, f.get("/api/v1/libraries"), http.StatusOK); len(list.Libraries) != 0 {
		t.Fatalf("libraries after the delete = %+v", list.Libraries)
	}
	// The job's own part file is Anvil's to remove. Nothing else is.
	if _, err := os.Stat(staged.job.OutputPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("part file %s of the deleted job is still there (%v)", staged.job.OutputPath, err)
	}
	if data, err := os.ReadFile(staged.job.DestinationPath); err != nil || string(data) != string(taken) {
		t.Errorf("destination after the delete = %q, %v; want it untouched", data, err)
	}
	if info, err := os.Stat(staged.source); err != nil || info.Size() != 5000 {
		t.Errorf("source after the delete: %v, %v; want it untouched", info, err)
	}
}

// A publish that was only interrupted still keeps its library, and the
// refusal says what resolves it.
func TestDeleteLibraryWithAnInterruptedPublishSaysWhatResolvesIt(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "", nil)
	staged := f.stageDownload("inbox")

	interrupted := errors.New("power lost")
	manager := replacepkg.Manager{Journal: f.state, Hook: func(boundary replacepkg.PublishBoundary) error {
		if boundary == replacepkg.BoundaryPublished {
			return interrupted
		}
		return nil
	}}
	_, err := manager.Handoff(ctx, staged.job)
	if !errors.Is(err, replacepkg.ErrPublishPending) || errors.Is(err, replacepkg.ErrPublishConflict) {
		t.Fatalf("Handoff = %v, want a pending publish", err)
	}
	f.failAttempt(staged, err)

	refused := wantError(t, f.send(http.MethodDelete, "/api/v1/libraries/inbox", nil), http.StatusConflict, CodeConflict)
	const advice = "1 job was interrupted while publishing its result. The publish finishes the next time the job runs: retry the job if it failed, and resume the library if it is paused."
	if !strings.Contains(refused.Message, advice) {
		t.Fatalf("refusal = %q, want it to say %q", refused.Message, advice)
	}
	// Nothing was reclaimed for a delete that did not happen.
	if _, err := os.Stat(staged.job.OutputPath); err != nil {
		t.Errorf("part file of the job with an interrupted publish: %v", err)
	}

	// The next attempt resumes the publish, and then the library can go.
	if _, err := (replacepkg.Manager{Journal: f.state}).Handoff(ctx, staged.job); err != nil {
		t.Fatalf("resumed Handoff = %v", err)
	}
	wantNoContent(t, f.send(http.MethodDelete, "/api/v1/libraries/inbox", nil))
	if data, err := os.ReadFile(staged.job.DestinationPath); err != nil || string(data) != "the encode" {
		t.Errorf("published file after the delete = %q, %v", data, err)
	}
}

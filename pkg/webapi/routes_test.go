package webapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/flowstep"
)

// TestEveryRoute walks the whole API the way an operator would: look around,
// build a flow, add a connection and a library, let a scan queue work, act on
// the jobs, and take everything down again.
func TestEveryRoute(t *testing.T) {
	f := newFixture(t, "", nil)
	film := f.writeMedia("film.mkv")
	f.writeMedia("show/episode.mkv")
	f.writeMedia("show/episode.nfo")

	session := want[Session](t, f.get("/api/v1/session"), http.StatusOK)
	if session.AuthRequired || !session.Authenticated {
		t.Fatalf("session = %+v, want open access without a password", session)
	}
	if session = want[Session](t, f.send(http.MethodPost, "/api/v1/session", LoginRequest{}), http.StatusOK); !session.Authenticated {
		t.Fatalf("login without a password = %+v", session)
	}

	status := want[Status](t, f.get("/api/v1/status"), http.StatusOK)
	if status.Flows != 1 || status.Libraries != 0 || status.Running == nil || status.Queue == nil {
		t.Fatalf("status = %+v", status)
	}
	settings := want[Settings](t, f.get("/api/v1/settings"), http.StatusOK)
	if settings.Web.Listen != testHost || settings.Web.AuthRequired || settings.Daemon.ScanInterval == "" || settings.ConfigPath == "" {
		t.Fatalf("settings = %+v", settings)
	}
	directory := want[Directory](t, f.get("/api/v1/fs?files=1&path="+url.QueryEscape(f.media)), http.StatusOK)
	if len(directory.Entries) != 2 || directory.Entries[0].Name != "film.mkv" || directory.Entries[0].Dir || !directory.Entries[1].Dir {
		t.Fatalf("directory = %+v, want film.mkv and show/", directory)
	}
	if folders := want[Directory](t, f.get("/api/v1/fs?path="+url.QueryEscape(f.media)), http.StatusOK); len(folders.Entries) != 1 {
		t.Fatalf("folders = %+v, want only show/", folders)
	}
	if catalog := want[NodeTypes](t, f.get("/api/v1/node-types"), http.StatusOK); len(catalog.NodeTypes) == 0 {
		t.Fatal("the node catalog is empty")
	}

	// Flows.
	seeded := f.defaultFlow()
	created := want[Flow](t, f.send(http.MethodPost, "/api/v1/flows", FlowWrite{Name: " Skip HEVC ", Graph: skipHEVC()}), http.StatusCreated)
	if created.Name != "Skip HEVC" || created.Revision != 1 || created.NodeCount != 6 || !created.Check.Valid || created.Libraries == nil {
		t.Fatalf("created flow = %+v", created)
	}
	check := want[FlowCheck](t, f.send(http.MethodPost, "/api/v1/flows/check", FlowCheckRequest{Graph: skipHEVC()}), http.StatusOK)
	if !check.Valid || check.Issues == nil || len(check.Nodes) != 6 || check.Nodes["encode"].Summary == "" {
		t.Fatalf("check = %+v", check)
	}
	flowPath := fmt.Sprintf("/api/v1/flows/%d", created.ID)
	if got := want[Flow](t, f.get(flowPath), http.StatusOK); len(got.Graph.Nodes) != 6 {
		t.Fatalf("flow = %+v", got)
	}
	updated := want[Flow](t, f.send(http.MethodPut, flowPath, FlowWrite{
		Name: "Skip HEVC", Description: "Leaves HEVC alone.", Graph: skipHEVC(), Revision: created.Revision,
	}), http.StatusOK)
	if updated.Revision != created.Revision+1 || updated.Description != "Leaves HEVC alone." {
		t.Fatalf("updated flow = %+v", updated)
	}
	duplicate := want[Flow](t, f.send(http.MethodPost, flowPath+"/duplicate", nil), http.StatusCreated)
	if duplicate.Name != "Skip HEVC copy" || duplicate.ID == created.ID || len(duplicate.Graph.Nodes) != 6 {
		t.Fatalf("duplicate = %+v", duplicate)
	}
	wantNoContent(t, f.send(http.MethodDelete, fmt.Sprintf("/api/v1/flows/%d", duplicate.ID), nil))

	// Arr connections. The key goes in and never comes back out.
	arrCreated := f.send(http.MethodPost, "/api/v1/arrs", ArrWrite{
		Name: "radarr", Type: "radarr", BaseURL: "http://localhost:7878/", APIKey: pointer("s3cret-key"),
	})
	arr := want[Arr](t, arrCreated, http.StatusCreated)
	if !arr.APIKeySet || arr.BaseURL != "http://localhost:7878" || arr.Libraries == nil {
		t.Fatalf("arr = %+v", arr)
	}
	if strings.Contains(arrCreated.Body.String(), "s3cret-key") {
		t.Fatalf("the Arr response carries the key: %s", arrCreated.Body.String())
	}
	kept := want[Arr](t, f.send(http.MethodPut, "/api/v1/arrs/radarr", ArrWrite{
		Name: "radarr", Type: "radarr", BaseURL: "http://localhost:7879",
	}), http.StatusOK)
	if !kept.APIKeySet || kept.BaseURL != "http://localhost:7879" {
		t.Fatalf("an update without api_key must keep the key: %+v", kept)
	}
	if stored, err := f.state.GetArr(context.Background(), "radarr"); err != nil || stored.APIKey != "s3cret-key" {
		t.Fatalf("stored arr = %+v, %v", stored, err)
	}
	cleared := want[Arr](t, f.send(http.MethodPut, "/api/v1/arrs/radarr", ArrWrite{
		Name: "radarr", Type: "radarr", BaseURL: "http://localhost:7879", APIKeyFile: "/run/secrets/radarr", APIKey: pointer(""),
	}), http.StatusOK)
	if cleared.APIKeySet || cleared.APIKeyFile != "/run/secrets/radarr" {
		t.Fatalf("an empty api_key must clear the key: %+v", cleared)
	}
	if arrs := want[ArrList](t, f.get("/api/v1/arrs"), http.StatusOK); len(arrs.Arrs) != 1 {
		t.Fatalf("arrs = %+v", arrs)
	}

	// Libraries.
	library := want[Library](t, f.send(http.MethodPost, "/api/v1/libraries", LibraryWrite{
		Name: "movies", Kind: "media", Path: f.media + "/", FlowID: created.ID, Arr: "radarr",
		Rules: []Rule{{Action: "deny", Field: "extension", Op: "is", Value: "nfo"}},
	}), http.StatusCreated)
	if library.Path != f.media || library.FlowName != "Skip HEVC" || library.RuleFallback != "allow" ||
		library.Media.ReplacementMode != "replace" || len(library.Rules) != 1 || library.Download.IgnorableGlobs == nil {
		t.Fatalf("library = %+v", library)
	}
	if got := f.config().Libraries["movies"]; got.Path != f.media {
		t.Fatalf("the runtime snapshot was not refreshed before the answer: %+v", f.config().Libraries)
	}
	if list := want[LibraryList](t, f.get("/api/v1/libraries"), http.StatusOK); len(list.Libraries) != 1 {
		t.Fatalf("libraries = %+v", list)
	}
	if got := want[Flow](t, f.get(flowPath), http.StatusOK); !slices.Contains(got.Libraries, "movies") {
		t.Fatalf("flow = %+v, want it to name its library", got.FlowSummary)
	}
	if arrs := want[ArrList](t, f.get("/api/v1/arrs"), http.StatusOK); !slices.Contains(arrs.Arrs[0].Libraries, "movies") {
		t.Fatalf("arrs = %+v, want the connection to name its library", arrs)
	}

	scan := want[ScanResult](t, f.send(http.MethodPost, "/api/v1/libraries/movies/scan", nil), http.StatusOK)
	if scan.EnqueuedJobs != 2 {
		t.Fatalf("scan = %+v, want two jobs", scan)
	}
	shown := want[Library](t, f.get("/api/v1/libraries/movies"), http.StatusOK)
	if shown.Stats.Files != 2 || shown.Stats.Pending != 2 || shown.LastScanAt == nil {
		t.Fatalf("library after the scan = %+v", shown)
	}
	files := want[FileList](t, f.get("/api/v1/libraries/movies/files?status=queued&sort=path&dir=desc&q=E&limit=10"), http.StatusOK)
	if files.Total != 1 || len(files.Files) != 1 || files.Files[0].Path != "show/episode.mkv" ||
		files.Files[0].AbsolutePath != f.media+"/show/episode.mkv" || files.Files[0].JobID == 0 || files.Files[0].JobState != "pending" {
		t.Fatalf("files = %+v", files)
	}

	// Jobs.
	jobs := want[JobList](t, f.get("/api/v1/jobs?library=movies&state=pending,running&q=film"), http.StatusOK)
	if jobs.Total != 1 || len(jobs.Jobs) != 1 || jobs.Jobs[0].Status != FileStatusQueued || jobs.Jobs[0].Source.AbsolutePath != film {
		t.Fatalf("jobs = %+v", jobs)
	}
	filmJob := jobs.Jobs[0].ID
	detail := want[JobDetail](t, f.get(fmt.Sprintf("/api/v1/jobs/%d", filmJob)), http.StatusOK)
	if detail.Job.ID != filmJob || detail.Status != FileStatusQueued || detail.Attempts == nil || detail.Flow != nil {
		t.Fatalf("job detail = %+v", detail)
	}

	paused := library.LibraryWrite
	paused.Paused = true
	paused.Rules = []Rule{}
	if got := want[Library](t, f.send(http.MethodPut, "/api/v1/libraries/movies", paused), http.StatusOK); !got.Paused || len(got.Rules) != 0 {
		t.Fatalf("paused library = %+v", got)
	}
	if !f.config().Libraries["movies"].Paused {
		t.Fatal("the pause did not reach the runtime snapshot")
	}

	canceled := want[JobActionResult](t, f.send(http.MethodPost, "/api/v1/jobs/cancel", JobActionRequest{IDs: []int64{filmJob, 99999}}), http.StatusOK)
	if canceled.Matched != 2 || canceled.Changed != 1 || len(canceled.Skipped) != 1 || canceled.Skipped[0].ID != 99999 {
		t.Fatalf("cancel = %+v", canceled)
	}
	retried := want[JobActionResult](t, f.send(http.MethodPost, "/api/v1/jobs/retry", JobActionRequest{IDs: []int64{filmJob, 99999}}), http.StatusOK)
	if retried.Matched != 2 || retried.Changed != 1 || len(retried.Skipped) != 1 {
		t.Fatalf("retry = %+v", retried)
	}
	if all := want[JobActionResult](t, f.send(http.MethodPost, "/api/v1/jobs/cancel", JobActionRequest{Library: "movies"}), http.StatusOK); all.Changed != 2 {
		t.Fatalf("cancel by library = %+v", all)
	}
	if again := want[JobActionResult](t, f.send(http.MethodPost, "/api/v1/jobs/retry", JobActionRequest{Library: "movies", States: []string{"canceled"}}), http.StatusOK); again.Matched != 2 || again.Changed != 2 {
		t.Fatalf("retry by state = %+v", again)
	}
	if all := want[JobActionResult](t, f.send(http.MethodPost, "/api/v1/jobs/cancel", JobActionRequest{States: []string{"pending"}}), http.StatusOK); all.Changed != 2 || all.Skipped == nil {
		t.Fatalf("cancel by state = %+v", all)
	}

	requeued := want[JobActionResult](t, f.send(http.MethodPost, "/api/v1/libraries/movies/requeue", RequeueRequest{Paths: []string{"film.mkv", "gone.mkv"}}), http.StatusOK)
	if requeued.Matched != 2 || requeued.Changed != 1 || len(requeued.Skipped) != 1 || !strings.Contains(requeued.Skipped[0].Reason, "gone.mkv") {
		t.Fatalf("requeue = %+v", requeued)
	}

	// Taking it down: everything still in use refuses with a 409 that says
	// what uses it.
	inUse := wantError(t, f.send(http.MethodDelete, flowPath, nil), http.StatusConflict, CodeConflict)
	if !strings.Contains(inUse.Message, "movies") {
		t.Fatalf("flow in use = %+v, want it to name the library", inUse)
	}
	if inUse = wantError(t, f.send(http.MethodDelete, "/api/v1/arrs/radarr", nil), http.StatusConflict, CodeConflict); !strings.Contains(inUse.Message, "movies") {
		t.Fatalf("arr in use = %+v, want it to name the library", inUse)
	}
	busy := wantError(t, f.send(http.MethodDelete, "/api/v1/libraries/movies", nil), http.StatusConflict, CodeConflict)
	if !strings.Contains(busy.Message, "queued or running") {
		t.Fatalf("library with a pending job = %+v", busy)
	}
	want[JobActionResult](t, f.send(http.MethodPost, "/api/v1/jobs/cancel", JobActionRequest{Library: "movies"}), http.StatusOK)
	wantNoContent(t, f.send(http.MethodDelete, "/api/v1/libraries/movies", nil))
	if len(f.config().Libraries) != 0 {
		t.Fatal("the deleted library is still in the runtime snapshot")
	}
	if left := want[JobList](t, f.get("/api/v1/jobs"), http.StatusOK); left.Total != 0 {
		t.Fatalf("jobs after deleting the library = %+v", left)
	}
	wantNoContent(t, f.send(http.MethodDelete, "/api/v1/arrs/radarr", nil))
	wantNoContent(t, f.send(http.MethodDelete, flowPath, nil))
	wantNoContent(t, f.send(http.MethodDelete, "/api/v1/session", nil))
	if f.defaultFlow().ID != seeded.ID {
		t.Fatal("the seeded flow changed")
	}
}

// TestListsAreNeverNull checks the empty answers of an empty daemon: a UI
// that maps over a list must never meet null.
func TestListsAreNeverNull(t *testing.T) {
	f := newFixture(t, "", nil)
	for _, path := range []string{"/api/v1/status", "/api/v1/libraries", "/api/v1/arrs", "/api/v1/jobs", "/api/v1/flows", "/api/v1/node-types"} {
		recorder := f.get(path)
		if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "null") {
			t.Errorf("GET %s = %d %s, want no null", path, recorder.Code, recorder.Body.String())
		}
	}
	f.writeMedia("film.mkv")
	f.mediaLibrary("movies", f.defaultFlow().ID)
	for _, path := range []string{"/api/v1/libraries", "/api/v1/libraries/movies/files", "/api/v1/flows/1"} {
		recorder := f.get(path)
		if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "null") {
			t.Errorf("GET %s = %d %s, want no null", path, recorder.Code, recorder.Body.String())
		}
	}
	recorder := f.send(http.MethodPost, "/api/v1/jobs/cancel", JobActionRequest{Library: "movies"})
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "null") {
		t.Errorf("cancel of nothing = %d %s, want no null", recorder.Code, recorder.Body.String())
	}
}

// TestJobDetailShowsTheFlowTheAttemptRan records an attempt the way the
// worker does and checks that the job reports the snapshot it ran, not the
// flow as it is now.
func TestJobDetailShowsTheFlowTheAttemptRan(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "", nil)
	f.writeMedia("film.mkv")
	created := want[Flow](t, f.send(http.MethodPost, "/api/v1/flows", FlowWrite{Name: "Skip HEVC", Graph: skipHEVC()}), http.StatusCreated)
	f.mediaLibrary("movies", created.ID)
	want[ScanResult](t, f.send(http.MethodPost, "/api/v1/libraries/movies/scan", nil), http.StatusOK)

	now := time.Now().UTC()
	job, err := f.state.LeaseNextJob(ctx, "worker-1", now.Add(time.Hour), now)
	if err != nil || job == nil {
		t.Fatalf("LeaseNextJob = %v, %v", job, err)
	}
	graph, err := json.Marshal(skipHEVC())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(domain.FlowSnapshot{ID: created.ID, Name: created.Name, Revision: created.Revision, Graph: graph})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := f.state.StartAttempt(ctx, job.ID, "worker-1", nil, snapshot, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := json.Marshal(domain.FlowResolution{
		FlowID: created.ID, FlowName: created.Name, Revision: created.Revision, Outcome: string(flow.OutcomeSkip),
		Path: []string{"in", "is-hevc", "skip"}, Decisions: []string{"Video is hevc? → true (video codec is hevc)"},
		Plan: []string{"File: skip, leave it untouched"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.state.RecordAttemptEvent(ctx, domain.AttemptEvent{
		AttemptID: attempt.ID, Type: domain.AttemptEventArtifact, Name: flowstep.ArtifactName, Payload: resolution, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// The flow is edited after the attempt: the job keeps showing what ran.
	want[Flow](t, f.send(http.MethodPut, fmt.Sprintf("/api/v1/flows/%d", created.ID), FlowWrite{Name: "Renamed", Graph: flow.Default(), Revision: created.Revision}), http.StatusOK)

	detail := want[JobDetail](t, f.get(fmt.Sprintf("/api/v1/jobs/%d", job.ID)), http.StatusOK)
	if detail.Status != FileStatusRunning || detail.Flow == nil {
		t.Fatalf("job detail = %+v, want a running job with its flow", detail)
	}
	got := detail.Flow
	if got.FlowName != "Skip HEVC" || got.Revision != created.Revision || got.Outcome != flow.OutcomeSkip ||
		len(got.Graph.Nodes) != 6 || !slices.Equal(got.Path, []string{"in", "is-hevc", "skip"}) || len(got.Decisions) != 1 || len(got.Plan) != 1 {
		t.Fatalf("job flow = %+v", got)
	}
}

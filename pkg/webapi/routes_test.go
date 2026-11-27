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
	"github.com/zekurio/anvil/pkg/store"
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

	// One job that cannot be canceled or retried never fails the request:
	// the others are acted on, and it is reported with its reason.
	reasons := func(result JobActionResult) map[int64]string {
		byJob := make(map[int64]string, len(result.Skipped))
		for _, skip := range result.Skipped {
			if skip.Detail == "" {
				t.Errorf("skip %+v has no detail", skip)
			}
			byJob[skip.ID] = skip.Reason
		}
		return byJob
	}
	episodeJob := want[JobList](t, f.get("/api/v1/jobs?q=episode"), http.StatusOK).Jobs[0].ID
	canceled := want[JobActionResult](t, f.send(http.MethodPost, "/api/v1/jobs/cancel", JobActionRequest{IDs: []int64{filmJob, 99999}}), http.StatusOK)
	if canceled.Matched != 2 || canceled.Changed != 1 || len(canceled.Skipped) != 1 || reasons(canceled)[99999] != SkipNotFound {
		t.Fatalf("cancel = %+v", canceled)
	}
	if again := want[JobActionResult](t, f.send(http.MethodPost, "/api/v1/jobs/cancel", JobActionRequest{IDs: []int64{filmJob}}), http.StatusOK); again.Changed != 0 || reasons(again)[filmJob] != SkipFinished {
		t.Fatalf("cancel of a canceled job = %+v", again)
	}
	retried := want[JobActionResult](t, f.send(http.MethodPost, "/api/v1/jobs/retry", JobActionRequest{IDs: []int64{episodeJob, filmJob, 99999}}), http.StatusOK)
	if got := reasons(retried); retried.Matched != 3 || retried.Changed != 1 || len(retried.Skipped) != 2 || got[episodeJob] != SkipNotRetryable || got[99999] != SkipNotFound {
		t.Fatalf("retry = %+v, want the canceled job queued and the pending and the missing one reported", retried)
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

	requeued := want[JobActionResult](t, f.send(http.MethodPost, "/api/v1/libraries/movies/requeue", RequeueRequest{Paths: []string{"film.mkv", "gone.mkv", "../outside.mkv"}}), http.StatusOK)
	byPath := make(map[string]string)
	for _, skip := range requeued.Skipped {
		byPath[skip.Path] = skip.Reason
	}
	if requeued.Matched != 3 || requeued.Changed != 1 || len(requeued.Skipped) != 2 || byPath["gone.mkv"] != SkipNotFound || byPath["../outside.mkv"] != SkipInvalid {
		t.Fatalf("requeue = %+v", requeued)
	}
	// The file that was just requeued has a pending job, and a file the rules
	// deny is not queued by force either.
	denying := want[Library](t, f.get("/api/v1/libraries/movies"), http.StatusOK).LibraryWrite
	denying.Rules = []Rule{{Action: "deny", Field: "path", Op: "matches", Value: "show/**"}}
	want[Library](t, f.send(http.MethodPut, "/api/v1/libraries/movies", denying), http.StatusOK)
	requeued = want[JobActionResult](t, f.send(http.MethodPost, "/api/v1/libraries/movies/requeue", RequeueRequest{Paths: []string{"film.mkv", "show/episode.mkv"}}), http.StatusOK)
	if requeued.Changed != 0 || len(requeued.Skipped) != 2 || requeued.Skipped[0].Reason != SkipActive || requeued.Skipped[1].Reason != SkipDenied {
		t.Fatalf("requeue of a queued and a denied file = %+v", requeued)
	}
	denying.Rules = []Rule{}
	want[Library](t, f.send(http.MethodPut, "/api/v1/libraries/movies", denying), http.StatusOK)

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

	// A running job says which job it is everywhere it is listed, before its
	// first progress report and with one: the Queue needs no second request
	// to name the file, and the job sheet none to know where the result goes.
	wantRunning := func(progress *Progress, step string, percent float64) {
		t.Helper()
		if progress == nil || progress.JobID != int64(job.ID) || progress.Library != "movies" || progress.Path != "film.mkv" ||
			progress.FlowName != "Skip HEVC" || progress.Step != step || progress.Percent != percent {
			t.Fatalf("progress = %+v, want job %d of movies at film.mkv on Skip HEVC, step %q at %v", progress, job.ID, step, percent)
		}
	}
	status := want[Status](t, f.get("/api/v1/status"), http.StatusOK)
	if len(status.Running) != 1 {
		t.Fatalf("status.running = %+v, want the one running job", status.Running)
	}
	wantRunning(&status.Running[0], "", -1)
	wantRunning(detail.Progress, "", -1)
	if detail.InputPath != f.media+"/film.mkv" || detail.DestinationPath != f.media+"/film.mkv" || detail.InputSizeBytes != 0 || detail.OutputSizeBytes != 0 {
		t.Fatalf("job detail = input %q, destination %q, sizes %d and %d", detail.InputPath, detail.DestinationPath, detail.InputSizeBytes, detail.OutputSizeBytes)
	}

	f.server.Progress = reports{job.ID: {JobID: job.ID, Step: "encode", Percent: 41.5, FPS: 88, Speed: 3.6, ETASeconds: 12, UpdatedAt: now}}
	status = want[Status](t, f.get("/api/v1/status"), http.StatusOK)
	wantRunning(&status.Running[0], "encode", 41.5)
	rows := want[JobList](t, f.get("/api/v1/jobs?status=running"), http.StatusOK)
	if len(rows.Jobs) != 1 || rows.Jobs[0].Path != "film.mkv" || rows.Jobs[0].StartedAt == nil || rows.Jobs[0].FinishedAt != nil {
		t.Fatalf("running rows = %+v, want film.mkv with a start and no end", rows.Jobs)
	}
	wantRunning(rows.Jobs[0].Progress, "encode", 41.5)
	files := want[FileList](t, f.get("/api/v1/libraries/movies/files"), http.StatusOK)
	wantRunning(files.Files[0].Progress, "encode", 41.5)

	// Once the job is done it is no longer listed as running, whatever the
	// tracker still holds, and the rows carry what the encode took and gave.
	for _, state := range []domain.JobState{domain.JobStateValidating, domain.JobStateReplacing} {
		if _, err := f.state.TransitionJob(ctx, job.ID, state, now, ""); err != nil {
			t.Fatal(err)
		}
	}
	if status = want[Status](t, f.get("/api/v1/status"), http.StatusOK); len(status.Running) != 1 {
		t.Fatalf("status.running while publishing = %+v, want the job still listed", status.Running)
	}
	if _, err := f.state.CompleteJobOccurrence(ctx, store.CompleteJobOccurrenceInput{
		JobID: job.ID, AttemptID: attempt.ID, InputSizeBytes: 5000, OutputSizeBytes: 2000, CompletedAt: now.Add(90 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if status = want[Status](t, f.get("/api/v1/status"), http.StatusOK); len(status.Running) != 0 {
		t.Fatalf("status.running after completion = %+v", status.Running)
	}
	rows = want[JobList](t, f.get("/api/v1/jobs?status=done"), http.StatusOK)
	if len(rows.Jobs) != 1 || rows.Jobs[0].Progress != nil || rows.Jobs[0].FinishedAt == nil || rows.Jobs[0].FinishedAt.Sub(*rows.Jobs[0].StartedAt) != 90*time.Second {
		t.Fatalf("finished rows = %+v, want a 90 s attempt", rows.Jobs)
	}
	detail = want[JobDetail](t, f.get(fmt.Sprintf("/api/v1/jobs/%d", job.ID)), http.StatusOK)
	if detail.InputSizeBytes != 5000 || detail.OutputSizeBytes != 2000 || detail.Progress != nil {
		t.Fatalf("finished job detail = sizes %d and %d, progress %+v", detail.InputSizeBytes, detail.OutputSizeBytes, detail.Progress)
	}
}

// reports is a progress tracker holding fixed reports.
type reports map[domain.JobID]domain.JobProgress

func (r reports) Snapshot(id domain.JobID) (domain.JobProgress, bool) {
	progress, found := r[id]
	return progress, found
}

func (r reports) All() []domain.JobProgress {
	all := make([]domain.JobProgress, 0, len(r))
	for _, progress := range r {
		all = append(all, progress)
	}
	return all
}

// TestListsFilterSortAndPage pins the query vocabulary the tables of the UI
// are built on: which keys sort, how job states are selected, and what a page
// past the end answers.
func TestListsFilterSortAndPage(t *testing.T) {
	f := newFixture(t, "", nil)
	for _, name := range []string{"a.mkv", "b.mkv", "c.mkv"} {
		f.writeMedia(name)
	}
	f.mediaLibrary("movies", f.defaultFlow().ID)
	want[ScanResult](t, f.send(http.MethodPost, "/api/v1/libraries/movies/scan", nil), http.StatusOK)
	all := want[JobList](t, f.get("/api/v1/jobs"), http.StatusOK)
	if all.Total != 3 {
		t.Fatalf("jobs = %+v, want three", all)
	}
	want[JobActionResult](t, f.send(http.MethodPost, "/api/v1/jobs/cancel", JobActionRequest{IDs: []int64{all.Jobs[0].ID}}), http.StatusOK)

	// Files: exactly four keys sort.
	for _, key := range []string{"path", "size", "status", "updated_at"} {
		for _, dir := range []string{"asc", "desc"} {
			if files := want[FileList](t, f.get("/api/v1/libraries/movies/files?sort="+key+"&dir="+dir), http.StatusOK); len(files.Files) != 3 {
				t.Errorf("files sorted by %s %s = %+v", key, dir, files)
			}
		}
	}
	for _, key := range []string{"container", "video_codec", "resolution", "audio", "updated"} {
		if refused := wantError(t, f.get("/api/v1/libraries/movies/files?sort="+key), http.StatusBadRequest, CodeInvalid); refused.Field != "sort" {
			t.Errorf("files sorted by %s = %+v, want the sort parameter refused", key, refused)
		}
	}
	byStatus := want[FileList](t, f.get("/api/v1/libraries/movies/files?sort=status"), http.StatusOK)
	if byStatus.Files[0].Status != FileStatusQueued || byStatus.Files[2].Status != FileStatusSkipped {
		t.Errorf("files by status = %+v, want the queued ones before the canceled one", byStatus.Files)
	}

	// Jobs: by status group, by exact state, repeated or comma-separated,
	// and both together.
	for query, total := range map[string]int64{
		"status=queued":                   2,
		"status=skipped":                  1,
		"status=queued,skipped":           3,
		"status=queued&status=skipped":    3,
		"status=done&status=failed":       0,
		"state=pending":                   2,
		"state=canceled&state=pending":    3,
		"state=canceled,complete":         1,
		"status=queued&state=canceled":    3,
		"status=running&state=validating": 0,
	} {
		if jobs := want[JobList](t, f.get("/api/v1/jobs?"+query), http.StatusOK); jobs.Total != total || int64(len(jobs.Jobs)) != total {
			t.Errorf("jobs?%s: total %d with %d rows, want %d", query, jobs.Total, len(jobs.Jobs), total)
		}
	}
	for _, query := range []string{"status=none", "status=pending", "status=odd"} {
		if refused := wantError(t, f.get("/api/v1/jobs?"+query), http.StatusBadRequest, CodeInvalid); refused.Field != "status" {
			t.Errorf("jobs?%s = %+v, want the status parameter refused", query, refused)
		}
	}

	// A page past the end is empty and still says how many rows there are.
	if files := want[FileList](t, f.get("/api/v1/libraries/movies/files?limit=2&offset=10"), http.StatusOK); files.Total != 3 || files.Files == nil || len(files.Files) != 0 {
		t.Errorf("files past the end = %+v, want total 3 and an empty page", files)
	}
	if jobs := want[JobList](t, f.get("/api/v1/jobs?limit=2&offset=10"), http.StatusOK); jobs.Total != 3 || jobs.Jobs == nil || len(jobs.Jobs) != 0 {
		t.Errorf("jobs past the end = %+v, want total 3 and an empty page", jobs)
	}
	if last := want[JobList](t, f.get("/api/v1/jobs?limit=2&offset=2"), http.StatusOK); last.Total != 3 || len(last.Jobs) != 1 {
		t.Errorf("last page of jobs = %+v, want one row", last)
	}
}

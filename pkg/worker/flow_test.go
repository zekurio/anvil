package worker

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/flowstep"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/scheduler"
	"github.com/zekurio/anvil/pkg/store"
)

// probeBlock stands in for ffprobe.
type probeBlock struct {
	probe domain.ProbeResult
}

func (probeBlock) Name() string { return "probe" }

func (b probeBlock) Run(_ context.Context, job *pipeline.JobContext) error {
	probe := b.probe
	job.Probe = &probe
	return nil
}

// recordingBlock notes that the pipeline reached it and with which profile.
type recordingBlock struct {
	name     string
	reached  *int
	profiles *[]domain.Profile
}

func (b recordingBlock) Name() string { return b.name }

func (b recordingBlock) Run(_ context.Context, job *pipeline.JobContext) error {
	*b.reached++
	*b.profiles = append(*b.profiles, job.Profile)
	return nil
}

// skipHEVC publishes everything except HEVC sources, which it skips.
func skipHEVC() flow.Graph {
	return flow.Graph{
		Nodes: []flow.Node{
			{ID: "in", Type: "input"},
			{ID: "is-hevc", Type: "check.video_codec", Label: "Video is hevc?", Options: map[string]any{"codecs": []string{"hevc"}}},
			{ID: "skip", Type: "output.skip"},
			{ID: "encode", Type: "video.encode", Options: map[string]any{"quality_mode": "fixed", "crf": 30, "preset": "10"}},
			{ID: "audio", Type: "audio.keep", Options: map[string]any{"languages": []string{"orig"}}},
			{ID: "publish", Type: "output.publish"},
		},
		Edges: []flow.Edge{
			{From: "in", To: "is-hevc"},
			{From: "is-hevc", Port: 0, To: "skip"},
			{From: "is-hevc", Port: 1, To: "encode"},
			{From: "encode", To: "audio"},
			{From: "audio", To: "publish"},
		},
	}
}

type flowFixture struct {
	t        *testing.T
	state    *store.SQLiteStore
	flowID   int64
	cfg      config.Config
	reached  int
	profiles []domain.Profile
}

func newFlowFixture(t *testing.T) *flowFixture {
	t.Helper()
	ctx := context.Background()
	state, err := store.Open(ctx, filepath.Join(t.TempDir(), "anvil.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	created, err := state.CreateFlow(ctx, store.FlowInput{Name: "Skip HEVC", Graph: skipHEVC()}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	library := config.LibraryConfig{Name: "movies", Kind: "media", Path: "/media/movies", FlowID: created.ID, Media: config.MediaLibraryConfig{ReplacementMode: "replace"}}
	if _, err := state.CreateLibrary(ctx, library, time.Now()); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Libraries = map[string]config.LibraryConfig{"movies": library}
	return &flowFixture{t: t, state: state, flowID: created.ID, cfg: cfg}
}

// run scans one file into the library, leases its job, and runs one attempt
// with the given source codec.
func (f *flowFixture) run(path, codec string) (domain.Job, domain.Attempt, error) {
	f.t.Helper()
	ctx := context.Background()
	token, err := f.state.BeginLibraryScan(ctx, "movies")
	if err != nil {
		f.t.Fatal(err)
	}
	entry := store.ScanEntry{
		SourceKind: domain.SourceKindFile, SourceRelativePath: path, SourceFingerprint: domain.FileFingerprint{SizeBytes: 10},
		AssetRelativePath: path, AssetRole: domain.MediaAssetRolePrimaryVideo, AssetFingerprint: domain.FileFingerprint{SizeBytes: 10},
		Persist: true, Enqueue: true,
	}
	if _, err := f.state.ApplyLibraryScan(ctx, token, store.ApplyScanInput{LibraryName: "movies", SourcePaths: []string{path}, Entries: []store.ScanEntry{entry}}); err != nil {
		f.t.Fatal(err)
	}
	return f.attempt(codec)
}

func (f *flowFixture) attempt(codec string) (domain.Job, domain.Attempt, error) {
	f.t.Helper()
	ctx := context.Background()
	now := time.Now()
	leased, err := f.state.LeaseNextJob(ctx, "w1", now.Add(time.Hour), now)
	if err != nil || leased == nil {
		f.t.Fatalf("LeaseNextJob = %v, %v", leased, err)
	}
	probe := domain.ProbeResult{DurationSeconds: 60, SizeBytes: 10, Streams: []domain.MediaStream{
		{Index: 0, Type: "video", Codec: codec, Width: 1920, Height: 1080},
		{Index: 1, Type: "audio", Codec: "ac3", Channels: 6, Language: "eng"},
	}}
	runner := Runner{
		Store:             f.state,
		ConfigProvider:    func() config.Config { return f.cfg },
		VerifyFingerprint: func(string, domain.FileFingerprint) error { return nil },
		TempDir:           f.t.TempDir(),
		Pipeline: pipeline.Runner{Blocks: []pipeline.Block{
			probeBlock{probe: probe},
			flowstep.Block{},
			recordingBlock{name: "after-flow", reached: &f.reached, profiles: &f.profiles},
		}},
	}
	runErr := runner.Run(ctx, scheduler.Assignment{Job: *leased, WorkerID: "w1"})
	job, err := f.state.GetJob(ctx, leased.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	attempts, err := f.state.ListAttemptsForJob(ctx, leased.ID)
	if err != nil || len(attempts) == 0 {
		f.t.Fatalf("ListAttemptsForJob = %v, %v", attempts, err)
	}
	return job, attempts[len(attempts)-1], runErr
}

func (f *flowFixture) resolution(attempt domain.Attempt) (domain.FlowResolution, []string) {
	f.t.Helper()
	events, err := f.state.ListAttemptEvents(context.Background(), attempt.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	var resolution domain.FlowResolution
	var summary []string
	for _, event := range events {
		summary = append(summary, string(event.Type)+":"+event.Name)
		if event.Type == domain.AttemptEventArtifact && event.Name == flowstep.ArtifactName {
			if resolution, err = flowstep.DecodeResolution(event.Payload); err != nil {
				f.t.Fatal(err)
			}
		}
	}
	return resolution, summary
}

func TestFlowSkipEndsTheAttemptSuccessfully(t *testing.T) {
	f := newFlowFixture(t)
	job, attempt, err := f.run("film.mkv", "hevc")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if job.State != domain.JobStateSkipped || attempt.State != domain.AttemptStateSucceeded {
		t.Fatalf("job %s, attempt %s", job.State, attempt.State)
	}
	if want := `skipped by flow "Skip HEVC": Video is hevc? → true (video codec is hevc)`; job.LastError != want {
		t.Fatalf("skip reason = %q, want %q", job.LastError, want)
	}
	if f.reached != 0 {
		t.Fatal("a step behind the flow ran for a skipped file")
	}
	resolution, events := f.resolution(attempt)
	if resolution.Outcome != domain.FlowOutcomeSkip || resolution.FlowName != "Skip HEVC" || resolution.Revision != 1 ||
		strings.Join(resolution.Path, ",") != "in,is-hevc,skip" || len(resolution.Decisions) != 1 || len(resolution.Plan) != 1 {
		t.Fatalf("resolution = %+v", resolution)
	}
	// The skip is the step's result, not its failure.
	joined := strings.Join(events, " ")
	if strings.Contains(joined, "block_failed") || !strings.Contains(joined, "block_finished:flow") {
		t.Fatalf("events = %s", joined)
	}
	if len(attempt.ResolvedProfile) != 0 {
		t.Fatalf("a skipped attempt recorded a profile: %s", attempt.ResolvedProfile)
	}
	var snapshot domain.FlowSnapshot
	if err := json.Unmarshal(attempt.ResolvedFlow, &snapshot); err != nil || snapshot.ID != f.flowID || snapshot.Revision != 1 || len(snapshot.Graph) == 0 {
		t.Fatalf("flow snapshot = %+v, %v", snapshot, err)
	}
}

func TestFlowProcessResolvesTheProfileForLaterSteps(t *testing.T) {
	f := newFlowFixture(t)
	job, attempt, err := f.run("film.mkv", "h264")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if job.State != domain.JobStateComplete || attempt.State != domain.AttemptStateSucceeded {
		t.Fatalf("job %s (%s), attempt %s", job.State, job.LastError, attempt.State)
	}
	if f.reached != 1 {
		t.Fatalf("the step behind the flow ran %d times", f.reached)
	}
	seen := f.profiles[0]
	if seen.Video.QualityMode != domain.VideoQualityFixed || seen.Video.CRF != 30 || seen.Video.Preset != "10" || seen.Name != "Skip HEVC" {
		t.Fatalf("profile seen by the next step = %+v", seen)
	}
	var recorded domain.Profile
	if err := json.Unmarshal(attempt.ResolvedProfile, &recorded); err != nil {
		t.Fatalf("resolved profile %q: %v", attempt.ResolvedProfile, err)
	}
	if recorded.Video.CRF != 30 || recorded.Video.QualityMode != domain.VideoQualityFixed {
		t.Fatalf("recorded profile = %+v", recorded)
	}
	resolution, _ := f.resolution(attempt)
	if resolution.Outcome != domain.FlowOutcomeProcess || strings.Join(resolution.Path, ",") != "in,is-hevc,encode,audio,publish" || len(resolution.Plan) != 4 {
		t.Fatalf("resolution = %+v", resolution)
	}
}

// The flow keeps the original language, and nobody could say what it is, so
// the language rules are switched off, as they were when the profile was
// known before the pipeline started.
func TestFlowStepDisablesCleanupWithoutAnOriginalLanguage(t *testing.T) {
	job := &pipeline.JobContext{
		Flow:  domain.FlowSnapshot{ID: 1, Name: "f", Revision: 1, Graph: mustJSON(skipHEVC())},
		Probe: &domain.ProbeResult{Streams: []domain.MediaStream{{Type: "video", Codec: "h264", Width: 1280, Height: 720}}},
	}
	if err := (flowstep.Block{}).Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if !job.Metadata.StreamCleanupDisabled || job.Metadata.StreamCleanupDisabledReason != "original language metadata is unavailable" {
		t.Fatalf("metadata = %+v", job.Metadata)
	}

	job.Metadata = domain.JobMetadata{OriginalLanguage: "jpn"}
	if err := (flowstep.Block{}).Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if job.Metadata.StreamCleanupDisabled {
		t.Fatalf("cleanup disabled although the original language is known: %+v", job.Metadata)
	}
}

func TestMissingFlowFailsTheAttemptPlainly(t *testing.T) {
	f := newFlowFixture(t)
	library := f.cfg.Libraries["movies"]
	library.FlowID = 9999
	f.cfg.Libraries = map[string]config.LibraryConfig{"movies": library}
	job, attempt, err := f.run("film.mkv", "h264")
	want := `library "movies" is assigned flow 9999, which does not exist`
	if err == nil || err.Error() != want {
		t.Fatalf("Run error = %v, want %q", err, want)
	}
	if attempt.State != domain.AttemptStateFailed || attempt.Error != want || job.LastError != want {
		t.Fatalf("attempt %s %q, job %s %q", attempt.State, attempt.Error, job.State, job.LastError)
	}
	if f.reached != 0 || len(attempt.ResolvedFlow) != 0 || len(attempt.ResolvedLibrary) == 0 {
		t.Fatalf("reached %d, flow %q, library %q", f.reached, attempt.ResolvedFlow, attempt.ResolvedLibrary)
	}
}

func TestInvalidFlowFailsBeforeAnyStep(t *testing.T) {
	f := newFlowFixture(t)
	broken := skipHEVC()
	broken.Edges = broken.Edges[:len(broken.Edges)-1]
	if _, err := f.state.UpdateFlow(context.Background(), f.flowID, store.FlowInput{Name: "Skip HEVC", Graph: broken}, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, attempt, err := f.run("film.mkv", "h264")
	if err == nil || !strings.Contains(err.Error(), `flow "Skip HEVC": flow is not valid`) {
		t.Fatalf("Run error = %v", err)
	}
	// The snapshot is kept even so: it says which flow, at which revision,
	// the attempt could not run.
	if f.reached != 0 || attempt.State != domain.AttemptStateFailed || len(attempt.ResolvedFlow) == 0 {
		t.Fatalf("reached %d, attempt %+v", f.reached, attempt)
	}
}

// An edit that changes what the flow does takes effect on the next attempt
// of a job that already ran once: the retry evaluates the flow again and
// follows the new graph.
func TestRetryFollowsTheEditedFlow(t *testing.T) {
	f := newFlowFixture(t)
	job, _, err := f.run("film.mkv", "hevc")
	if err != nil || job.State != domain.JobStateSkipped {
		t.Fatalf("first attempt: %s, %v", job.State, err)
	}
	// The operator swaps the branches, then retries the skipped job.
	edited := skipHEVC()
	edited.Edges[1].Port, edited.Edges[2].Port = 1, 0
	if _, err := f.state.UpdateFlow(context.Background(), f.flowID, store.FlowInput{Name: "Skip HEVC", Graph: edited}, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.state.RetryJob(context.Background(), job.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	job, attempt, err := f.attempt("hevc")
	if err != nil || job.State != domain.JobStateComplete || f.reached != 1 {
		t.Fatalf("second attempt: %s (%s), reached %d, %v", job.State, job.LastError, f.reached, err)
	}
	var snapshot domain.FlowSnapshot
	if err := json.Unmarshal(attempt.ResolvedFlow, &snapshot); err != nil || snapshot.Revision != 2 {
		t.Fatalf("second attempt ran revision %d, %v", snapshot.Revision, err)
	}
}

func TestFlowSkipIsAPipelineStop(t *testing.T) {
	if !errors.Is(flowstep.ErrSkipped, pipeline.ErrStop) {
		t.Fatal("a flow skip is not a pipeline stop")
	}
}

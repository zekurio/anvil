package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/probe"
	"github.com/zekurio/anvil/pkg/process"
	"github.com/zekurio/anvil/pkg/scheduler"
	"github.com/zekurio/anvil/pkg/staging"
	"github.com/zekurio/anvil/pkg/store"
)

func TestRunnerResolvesLatestConfigAndCompletesJob(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	store := newFakeWorkerStore()
	store.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv"}
	store.asset = domain.MediaAsset{ID: 2, SourceID: 1, RelativePath: "Movie.mkv"}

	runner := Runner{
		Store:             store,
		ConfigProvider:    workerConfig,
		MetadataResolver:  staticMetadataResolver{metadata: domain.JobMetadata{OriginalLanguage: "eng"}},
		VerifyFingerprint: acceptFingerprint,
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(pipeline.BlockFunc{BlockName: "noop", Fn: func(_ context.Context, job *pipeline.JobContext) error {
				if job.InputPath == "" {
					t.Fatal("input path was empty")
				}
				if got, want := job.Metadata.OriginalLanguage, "eng"; got != want {
					t.Fatalf("original language = %q, want %q", got, want)
				}
				return nil
			}}),
		},
		Now: func() time.Time { return now },
	}

	err := runner.Run(ctx, scheduler.Assignment{
		Job:       domain.Job{ID: 99, SourceID: 1, AssetID: 2, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID:  "worker-1",
		Resources: domain.ResourceAllocation{WorkerID: "worker-1", Threads: 2},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if store.attempt.State != domain.AttemptStateSucceeded {
		t.Fatalf("attempt state = %q, want succeeded", store.attempt.State)
	}
	if got := store.transitions[len(store.transitions)-1]; got != domain.JobStateComplete {
		t.Fatalf("last transition = %q, want complete", got)
	}
	var library domain.Library
	if err := json.Unmarshal(store.resolvedLibrary, &library); err != nil {
		t.Fatalf("resolved library json: %v", err)
	}
	if library.Name != "movies" {
		t.Fatalf("resolved library name = %q, want movies", library.Name)
	}
}

func TestRunnerRecordsJobFileSizes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "output.mkv")

	store := newFakeWorkerStore()
	store.source = domain.MediaSource{
		ID:           1,
		LibraryName:  "movies",
		Kind:         domain.SourceKindFile,
		RelativePath: "Movie.mkv",
		Fingerprint:  domain.FileFingerprint{SizeBytes: 1000},
	}
	runner := Runner{
		Store:             store,
		ConfigProvider:    workerConfig,
		VerifyFingerprint: acceptFingerprint,
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(pipeline.BlockFunc{BlockName: "noop", Fn: func(_ context.Context, job *pipeline.JobContext) error {
				if err := os.WriteFile(outputPath, make([]byte, 650), 0o600); err != nil {
					t.Fatalf("write output: %v", err)
				}
				job.FinalPath = outputPath
				return nil
			}}),
		},
		Now: func() time.Time { return now },
	}

	err := runner.Run(ctx, scheduler.Assignment{
		Job:      domain.Job{ID: 99, SourceID: 1, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID: "worker-1",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if store.recordedInputSize != 1000 || store.recordedOutputSize != 650 {
		t.Fatalf("recorded sizes = %d/%d, want 1000/650", store.recordedInputSize, store.recordedOutputSize)
	}
}

func TestRunnerRequeuesFailedAttemptBeforeMaxAttempts(t *testing.T) {
	ctx := context.Background()
	store := newFakeWorkerStore()
	store.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv"}
	runner := Runner{
		Store:             store,
		ConfigProvider:    workerConfig,
		MaxAttempts:       2,
		VerifyFingerprint: acceptFingerprint,
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(pipeline.BlockFunc{BlockName: "noop", Fn: func(_ context.Context, _ *pipeline.JobContext) error {
				return errors.New("encode failed")
			}}),
		},
	}

	err := runner.Run(ctx, scheduler.Assignment{
		Job:      domain.Job{ID: 99, SourceID: 1, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID: "worker-1",
	})
	if err == nil {
		t.Fatal("Run() error = nil, want failure")
	}
	if got := store.transitions[len(store.transitions)-1]; got != domain.JobStatePending {
		t.Fatalf("last transition = %q, want pending", got)
	}
}

func TestRunnerCleansStagingAfterPipelineFailure(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	cfg := workerConfig()
	cfg.Daemon.TempDir = tempDir
	cfg.Flows["test-flow"] = config.FlowConfig{Steps: []string{"stage", "fail"}}

	store := newFakeWorkerStore()
	store.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv"}
	var stagedDir string
	runner := Runner{
		Store:             store,
		ConfigProvider:    func() config.Config { return cfg },
		TempDir:           tempDir,
		MaxAttempts:       2,
		VerifyFingerprint: acceptFingerprint,
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(
				staging.StageBlock{Manager: staging.Manager{Root: filepath.Join(tempDir, "staging")}},
				pipeline.BlockFunc{BlockName: "fail", Fn: func(_ context.Context, job *pipeline.JobContext) error {
					stagedDir = job.StagingDir
					if stagedDir == "" {
						t.Fatal("staging dir was empty")
					}
					if err := os.WriteFile(filepath.Join(stagedDir, "partial.mkv"), []byte("partial"), 0o640); err != nil {
						t.Fatalf("write partial output: %v", err)
					}
					return errors.New("encode failed")
				}},
			),
		},
	}

	err := runner.Run(ctx, scheduler.Assignment{
		Job:      domain.Job{ID: 99, SourceID: 1, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID: "worker-1",
	})
	if err == nil {
		t.Fatal("Run() error = nil, want failure")
	}
	if _, statErr := os.Stat(stagedDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("staging dir stat error = %v, want not exist", statErr)
	}
	if got := store.transitions[len(store.transitions)-1]; got != domain.JobStatePending {
		t.Fatalf("last transition = %q, want pending", got)
	}
}

func TestRunnerCapturesProcessOutputLogs(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	ffmpegPath := filepath.Join(tempDir, "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nprintf stdout\nprintf stderr >&2\n"), 0o750); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	cfg := workerConfig()
	cfg.Daemon.TempDir = tempDir
	cfg.Flows["test-flow"] = config.FlowConfig{Steps: []string{"run-ffmpeg"}}
	store := newFakeWorkerStore()
	store.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv"}
	runner := Runner{
		Store:             store,
		ConfigProvider:    func() config.Config { return cfg },
		TempDir:           tempDir,
		VerifyFingerprint: acceptFingerprint,
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(pipeline.BlockFunc{BlockName: "run-ffmpeg", Fn: func(ctx context.Context, _ *pipeline.JobContext) error {
				_, err := process.OSRunner{}.Run(ctx, process.Command{Name: ffmpegPath})
				return err
			}}),
		},
	}

	err := runner.Run(ctx, scheduler.Assignment{
		Job:      domain.Job{ID: 99, SourceID: 1, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID: "worker-1",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	logDir := filepath.Join(tempDir, "process-logs", "job-99", "attempt-1")
	stdoutPath := filepath.Join(logDir, "run-ffmpeg-ffmpeg.stdout.log")
	stderrPath := filepath.Join(logDir, "run-ffmpeg-ffmpeg.stderr.log")
	if got, err := os.ReadFile(stdoutPath); err != nil || string(got) != "stdout" {
		t.Fatalf("stdout log = %q, err = %v", got, err)
	}
	if got, err := os.ReadFile(stderrPath); err != nil || string(got) != "stderr" {
		t.Fatalf("stderr log = %q, err = %v", got, err)
	}
	if !hasAttemptEvent(store.events, "process-output") {
		t.Fatalf("recorded events = %+v, want process-output artifact", store.events)
	}
}

func TestRunnerDoesNotFailJobWhenMetadataResolutionFails(t *testing.T) {
	ctx := context.Background()
	store := newFakeWorkerStore()
	store.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv"}
	runner := Runner{
		Store:             store,
		ConfigProvider:    workerConfig,
		MetadataResolver:  staticMetadataResolver{err: errors.New("arr unavailable")},
		VerifyFingerprint: acceptFingerprint,
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(pipeline.BlockFunc{BlockName: "noop", Fn: func(_ context.Context, job *pipeline.JobContext) error {
				if !job.Metadata.StreamCleanupDisabled {
					t.Fatal("StreamCleanupDisabled = false, want true")
				}
				if job.Metadata.StreamCleanupDisabledReason == "" {
					t.Fatal("StreamCleanupDisabledReason was empty")
				}
				return nil
			}}),
		},
	}

	err := runner.Run(ctx, scheduler.Assignment{
		Job:      domain.Job{ID: 99, SourceID: 1, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID: "worker-1",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if store.attempt.State != domain.AttemptStateSucceeded {
		t.Fatalf("attempt state = %q, want succeeded", store.attempt.State)
	}
}

func TestRunnerRejectsChangedOccurrenceFingerprintBeforeWork(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	inputPath := filepath.Join(root, "Movie.mkv")
	if err := os.WriteFile(inputPath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := workerConfig()
	library := cfg.Libraries["movies"]
	library.Path = root
	cfg.Libraries["movies"] = library
	store := newFakeWorkerStore()
	store.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv", Generation: 1, Current: true, Status: domain.MediaSourceActive, Fingerprint: domain.FileFingerprint{SizeBytes: info.Size() - 1, ModTime: info.ModTime()}}
	store.asset = domain.MediaAsset{ID: 2, SourceID: 1, RelativePath: "Movie.mkv", Generation: 1, Current: true, Status: domain.MediaAssetActive, Fingerprint: store.source.Fingerprint}
	var pipelineRan bool
	runner := Runner{
		Store: store, ConfigProvider: func() config.Config { return cfg }, MaxAttempts: 1,
		Pipeline: pipeline.Runner{Registry: pipeline.NewRegistry(pipeline.BlockFunc{BlockName: "noop", Fn: func(context.Context, *pipeline.JobContext) error {
			pipelineRan = true
			return nil
		}})},
	}
	err = runner.Run(ctx, scheduler.Assignment{Job: domain.Job{ID: 99, SourceID: 1, AssetID: 2, LibraryName: "movies", State: domain.JobStateLeased}, WorkerID: "worker-1"})
	if err == nil || !strings.Contains(err.Error(), "input fingerprint changed") {
		t.Fatalf("Run() error = %v, want fingerprint rejection", err)
	}
	if pipelineRan {
		t.Fatal("pipeline ran after initial fingerprint rejection")
	}
	if got := store.transitions[len(store.transitions)-1]; got != domain.JobStateSkipped {
		t.Fatalf("last transition = %q, want skipped", got)
	}
}

func TestRunnerRechecksFingerprintBeforePublish(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	inputPath := filepath.Join(root, "Movie.mkv")
	if err := os.WriteFile(inputPath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := workerConfig()
	library := cfg.Libraries["movies"]
	library.Path = root
	cfg.Libraries["movies"] = library
	cfg.Flows["test-flow"] = config.FlowConfig{Steps: []string{"mutate", "replace"}}
	fingerprint := domain.FileFingerprint{SizeBytes: info.Size(), ModTime: info.ModTime()}
	store := newFakeWorkerStore()
	store.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv", Generation: 1, Current: true, Status: domain.MediaSourceActive, Fingerprint: fingerprint}
	store.asset = domain.MediaAsset{ID: 2, SourceID: 1, RelativePath: "Movie.mkv", Generation: 1, Current: true, Status: domain.MediaAssetActive, Fingerprint: fingerprint}
	var publishRan bool
	runner := Runner{
		Store: store, ConfigProvider: func() config.Config { return cfg }, MaxAttempts: 1,
		Pipeline: pipeline.Runner{Registry: pipeline.NewRegistry(
			pipeline.BlockFunc{BlockName: "mutate", Fn: func(context.Context, *pipeline.JobContext) error {
				return os.WriteFile(inputPath, []byte("replacement content"), 0o600)
			}},
			pipeline.BlockFunc{BlockName: "replace", Fn: func(context.Context, *pipeline.JobContext) error {
				publishRan = true
				return nil
			}},
		)},
	}
	err = runner.Run(ctx, scheduler.Assignment{Job: domain.Job{ID: 99, SourceID: 1, AssetID: 2, LibraryName: "movies", State: domain.JobStateLeased}, WorkerID: "worker-1"})
	if err == nil || !strings.Contains(err.Error(), "input fingerprint changed") {
		t.Fatalf("Run() error = %v, want pre-publish fingerprint rejection", err)
	}
	if publishRan {
		t.Fatal("publish block ran after fingerprint changed")
	}
	if got := store.transitions[len(store.transitions)-1]; got != domain.JobStateSkipped {
		t.Fatalf("last transition = %q, want skipped", got)
	}
}

func TestRunnerResumesPersistedPipelineContext(t *testing.T) {
	ctx := context.Background()
	cfg := workerConfig()
	cfg.Flows["test-flow"] = config.FlowConfig{Steps: []string{"probe", "crop-detect", "crf-search", "encode"}}

	store := newFakeWorkerStore()
	store.source = domain.MediaSource{
		ID:           1,
		LibraryName:  "movies",
		Kind:         domain.SourceKindFile,
		RelativePath: "Movie.mkv",
		Fingerprint:  domain.FileFingerprint{SizeBytes: 1000, ModTime: testTime()},
	}
	store.asset = domain.MediaAsset{
		ID:           2,
		SourceID:     1,
		RelativePath: "Movie.mkv",
		Fingerprint:  domain.FileFingerprint{SizeBytes: 1000, ModTime: testTime()},
	}

	first := Runner{
		Store:             store,
		ConfigProvider:    func() config.Config { return cfg },
		MaxAttempts:       2,
		VerifyFingerprint: acceptFingerprint,
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(
				pipeline.BlockFunc{BlockName: "probe", Fn: func(_ context.Context, job *pipeline.JobContext) error {
					job.Probe = &domain.ProbeResult{Path: job.InputPath, Streams: []domain.MediaStream{{Type: "video", Codec: "h264", Width: 1920, Height: 1080}}}
					return nil
				}},
				pipeline.BlockFunc{BlockName: "crop-detect", Fn: func(_ context.Context, job *pipeline.JobContext) error {
					job.Crop = &domain.CropResult{Filter: "crop=1920:800:0:140"}
					job.Metadata.CropFilter = job.Crop.Filter
					return nil
				}},
				pipeline.BlockFunc{BlockName: "crf-search", Fn: func(_ context.Context, job *pipeline.JobContext) error {
					job.Search = &domain.SearchResult{CRF: 24, VMAF: 96.2}
					return nil
				}},
				pipeline.BlockFunc{BlockName: "encode", Fn: func(context.Context, *pipeline.JobContext) error {
					return errors.New("interrupted")
				}},
			),
		},
		Now: testTime,
	}

	err := first.Run(ctx, scheduler.Assignment{
		Job:       domain.Job{ID: 99, SourceID: 1, AssetID: 2, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID:  "worker-1",
		Resources: domain.ResourceAllocation{WorkerID: "worker-1", Threads: 4},
	})
	if err == nil {
		t.Fatal("first Run() error = nil, want interrupted failure")
	}
	if !store.hasPipelineContext {
		t.Fatal("pipeline context was not persisted")
	}
	if _, ok := store.pipelineContext.Steps["crf-search"]; !ok {
		t.Fatalf("persisted steps = %+v, want crf-search", store.pipelineContext.Steps)
	}

	store.attempt = domain.Attempt{ID: 2, JobID: 99, Number: 2, WorkerID: "worker-2", State: domain.AttemptStateRunning}
	var encoded bool
	second := Runner{
		Store:             store,
		ConfigProvider:    func() config.Config { return cfg },
		MaxAttempts:       2,
		VerifyFingerprint: acceptFingerprint,
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(
				pipeline.BlockFunc{BlockName: "probe", Fn: func(context.Context, *pipeline.JobContext) error {
					t.Fatal("probe block ran; want persisted context resume")
					return nil
				}},
				pipeline.BlockFunc{BlockName: "crop-detect", Fn: func(context.Context, *pipeline.JobContext) error {
					t.Fatal("crop-detect block ran; want persisted context resume")
					return nil
				}},
				pipeline.BlockFunc{BlockName: "crf-search", Fn: func(context.Context, *pipeline.JobContext) error {
					t.Fatal("crf-search block ran; want persisted context resume")
					return nil
				}},
				pipeline.BlockFunc{BlockName: "encode", Fn: func(_ context.Context, job *pipeline.JobContext) error {
					encoded = true
					if job.Probe == nil || job.Probe.Streams[0].Width != 1920 {
						t.Fatalf("resumed probe = %#v, want persisted probe", job.Probe)
					}
					if got, want := job.Metadata.CropFilter, "crop=1920:800:0:140"; got != want {
						t.Fatalf("resumed crop filter = %q, want %q", got, want)
					}
					if job.Search == nil || job.Search.CRF != 24 {
						t.Fatalf("resumed search = %#v, want CRF 24", job.Search)
					}
					return nil
				}},
			),
		},
		Now: testTime,
	}

	if err := second.Run(ctx, scheduler.Assignment{
		Job:       domain.Job{ID: 99, SourceID: 1, AssetID: 2, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID:  "worker-2",
		Resources: domain.ResourceAllocation{WorkerID: "worker-2", Threads: 4},
	}); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if !encoded {
		t.Fatal("encode block did not run")
	}
}

func TestRunnerRebuildsPipelineContextWhenDolbyVisionToolAvailabilityChanges(t *testing.T) {
	ctx := context.Background()
	cfg := workerConfig()
	cfg.Flows["test-flow"] = config.FlowConfig{Steps: []string{"probe", "crf-search", "encode"}}
	profile := cfg.Profiles[config.DefaultProfileName]
	profile.Video.DolbyVision.Codec = "hevc"
	cfg.Profiles[config.DefaultProfileName] = profile

	store := newFakeWorkerStore()
	store.source = domain.MediaSource{
		ID:           1,
		LibraryName:  "movies",
		Kind:         domain.SourceKindFile,
		RelativePath: "Movie.mkv",
		Fingerprint:  domain.FileFingerprint{SizeBytes: 1000, ModTime: testTime()},
	}
	store.asset = domain.MediaAsset{
		ID:           2,
		SourceID:     1,
		RelativePath: "Movie.mkv",
		Fingerprint:  domain.FileFingerprint{SizeBytes: 1000, ModTime: testTime()},
	}

	var doviToolAvailable bool
	var probeCalls int
	var searchCodecs []string
	interruptEncode := true
	runner := Runner{
		Store:             store,
		ConfigProvider:    func() config.Config { return cfg },
		MaxAttempts:       2,
		VerifyFingerprint: acceptFingerprint,
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(
				probe.Block{
					Prober: countingProber{
						result: domain.ProbeResult{
							Streams: []domain.MediaStream{{
								Type:        "video",
								Codec:       "hevc",
								DolbyVision: &domain.DolbyVisionMetadata{Profile: 8, RPUPresent: true, BLPresent: true},
							}},
						},
						calls: &probeCalls,
					},
					DolbyVisionTool: mutableDolbyVisionTool{available: &doviToolAvailable},
				},
				pipeline.BlockFunc{BlockName: "crf-search", Fn: func(_ context.Context, job *pipeline.JobContext) error {
					video := domain.EffectiveVideoProfile(job.Profile, job.Metadata)
					searchCodecs = append(searchCodecs, video.Codec)
					job.Search = &domain.SearchResult{CRF: 24}
					return nil
				}},
				pipeline.BlockFunc{BlockName: "encode", Fn: func(_ context.Context, job *pipeline.JobContext) error {
					if interruptEncode {
						return errors.New("interrupted")
					}
					if !job.Metadata.HDR.DolbyVisionEncoderSelected {
						t.Fatal("DolbyVisionEncoderSelected = false, want true after dovi_tool became available")
					}
					return nil
				}},
			),
		},
		Now: testTime,
	}

	err := runner.Run(ctx, scheduler.Assignment{
		Job:       domain.Job{ID: 99, SourceID: 1, AssetID: 2, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID:  "worker-1",
		Resources: domain.ResourceAllocation{WorkerID: "worker-1", Threads: 4},
	})
	if err == nil {
		t.Fatal("first Run() error = nil, want interrupted failure")
	}
	if !store.hasPipelineContext {
		t.Fatal("pipeline context was not persisted")
	}

	doviToolAvailable = true
	interruptEncode = false
	store.attempt = domain.Attempt{ID: 2, JobID: 99, Number: 2, WorkerID: "worker-2", State: domain.AttemptStateRunning}
	if err := runner.Run(ctx, scheduler.Assignment{
		Job:       domain.Job{ID: 99, SourceID: 1, AssetID: 2, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID:  "worker-2",
		Resources: domain.ResourceAllocation{WorkerID: "worker-2", Threads: 4},
	}); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if probeCalls != 2 {
		t.Fatalf("probe calls = %d, want 2 after Dolby Vision tool availability changed", probeCalls)
	}
	if len(searchCodecs) != 2 || searchCodecs[0] != "av1" || searchCodecs[1] != "hevc" {
		t.Fatalf("search effective codecs = %v, want [av1 hevc]", searchCodecs)
	}
}

func TestPipelineContextPersistenceSkipsSaveForNonResumableStep(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		step string
	}{
		{name: "replace", step: "replace"},
		{name: "handoff", step: "handoff"},
		{name: "cleanup", step: "cleanup"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeWorkerStore()
			store.savePipelineContextErr = errors.New("save should not be called")
			persistence := &pipelineContextPersistence{store: store, now: testTime}
			job := &pipeline.JobContext{
				Job:     domain.Job{ID: 99},
				Attempt: domain.Attempt{ID: 1},
			}

			if err := persistence.StepSucceeded(ctx, tt.step, job); err != nil {
				t.Fatalf("StepSucceeded() error = %v", err)
			}
			if store.savePipelineContextCalls != 0 {
				t.Fatalf("SaveJobPipelineContext calls = %d, want 0", store.savePipelineContextCalls)
			}
			if _, ok := persistence.current.Steps[tt.step]; !ok {
				t.Fatalf("current steps = %+v, want captured %q", persistence.current.Steps, tt.step)
			}
		})
	}
}

func TestRecoverPendingPublishRunsBeforePipeline(t *testing.T) {
	block := &fakePublishRecoveryBlock{}
	runner := pipeline.Runner{Registry: pipeline.NewRegistry(block)}
	job := &pipeline.JobContext{
		Job:  domain.Job{ID: 99},
		Flow: domain.Flow{Steps: []domain.FlowStep{{Name: "handoff"}}},
	}
	recovered, err := recoverPendingPublish(context.Background(), runner, job)
	if err != nil {
		t.Fatalf("recoverPendingPublish() error = %v", err)
	}
	if !recovered || block.calls != 1 {
		t.Fatalf("recoverPendingPublish() = %t, calls = %d; want true, 1", recovered, block.calls)
	}
	if job.FinalPath != "/imports/recovered.mkv" {
		t.Fatalf("FinalPath = %q, want recovered destination", job.FinalPath)
	}
}

func TestRunnerRecoversPublishBeforeOccurrenceFingerprintCheck(t *testing.T) {
	cfg := workerConfig()
	cfg.Flows["test-flow"] = config.FlowConfig{Steps: []string{"handoff"}}
	state := newFakeWorkerStore()
	state.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv"}
	block := &fakePublishRecoveryBlock{}
	verifyCalls := 0
	runner := Runner{
		Store:          state,
		ConfigProvider: func() config.Config { return cfg },
		VerifyFingerprint: func(string, domain.FileFingerprint) error {
			verifyCalls++
			return errors.New("published source is already gone")
		},
		Pipeline: pipeline.Runner{Registry: pipeline.NewRegistry(block)},
		Now:      testTime,
	}
	err := runner.Run(context.Background(), scheduler.Assignment{
		Job:      domain.Job{ID: 99, SourceID: 1, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID: "worker-1",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if verifyCalls != 0 {
		t.Fatalf("fingerprint verification calls = %d, want 0 before journal recovery", verifyCalls)
	}
	if block.calls != 1 {
		t.Fatalf("recovery calls = %d, want 1", block.calls)
	}
	if state.attempt.State != domain.AttemptStateSucceeded {
		t.Fatalf("attempt state = %q, want succeeded", state.attempt.State)
	}
}

type fakePublishRecoveryBlock struct {
	calls int
}

func (*fakePublishRecoveryBlock) Name() string {
	return "handoff"
}

func (*fakePublishRecoveryBlock) Run(context.Context, *pipeline.JobContext) error {
	return errors.New("pipeline block should not run during recovery")
}

func (b *fakePublishRecoveryBlock) Recover(_ context.Context, job *pipeline.JobContext) (bool, error) {
	b.calls++
	job.FinalPath = "/imports/recovered.mkv"
	return true, nil
}

func TestPipelineContextPersistenceReturnsSaveErrorForResumableStep(t *testing.T) {
	sentinel := errors.New("save failed")
	store := newFakeWorkerStore()
	store.savePipelineContextErr = sentinel
	persistence := &pipelineContextPersistence{store: store, now: testTime}
	job := &pipeline.JobContext{
		Job:     domain.Job{ID: 99},
		Attempt: domain.Attempt{ID: 1},
	}

	err := persistence.StepSucceeded(context.Background(), "probe", job)
	if !errors.Is(err, sentinel) {
		t.Fatalf("StepSucceeded() error = %v, want %v", err, sentinel)
	}
	if store.savePipelineContextCalls != 1 {
		t.Fatalf("SaveJobPipelineContext calls = %d, want 1", store.savePipelineContextCalls)
	}
}

func TestPipelineContextMatchesRequiresCurrentFingerprint(t *testing.T) {
	now := testTime()
	base := domain.JobPipelineContext{
		Version:           domain.JobPipelineContextVersion,
		InputPath:         "/media/Movie.mkv",
		SourceFingerprint: domain.FileFingerprint{SizeBytes: 100, ModTime: now},
		InitialMetadata:   domain.JobMetadata{OriginalLanguage: "eng"},
	}
	cached := base
	if !pipelineContextMatches(base, cached) {
		t.Fatal("pipelineContextMatches() = false, want true for identical context")
	}

	cached.SourceFingerprint.SizeBytes = 200
	if pipelineContextMatches(base, cached) {
		t.Fatal("pipelineContextMatches() = true, want false after source fingerprint change")
	}
}

func workerConfig() config.Config {
	cfg := config.Default()
	cfg.Daemon.LeaseDuration = "1m"
	cfg.Daemon.MaxAttempts = 2
	cfg.Flows = map[string]config.FlowConfig{"test-flow": {Steps: []string{"noop"}}}
	cfg.Libraries = map[string]config.LibraryConfig{"movies": {
		Kind:    "media",
		Path:    "/media/movies",
		Flow:    "test-flow",
		Profile: config.DefaultProfileName,
	}}
	return cfg
}

func testTime() time.Time {
	return time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
}

func acceptFingerprint(string, domain.FileFingerprint) error {
	return nil
}

type staticMetadataResolver struct {
	metadata domain.JobMetadata
	err      error
}

func (s staticMetadataResolver) ResolveJobMetadata(context.Context, domain.Library, domain.MediaSource, domain.MediaAsset, string) (domain.JobMetadata, error) {
	return s.metadata, s.err
}

type fakeWorkerStore struct {
	source                   domain.MediaSource
	asset                    domain.MediaAsset
	attempt                  domain.Attempt
	resolvedLibrary          []byte
	pipelineContext          domain.JobPipelineContext
	hasPipelineContext       bool
	savePipelineContextCalls int
	savePipelineContextErr   error
	recordedInputSize        int64
	recordedOutputSize       int64
	transitions              []domain.JobState
	events                   []domain.AttemptEvent
}

func newFakeWorkerStore() *fakeWorkerStore {
	return &fakeWorkerStore{
		attempt: domain.Attempt{ID: 1, JobID: 99, Number: 1, WorkerID: "worker-1", State: domain.AttemptStateRunning},
	}
}

func (f *fakeWorkerStore) GetMediaSource(_ context.Context, id domain.MediaSourceID) (domain.MediaSource, error) {
	if f.source.ID == id {
		source := f.source
		if source.Generation == 0 {
			source.Generation = 1
			source.Current = true
			source.Status = domain.MediaSourceActive
		}
		return source, nil
	}
	return domain.MediaSource{}, errors.New("source not found")
}

func (f *fakeWorkerStore) GetMediaAsset(_ context.Context, id domain.MediaAssetID) (domain.MediaAsset, error) {
	if f.asset.ID == id {
		asset := f.asset
		if asset.Generation == 0 {
			asset.Generation = 1
			asset.Current = true
			asset.Status = domain.MediaAssetActive
		}
		return asset, nil
	}
	return domain.MediaAsset{}, errors.New("asset not found")
}

func (f *fakeWorkerStore) StartAttempt(_ context.Context, _ domain.JobID, _ string, resolvedLibrary []byte, _ []byte, _ []byte, _ time.Time) (domain.Attempt, error) {
	f.resolvedLibrary = resolvedLibrary
	return f.attempt, nil
}

func (f *fakeWorkerStore) FinishAttempt(_ context.Context, _ domain.AttemptID, state domain.AttemptState, message string, finishedAt time.Time) (domain.Attempt, error) {
	f.attempt.State = state
	f.attempt.Error = message
	f.attempt.FinishedAt = &finishedAt
	return f.attempt, nil
}

func (f *fakeWorkerStore) TransitionJob(_ context.Context, _ domain.JobID, to domain.JobState, _ time.Time, _ string) (domain.Job, error) {
	f.transitions = append(f.transitions, to)
	return domain.Job{State: to}, nil
}

func (f *fakeWorkerStore) CompleteJobOccurrence(_ context.Context, input store.CompleteJobOccurrenceInput) (domain.Job, error) {
	f.recordedInputSize = input.InputSizeBytes
	f.recordedOutputSize = input.OutputSizeBytes
	f.attempt.State = domain.AttemptStateSucceeded
	f.attempt.FinishedAt = &input.CompletedAt
	f.transitions = append(f.transitions, domain.JobStateComplete)
	return domain.Job{ID: input.JobID, State: domain.JobStateComplete, InputSizeBytes: input.InputSizeBytes, OutputSizeBytes: input.OutputSizeBytes}, nil
}

func (f *fakeWorkerStore) HeartbeatJob(_ context.Context, _ domain.JobID, _ string, _ time.Time, _ time.Time) (domain.Job, error) {
	return domain.Job{}, nil
}

func (f *fakeWorkerStore) RecordAttemptEvent(_ context.Context, event domain.AttemptEvent) (domain.AttemptEvent, error) {
	event.ID = domain.AttemptEventID(len(f.events) + 1)
	f.events = append(f.events, event)
	return event, nil
}

func (f *fakeWorkerStore) GetJobPipelineContext(_ context.Context, _ domain.JobID) (domain.JobPipelineContext, bool, error) {
	return f.pipelineContext, f.hasPipelineContext, nil
}

func (f *fakeWorkerStore) SaveJobPipelineContext(_ context.Context, _ domain.JobID, snapshot domain.JobPipelineContext, _ time.Time) error {
	f.savePipelineContextCalls++
	if f.savePipelineContextErr != nil {
		return f.savePipelineContextErr
	}
	f.pipelineContext = snapshot
	f.hasPipelineContext = true
	return nil
}

func hasAttemptEvent(events []domain.AttemptEvent, name string) bool {
	for _, event := range events {
		if event.Name == name {
			return true
		}
	}
	return false
}

type countingProber struct {
	result domain.ProbeResult
	calls  *int
}

func (p countingProber) Probe(_ context.Context, path string) (domain.ProbeResult, error) {
	if p.calls != nil {
		*p.calls = *p.calls + 1
	}
	result := p.result
	result.Path = path
	return result, nil
}

type mutableDolbyVisionTool struct {
	available *bool
}

func (m mutableDolbyVisionTool) Available(context.Context) (bool, string, error) {
	if m.available == nil {
		return false, "", nil
	}
	return *m.available, "", nil
}

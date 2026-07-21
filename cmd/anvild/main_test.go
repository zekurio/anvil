package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
)

func TestParseOptionsKeepsLegacyRunFlags(t *testing.T) {
	opts, err := parseOptions([]string{"--config", "anvil.toml", "--daemon", "--shutdown-policy", "cancel", "--shutdown-timeout", "10s"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandRun {
		t.Fatalf("command = %q, want run", opts.command)
	}
	if opts.configPath != "anvil.toml" {
		t.Fatalf("config path = %q, want anvil.toml", opts.configPath)
	}
	if !opts.daemonMode {
		t.Fatal("daemon mode = false, want true")
	}
	if opts.shutdownPolicy != "cancel" {
		t.Fatalf("shutdown policy = %q, want cancel", opts.shutdownPolicy)
	}
	if opts.shutdownTimeout != "10s" {
		t.Fatalf("shutdown timeout = %q, want 10s", opts.shutdownTimeout)
	}
}

func TestParseOptionsSupportsLegacyCheckConfig(t *testing.T) {
	opts, err := parseOptions([]string{"--config", "anvil.toml", "--check-config"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandCheckConfig {
		t.Fatalf("command = %q, want check-config", opts.command)
	}
}

func TestDaemonWorkerContextOutlivesParentContext(t *testing.T) {
	parentCtx, stopParent := context.WithCancel(context.Background())
	serviceCtx, stopServices, workerCtx, stopWorkers := daemonContexts(parentCtx)
	defer stopServices()
	defer stopWorkers()

	stopParent()
	if err := serviceCtx.Err(); err != context.Canceled {
		t.Fatalf("service context error = %v, want context canceled", err)
	}
	if err := workerCtx.Err(); err != nil {
		t.Fatalf("worker context error = %v, want nil before explicit worker stop", err)
	}

	stopWorkers()
	if err := workerCtx.Err(); err != context.Canceled {
		t.Fatalf("worker context error after stop = %v, want context canceled", err)
	}
}

func TestParseOptionsParsesJobsCommand(t *testing.T) {
	opts, err := parseOptions([]string{"--config", "anvil.toml", "jobs", "--library", "movies", "--state", "pending,failed", "--limit", "5", "--json"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandJobs {
		t.Fatalf("command = %q, want jobs", opts.command)
	}
	if opts.configPath != "anvil.toml" {
		t.Fatalf("config path = %q, want anvil.toml", opts.configPath)
	}
	if opts.libraryName != "movies" {
		t.Fatalf("library = %q, want movies", opts.libraryName)
	}
	if opts.jobLimit != 5 {
		t.Fatalf("limit = %d, want 5", opts.jobLimit)
	}
	if !opts.jsonOutput {
		t.Fatal("json output = false, want true")
	}
	want := []domain.JobState{domain.JobStatePending, domain.JobStateFailed}
	if len(opts.jobStates) != len(want) {
		t.Fatalf("states len = %d, want %d", len(opts.jobStates), len(want))
	}
	for i := range want {
		if opts.jobStates[i] != want[i] {
			t.Fatalf("state[%d] = %q, want %q", i, opts.jobStates[i], want[i])
		}
	}
}

func TestParseOptionsParsesStatsCommand(t *testing.T) {
	opts, err := parseOptions([]string{"stats", "--config", "anvil.toml", "--library", "movies", "--json"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandStats {
		t.Fatalf("command = %q, want stats", opts.command)
	}
	if opts.configPath != "anvil.toml" {
		t.Fatalf("config path = %q, want anvil.toml", opts.configPath)
	}
	if opts.libraryName != "movies" {
		t.Fatalf("library = %q, want movies", opts.libraryName)
	}
	if !opts.jsonOutput {
		t.Fatal("json output = false, want true")
	}
}

func TestParseOptionsParsesInspectCommand(t *testing.T) {
	opts, err := parseOptions([]string{"inspect", "--config", "anvil.toml", "--json", "42"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandInspect {
		t.Fatalf("command = %q, want inspect", opts.command)
	}
	if opts.configPath != "anvil.toml" {
		t.Fatalf("config path = %q, want anvil.toml", opts.configPath)
	}
	if !opts.jsonOutput {
		t.Fatal("json output = false, want true")
	}
	if got, want := opts.jobIDs, []domain.JobID{42}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("job ids = %v, want %v", got, want)
	}
}

func TestParseOptionsAcceptsJobSlug(t *testing.T) {
	opts, err := parseOptions([]string{"inspect", "pretty-pink-panther"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if len(opts.jobRefs) != 1 || opts.jobRefs[0] != "pretty-pink-panther" {
		t.Fatalf("job refs = %v, want pretty-pink-panther", opts.jobRefs)
	}
	if len(opts.jobIDs) != 0 {
		t.Fatalf("job ids = %v, want none for slug", opts.jobIDs)
	}
}

func TestCharmLoggerFormatsHumanReadableLine(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newCharmLogger(&buf, slog.LevelInfo))
	logger.Info("ffmpeg progress", "job", "pretty-pink-panther", "attempt", 2, "speed", "1.4x")
	line := buf.String()
	for _, want := range []string{"INFO", "ffmpeg progress", "job=pretty-pink-panther", "attempt=2", "speed=1.4x"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line %q missing %q", line, want)
		}
	}
}

func TestParseOptionsRejectsInspectWithoutSingleJob(t *testing.T) {
	if _, err := parseOptions([]string{"inspect"}); err == nil {
		t.Fatal("parseOptions() error = nil, want missing inspect target error")
	}
	if _, err := parseOptions([]string{"inspect", "1", "2"}); err == nil {
		t.Fatal("parseOptions() error = nil, want multiple inspect target error")
	}
}

func TestParseOptionsParsesPreflightCommand(t *testing.T) {
	opts, err := parseOptions([]string{"preflight", "--config", "anvil.toml", "--library", "movies", "--limit", "10", "--json"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandPreflight {
		t.Fatalf("command = %q, want preflight", opts.command)
	}
	if opts.configPath != "anvil.toml" {
		t.Fatalf("config path = %q, want anvil.toml", opts.configPath)
	}
	if opts.libraryName != "movies" {
		t.Fatalf("library = %q, want movies", opts.libraryName)
	}
	if opts.preflightLimit != 10 {
		t.Fatalf("preflight limit = %d, want 10", opts.preflightLimit)
	}
	if !opts.jsonOutput {
		t.Fatal("json output = false, want true")
	}
}

func TestParseOptionsRejectsNegativePreflightLimit(t *testing.T) {
	if _, err := parseOptions([]string{"preflight", "--limit", "-1"}); err == nil {
		t.Fatal("parseOptions() error = nil, want negative limit rejection")
	}
}

func TestParseOptionsParsesRetryCommand(t *testing.T) {
	opts, err := parseOptions([]string{"retry", "12", "13"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandRetry {
		t.Fatalf("command = %q, want retry", opts.command)
	}
	if got, want := opts.jobIDs, []domain.JobID{12, 13}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("job ids = %v, want %v", got, want)
	}
}

func TestParseOptionsRejectsRetryWithoutTarget(t *testing.T) {
	if _, err := parseOptions([]string{"retry"}); err == nil {
		t.Fatal("parseOptions() error = nil, want retry target error")
	}
}

func TestParseOptionsParsesCleanupStagingCommand(t *testing.T) {
	opts, err := parseOptions([]string{"cleanup-staging", "--older-than", "24h", "--dry-run"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandCleanup {
		t.Fatalf("command = %q, want cleanup-staging", opts.command)
	}
	if opts.cleanupOlder != "24h" {
		t.Fatalf("cleanup older = %q, want 24h", opts.cleanupOlder)
	}
	if !opts.cleanupDryRun {
		t.Fatal("cleanup dry run = false, want true")
	}
}

func TestWriteInspectReportShowsProcessOutputAndPayloads(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	finished := now.Add(2 * time.Minute)
	report := inspectReport{
		Job: inspectJob{
			ID:           42,
			Slug:         "pretty-pink-panther",
			State:        "failed",
			Library:      "movies",
			AttemptCount: 1,
			UpdatedAt:    now,
			SourcePath:   "Movie.mkv",
			AssetPath:    "Movie.mkv",
			Path:         "Movie.mkv",
			LastError:    "encode failed",
		},
		PipelineContext: &inspectPipelineContext{
			Version: 1,
			Steps: []inspectPipelineStep{
				{Name: "crop-detect", AttemptID: 7, FinishedAt: now, Resumable: true},
				{Name: "crf-search", AttemptID: 7, FinishedAt: now.Add(time.Second), Resumable: true},
				{Name: "encode", AttemptID: 7, FinishedAt: now.Add(2 * time.Second)},
			},
			CropFilter:       "crop=1920:800:0:140",
			SearchCRF:        24,
			SearchVMAF:       96.25,
			EncodeVideoCodec: "libsvtav1",
			EncodeCRF:        24,
		},
		PublishOperation: &inspectPublishOperation{
			Kind:                "handoff",
			Mode:                "move",
			Stage:               "conflict",
			ArtifactPath:        "/tmp/output.mkv",
			DestinationPath:     "/imports/Movie.mkv",
			CleanupSourcePath:   "/downloads/Movie.mkv",
			ArtifactSizeBytes:   1234,
			DigestAlgorithm:     "sha256",
			ConflictDescription: "destination differs",
			UpdatedAt:           now,
		},
		Attempts: []inspectAttempt{
			{
				ID:         7,
				Number:     1,
				State:      "failed",
				WorkerID:   "worker-1",
				StartedAt:  now,
				FinishedAt: &finished,
				Error:      "exit status 1",
				Events: []inspectEvent{
					{
						ID:        10,
						AttemptID: 7,
						CreatedAt: now,
						Type:      "block_started",
						Name:      "probe",
						Message:   "",
						Payload: &inspectPayload{
							Kind:      "json",
							SizeBytes: len(`{"step_index":0}`),
							JSON:      json.RawMessage(`{"step_index":0}`),
						},
					},
					{
						ID:        11,
						AttemptID: 7,
						CreatedAt: now.Add(time.Second),
						Type:      "artifact",
						Name:      processOutputArtifactName,
						Message:   "captured process output for ffmpeg",
						ProcessOutput: &inspectProcessOutput{
							Step:           "encode",
							Command:        []string{"ffmpeg", "-i", "Movie.mkv"},
							ExitCode:       1,
							DurationMillis: 1534,
							StdoutPath:     "/tmp/stdout.log",
							StderrPath:     "/tmp/stderr.log",
							StdoutBytes:    12,
							StderrBytes:    34,
							Error:          "exit status 1",
						},
					},
					{
						ID:        12,
						AttemptID: 7,
						CreatedAt: now.Add(2 * time.Second),
						Type:      "artifact",
						Name:      "raw-output",
						Message:   "captured raw bytes",
						Payload:   decodeInspectPayload([]byte{0xff, 0x00}),
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := writeInspectReport(&buf, report); err != nil {
		t.Fatalf("writeInspectReport() error = %v", err)
	}
	output := buf.String()
	for _, want := range []string{
		"Job pretty-pink-panther (id=42)",
		"Last error: encode failed",
		"Saved context:",
		"Steps: crop-detect*, crf-search*, encode",
		"Crop: crop=1920:800:0:140",
		"Search: CRF 24 VMAF 96.25",
		"Encode plan: codec=libsvtav1 crf=24",
		"Publish operation:",
		"Stage: conflict",
		"Destination: /imports/Movie.mkv",
		"Conflict: destination differs",
		"[10] 2026-06-27T12:00:00Z type=block_started name=probe message=\"\"",
		"payload: {\"step_index\":0}",
		"process output:",
		"command: [\"ffmpeg\",\"-i\",\"Movie.mkv\"]",
		"exit_code: 1",
		"duration: 1.534s (1534ms)",
		"stdout: /tmp/stdout.log (12 bytes)",
		"stderr: /tmp/stderr.log (34 bytes)",
		"payload: base64:/wA=",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("inspect output missing %q:\n%s", want, output)
		}
	}
}

func TestValidateReloadRejectsStoreTempDirAndControlSocketChanges(t *testing.T) {
	current := config.Default()
	next := current
	next.Daemon.StorePath = "/other/anvil.db"
	if err := validateReload(current, next); err == nil {
		t.Fatal("validateReload() error = nil, want store path rejection")
	}

	next = current
	next.Daemon.TempDir = "/other/tmp"
	if err := validateReload(current, next); err == nil {
		t.Fatal("validateReload() error = nil, want temp dir rejection")
	}

	next = current
	next.Daemon.ControlSocket = "/other/anvild.sock"
	if err := validateReload(current, next); err == nil {
		t.Fatal("validateReload() error = nil, want control socket rejection")
	}
}

func TestValidateReloadAllowsRuntimeKnobs(t *testing.T) {
	current := config.Default()
	next := current
	next.Daemon.WorkerCount = current.Daemon.WorkerCount + 1
	next.Daemon.TotalThreads = current.Daemon.TotalThreads + 2
	next.Daemon.ScanInterval = "1m"
	next.Daemon.LogLevel = "debug"
	if err := validateReload(current, next); err != nil {
		t.Fatalf("validateReload() error = %v", err)
	}
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantLevel slog.Level
		wantLabel string
		wantErr   bool
	}{
		{name: "debug", value: "debug", wantLevel: slog.LevelDebug, wantLabel: "debug"},
		{name: "info", value: "info", wantLevel: slog.LevelInfo, wantLabel: "info"},
		{name: "warn", value: "warn", wantLevel: slog.LevelWarn, wantLabel: "warn"},
		{name: "error", value: "error", wantLevel: slog.LevelError, wantLabel: "error"},
		{name: "trim and lower", value: " WARN ", wantLevel: slog.LevelWarn, wantLabel: "warn"},
		{name: "invalid", value: "trace", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, label, err := parseLogLevel(tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatal("parseLogLevel() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLogLevel() error = %v", err)
			}
			if level != tt.wantLevel {
				t.Fatalf("level = %v, want %v", level, tt.wantLevel)
			}
			if label != tt.wantLabel {
				t.Fatalf("label = %q, want %q", label, tt.wantLabel)
			}
		})
	}
}

func TestApplyLogLevelUpdatesLevelVar(t *testing.T) {
	var levelVar slog.LevelVar
	label, err := applyLogLevel(&levelVar, " DEBUG ")
	if err != nil {
		t.Fatalf("applyLogLevel() error = %v", err)
	}
	if label != "debug" {
		t.Fatalf("label = %q, want debug", label)
	}
	if got := levelVar.Level(); got != slog.LevelDebug {
		t.Fatalf("level = %v, want %v", got, slog.LevelDebug)
	}
}

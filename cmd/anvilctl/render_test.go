package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/domain"
)

// TestWriteJobsRendersSelectionsAndMatchSides covers the operator-facing text
// output, including the branches an automated consumer cannot see but a human
// double-checking it will: an unreadable record, and a path that matched
// nothing because it lies outside every library.
func TestWriteJobsRendersSelectionsAndMatchSides(t *testing.T) {
	tests := []struct {
		name     string
		response control.JobListResponse
		want     []string
		absent   []string
	}{
		{
			name: "match sides and a decision",
			response: control.JobListResponse{
				Jobs: []control.JobResponse{{
					Slug: "kind-pink-heron", ID: 7, State: "complete", Library: "anime",
					MatchedOn: []control.PathMatchSide{control.PathMatchAsset, control.PathMatchDestination},
					StreamSelection: []control.StreamSelectionResponse{{
						AttemptID: 41,
						Decision: &domain.StreamSelectionDecision{
							Kind: domain.StreamKindAudio, Rule: domain.StreamSelectionRuleLanguageFilter,
							RequestedLanguages: []string{"orig", "deu"},
							MissingLanguages:   []string{"deu"},
							Streams: []domain.StreamDecision{
								{Index: 0, Codec: "aac", Language: "jpn", Kept: true, Reason: domain.StreamKeptOriginalLanguage},
								{Index: 1, Codec: "aac", Language: "eng", Reason: domain.StreamDroppedLanguage},
							},
						},
					}},
				}},
			},
			want: []string{
				"MATCHED", "asset+destination",
				"missing from source: deu",
				"#0 aac jpn kept (original_language)",
				"#1 aac eng dropped (language_not_requested)",
			},
		},
		{
			name: "no match side means no column",
			response: control.JobListResponse{
				Jobs: []control.JobResponse{{Slug: "kind-pink-heron", ID: 7, State: "pending"}},
			},
			absent: []string{"MATCHED"},
		},
		{
			name: "unreadable decision is reported",
			response: control.JobListResponse{
				Jobs: []control.JobResponse{{
					Slug: "kind-pink-heron", ID: 7,
					StreamSelection: []control.StreamSelectionResponse{{AttemptID: 41, DecisionError: "decode stream selection: boom"}},
				}},
			},
			want: []string{"unreadable: decode stream selection: boom"},
		},
		{
			name:     "path outside every library",
			response: control.JobListResponse{PathOutsideLibraries: true, Jobs: []control.JobResponse{}},
			want:     []string{"path resolves under no configured library root"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := writeJobs(&out, tt.response); err != nil {
				t.Fatalf("writeJobs() error = %v", err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("output missing %q:\n%s", want, out.String())
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(out.String(), absent) {
					t.Fatalf("output unexpectedly contains %q:\n%s", absent, out.String())
				}
			}
		})
	}
}

// TestWriteJobShowRendersProcessOutputAndPayloads keeps the post-mortem view
// readable: the recorded command, its exit code, and where its output went are
// the whole point of asking.
func TestWriteJobShowRendersProcessOutputAndPayloads(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	finished := now.Add(2 * time.Minute)
	report := control.JobShowResponse{
		Job: control.JobDetail{
			ID: 42, Slug: "pretty-pink-panther", State: "failed", Library: "movies",
			AttemptCount: 1, UpdatedAt: now, SourcePath: "Movie.mkv", AssetPath: "Movie.mkv",
			Path: "Movie.mkv", LastError: "encode failed",
		},
		PipelineContext: &control.PipelineContextDetail{
			Version: 1,
			Steps: []control.PipelineStepDetail{
				{Name: "crop-detect", AttemptID: 7, FinishedAt: now, Resumable: true},
				{Name: "crf-search", AttemptID: 7, FinishedAt: now.Add(time.Second), Resumable: true},
				{Name: "encode", AttemptID: 7, FinishedAt: now.Add(2 * time.Second)},
			},
			CropFilter: "crop=1920:800:0:140", SearchCRF: 24, SearchVMAF: 96.25,
			EncodeVideoCodec: "libsvtav1", EncodeCRF: 24,
		},
		PublishOperation: &control.PublishOperationDetail{
			Kind: "handoff", Mode: "move", Stage: "conflict",
			ArtifactPath: "/tmp/output.mkv", DestinationPath: "/imports/Movie.mkv",
			CleanupSourcePath: "/downloads/Movie.mkv", ArtifactSizeBytes: 1234,
			DigestAlgorithm: "sha256", ConflictDescription: "destination differs", UpdatedAt: now,
		},
		Attempts: []control.AttemptDetail{{
			ID: 7, Number: 1, State: "failed", WorkerID: "worker-1",
			StartedAt: now, FinishedAt: &finished, Error: "exit status 1",
			Events: []control.AttemptEventDetail{
				{
					ID: 10, AttemptID: 7, CreatedAt: now, Type: "block_started", Name: "probe",
					Payload: &control.EventPayload{Kind: "json", SizeBytes: 16, JSON: []byte(`{"step_index":0}`)},
				},
				{
					ID: 11, AttemptID: 7, CreatedAt: now.Add(time.Second), Type: "artifact",
					Name: "process-output", Message: "captured process output for ffmpeg",
					ProcessOutput: &control.ProcessOutputDetail{
						Step: "encode", Command: []string{"ffmpeg", "-i", "Movie.mkv"},
						ExitCode: 1, DurationMillis: 1534,
						StdoutPath: "/tmp/stdout.log", StderrPath: "/tmp/stderr.log",
						StdoutBytes: 12, StderrBytes: 34, Error: "exit status 1",
					},
				},
				{
					ID: 12, AttemptID: 7, CreatedAt: now.Add(2 * time.Second), Type: "artifact",
					Name: "raw-output", Payload: &control.EventPayload{Kind: "bytes", SizeBytes: 2, BytesBase64: "/wA="},
				},
			},
		}},
	}

	var out bytes.Buffer
	if err := writeJobShow(&out, report); err != nil {
		t.Fatalf("writeJobShow() error = %v", err)
	}
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
		if !strings.Contains(out.String(), want) {
			t.Fatalf("job show output missing %q:\n%s", want, out.String())
		}
	}
}

// TestWriteProtectedJobsExplainsRefusals keeps maintenance honest: "0 deleted"
// with no explanation is indistinguishable from "nothing to do".
func TestWriteProtectedJobsExplainsRefusals(t *testing.T) {
	var out bytes.Buffer
	err := writePrunedJobs(&out, control.JobPruneResponse{
		DryRun: true, MatchedJobs: 0,
		ByState:       map[string]int64{"complete": 2},
		ProtectedJobs: []control.ProtectedJob{{ID: 9, Slug: "kind-pink-heron", Reason: "unresolved_publish_journal"}},
	})
	if err != nil {
		t.Fatalf("writePrunedJobs() error = %v", err)
	}
	for _, want := range []string{"state_complete=2", "protected job=kind-pink-heron id=9 reason=unresolved_publish_journal"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("prune output missing %q:\n%s", want, out.String())
		}
	}
}

// TestWriteStagingCleanupReportsErrorsAsFailure keeps a partially failed sweep
// from exiting zero, which would let a scheduled cleanup hide a real problem.
func TestWriteStagingCleanupReportsErrorsAsFailure(t *testing.T) {
	var out, errOut bytes.Buffer
	err := writeStagingCleanup(&out, &errOut, control.StagingCleanupResponse{
		Root: "/var/lib/anvil/tmp/staging", OlderThan: "24h0m0s",
		Candidates: 2, Removed: 1, Errors: []string{"/var/lib/anvil/tmp/staging/job-1-attempt-1: permission denied"},
	})
	if err == nil {
		t.Fatal("writeStagingCleanup() error = nil, want the errors reported as failure")
	}
	if !strings.Contains(errOut.String(), "permission denied") {
		t.Fatalf("stderr = %s", errOut.String())
	}
	if !strings.Contains(out.String(), "removed=1") {
		t.Fatalf("stdout = %s", out.String())
	}
}

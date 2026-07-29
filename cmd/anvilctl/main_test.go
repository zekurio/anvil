package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/controlapi"
	"github.com/zekurio/anvil/pkg/domain"
)

func TestRunStatusAndJobListUseControlAPI(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "anvild.sock")
	listener, cleanup, err := controlapi.ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup() error = %v", err)
		}
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, w, controlapi.StatusResponse{
			APIVersion: controlapi.Version,
			Daemon:     controlapi.DaemonStatus{State: "ready", Version: "test"},
		})
	})
	mux.HandleFunc("/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("absolute_path"); got != "/downloads/Release" {
			t.Errorf("absolute_path = %q", got)
		}
		if got := r.URL.Query().Get("current_only"); got != "true" {
			t.Errorf("current_only = %q", got)
		}
		writeTestJSON(t, w, controlapi.JobListResponse{
			APIVersion: controlapi.Version, Matched: 1,
			Jobs: []controlapi.JobResponse{{
				ID: 9, Slug: "kind-pink-heron", State: "running", Library: "downloads",
				Source: controlapi.OccurrenceResponse{AbsolutePath: "/downloads/Release"},
			}},
		})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("server.Close() error = %v", err)
		}
		if err := <-serverDone; err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			t.Errorf("server.Serve() error = %v", err)
		}
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run(context.Background(), []string{"--socket", socketPath, "status", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(status) error = %v, stderr = %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"state": "ready"`) {
		t.Fatalf("status output = %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if err := run(context.Background(), []string{
		"--socket", socketPath, "job", "list", "--absolute-path", "/downloads/Release", "--current-only", "--json",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("run(job list) error = %v, stderr = %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"slug": "kind-pink-heron"`) {
		t.Fatalf("job output = %s", stdout.String())
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := writeJSON(w, value); err != nil {
		t.Errorf("writeJSON() error = %v", err)
	}
}

func TestRunHelpDoesNotRequireDaemon(t *testing.T) {
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"help"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("run(help) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "anvilctl") {
		t.Fatalf("help output = %q", stdout.String())
	}
}

func TestRunJobCancelRequiresSelectorAndPostsSelection(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "anvild.sock")
	listener, cleanup, err := controlapi.ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup() error = %v", err)
		}
	})
	var received controlapi.JobCancelRequest
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/cancel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode cancel request: %v", err)
		}
		writeTestJSON(t, w, controlapi.JobCancelResponse{
			APIVersion: controlapi.Version, Matched: 1, Canceled: 1,
			Jobs: []controlapi.JobCancelResult{{
				ID: 167, Slug: "kind-pink-heron", Library: "downloads",
				PreviousState: "running", State: "canceled", Canceled: true, WorkerSignaled: true,
			}},
		})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("server.Close() error = %v", err)
		}
		if err := <-serverDone; err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			t.Errorf("server.Serve() error = %v", err)
		}
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run(context.Background(), []string{"--socket", socketPath, "job", "cancel"}, &stdout, &stderr); err == nil {
		t.Fatal("run(job cancel) error = nil, want a rejected bare cancel")
	}
	if received.Library != "" || len(received.IDs) != 0 {
		t.Fatalf("bare cancel reached the daemon: %+v", received)
	}

	stdout.Reset()
	if err := run(context.Background(), []string{
		"--socket", socketPath, "job", "cancel", "--library", "downloads", "--state", "pending,running", "167",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("run(job cancel) error = %v, stderr = %s", err, stderr.String())
	}
	if received.Library != "downloads" || len(received.IDs) != 1 || received.IDs[0] != 167 {
		t.Fatalf("cancel request = %+v", received)
	}
	if len(received.States) != 1 || received.States[0] != "pending,running" {
		t.Fatalf("cancel states = %v", received.States)
	}
	if !strings.Contains(stdout.String(), "canceled 1 of 1 matching jobs") {
		t.Fatalf("cancel output = %s", stdout.String())
	}

	stdout.Reset()
	if err := run(context.Background(), []string{"--socket", socketPath, "job", "cancel", "--library", "downloads", "abc"}, &stdout, &stderr); err == nil {
		t.Fatal("run(job cancel with invalid id) error = nil, want failure")
	}
}

// TestWriteJobsRendersSelectionsAndMatchSides covers the operator-facing text
// output, including the branches an automated consumer cannot see but a human
// double-checking it will: an unreadable record, and a path that matched
// nothing because it lies outside every library.
func TestWriteJobsRendersSelectionsAndMatchSides(t *testing.T) {
	tests := []struct {
		name     string
		response controlapi.JobListResponse
		want     []string
		absent   []string
	}{
		{
			name: "match sides and a decision",
			response: controlapi.JobListResponse{
				Jobs: []controlapi.JobResponse{{
					Slug: "kind-pink-heron", ID: 7, State: "complete", Library: "anime",
					MatchedOn: []controlapi.PathMatchSide{controlapi.PathMatchAsset, controlapi.PathMatchDestination},
					StreamSelection: []controlapi.StreamSelectionResponse{{
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
			response: controlapi.JobListResponse{
				Jobs: []controlapi.JobResponse{{Slug: "kind-pink-heron", ID: 7, State: "pending"}},
			},
			absent: []string{"MATCHED"},
		},
		{
			name: "unreadable decision is reported",
			response: controlapi.JobListResponse{
				Jobs: []controlapi.JobResponse{{
					Slug: "kind-pink-heron", ID: 7,
					StreamSelection: []controlapi.StreamSelectionResponse{{AttemptID: 41, DecisionError: "decode stream selection: boom"}},
				}},
			},
			want: []string{"unreadable: decode stream selection: boom"},
		},
		{
			name:     "path outside every library",
			response: controlapi.JobListResponse{PathOutsideLibraries: true, Jobs: []controlapi.JobResponse{}},
			want:     []string{"path resolves under no configured library"},
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

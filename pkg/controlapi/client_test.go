package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestClientUsesUnixSocketAndPreservesJobQuery(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "anvild.sock")
	listener, cleanup, err := ListenUnix(socketPath)
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
		writeJSON(w, http.StatusOK, StatusResponse{APIVersion: Version, Daemon: DaemonStatus{State: "ready"}})
	})
	mux.HandleFunc("/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("absolute_path") != "/downloads/Release" || query.Get("current_only") != "true" || query.Get("state") != "running,failed" {
			t.Errorf("job query = %q", r.URL.RawQuery)
		}
		// The selection flag has to survive the hop, or --with-selection is a
		// silent no-op that no other test would catch.
		if query.Get("with_selection") != "true" {
			t.Errorf("with_selection = %q, want it forwarded", query.Get("with_selection"))
		}
		writeJSON(w, http.StatusOK, JobListResponse{
			APIVersion: Version, Matched: 1,
			Jobs: []JobResponse{{ID: 7, Slug: "kind-pink-heron", State: "running"}},
		})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		if err := server.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("server.Close() error = %v", err)
		}
		if err := <-serverDone; err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			t.Errorf("server.Serve() error = %v", err)
		}
	})

	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Daemon.State != "ready" {
		t.Fatalf("Status() = %+v", status)
	}
	jobs, err := client.ListJobs(context.Background(), JobQuery{
		AbsolutePath: "/downloads/Release", CurrentOnly: true, States: []string{"running,failed"},
		WithSelection: true,
	})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if jobs.Matched != 1 || len(jobs.Jobs) != 1 || jobs.Jobs[0].ID != 7 {
		t.Fatalf("ListJobs() = %+v", jobs)
	}
	if _, err := client.ListJobs(context.Background(), JobQuery{Limit: -1}); err == nil || err.Error() != "limit must be non-negative" {
		t.Fatalf("ListJobs(invalid) error = %v", err)
	}
}

func TestClientSurfacesStructuredAPIErrors(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "anvild.sock")
	listener, cleanup, err := ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup() error = %v", err)
		}
	})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		if err := json.NewEncoder(w).Encode(ErrorResponse{Error: APIError{Code: "invalid_argument", Message: "bad path"}}); err != nil {
			t.Errorf("Encode() error = %v", err)
		}
	})}
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

	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.ListJobs(context.Background(), JobQuery{}); err == nil || err.Error() != "control API invalid_argument: bad path" {
		t.Fatalf("ListJobs() error = %v", err)
	}
}

package webapi

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestServeAnswersAndShutsDown runs the server on a real socket: it answers
// while the context lives and returns nil once it is canceled.
func TestServeAnswersAndShutsDown(t *testing.T) {
	f := newFixture(t, "", nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- f.server.Serve(ctx, listener) }()

	response, err := http.Get("http://" + listener.Addr().String() + "/api/v1/session")
	if err != nil {
		stop()
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Error(closeErr)
	}
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"authenticated":true`) {
		t.Errorf("GET /api/v1/session = %d %s, %v", response.StatusCode, body, err)
	}

	stop()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve = %v, want nil after an orderly shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after its context was canceled")
	}
	if _, err := http.Get("http://" + listener.Addr().String() + "/api/v1/session"); err == nil {
		t.Fatal("the server still answers after shutdown")
	}
}

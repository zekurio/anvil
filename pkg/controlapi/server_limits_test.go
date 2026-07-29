package controlapi

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/control"
)

// TestEncodeResponseRefusesAnOversizedResult keeps the daemon from emitting a
// frame the client is required to reject. Writing one would leave the operator
// with a transport error for a command that ran, and no way to tell that from a
// command that never did.
func TestEncodeResponseRefusesAnOversizedResult(t *testing.T) {
	result, err := json.Marshal(strings.Repeat("x", 4096))
	if err != nil {
		t.Fatalf("marshal oversized result error = %v", err)
	}
	answer := control.Response{Command: control.CommandJobShow, Result: result}

	payload, err := encodeResponseWithin(answer, 1024)
	if err != nil {
		t.Fatalf("encodeResponseWithin() error = %v", err)
	}
	if len(payload) > 1024 {
		t.Fatalf("refusal is %d bytes, want one that fits inside the limit it reports", len(payload))
	}
	var decoded control.Response
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode refusal error = %v", err)
	}
	if decoded.Error == nil || decoded.Error.Code != control.CodeInvalidArgument {
		t.Fatalf("refusal = %+v, want an invalid_argument the operator can act on", decoded)
	}
	if len(decoded.Result) != 0 {
		t.Fatalf("refusal carried a result of %d bytes, want none", len(decoded.Result))
	}
	if !strings.Contains(decoded.Error.Message, "narrow the request") {
		t.Fatalf("message = %q, want a way out", decoded.Error.Message)
	}
	// A result inside the limit is passed through untouched.
	fits, err := encodeResponseWithin(answer, 1<<20)
	if err != nil {
		t.Fatalf("encodeResponseWithin(large limit) error = %v", err)
	}
	var passthrough control.Response
	if err := json.Unmarshal(fits, &passthrough); err != nil {
		t.Fatalf("decode passthrough error = %v", err)
	}
	if passthrough.Error != nil || len(passthrough.Result) != len(result) {
		t.Fatalf("in-limit response error = %v, result bytes = %d, want the result itself", passthrough.Error, len(passthrough.Result))
	}
}

// TestServerDoesNotBlameTheProtocolVersionForAPartialFrame is the difference
// between "upgrade both binaries" and "the connection died". A peer that never
// finished its header revealed no version, so there is nothing to disagree with
// and nothing to answer.
func TestServerDoesNotBlameTheProtocolVersionForAPartialFrame(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	client, socketPath := serveTestControl(t, service)

	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Dial() error = %v", err)
	}
	defer connection.Close() //nolint:errcheck // the test only reads the answer
	if _, err := connection.Write([]byte{'A', 'N'}); err != nil {
		t.Fatalf("write partial header error = %v", err)
	}
	if err := connection.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatalf("CloseWrite() error = %v", err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	frame, err := control.ReadFrame(connection, control.MaxResponseBytes)
	if err == nil {
		t.Fatalf("daemon answered a peer that sent no request: %s", frame.Payload)
	}

	// The daemon is unharmed by it, which is the other half of the guarantee.
	if _, err := client.Status(ctx); err != nil {
		t.Fatalf("Status() after a partial frame error = %v", err)
	}
}

// TestServerBoundsConcurrentCommands keeps socket access from being an
// unbounded way to start daemon work. Peers past the bound wait for a slot
// rather than being refused, so an operator never has a command rejected for
// being unlucky.
func TestServerBoundsConcurrentCommands(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)

	var mu sync.Mutex
	inFlight, peak := 0, 0
	release := make(chan struct{})
	service.ActiveWorkers = func() int {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
		return 0
	}

	socketPath := testSocketPath(t)
	listener, cleanup, err := ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	serveCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- Server{Service: service, MaxConnections: 2}.Serve(serveCtx, listener)
	}()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("Serve() error = %v", err)
		}
		if err := cleanup(); err != nil {
			t.Errorf("cleanup() error = %v", err)
		}
	})

	client, err := control.NewClient(socketPath)
	if err != nil {
		t.Fatalf("control.NewClient() error = %v", err)
	}
	var callers sync.WaitGroup
	for range 6 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			_, _ = client.Status(ctx) //nolint:errcheck // only concurrency is under test
		}()
	}
	// Give the accepted connections time to pile up against the bound before
	// any of them is allowed to finish.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		reached := peak >= 2
		mu.Unlock()
		if reached {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	callers.Wait()

	mu.Lock()
	defer mu.Unlock()
	if peak > 2 {
		t.Fatalf("peak concurrent commands = %d, want at most the configured 2", peak)
	}
	if peak == 0 {
		t.Fatal("no command ran, so the bound was not exercised")
	}
}

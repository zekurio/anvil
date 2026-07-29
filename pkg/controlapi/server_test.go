package controlapi

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/store"
)

// serveTestControl starts a real server on a real Unix socket and returns a
// client for it. Every transport test goes through the socket, because the
// framing, deadlines, and one-command-per-connection rule only exist there.
func serveTestControl(t *testing.T, service Service) (*Client, string) {
	t.Helper()
	socketPath := testSocketPath(t)
	listener, cleanup, err := ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Server{Service: service}.Serve(ctx, listener)
	}()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("Serve() error = %v", err)
		}
		if err := cleanup(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("cleanup() error = %v", err)
		}
	})
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client, socketPath
}

func TestClientAndServerRoundTripAJobQuery(t *testing.T) {
	ctx := context.Background()
	service, state, cfg, job := testService(t, ctx)
	recordStreamSelection(t, ctx, state, job.ID, germanMissingDecision())
	client, _ := serveTestControl(t, service)

	status, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Daemon.State != "ready" || status.APIVersion != Version {
		t.Fatalf("Status() = %+v", status)
	}

	absolutePath := cfg.Libraries["downloads"].Path + "/Release/Season/Episode.mkv"
	response, err := client.ListJobs(ctx, JobQuery{
		AbsolutePath: absolutePath, CurrentOnly: true, States: []string{"pending,running,failed"},
		WithSelection: true,
	})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if response.Matched != 1 || len(response.Jobs) != 1 || response.Jobs[0].ID != int64(job.ID) {
		t.Fatalf("ListJobs() = %+v", response)
	}
	// Every selector has to survive the hop; a dropped one silently widens or
	// narrows what the operator asked about.
	if !containsSide(response.Jobs[0].MatchedOn, PathMatchAsset) {
		t.Fatalf("MatchedOn = %v, want the asset side", response.Jobs[0].MatchedOn)
	}
	if len(response.Jobs[0].StreamSelection) != 1 {
		t.Fatalf("stream selection = %+v, want the flag forwarded", response.Jobs[0].StreamSelection)
	}
}

func containsSide(sides []PathMatchSide, want PathMatchSide) bool {
	for _, side := range sides {
		if side == want {
			return true
		}
	}
	return false
}

func TestServerReturnsStructuredErrors(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	client, _ := serveTestControl(t, service)

	tests := []struct {
		name string
		call func() error
		want ErrorCode
	}{
		{
			name: "invalid argument",
			call: func() error {
				_, err := client.ListJobs(ctx, JobQuery{AbsolutePath: "relative/path"})
				return err
			},
			want: CodeInvalidArgument,
		},
		{
			name: "not found",
			call: func() error {
				_, err := client.ShowJob(ctx, JobShowRequest{Reference: "no-such-job"})
				return err
			},
			want: CodeNotFound,
		},
		{
			name: "unknown library",
			call: func() error {
				_, err := client.ScanLibraries(ctx, LibraryScanRequest{Library: "missing"})
				return err
			},
			want: CodeNotFound,
		},
		{
			name: "cancel without a selector",
			call: func() error {
				_, err := client.CancelJobs(ctx, JobCancelRequest{})
				return err
			},
			want: CodeInvalidArgument,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			var controlErr *Error
			if !errors.As(err, &controlErr) {
				t.Fatalf("error = %v, want a structured control error", err)
			}
			if controlErr.Code != tt.want {
				t.Fatalf("code = %q, want %q (%v)", controlErr.Code, tt.want, err)
			}
		})
	}
}

// TestServerAnswersAProtocolVersionMismatch is the whole reason the version is
// on the wire: two independently packaged binaries must say so instead of
// failing to parse each other.
func TestServerAnswersAProtocolVersionMismatch(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	_, socketPath := serveTestControl(t, service)

	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Dial() error = %v", err)
	}
	defer connection.Close() //nolint:errcheck // the test only reads the answer

	payload := []byte(`{"command":"status"}`)
	frame := make([]byte, frameHeaderSize, frameHeaderSize+len(payload))
	copy(frame, frameMagic[:])
	binary.BigEndian.PutUint16(frame[4:6], ProtocolVersion+1)
	binary.BigEndian.PutUint32(frame[6:frameHeaderSize], uint32(len(payload)))
	if _, err := connection.Write(append(frame, payload...)); err != nil {
		t.Fatalf("write frame error = %v", err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	_, body, err := readFrame(connection, maxResponseBytes)
	if err != nil {
		t.Fatalf("readFrame() error = %v", err)
	}
	var answer response
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("decode response error = %v", err)
	}
	if answer.Error == nil || answer.Error.Code != CodeVersionMismatch {
		t.Fatalf("response = %+v, want a version mismatch", answer)
	}
	if !strings.Contains(answer.Error.Message, "upgraded together") {
		t.Fatalf("message = %q, want an upgrade instruction", answer.Error.Message)
	}
}

func TestServerRejectsUnsupportedCommandsAndOversizedRequests(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	_, socketPath := serveTestControl(t, service)

	tests := []struct {
		name    string
		payload []byte
		length  uint32
		want    ErrorCode
	}{
		{name: "unsupported command", payload: []byte(`{"command":"drop.everything"}`), want: CodeUnsupported},
		{name: "unknown envelope field", payload: []byte(`{"command":"status","extra":1}`), want: CodeInvalidArgument},
		{name: "oversized frame", payload: []byte(`{"command":"status"}`), length: maxRequestBytes + 1, want: CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connection, err := net.Dial("unix", socketPath)
			if err != nil {
				t.Fatalf("net.Dial() error = %v", err)
			}
			defer connection.Close() //nolint:errcheck // the test only reads the answer
			length := tt.length
			if length == 0 {
				length = uint32(len(tt.payload))
			}
			frame := make([]byte, frameHeaderSize, frameHeaderSize+len(tt.payload))
			copy(frame, frameMagic[:])
			binary.BigEndian.PutUint16(frame[4:6], ProtocolVersion)
			binary.BigEndian.PutUint32(frame[6:frameHeaderSize], length)
			if _, err := connection.Write(append(frame, tt.payload...)); err != nil {
				t.Fatalf("write frame error = %v", err)
			}
			if err := connection.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatalf("SetReadDeadline() error = %v", err)
			}
			_, body, err := readFrame(connection, maxResponseBytes)
			if err != nil {
				t.Fatalf("readFrame() error = %v", err)
			}
			var answer response
			if err := json.Unmarshal(body, &answer); err != nil {
				t.Fatalf("decode response error = %v", err)
			}
			if answer.Error == nil || answer.Error.Code != tt.want {
				t.Fatalf("response = %+v, want %q", answer, tt.want)
			}
		})
	}
}

// TestServerIgnoresAConnectionThatSendsNothing keeps a liveness probe from
// being logged or answered as a protocol error.
func TestServerIgnoresAConnectionThatSendsNothing(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	client, socketPath := serveTestControl(t, service)

	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Dial() error = %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("connection.Close() error = %v", err)
	}
	if _, err := client.Status(ctx); err != nil {
		t.Fatalf("Status() after a bare connect error = %v", err)
	}
}

func TestClientReportsAnUnreachableDaemon(t *testing.T) {
	client, err := NewClient(testSocketPath(t))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.Status(context.Background())
	var controlErr *Error
	if !errors.As(err, &controlErr) || controlErr.Code != CodeUnavailable {
		t.Fatalf("Status() error = %v, want unavailable", err)
	}
	if !strings.Contains(controlErr.Message, "is anvild running?") {
		t.Fatalf("message = %q, want a running-daemon hint", controlErr.Message)
	}
}

func TestClientRejectsARelativeSocketPath(t *testing.T) {
	if _, err := NewClient("anvild.sock"); err == nil {
		t.Fatal("NewClient() error = nil, want an absolute-path requirement")
	}
}

// TestClientValidatesLocallyBeforeDialing keeps an obviously wrong request from
// being answered differently depending on whether the daemon happens to be up.
func TestClientValidatesLocallyBeforeDialing(t *testing.T) {
	client, err := NewClient(testSocketPath(t))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.ListJobs(context.Background(), JobQuery{Limit: -1}); err == nil ||
		!strings.Contains(err.Error(), "limit must be non-negative") {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if _, err := client.CancelJobs(context.Background(), JobCancelRequest{}); err == nil ||
		!strings.Contains(err.Error(), "at least one selector") {
		t.Fatalf("CancelJobs() error = %v", err)
	}
}

// TestCancelOverTheSocketSignalsTheWorker covers the full operator path for the
// one command that stops running work, including the worker signal that kills
// ffmpeg and its children.
func TestCancelOverTheSocketSignalsTheWorker(t *testing.T) {
	ctx := context.Background()
	service, _, _, job := testService(t, ctx)
	signaled := make(chan domain.JobID, 1)
	service.CancelRunningJob = func(jobID domain.JobID) bool {
		signaled <- jobID
		return true
	}
	client, _ := serveTestControl(t, service)

	response, err := client.CancelJobs(ctx, JobCancelRequest{
		Library: "downloads", References: []string{job.Label()}, Reason: "queued by mistake",
	})
	if err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	if response.Canceled != 1 || !response.Jobs[0].WorkerSignaled {
		t.Fatalf("CancelJobs() = %+v", response)
	}
	select {
	case id := <-signaled:
		if id != job.ID {
			t.Fatalf("signaled job = %d, want %d", id, job.ID)
		}
	default:
		t.Fatal("no worker was signaled")
	}
	if response.Jobs[0].State != string(domain.JobStateCanceled) {
		t.Fatalf("state = %q, want canceled", response.Jobs[0].State)
	}
}

// TestCancelBySlugNarrowsTheSameWayAnIDDoes pins that slugs and ids are
// interchangeable and that a reference outside the selector is refused rather
// than quietly ignored.
func TestCancelBySlugNarrowsTheSameWayAnIDDoes(t *testing.T) {
	ctx := context.Background()
	service, _, _, job := testService(t, ctx)
	client, _ := serveTestControl(t, service)

	_, err := client.CancelJobs(ctx, JobCancelRequest{Library: "other", References: []string{job.Label()}})
	var controlErr *Error
	if !errors.As(err, &controlErr) || controlErr.Code != CodeInvalidArgument {
		t.Fatalf("CancelJobs() error = %v, want invalid_argument", err)
	}
	if _, err := client.CancelJobs(ctx, JobCancelRequest{References: []string{"missing-job-slug"}}); err == nil {
		t.Fatal("CancelJobs() for an unknown slug error = nil, want not found")
	}
	listed, err := client.ListJobs(ctx, JobQuery{Library: "downloads"})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if listed.Jobs[0].State != string(domain.JobStatePending) {
		t.Fatalf("refused cancel changed job state to %q", listed.Jobs[0].State)
	}
}

// storeWithoutProtection lets a test drive the maintenance paths that must fail
// closed when the daemon cannot tell which jobs are protected.
type storeWithoutProtection struct {
	Store
}

func (storeWithoutProtection) ProtectedJobs(context.Context) ([]store.ProtectedJob, error) {
	return nil, errors.New("protected jobs are unavailable")
}

func TestStagingCleanupFailsWhenProtectionIsUnknown(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)
	service.Store = storeWithoutProtection{Store: service.Store}
	client, _ := serveTestControl(t, service)

	if _, err := client.CleanupStaging(ctx, StagingCleanupRequest{OlderThan: "1h"}); err == nil {
		t.Fatal("CleanupStaging() error = nil, want a refusal while protection is unknown")
	}
}

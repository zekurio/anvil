package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dialTimeout bounds the connect itself. A daemon that is up answers instantly
// over a Unix socket; a longer wait only delays a clear "not running" message.
const dialTimeout = 5 * time.Second

// emptyRequest is the payload type of commands that take no arguments. It still
// goes through the normal encode and decode path so a client that sends
// arguments to a no-argument command is told instead of ignored.
type emptyRequest struct{}

// Client speaks the private control protocol over the daemon's Unix socket. It
// opens one connection per command, so a slow or abandoned command can never
// wedge later ones.
type Client struct {
	socketPath string
	// Timeout overrides the per-command default deadline. Zero uses the
	// command's own timeout, which is what operators want by default.
	Timeout time.Duration
}

func NewClient(socketPath string) (*Client, error) {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		return nil, errors.New("control socket path is required")
	}
	if !filepath.IsAbs(socketPath) {
		return nil, fmt.Errorf("control socket path %q must be absolute", socketPath)
	}
	return &Client{socketPath: socketPath}, nil
}

// SocketPath reports the socket this client talks to, so callers can name it in
// their own diagnostics without tracking it separately.
func (c *Client) SocketPath() string {
	if c == nil {
		return ""
	}
	return c.socketPath
}

func (c *Client) Status(ctx context.Context) (StatusResponse, error) {
	return call[emptyRequest, StatusResponse](ctx, c, CommandStatus, emptyRequest{})
}

func (c *Client) ListJobs(ctx context.Context, query JobQuery) (JobListResponse, error) {
	if err := query.validate(); err != nil {
		return JobListResponse{}, err
	}
	return call[JobQuery, JobListResponse](ctx, c, CommandJobList, query)
}

func (c *Client) ShowJob(ctx context.Context, request JobShowRequest) (JobShowResponse, error) {
	return call[JobShowRequest, JobShowResponse](ctx, c, CommandJobShow, request)
}

// CancelJobs requires an explicit selector; the daemon rejects an empty one so
// a mistyped command can never cancel the whole queue. The client checks too,
// so the mistake never leaves the terminal.
func (c *Client) CancelJobs(ctx context.Context, request JobCancelRequest) (JobCancelResponse, error) {
	if !request.HasSelector() {
		return JobCancelResponse{}, NewError(CodeInvalidArgument, "cancel requires at least one selector")
	}
	if err := request.Query().validate(); err != nil {
		return JobCancelResponse{}, err
	}
	return call[JobCancelRequest, JobCancelResponse](ctx, c, CommandJobCancel, request)
}

func (c *Client) RetryJobs(ctx context.Context, request JobRetryRequest) (JobRetryResponse, error) {
	return call[JobRetryRequest, JobRetryResponse](ctx, c, CommandJobRetry, request)
}

func (c *Client) PruneJobs(ctx context.Context, request JobPruneRequest) (JobPruneResponse, error) {
	return call[JobPruneRequest, JobPruneResponse](ctx, c, CommandJobPrune, request)
}

func (c *Client) RecoverJobs(ctx context.Context) (JobRecoverResponse, error) {
	return call[emptyRequest, JobRecoverResponse](ctx, c, CommandJobRecover, emptyRequest{})
}

func (c *Client) ScanLibraries(ctx context.Context, request LibraryScanRequest) (LibraryScanResponse, error) {
	return call[LibraryScanRequest, LibraryScanResponse](ctx, c, CommandLibraryScan, request)
}

func (c *Client) LibraryStats(ctx context.Context, request LibraryStatsRequest) (LibraryStatsResponse, error) {
	return call[LibraryStatsRequest, LibraryStatsResponse](ctx, c, CommandLibraryStats, request)
}

func (c *Client) ForceOccurrence(ctx context.Context, request ForceOccurrenceRequest) (ForceOccurrenceResponse, error) {
	return call[ForceOccurrenceRequest, ForceOccurrenceResponse](ctx, c, CommandOccurrenceForce, request)
}

func (c *Client) CleanupStaging(ctx context.Context, request StagingCleanupRequest) (StagingCleanupResponse, error) {
	return call[StagingCleanupRequest, StagingCleanupResponse](ctx, c, CommandStagingCleanup, request)
}

func (c *Client) BackupStore(ctx context.Context, request StoreBackupRequest) (StoreBackupResponse, error) {
	return call[StoreBackupRequest, StoreBackupResponse](ctx, c, CommandStoreBackup, request)
}

func call[Req any, Res any](ctx context.Context, c *Client, command Command, payload Req) (Res, error) {
	var result Res
	if c == nil || c.socketPath == "" {
		return result, NewError(CodeInvalidArgument, "control client is not configured")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return result, NewError(CodeInvalidArgument, "encode %s request: %v", command, err)
	}
	body, err := json.Marshal(Request{Command: command, Payload: encoded})
	if err != nil {
		return result, NewError(CodeInvalidArgument, "encode %s envelope: %v", command, err)
	}
	// The daemon rejects an oversized request frame, so saying so here keeps the
	// answer identical whether or not the daemon happens to be reachable.
	if len(body) > MaxRequestBytes {
		return result, NewError(CodeInvalidArgument, "the %s request is %d bytes, which exceeds the %d byte control request limit; narrow it", command, len(body), MaxRequestBytes)
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = command.Timeout()
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	raw, err := c.roundTrip(commandCtx, body)
	if err != nil {
		return result, err
	}
	var answer Response
	if err := json.Unmarshal(raw, &answer); err != nil {
		return result, NewError(CodeInternal, "decode %s response: %v", command, err)
	}
	if answer.Error != nil {
		return result, answer.Error
	}
	if len(answer.Result) == 0 {
		return result, NewError(CodeInternal, "daemon returned no result for %s", command)
	}
	if err := json.Unmarshal(answer.Result, &result); err != nil {
		return result, NewError(CodeInternal, "decode %s result: %v", command, err)
	}
	return result, nil
}

func (c *Client) roundTrip(ctx context.Context, body []byte) (payload []byte, err error) {
	dialer := net.Dialer{Timeout: dialTimeout}
	connection, err := dialer.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return nil, c.dialError(err)
	}
	defer func() {
		if closeErr := connection.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) && err == nil {
			err = NewError(CodeInternal, "close control connection: %v", closeErr)
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return nil, NewError(CodeInternal, "set control deadline: %v", err)
		}
	}
	// Cancellation has to reach a blocked read, and closing the connection is
	// the only thing that does. Reading the daemon's answer is abandoned; the
	// command itself keeps running on the daemon, which is why the caller is
	// told the outcome is unknown rather than that nothing happened.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close() //nolint:errcheck // the deferred close reports the real result
		case <-done:
		}
	}()

	if err := WriteFrame(connection, body); err != nil {
		return nil, c.transportError(ctx, err)
	}
	frame, err := ReadFrame(connection, MaxResponseBytes)
	// A version mismatch is only reported once a complete header revealed the
	// peer's version. A stalled or truncated header reveals nothing, and
	// blaming it on the protocol version would send an operator upgrading
	// binaries over a network problem.
	if frame.HeaderComplete && frame.Version != ProtocolVersion {
		return nil, versionMismatchError(frame.Version)
	}
	if err != nil {
		return nil, c.transportError(ctx, err)
	}
	return frame.Payload, nil
}

func (c *Client) dialError(err error) *Error {
	if errors.Is(err, os.ErrNotExist) {
		return NewError(CodeUnavailable, "control socket %s does not exist; is anvild running?", c.socketPath)
	}
	if errors.Is(err, os.ErrPermission) {
		return NewError(CodeUnavailable, "control socket %s is not accessible; the daemon grants access by group, so this user must be in anvild's group", c.socketPath)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return NewError(CodeDeadlineExceeded, "connecting to control socket %s timed out", c.socketPath)
	}
	return NewError(CodeUnavailable, "connect to control socket %s: %v", c.socketPath, err)
}

// transportError classifies a failure that happened while talking to the
// daemon. A structured error the daemon or the framing already classified is
// returned unchanged: wrapping "this response is too large" as "the socket
// misbehaved" would send an operator looking at the wrong thing.
func (c *Client) transportError(ctx context.Context, err error) *Error {
	var controlErr *Error
	if errors.As(err, &controlErr) {
		return controlErr
	}
	switch {
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return NewError(CodeDeadlineExceeded, "the daemon did not answer within the command deadline; the command may still be running")
	case ctx.Err() != nil:
		return NewError(CodeUnavailable, "control command canceled; the command may still be running on the daemon")
	case isTimeout(err):
		// The socket deadline and the command deadline are the same instant, so
		// which one fires first is a race. Both mean the daemon did not answer
		// in time, and neither means it stopped working.
		return NewError(CodeDeadlineExceeded, "the daemon did not answer within the command deadline; the command may still be running")
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return NewError(CodeUnavailable, "the daemon closed the control connection before answering")
	default:
		return NewError(CodeUnavailable, "control socket %s: %v", c.socketPath, err)
	}
}

// isTimeout reports a read or write that hit a socket deadline, in both the
// os.ErrDeadlineExceeded and the net.Error spellings.
func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func versionMismatchError(peer uint16) *Error {
	return NewError(CodeVersionMismatch,
		"the daemon speaks control protocol version %d and this client speaks version %d; upgrade anvild and anvilctl together",
		peer, ProtocolVersion)
}

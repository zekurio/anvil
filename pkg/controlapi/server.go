package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/store"
)

// readRequestTimeout bounds how long a connected peer may take to send its one
// request frame. It is separate from the command deadline: a client that
// connects and stalls must not hold a daemon goroutine for a scan's worth of
// time.
const readRequestTimeout = 10 * time.Second

// writeResponseTimeout bounds a peer that stops reading its own answer.
const writeResponseTimeout = 30 * time.Second

// DefaultMaxConnections bounds how many control commands the daemon serves at
// once. Local peers are cheap, but a scan or a prune is not, and an unbounded
// accept loop lets anyone with socket access start as many of them as they can
// open connections. Excess peers wait for a slot rather than being refused,
// because they are already bounded by readRequestTimeout.
const DefaultMaxConnections = 16

// emptyRequest is the payload type of commands that take no arguments. It still
// goes through the normal decode path so a client that sends arguments to a
// no-argument command is told instead of ignored.
type emptyRequest struct{}

type handlerFunc func(context.Context, Service, json.RawMessage) (any, error)

// handle adapts a typed service method to the dispatch table. The request type
// is decoded, not switched on, so adding a command cannot forget its validation
// and cannot silently accept fields it does not implement.
func handle[Req any, Res any](fn func(context.Context, Service, Req) (Res, error)) handlerFunc {
	return func(ctx context.Context, service Service, payload json.RawMessage) (any, error) {
		var request Req
		if err := control.DecodePayload(payload, &request); err != nil {
			return nil, err
		}
		return fn(ctx, service, request)
	}
}

var commands = map[control.Command]handlerFunc{
	control.CommandStatus: handle(func(ctx context.Context, s Service, _ emptyRequest) (control.StatusResponse, error) {
		return s.Status(ctx)
	}),
	control.CommandJobList: handle(func(ctx context.Context, s Service, request control.JobQuery) (control.JobListResponse, error) {
		return s.ListJobs(ctx, request)
	}),
	control.CommandJobShow: handle(func(ctx context.Context, s Service, request control.JobShowRequest) (control.JobShowResponse, error) {
		return s.ShowJob(ctx, request)
	}),
	control.CommandJobCancel: handle(func(ctx context.Context, s Service, request control.JobCancelRequest) (control.JobCancelResponse, error) {
		return s.CancelJobs(ctx, request)
	}),
	control.CommandJobRetry: handle(func(ctx context.Context, s Service, request control.JobRetryRequest) (control.JobRetryResponse, error) {
		return s.RetryJobs(ctx, request)
	}),
	control.CommandJobPrune: handle(func(ctx context.Context, s Service, request control.JobPruneRequest) (control.JobPruneResponse, error) {
		return s.PruneJobs(ctx, request)
	}),
	control.CommandJobRecover: handle(func(ctx context.Context, s Service, _ emptyRequest) (control.JobRecoverResponse, error) {
		return s.RecoverJobs(ctx)
	}),
	control.CommandLibraryScan: handle(func(ctx context.Context, s Service, request control.LibraryScanRequest) (control.LibraryScanResponse, error) {
		return s.ScanLibraries(ctx, request)
	}),
	control.CommandLibraryStats: handle(func(ctx context.Context, s Service, request control.LibraryStatsRequest) (control.LibraryStatsResponse, error) {
		return s.LibraryStats(ctx, request)
	}),
	control.CommandOccurrenceForce: handle(func(ctx context.Context, s Service, request control.ForceOccurrenceRequest) (control.ForceOccurrenceResponse, error) {
		return s.ForceOccurrence(ctx, request)
	}),
	control.CommandStagingCleanup: handle(func(ctx context.Context, s Service, request control.StagingCleanupRequest) (control.StagingCleanupResponse, error) {
		return s.CleanupStaging(ctx, request)
	}),
	control.CommandStoreBackup: handle(func(ctx context.Context, s Service, request control.StoreBackupRequest) (control.StoreBackupResponse, error) {
		return s.BackupStore(ctx, request)
	}),
}

// Server answers control commands on an already-listening Unix socket. The
// listener is created separately so the daemon can claim the socket — and fail
// a second daemon — before it performs any start-up side effect.
type Server struct {
	Service Service
	// MaxConnections bounds concurrently served commands. Zero uses
	// DefaultMaxConnections; a negative value disables the bound, which is only
	// useful in tests.
	MaxConnections int
}

// Serve accepts connections until ctx is canceled or the listener fails. It
// returns nil for an orderly shutdown so a canceled daemon does not report a
// control-plane failure.
func (s Server) Serve(ctx context.Context, listener net.Listener) error {
	var connections sync.WaitGroup
	closed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Closing the listener is the only way to unblock Accept. The
			// error it produces is expected and filtered below.
			_ = listener.Close() //nolint:errcheck // cleanup owns the socket file
		case <-closed:
		}
	}()
	defer close(closed)
	defer connections.Wait()

	slots := s.connectionSlots()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return fmt.Errorf("accept control connection: %w", err)
		}
		if slots != nil {
			slots <- struct{}{}
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			if slots != nil {
				defer func() { <-slots }()
			}
			s.handleConnection(ctx, connection)
		}()
	}
}

func (s Server) connectionSlots() chan struct{} {
	limit := s.MaxConnections
	switch {
	case limit < 0:
		return nil
	case limit == 0:
		limit = DefaultMaxConnections
	}
	return make(chan struct{}, limit)
}

func (s Server) handleConnection(ctx context.Context, connection net.Conn) {
	defer func() {
		if err := connection.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Debug("close control connection", "error", err)
		}
	}()

	if err := connection.SetReadDeadline(time.Now().Add(readRequestTimeout)); err != nil {
		slog.Debug("set control read deadline", "error", err)
		return
	}
	frame, readErr := control.ReadFrame(connection, control.MaxRequestBytes)
	switch {
	case errors.Is(readErr, control.ErrMalformedFrame):
		s.reply(connection, control.Response{Error: control.NewError(control.CodeInvalidArgument, "%s", control.ErrMalformedFrame.Error())})
		return
	case readErr != nil && !frame.HeaderComplete:
		// Nothing usable arrived: a liveness probe that connects and hangs up,
		// a peer that stalled, or a connection that died. There is no peer
		// version to disagree with and no request to answer, and claiming a
		// protocol mismatch here would send an operator upgrading binaries
		// over a network problem.
		if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			slog.Debug("control peer sent no request frame", "error", readErr)
		}
		return
	case frame.Version != control.ProtocolVersion:
		s.replyVersionMismatch(connection, frame.Version)
		return
	case readErr != nil:
		s.reply(connection, control.Response{Error: controlError(readErr)})
		return
	}

	var decoded control.Request
	if err := control.DecodePayload(frame.Payload, &decoded); err != nil {
		s.reply(connection, control.Response{Error: controlError(err)})
		return
	}
	s.reply(connection, s.dispatch(ctx, decoded))
}

func (s Server) dispatch(ctx context.Context, decoded control.Request) control.Response {
	handler, ok := commands[decoded.Command]
	if !ok {
		return control.Response{Command: decoded.Command, Error: control.NewError(control.CodeUnsupported, "unsupported command %q", decoded.Command)}
	}
	commandCtx, cancel := context.WithTimeout(ctx, decoded.Command.Timeout())
	defer cancel()

	result, err := handler(commandCtx, s.Service, decoded.Payload)
	if err != nil {
		return control.Response{Command: decoded.Command, Error: controlError(err)}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return control.Response{Command: decoded.Command, Error: control.NewError(control.CodeInternal, "encode %s result: %v", decoded.Command, err)}
	}
	return control.Response{Command: decoded.Command, Result: encoded}
}

func (s Server) replyVersionMismatch(connection net.Conn, peer uint16) {
	s.reply(connection, control.Response{Error: control.NewError(control.CodeVersionMismatch,
		"control protocol version %d is not supported; this daemon speaks version %d, so anvild and anvilctl must be upgraded together",
		peer, control.ProtocolVersion)})
}

func (s Server) reply(connection net.Conn, answer control.Response) {
	payload, err := encodeResponse(answer)
	if err != nil {
		slog.Error("encode control error response", "error", err)
		return
	}
	if err := connection.SetWriteDeadline(time.Now().Add(writeResponseTimeout)); err != nil {
		slog.Debug("set control write deadline", "error", err)
		return
	}
	if err := control.WriteFrame(connection, payload); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.Debug("write control response", "error", err)
	}
}

// encodeResponse serializes a reply and refuses to emit one the client is
// required to reject. Writing an oversized frame would leave the operator with
// a transport error for a command that actually succeeded, and no way to tell
// the difference; telling them the result is too large, and how to narrow it,
// is both true and actionable.
func encodeResponse(answer control.Response) ([]byte, error) {
	return encodeResponseWithin(answer, control.MaxResponseBytes)
}

func encodeResponseWithin(answer control.Response, limit int) ([]byte, error) {
	payload, err := json.Marshal(answer)
	if err != nil {
		return json.Marshal(control.Response{Command: answer.Command, Error: control.NewError(control.CodeInternal, "encode control response: %v", err)})
	}
	if len(payload) <= limit {
		return payload, nil
	}
	slog.Warn("control result exceeds the response limit", "command", answer.Command, "size_bytes", len(payload), "limit_bytes", limit)
	return json.Marshal(control.Response{Command: answer.Command, Error: control.NewError(control.CodeInvalidArgument,
		"the %s result is %d bytes, which exceeds the %d byte control response limit; narrow the request, for example with --limit or a more specific selector",
		answer.Command, len(payload), limit)})
}

// controlError maps a service failure onto the stable code set. Expected
// conditions get their own code so a client can act on them; everything else is
// internal, and its message is still returned because the operator running
// anvilctl is the same person who reads the daemon log.
func controlError(err error) *control.Error {
	var controlErr *control.Error
	if errors.As(err, &controlErr) {
		return controlErr
	}
	switch {
	case errors.Is(err, store.ErrNotFound) || errors.Is(err, os.ErrNotExist):
		return control.NewError(control.CodeNotFound, "%s", err.Error())
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded):
		return control.NewError(control.CodeDeadlineExceeded, "%s", err.Error())
	case errors.Is(err, context.Canceled):
		return control.NewError(control.CodeUnavailable, "daemon canceled the command: %s", err.Error())
	default:
		return control.NewError(control.CodeInternal, "%s", err.Error())
	}
}

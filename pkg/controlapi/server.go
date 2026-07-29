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

	"github.com/zekurio/anvil/pkg/store"
)

// readRequestTimeout bounds how long a connected peer may take to send its one
// request frame. It is separate from the command deadline: a client that
// connects and stalls must not hold a daemon goroutine for a scan's worth of
// time.
const readRequestTimeout = 10 * time.Second

// writeResponseTimeout bounds a peer that stops reading its own answer.
const writeResponseTimeout = 30 * time.Second

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
		if err := decodePayload(payload, &request); err != nil {
			return nil, err
		}
		return fn(ctx, service, request)
	}
}

var commands = map[Command]handlerFunc{
	CommandStatus: handle(func(ctx context.Context, s Service, _ emptyRequest) (StatusResponse, error) {
		return s.Status(ctx)
	}),
	CommandJobList: handle(func(ctx context.Context, s Service, request JobQuery) (JobListResponse, error) {
		return s.ListJobs(ctx, request)
	}),
	CommandJobShow: handle(func(ctx context.Context, s Service, request JobShowRequest) (JobShowResponse, error) {
		return s.ShowJob(ctx, request)
	}),
	CommandJobCancel: handle(func(ctx context.Context, s Service, request JobCancelRequest) (JobCancelResponse, error) {
		return s.CancelJobs(ctx, request)
	}),
	CommandJobRetry: handle(func(ctx context.Context, s Service, request JobRetryRequest) (JobRetryResponse, error) {
		return s.RetryJobs(ctx, request)
	}),
	CommandJobPrune: handle(func(ctx context.Context, s Service, request JobPruneRequest) (JobPruneResponse, error) {
		return s.PruneJobs(ctx, request)
	}),
	CommandJobRecover: handle(func(ctx context.Context, s Service, _ emptyRequest) (JobRecoverResponse, error) {
		return s.RecoverJobs(ctx)
	}),
	CommandLibraryScan: handle(func(ctx context.Context, s Service, request LibraryScanRequest) (LibraryScanResponse, error) {
		return s.ScanLibraries(ctx, request)
	}),
	CommandLibraryStats: handle(func(ctx context.Context, s Service, request LibraryStatsRequest) (LibraryStatsResponse, error) {
		return s.LibraryStats(ctx, request)
	}),
	CommandOccurrenceForce: handle(func(ctx context.Context, s Service, request ForceOccurrenceRequest) (ForceOccurrenceResponse, error) {
		return s.ForceOccurrence(ctx, request)
	}),
	CommandStagingCleanup: handle(func(ctx context.Context, s Service, request StagingCleanupRequest) (StagingCleanupResponse, error) {
		return s.CleanupStaging(ctx, request)
	}),
	CommandStoreBackup: handle(func(ctx context.Context, s Service, request StoreBackupRequest) (StoreBackupResponse, error) {
		return s.BackupStore(ctx, request)
	}),
}

// Server answers control commands on an already-listening Unix socket. The
// listener is created separately so the daemon can claim the socket — and fail
// a second daemon — before it performs any start-up side effect.
type Server struct {
	Service Service
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
		connections.Add(1)
		go func() {
			defer connections.Done()
			s.handleConnection(ctx, connection)
		}()
	}
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
	version, payload, readErr := readFrame(connection, maxRequestBytes)
	switch {
	case errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF):
		// A liveness probe that connects and disconnects is not an error.
		return
	case errors.Is(readErr, errMalformedFrame):
		s.reply(connection, response{Error: newError(CodeInvalidArgument, "%s", errMalformedFrame.Error())})
		return
	case readErr != nil && version != ProtocolVersion:
		s.replyVersionMismatch(connection, version)
		return
	case readErr != nil:
		s.reply(connection, response{Error: controlError(readErr)})
		return
	}
	if version != ProtocolVersion {
		s.replyVersionMismatch(connection, version)
		return
	}

	var decoded request
	if err := decodePayload(payload, &decoded); err != nil {
		s.reply(connection, response{Error: controlError(err)})
		return
	}
	s.reply(connection, s.dispatch(ctx, decoded))
}

func (s Server) dispatch(ctx context.Context, decoded request) response {
	handler, ok := commands[decoded.Command]
	if !ok {
		return response{Command: decoded.Command, Error: newError(CodeUnsupported, "unsupported command %q", decoded.Command)}
	}
	commandCtx, cancel := context.WithTimeout(ctx, decoded.Command.Timeout())
	defer cancel()

	result, err := handler(commandCtx, s.Service, decoded.Payload)
	if err != nil {
		return response{Command: decoded.Command, Error: controlError(err)}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return response{Command: decoded.Command, Error: newError(CodeInternal, "encode %s result: %v", decoded.Command, err)}
	}
	return response{Command: decoded.Command, Result: encoded}
}

func (s Server) replyVersionMismatch(connection net.Conn, peer uint16) {
	s.reply(connection, response{Error: newError(CodeVersionMismatch,
		"control protocol version %d is not supported; this daemon speaks version %d, so anvild and anvilctl must be upgraded together",
		peer, ProtocolVersion)})
}

func (s Server) reply(connection net.Conn, answer response) {
	payload, err := json.Marshal(answer)
	if err != nil {
		payload, err = json.Marshal(response{Command: answer.Command, Error: newError(CodeInternal, "encode control response: %v", err)})
		if err != nil {
			slog.Error("encode control error response", "error", err)
			return
		}
	}
	if err := connection.SetWriteDeadline(time.Now().Add(writeResponseTimeout)); err != nil {
		slog.Debug("set control write deadline", "error", err)
		return
	}
	if err := writeFrame(connection, payload); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.Debug("write control response", "error", err)
	}
}

// controlError maps a service failure onto the stable code set. Expected
// conditions get their own code so a client can act on them; everything else is
// internal, and its message is still returned because the operator running
// anvilctl is the same person who reads the daemon log.
func controlError(err error) *Error {
	var controlErr *Error
	if errors.As(err, &controlErr) {
		return controlErr
	}
	switch {
	case errors.Is(err, store.ErrNotFound) || errors.Is(err, os.ErrNotExist):
		return newError(CodeNotFound, "%s", err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return newError(CodeDeadlineExceeded, "%s", err.Error())
	case errors.Is(err, context.Canceled):
		return newError(CodeUnavailable, "daemon canceled the command: %s", err.Error())
	default:
		return newError(CodeInternal, "%s", err.Error())
	}
}

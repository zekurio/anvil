package controlapi

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// The control protocol is private: it exists only so anvilctl and anvild can
// talk over daemon.control_socket. The stable contract operators depend on is
// the anvilctl command surface, its human and --json output, its error codes,
// and its exit status. Anything here may change whenever both binaries change
// together; ProtocolVersion exists so independently packaged binaries say so
// instead of misreading each other.
//
// Wire format, one frame:
//
//	magic    4 bytes   "ANVL"
//	version  2 bytes   big-endian protocol version
//	length   4 bytes   big-endian payload length
//	payload  length bytes of JSON
//
// One command per connection: the client writes exactly one request frame, the
// daemon writes exactly one response frame, and the connection is closed. That
// keeps every request independently bounded and cancellable, and removes any
// need for stream state, request ids, or pipelining rules.
const ProtocolVersion uint16 = 1

const (
	frameHeaderSize = 10
	// maxRequestBytes bounds what an unauthenticated-by-anything-but-file-mode
	// peer can make the daemon allocate.
	maxRequestBytes = 1 << 20
	// maxResponseBytes bounds the client side. Job detail carries every attempt
	// event of a job, so it is far larger than any request.
	maxResponseBytes = 64 << 20
)

var frameMagic = [4]byte{'A', 'N', 'V', 'L'}

// errMalformedFrame reports a peer that is not speaking this protocol at all,
// as opposed to one speaking an incompatible version of it.
var errMalformedFrame = errors.New("control socket peer did not send an Anvil control frame")

// Command names one daemon operation. Names are noun.verb so the client command
// tree and the protocol stay recognizably the same surface.
type Command string

const (
	CommandStatus          Command = "status"
	CommandJobList         Command = "job.list"
	CommandJobShow         Command = "job.show"
	CommandJobCancel       Command = "job.cancel"
	CommandJobRetry        Command = "job.retry"
	CommandJobPrune        Command = "job.prune"
	CommandJobRecover      Command = "job.recover"
	CommandLibraryScan     Command = "library.scan"
	CommandLibraryStats    Command = "library.stats"
	CommandOccurrenceForce Command = "occurrence.force"
	CommandStagingCleanup  Command = "staging.cleanup"
	CommandStoreBackup     Command = "store.backup"
)

// Timeout is the deadline both sides apply to a command by default. It is
// shared so a client never gives up on work the daemon is still allowed to
// finish, which would leave the operator unsure whether it ran.
func (c Command) Timeout() time.Duration {
	switch c {
	case CommandLibraryScan, CommandOccurrenceForce, CommandStoreBackup, CommandStagingCleanup, CommandJobPrune:
		return 10 * time.Minute
	default:
		return 30 * time.Second
	}
}

// ErrorCode is the stable machine-readable half of a control error. Clients map
// codes to exit status; they never match on message text.
type ErrorCode string

const (
	// CodeInvalidArgument is a caller-fixable request problem.
	CodeInvalidArgument ErrorCode = "invalid_argument"
	// CodeNotFound is a well-formed request naming something that does not exist.
	CodeNotFound ErrorCode = "not_found"
	// CodeUnsupported is a command this daemon does not implement.
	CodeUnsupported ErrorCode = "unsupported_command"
	// CodeUnavailable means the daemon could not be reached at all.
	CodeUnavailable ErrorCode = "unavailable"
	// CodeDeadlineExceeded means the command outlived its deadline. It never
	// implies the daemon stopped doing the work.
	CodeDeadlineExceeded ErrorCode = "deadline_exceeded"
	// CodeVersionMismatch means the two binaries speak different protocol
	// versions and must be upgraded together.
	CodeVersionMismatch ErrorCode = "protocol_version_mismatch"
	// CodeInternal is a daemon-side failure.
	CodeInternal ErrorCode = "internal"
)

// Error is the structured error both sides exchange. It is an error value so
// callers can use errors.As instead of parsing strings.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

func newError(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// request is the decoded request envelope. Payload stays raw so each command
// decodes its own typed request with unknown-field rejection.
type request struct {
	Command Command         `json:"command"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// response is the reply envelope. Exactly one of Result and Error is set.
type response struct {
	Command Command         `json:"command,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

func writeFrame(w io.Writer, payload []byte) error {
	if len(payload) > int(^uint32(0)) {
		return fmt.Errorf("control frame payload of %d bytes is too large", len(payload))
	}
	header := make([]byte, frameHeaderSize, frameHeaderSize+len(payload))
	copy(header, frameMagic[:])
	binary.BigEndian.PutUint16(header[4:6], ProtocolVersion)
	binary.BigEndian.PutUint32(header[6:frameHeaderSize], uint32(len(payload)))
	if _, err := w.Write(append(header, payload...)); err != nil {
		return fmt.Errorf("write control frame: %w", err)
	}
	return nil
}

// readFrame reads one frame and reports the peer's protocol version even when
// the payload is rejected, so the caller can answer a mismatch precisely.
func readFrame(r io.Reader, limit int) (uint16, []byte, error) {
	header := make([]byte, frameHeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	if !bytes.Equal(header[:4], frameMagic[:]) {
		return 0, nil, errMalformedFrame
	}
	version := binary.BigEndian.Uint16(header[4:6])
	length := binary.BigEndian.Uint32(header[6:frameHeaderSize])
	if int64(length) > int64(limit) {
		return version, nil, newError(CodeInvalidArgument, "control frame of %d bytes exceeds the %d byte limit", length, limit)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return version, nil, err
	}
	return version, payload, nil
}

// decodePayload decodes a typed request body. Unknown fields and trailing data
// are rejected so a client asking for something this daemon does not understand
// is told, rather than silently getting a narrower operation than it requested.
func decodePayload(payload []byte, target any) error {
	if len(payload) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return newError(CodeInvalidArgument, "decode request: %v", err)
	}
	if decoder.More() {
		return newError(CodeInvalidArgument, "request payload must be exactly one JSON object")
	}
	return nil
}

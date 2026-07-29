// Package control is the wire contract between anvild and anvilctl: the frame
// format, the command names, the request and response types, the stable error
// codes, and the client that speaks them.
//
// It deliberately depends on nothing but the standard library and pkg/domain.
// anvilctl links this package and nothing else of Anvil's daemon internals, so
// the operator client cannot grow a SQLite driver, a scanner, or a staging
// manager by accident — two processes writing Anvil's database while jobs are
// running is how half-published files happen. The daemon-side service and
// server live in pkg/controlapi, which imports this package.
package control

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	// FrameHeaderSize is the fixed magic+version+length prefix of every frame.
	FrameHeaderSize = 10
	// MaxRequestBytes bounds what an unauthenticated-by-anything-but-file-mode
	// peer can make the daemon allocate.
	MaxRequestBytes = 1 << 20
	// MaxResponseBytes bounds the client side. Job detail carries every attempt
	// event of a job, so it is far larger than any request. The daemon applies
	// the same bound before writing, so it never emits a frame the client is
	// required to reject.
	MaxResponseBytes = 64 << 20
)

// The length field is 32 bits, so no frame can ever describe a longer payload.
// Both limits are asserted at compile time on every architecture, including the
// 32-bit ones where an int cannot hold the whole range.
const (
	_ = uint32(MaxRequestBytes)
	_ = uint32(MaxResponseBytes)
)

var frameMagic = [4]byte{'A', 'N', 'V', 'L'}

// ErrMalformedFrame reports a peer that is not speaking this protocol at all,
// as opposed to one speaking an incompatible version of it.
var ErrMalformedFrame = errors.New("control socket peer did not send an Anvil control frame")

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

// NewError builds a structured control error. Both sides use it so a code is
// decided once, where the condition is detected, rather than by string matching
// at the edge.
func NewError(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Request is the request envelope. Payload stays raw so each command decodes
// its own typed request with unknown-field rejection.
type Request struct {
	Command Command         `json:"command"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Response is the reply envelope. Exactly one of Result and Error is set.
type Response struct {
	Command Command         `json:"command,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// WriteFrame writes one frame. The length field is 32 bits wide, so the
// comparison is done in 64-bit arithmetic rather than through int, which is
// narrower than uint32 on 32-bit architectures.
func WriteFrame(w io.Writer, payload []byte) error {
	if uint64(len(payload)) > uint64(math.MaxUint32) {
		return fmt.Errorf("control frame payload of %d bytes is too large", len(payload))
	}
	header := make([]byte, FrameHeaderSize, FrameHeaderSize+len(payload))
	copy(header, frameMagic[:])
	binary.BigEndian.PutUint16(header[4:6], ProtocolVersion)
	binary.BigEndian.PutUint32(header[6:FrameHeaderSize], uint32(len(payload)))
	if _, err := w.Write(append(header, payload...)); err != nil {
		return fmt.Errorf("write control frame: %w", err)
	}
	return nil
}

// Frame is one read frame together with what the reader learned about the peer
// before it failed.
type Frame struct {
	// HeaderComplete reports that a full, well-formed header was read. Version
	// is only meaningful when it is true: a peer that stalled or hung up
	// mid-header revealed no version, and answering it with a version mismatch
	// would blame the wrong thing.
	HeaderComplete bool
	Version        uint16
	Payload        []byte
}

// ReadFrame reads one frame. It reports the peer's protocol version even when
// the payload is rejected, so the caller can answer a mismatch precisely, and
// reports whether that version was ever actually observed.
func ReadFrame(r io.Reader, limit int) (Frame, error) {
	header := make([]byte, FrameHeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return Frame{}, err
	}
	if !bytes.Equal(header[:4], frameMagic[:]) {
		return Frame{}, ErrMalformedFrame
	}
	frame := Frame{
		HeaderComplete: true,
		Version:        binary.BigEndian.Uint16(header[4:6]),
	}
	length := binary.BigEndian.Uint32(header[6:FrameHeaderSize])
	if limit < 0 {
		limit = 0
	}
	if uint64(length) > uint64(limit) {
		return frame, NewError(CodeInvalidArgument, "control frame of %d bytes exceeds the %d byte limit", length, limit)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return frame, err
	}
	frame.Payload = payload
	return frame, nil
}

// DecodePayload decodes a typed request body. Unknown fields and trailing data
// are rejected so a client asking for something this daemon does not understand
// is told, rather than silently getting a narrower operation than it requested.
func DecodePayload(payload []byte, target any) error {
	if len(payload) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return NewError(CodeInvalidArgument, "decode request: %v", err)
	}
	if decoder.More() {
		return NewError(CodeInvalidArgument, "request payload must be exactly one JSON object")
	}
	return nil
}

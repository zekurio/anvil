package controlapi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestFrameRoundTripPreservesThePayload(t *testing.T) {
	var buffer bytes.Buffer
	payload := []byte(`{"command":"status"}`)
	if err := writeFrame(&buffer, payload); err != nil {
		t.Fatalf("writeFrame() error = %v", err)
	}
	version, decoded, err := readFrame(&buffer, maxRequestBytes)
	if err != nil {
		t.Fatalf("readFrame() error = %v", err)
	}
	if version != ProtocolVersion {
		t.Fatalf("version = %d, want %d", version, ProtocolVersion)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("payload = %q, want %q", decoded, payload)
	}
	if buffer.Len() != 0 {
		t.Fatalf("%d bytes left after one frame, want the frame to be exactly bounded", buffer.Len())
	}
}

// TestReadFrameReportsAPeerVersionEvenWhenTheFrameIsRejected is what makes a
// version mismatch diagnosable: an old client's frame may be unreadable, and
// the only useful thing to say back is which version it spoke.
func TestReadFrameReportsAPeerVersionEvenWhenTheFrameIsRejected(t *testing.T) {
	frame := make([]byte, frameHeaderSize)
	copy(frame, frameMagic[:])
	binary.BigEndian.PutUint16(frame[4:6], 99)
	binary.BigEndian.PutUint32(frame[6:frameHeaderSize], maxRequestBytes+1)

	version, payload, err := readFrame(bytes.NewReader(frame), maxRequestBytes)
	if version != 99 {
		t.Fatalf("version = %d, want the peer's 99", version)
	}
	if payload != nil {
		t.Fatalf("payload = %q, want none", payload)
	}
	var controlErr *Error
	if !errors.As(err, &controlErr) || controlErr.Code != CodeInvalidArgument {
		t.Fatalf("error = %v, want invalid_argument", err)
	}
}

func TestReadFrameRejectsForeignTraffic(t *testing.T) {
	_, _, err := readFrame(strings.NewReader("GET /v1/status HTTP/1.1\r\n\r\n"), maxRequestBytes)
	if !errors.Is(err, errMalformedFrame) {
		t.Fatalf("readFrame() error = %v, want a malformed-frame refusal", err)
	}
}

func TestReadFrameRejectsATruncatedPayload(t *testing.T) {
	var buffer bytes.Buffer
	if err := writeFrame(&buffer, []byte(`{"command":"status"}`)); err != nil {
		t.Fatalf("writeFrame() error = %v", err)
	}
	truncated := buffer.Bytes()[:buffer.Len()-3]
	if _, _, err := readFrame(bytes.NewReader(truncated), maxRequestBytes); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("readFrame() error = %v, want unexpected EOF", err)
	}
}

func TestDecodePayloadRejectsUnknownFieldsAndTrailingData(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "unknown field", payload: `{"all":true}`},
		{name: "trailing object", payload: `{"library":"a"}{"library":"b"}`},
		{name: "not an object", payload: `["library"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var request JobCancelRequest
			err := decodePayload([]byte(tt.payload), &request)
			var controlErr *Error
			if !errors.As(err, &controlErr) || controlErr.Code != CodeInvalidArgument {
				t.Fatalf("decodePayload() error = %v, want invalid_argument", err)
			}
		})
	}
}

// TestCommandTimeoutsCoverLongRunningWork keeps a scan or a backup from being
// abandoned by a deadline meant for a status query.
func TestCommandTimeoutsCoverLongRunningWork(t *testing.T) {
	if got := CommandStatus.Timeout(); got != 30*time.Second {
		t.Fatalf("status timeout = %s", got)
	}
	for _, command := range []Command{CommandLibraryScan, CommandStoreBackup, CommandStagingCleanup, CommandJobPrune, CommandOccurrenceForce} {
		if got := command.Timeout(); got < 10*time.Minute {
			t.Fatalf("%s timeout = %s, want room for real work", command, got)
		}
	}
}

// TestEveryCommandHasAHandler keeps the named command set and the dispatch table
// from drifting apart, which would surface as an unsupported_command error for a
// command the client knows how to send.
func TestEveryCommandHasAHandler(t *testing.T) {
	for _, command := range []Command{
		CommandStatus, CommandJobList, CommandJobShow, CommandJobCancel, CommandJobRetry,
		CommandJobPrune, CommandJobRecover, CommandLibraryScan, CommandLibraryStats,
		CommandOccurrenceForce, CommandStagingCleanup, CommandStoreBackup,
	} {
		if _, ok := commands[command]; !ok {
			t.Fatalf("command %q has no handler", command)
		}
	}
	if len(commands) != 12 {
		t.Fatalf("handler count = %d, want every command listed in this test", len(commands))
	}
}

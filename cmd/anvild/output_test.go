package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/scanner"
)

type failingWriter struct {
	err error
}

func (w failingWriter) Write(_ []byte) (int, error) {
	return 0, w.err
}

func TestWriteOutputPropagatesWriterErrors(t *testing.T) {
	want := errors.New("closed")
	err := writeScanResult(failingWriter{err: want}, scanner.ScanResult{})
	if !errors.Is(err, want) {
		t.Fatalf("writeScanResult() error = %v, want %v", err, want)
	}
	if !strings.Contains(err.Error(), "write output") {
		t.Fatalf("writeScanResult() error = %q, want output context", err)
	}
}

func TestWriteTablePropagatesFlushErrors(t *testing.T) {
	want := errors.New("closed")
	err := writeJobs(failingWriter{err: want}, nil)
	if !errors.Is(err, want) {
		t.Fatalf("writeJobs() error = %v, want %v", err, want)
	}
	if !strings.Contains(err.Error(), "flush table") {
		t.Fatalf("writeJobs() error = %q, want flush context", err)
	}
}

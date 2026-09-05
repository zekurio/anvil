package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCaptureTailAndCounts(t *testing.T) {
	w := captureWriter{}
	input := bytes.Repeat([]byte("abcdefgh"), TailCaptureLimit)
	for start := 0; start < len(input); start += 113 {
		if _, err := w.Write(input[start:min(start+113, len(input))]); err != nil {
			t.Fatal(err)
		}
	}
	if w.total != int64(len(input)) || !bytes.Equal(w.data, input[len(input)-TailCaptureLimit:]) {
		t.Fatalf("capture length %d, total %d", len(w.data), w.total)
	}
}

func TestFullCaptureAndOverflow(t *testing.T) {
	w := captureWriter{full: true}
	input := bytes.Repeat([]byte("x"), FullCaptureLimit+1)
	if _, err := w.Write(input); err != nil {
		t.Fatal(err)
	}
	if !w.overflow || len(w.data) != FullCaptureLimit || w.total != int64(len(input)) {
		t.Fatal("full capture must report overflow and count all bytes")
	}
}

type cancelLogger struct {
	cancel    context.CancelFunc
	closed    bool
	streamErr error
}

func (l *cancelLogger) LogProcessOutput(_ context.Context, _ Command, _ string, _ []byte) error {
	l.cancel()
	return l.streamErr
}
func (l *cancelLogger) LogProcess(_ context.Context, _ Command, _ Result, _ error) error {
	l.closed = true
	return nil
}

func TestLiveStreamCancellationAndFailure(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sinkErr := errors.New("disk full")
	logger := &cancelLogger{cancel: cancel, streamErr: sinkErr}
	result, err := (OSRunner{}).Run(WithLogger(ctx, logger), Command{Name: executable, Args: []string{"-test.run=^TestProcessHelper$"}, Env: []string{"ANVIL_PROCESS_HELPER=wait"}})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, sinkErr) || !errors.Is(err, ErrOutputLog) || !logger.closed || result.StdoutBytes == 0 {
		t.Fatalf("result %+v, err %v, closed %v", result, err, logger.closed)
	}
}

func TestStructuredCapture(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result, err := (OSRunner{}).Run(context.Background(), Command{Name: executable, Args: []string{"-test.run=^TestProcessHelper$"}, Env: []string{"ANVIL_PROCESS_HELPER=json"}, RequireFullStdout: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(result.Stdout, []byte(`{"value":"`)) || !bytes.HasSuffix(result.Stdout, []byte(`"}`)) || len(result.Stdout) <= TailCaptureLimit {
		t.Fatal("structured output was truncated")
	}
}

func TestProcessHelper(t *testing.T) {
	switch os.Getenv("ANVIL_PROCESS_HELPER") {
	case "wait":
		if _, err := fmt.Fprint(os.Stdout, "ready\n"); err != nil {
			os.Exit(2)
		}
		// Keep this process alive until the live callback cancels it.
		time.Sleep(time.Minute)
	case "overflow":
		chunk := strings.Repeat("x", TailCaptureLimit)
		for i := 0; i <= FullCaptureLimit/TailCaptureLimit; i++ {
			if _, err := fmt.Fprint(os.Stdout, chunk); err != nil {
				os.Exit(2)
			}
		}
	case "json":
		if _, err := fmt.Fprintf(os.Stdout, `{"value":"%s"}`, strings.Repeat("x", 2*TailCaptureLimit)); err != nil {
			os.Exit(2)
		}
	default:
		return
	}
	os.Exit(0)
}

func TestRunnerFullCaptureOverflow(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result, err := (OSRunner{}).Run(context.Background(), Command{Name: executable, Args: []string{"-test.run=^TestProcessHelper$"}, Env: []string{"ANVIL_PROCESS_HELPER=overflow"}, RequireFullStdout: true})
	if !errors.Is(err, ErrOutputCapture) || !strings.Contains(err.Error(), "required process output exceeds") {
		t.Fatalf("missing overflow error: %v", err)
	}
	if len(result.Stdout) != FullCaptureLimit || result.StdoutBytes != FullCaptureLimit+TailCaptureLimit {
		t.Fatalf("size %d, count %d", len(result.Stdout), result.StdoutBytes)
	}
}

type failingLogger struct {
	startErr error
	closeErr error
}

func (l failingLogger) StartProcess(context.Context, Command) (Logger, error)    { return l, l.startErr }
func (l failingLogger) LogProcess(context.Context, Command, Result, error) error { return l.closeErr }

func TestLogLifecycleErrors(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("log failure")
	for _, phase := range []string{"open", "close"} {
		t.Run(phase, func(t *testing.T) {
			logger := failingLogger{}
			if phase == "open" {
				logger.startErr = failure
			} else {
				logger.closeErr = failure
			}
			_, err := (OSRunner{}).Run(WithLogger(context.Background(), logger), Command{Name: executable, Args: []string{"-test.run=^TestProcessHelper$"}, Env: []string{"ANVIL_PROCESS_HELPER=json"}})
			if !errors.Is(err, ErrOutputLog) || !errors.Is(err, failure) {
				t.Fatalf("unclassified log error: %v", err)
			}
		})
	}
}

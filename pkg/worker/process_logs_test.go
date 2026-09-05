package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/process"
)

type artifactStore struct{ events []domain.AttemptEvent }

func (s *artifactStore) RecordAttemptEvent(_ context.Context, event domain.AttemptEvent) (domain.AttemptEvent, error) {
	s.events = append(s.events, event)
	return event, nil
}

func TestProcessLogsLiveAndClosed(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "cancel"}[failed], func(t *testing.T) {
			events := &artifactStore{}
			recorder := &processLogRecorder{root: t.TempDir(), jobID: 1, attempt: 1, attemptID: 1, events: events}
			ctx := process.WithStep(context.Background(), "encode")
			command := process.Command{Name: "ffmpeg"}
			logger, err := recorder.StartProcess(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			session := logger.(*processLogSession)
			output := strings.Repeat("x", 2*process.TailCaptureLimit)
			if err := session.LogProcessOutput(ctx, command, "stderr", []byte(output)); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(session.stderr.Name())
			if err != nil || string(data) != output {
				t.Fatalf("output missing before close: %v", err)
			}
			if len(session.progress.stderr) > maxProgressLine {
				t.Fatal("partial line unbounded")
			}
			var runErr error
			if failed {
				runErr = context.Canceled
			}
			if err := session.LogProcess(ctx, command, process.Result{StderrBytes: int64(len(output))}, runErr); err != nil {
				t.Fatal(err)
			}
			if _, err := session.stderr.Write([]byte("closed")); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("file still open: %v", err)
			}
			if _, err := session.stdout.Write([]byte("closed")); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("file still open: %v", err)
			}
			var artifact processLogArtifact
			if len(events.events) != 1 {
				t.Fatal("missing artifact")
			}
			if err := json.Unmarshal(events.events[0].Payload, &artifact); err != nil {
				t.Fatal(err)
			}
			if artifact.StderrBytes != int64(len(output)) || artifact.StderrPath != session.stderr.Name() {
				t.Fatalf("wrong artifact: %+v", artifact)
			}
		})
	}
}

func TestProcessLogOpenFailure(t *testing.T) {
	root := t.TempDir() + "/file"
	if err := os.WriteFile(root, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &processLogRecorder{root: root}
	if _, err := recorder.StartProcess(context.Background(), process.Command{Name: "ffmpeg"}); err == nil {
		t.Fatal("expected open error")
	}
}

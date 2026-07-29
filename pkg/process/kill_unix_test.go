//go:build unix

package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestOSRunnerCancelKillsGrandchildrenHoldingOutput covers the shape of an
// ab-av1 run: the direct child spawns an ffmpeg-like grandchild that inherits
// the captured output pipes. Killing only the direct child leaves that
// grandchild holding the pipe, and Wait then blocks long after the job was
// canceled.
func TestOSRunnerCancelKillsGrandchildrenHoldingOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	marker := filepath.Join(t.TempDir(), "started")
	done := make(chan error, 1)
	go func() {
		_, err := OSRunner{}.Run(ctx, Command{
			Name: "/bin/sh",
			Args: []string{"-c", "sleep 60 & touch " + marker + "; exec sleep 60"},
		})
		done <- err
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled command did not stop its whole process group in bounded time")
	}
}

//go:build unix

package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestOSRunnerCancelKillsGrandchildrenHoldingOutput covers the shape of an
// ab-av1 run: the direct child spawns an ffmpeg-like grandchild that inherits
// the captured output pipes. Killing only the direct child leaves that
// grandchild holding the pipe, and Wait then blocks long after the job was
// canceled.
//
// The assertion is that the grandchild is gone, not just that Run returned:
// WaitDelay alone unblocks Run while leaving the grandchild running, which is
// the failure mode this test exists to catch.
func TestOSRunnerCancelKillsGrandchildrenHoldingOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	done := make(chan error, 1)
	go func() {
		_, err := OSRunner{}.Run(ctx, Command{
			Name: "/bin/sh",
			Args: []string{"-c", "sleep 60 & echo $! > " + pidFile + "; exec sleep 60"},
		})
		done <- err
	}()

	grandchild := waitForPIDFile(t, pidFile)
	if err := syscall.Kill(grandchild, syscall.Signal(0)); err != nil {
		t.Fatalf("grandchild %d was not running before cancel: %v", grandchild, err)
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

	// The grandchild is reparented to init, so this process never reaps it and
	// signal 0 reports the real liveness rather than a zombie.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(grandchild, syscall.Signal(0)); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived cancellation of its process group", grandchild)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("child process did not report its grandchild pid")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

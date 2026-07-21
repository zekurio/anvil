package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/scanner"
)

func TestQueueStalledSignature(t *testing.T) {
	stalled := scanner.ScanResult{Sources: 2, ExistingJobs: 2}
	tests := []struct {
		name          string
		result        scanner.ScanResult
		activeWorkers int
		want          bool
	}{
		{name: "exact signature", result: stalled, want: true},
		{name: "no sources", result: scanner.ScanResult{ExistingJobs: 2}},
		{name: "no existing jobs", result: scanner.ScanResult{Sources: 2}},
		{name: "new job enqueued", result: scanner.ScanResult{Sources: 2, ExistingJobs: 2, EnqueuedJobs: 1}},
		{name: "worker active", result: stalled, activeWorkers: 1},
		{name: "unstable source", result: scanner.ScanResult{Sources: 2, ExistingJobs: 2, SkippedUnstable: 1}},
		{name: "stability retry pending", result: scanner.ScanResult{Sources: 2, ExistingJobs: 2, NextStableAt: time.Now().Add(time.Minute)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := queueStalled(tt.result, tt.activeWorkers); got != tt.want {
				t.Fatalf("queueStalled() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestLogQueueHealthEmitsStableStructuredWarning(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	logQueueHealth("downloads", "interval", scanner.ScanResult{Sources: 3, ExistingJobs: 3}, 0)
	line := output.String()
	for _, want := range []string{
		`"level":"WARN"`,
		`"msg":"queue stall detected"`,
		`"library":"downloads"`,
		`"reason":"interval"`,
		`"event":"queue_stall_detected"`,
		`"metric":"anvil_queue_stalled"`,
		`"value":1`,
		`"active_workers":0`,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("queue health log %q missing %q", line, want)
		}
	}
}

func TestLogQueueHealthSuppressesNormalProcessing(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	logQueueHealth("downloads", "interval", scanner.ScanResult{Sources: 3, ExistingJobs: 3}, 1)
	if output.Len() != 0 {
		t.Fatalf("queue health log = %q, want no warning", output.String())
	}
}

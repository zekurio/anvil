package main

import (
	"log/slog"

	"github.com/zekurio/anvil/pkg/scanner"
)

const queueStallMetric = "anvil_queue_stalled"

func queueStalled(result scanner.ScanResult, activeWorkers int) bool {
	return result.Sources > 0 &&
		result.ExistingJobs > 0 &&
		result.EnqueuedJobs == 0 &&
		activeWorkers == 0 &&
		result.SkippedUnstable == 0 &&
		result.NextStableAt.IsZero()
}

func logQueueHealth(library string, reason string, result scanner.ScanResult, activeWorkers int) {
	if !queueStalled(result, activeWorkers) {
		return
	}
	args := []any{
		"event", "queue_stall_detected",
		"metric", queueStallMetric,
		"value", 1,
		"sources", result.Sources,
		"existing_jobs", result.ExistingJobs,
		"enqueued_jobs", result.EnqueuedJobs,
		"active_workers", activeWorkers,
	}
	if library != "" {
		args = append([]any{"library", library}, args...)
	}
	if reason != "" {
		args = append([]any{"reason", reason}, args...)
	}
	slog.Warn("queue stall detected", args...)
}

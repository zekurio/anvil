package main

import (
	"context"
	"log/slog"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/scanner"
)

const queueStallMetric = "anvil_queue_stalled"

type queueHealthStore interface {
	HasViableQueueWorkForLibrary(context.Context, domain.LibraryName) (bool, error)
}

func queueStalled(result scanner.ScanResult, activeWorkers int, viableQueueWork bool) bool {
	return result.Sources > 0 &&
		result.ExistingJobs > 0 &&
		result.EnqueuedJobs == 0 &&
		activeWorkers == 0 &&
		!viableQueueWork &&
		result.SkippedUnstable == 0 &&
		result.NextStableAt.IsZero()
}

func observeQueueHealth(ctx context.Context, state queueHealthStore, library string, reason string, result scanner.ScanResult, activeWorkers int) {
	if !queueStalled(result, activeWorkers, false) {
		return
	}
	viableQueueWork, err := state.HasViableQueueWorkForLibrary(ctx, domain.LibraryName(library))
	if err != nil {
		slog.Error("queue health query failed", "library", library, "reason", reason, "error", err)
		return
	}
	if !queueStalled(result, activeWorkers, viableQueueWork) {
		return
	}
	logQueueStall(library, reason, result, activeWorkers)
}

func logQueueStall(library string, reason string, result scanner.ScanResult, activeWorkers int) {
	args := []any{
		"event", "queue_stall_detected",
		"metric", queueStallMetric,
		"value", 1,
		"sources", result.Sources,
		"existing_jobs", result.ExistingJobs,
		"enqueued_jobs", result.EnqueuedJobs,
		"active_workers", activeWorkers,
		"viable_queue_work", false,
	}
	if library != "" {
		args = append([]any{"library", library}, args...)
	}
	if reason != "" {
		args = append([]any{"reason", reason}, args...)
	}
	slog.Warn("queue stall detected", args...)
}

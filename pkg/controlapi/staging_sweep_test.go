package controlapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/staging"
	"github.com/zekurio/anvil/pkg/store"
)

// TestSweepStagingProtectsLiveWorkForEveryCaller is the one behavior the
// daemon's start-up sweep and the control command must never differ on. Both go
// through SweepStaging, so this covers both: a directory's mtime stops moving
// once its output file exists, which makes a running multi-hour encode look
// exactly as stale as an abandoned attempt.
func TestSweepStagingProtectsLiveWorkForEveryCaller(t *testing.T) {
	ctx := context.Background()
	service, _, _, job := testService(t, ctx)
	root := staging.Root(t.TempDir())
	now := time.Now().UTC()

	live := filepath.Join(root, "job-"+strconv.FormatInt(int64(job.ID), 10)+"-attempt-1")
	liveRetry := filepath.Join(root, "job-"+strconv.FormatInt(int64(job.ID), 10)+"-attempt-2")
	abandoned := filepath.Join(root, "job-9999-attempt-1")
	for _, dir := range []string{live, liveRetry, abandoned} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
		old := now.Add(-48 * time.Hour)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatalf("Chtimes(%s) error = %v", dir, err)
		}
	}

	result, err := SweepStaging(ctx, service.Store, root, StagingSweep{OlderThan: 24 * time.Hour, Now: now})
	if err != nil {
		t.Fatalf("SweepStaging() error = %v", err)
	}
	if result.Removed != 1 {
		t.Fatalf("Removed = %d, want only the abandoned directory", result.Removed)
	}
	if result.Protected != 2 {
		t.Fatalf("Protected = %d, want both attempt directories of the live job", result.Protected)
	}
	// Two directories, one job: an operator is told which work is holding
	// staging space, not how many directories it happens to own.
	if len(result.ProtectedJobs) != 1 || result.ProtectedJobs[0].ID != int64(job.ID) {
		t.Fatalf("ProtectedJobs = %+v, want one entry for job %d", result.ProtectedJobs, job.ID)
	}
	if result.ProtectedJobs[0].Reason != string(store.JobProtectedActive) {
		t.Fatalf("reason = %q, want the active-job reason", result.ProtectedJobs[0].Reason)
	}
	for _, dir := range []string{live, liveRetry} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("live staging directory %s was removed: %v", dir, err)
		}
	}
}

// TestSweepStagingFailsClosedWhenProtectionIsUnknown keeps a store failure from
// being read as "nothing is protected", which would delete the working
// directory of every running encode.
func TestSweepStagingFailsClosedWhenProtectionIsUnknown(t *testing.T) {
	ctx := context.Background()
	root := staging.Root(t.TempDir())
	abandoned := filepath.Join(root, "job-1-attempt-1")
	if err := os.MkdirAll(abandoned, 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(abandoned, old, old); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	_, err := SweepStaging(ctx, unknownProtection{}, root, StagingSweep{OlderThan: time.Hour, Now: time.Now().UTC()})
	if err == nil {
		t.Fatal("SweepStaging() error = nil, want a refusal while protection is unknown")
	}
	if _, err := os.Stat(abandoned); err != nil {
		t.Fatalf("a refused sweep still removed %s: %v", abandoned, err)
	}
	if _, err := SweepStaging(ctx, nil, root, StagingSweep{OlderThan: time.Hour}); err == nil {
		t.Fatal("SweepStaging(nil protection) error = nil, want a refusal")
	}
}

type unknownProtection struct{}

func (unknownProtection) ProtectedJobs(context.Context) ([]store.ProtectedJob, error) {
	return nil, errors.New("protected jobs are unavailable")
}

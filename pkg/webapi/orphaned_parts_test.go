package webapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/controlapi"
	"github.com/zekurio/anvil/pkg/domain"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
)

// expireLease lets stale-lease recovery see the staged job as abandoned, with
// no attempt left to give it.
func (f *fixture) expireLease() {
	f.configure(func(cfg *config.Config) { cfg.Daemon.MaxAttempts = 1 })
	f.advance(2 * time.Hour)
}

func (f *fixture) recoverStaleJobs() {
	f.t.Helper()
	recovered, err := f.server.Jobs.(controlapi.Service).RecoverStaleJobs(context.Background())
	if err != nil || recovered != 1 {
		f.t.Fatalf("RecoverStaleJobs = %d, %v; want the one staged job", recovered, err)
	}
}

func (f *fixture) wantJobState(id domain.JobID, state domain.JobState) {
	f.t.Helper()
	job, err := f.state.GetJob(context.Background(), id)
	if err != nil || job.State != state {
		f.t.Fatalf("job = %+v, %v; want state %s", job, err, state)
	}
}

// A job that stale-lease recovery fails has no worker to clean up after it
// and never runs again, so its part file used to stay for good.
func TestStaleRecoveryReclaimsThePartOfAJobItEnds(t *testing.T) {
	f := newFixture(t, "", nil)
	staged := f.stageDownload("inbox")
	f.expireLease()
	f.recoverStaleJobs()

	f.wantJobState(staged.job.Job.ID, domain.JobStateFailed)
	if _, err := os.Stat(staged.job.OutputPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("part file %s of the failed job is still there (%v)", staged.job.OutputPath, err)
	}
	if info, err := os.Stat(staged.source); err != nil || info.Size() != 5000 {
		t.Errorf("source after recovery: %v, %v; want it untouched", info, err)
	}
}

// A publish journal that is not resolved owns the artifact: recovery of the
// lease must not take it away from the publish that still has to finish.
func TestStaleRecoveryKeepsThePartOfAnInterruptedPublish(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "", nil)
	staged := f.stageDownload("inbox")
	manager := replacepkg.Manager{Journal: f.state, Hook: func(boundary replacepkg.PublishBoundary) error {
		if boundary == replacepkg.BoundaryPrepared {
			return errors.New("power lost")
		}
		return nil
	}}
	if _, err := manager.Handoff(ctx, staged.job); !errors.Is(err, replacepkg.ErrPublishPending) {
		t.Fatalf("Handoff = %v, want a pending publish", err)
	}
	f.expireLease()
	f.recoverStaleJobs()

	f.wantJobState(staged.job.Job.ID, domain.JobStateFailed)
	if _, err := os.Stat(staged.job.OutputPath); err != nil {
		t.Fatalf("part file of a job with an interrupted publish: %v", err)
	}
	// It is still there for the retry that resumes the publish.
	if _, err := (replacepkg.Manager{Journal: f.state}).Handoff(ctx, staged.job); err != nil {
		t.Fatalf("resumed Handoff = %v", err)
	}
	if data, err := os.ReadFile(staged.job.DestinationPath); err != nil || string(data) != "the encode" {
		t.Errorf("published file = %q, %v", data, err)
	}
}

// Deleting a library deletes its job rows, and with them the only record of
// where a crashed attempt left its part file.
func TestDeleteLibraryReclaimsThePartsOfItsJobs(t *testing.T) {
	f := newFixture(t, "", nil)
	staged := f.stageDownload("inbox")
	// The attempt ends without its own cleanup, as when the daemon is killed,
	// and the job waits for its next attempt.
	f.failAttempt(staged, errors.New("daemon stopped"))
	f.wantJobState(staged.job.Job.ID, domain.JobStatePending)

	wantNoContent(t, f.send(http.MethodDelete, "/api/v1/libraries/inbox", nil))
	if _, err := os.Stat(staged.job.OutputPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("part file %s of a deleted job is still there (%v)", staged.job.OutputPath, err)
	}
	if info, err := os.Stat(staged.source); err != nil || info.Size() != 5000 {
		t.Errorf("source after the delete: %v, %v; want it untouched", info, err)
	}
}

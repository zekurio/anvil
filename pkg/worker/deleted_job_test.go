package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/scheduler"
	"github.com/zekurio/anvil/pkg/store"
)

// cancelAndDeleteLibrary cancels a job the way an operator does and then
// deletes its library, which a cancel leaves possible as far as the rows go.
func (f *flowFixture) cancelAndDeleteLibrary(id domain.JobID) {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.state.CancelJobs(ctx, store.CancelJobsInput{IDs: []domain.JobID{id}, Now: time.Now()}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.state.DeleteLibrary(ctx, "movies", nil); err != nil {
		f.t.Fatal(err)
	}
}

func (f *flowFixture) runner() Runner {
	return Runner{Store: f.state, ConfigProvider: func() config.Config { return f.cfg }, TempDir: f.t.TempDir()}
}

// A job can be canceled and its library deleted between the lease and the
// worker starting its attempt. There is nothing to do then, and nothing has
// gone wrong.
func TestAttemptOfAJobDeletedBeforeItStartedEndsQuietly(t *testing.T) {
	f := newFlowFixture(t)
	f.queue("film.mkv")
	now := time.Now()
	leased, err := f.state.LeaseNextJob(context.Background(), "w1", now.Add(time.Hour), now)
	if err != nil || leased == nil {
		t.Fatalf("LeaseNextJob = %v, %v", leased, err)
	}
	f.cancelAndDeleteLibrary(leased.ID)

	if err := f.runner().Run(context.Background(), scheduler.Assignment{Job: *leased, WorkerID: "w1"}); err != nil {
		t.Fatalf("Run of a deleted job = %v, want it to end quietly", err)
	}
}

// An attempt that is winding down after a cancel may find its job row gone,
// if nothing kept the library from being deleted. Recording the outcome is
// then moot, and must not be reported as a failure.
func TestAttemptWhoseJobWasDeletedWhileItWoundDownEndsQuietly(t *testing.T) {
	ctx := context.Background()
	f := newFlowFixture(t)
	f.queue("film.mkv")
	now := time.Now()
	leased, err := f.state.LeaseNextJob(ctx, "w1", now.Add(time.Hour), now)
	if err != nil || leased == nil {
		t.Fatalf("LeaseNextJob = %v, %v", leased, err)
	}
	attempt, err := f.state.StartAttempt(ctx, leased.ID, "w1", nil, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	f.cancelAndDeleteLibrary(leased.ID)

	runner := f.runner()
	canceled, cancel := context.WithCancelCause(ctx)
	cancel(scheduler.ErrJobCanceled)
	if err := runner.fail(canceled, *leased, attempt, f.cfg, errors.New("ffmpeg was stopped")); err != nil {
		t.Errorf("fail after a cancel of a deleted job = %v, want nil", err)
	}
	if err := runner.fail(ctx, *leased, attempt, f.cfg, errors.New("ffmpeg failed")); err != nil {
		t.Errorf("fail of a deleted job = %v, want nil", err)
	}
}

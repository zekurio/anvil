# Plan 003: Release leased jobs when scheduler cancellation races with drain dispatch

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 4149ac0..HEAD -- pkg/scheduler/scheduler.go pkg/scheduler/scheduler_test.go cmd/anvild/main.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `4149ac0`, 2026-07-02

## Why this matters

Drain shutdown intentionally lets already-running workers finish, but it should not start new jobs after the scheduler service has been canceled. The scheduler currently checks the worker context after leasing; in drain mode that worker context is deliberately still live. If service cancellation lands after a lease but before dispatch, the scheduler can start a new worker instead of releasing the lease. This makes shutdown behavior less predictable and can start fresh encode work during drain.

## Current state

Relevant files:

- `pkg/scheduler/scheduler.go` — leases pending jobs and dispatches workers.
- `pkg/scheduler/scheduler_test.go` — scheduler cancellation, worker-context, and drain behavior tests.
- `cmd/anvild/main.go` — reference only; shows drain uses separate service and worker contexts.

Current excerpts to confirm before editing:

```go
// cmd/anvild/main.go:371-373
serviceCtx, stopServices := context.WithCancel(ctx)
workerCtx, stopWorkers := context.WithCancel(context.WithoutCancel(ctx))
return serviceCtx, stopServices, workerCtx, stopWorkers
```

```go
// pkg/scheduler/scheduler.go:126-134
leased, err := s.leaseAvailable(ctx, cfg, active.byLibrary, maxJobs)
if workerErr := s.workerContextError(ctx); workerErr != nil {
    releaseErr := s.releaseLeased(context.WithoutCancel(ctx), leased)
    return 0, errors.Join(err, workerErr, releaseErr)
}

started := s.dispatchLeased(ctx, leased, allocator, availableThreads, active.count)
if err != nil {
    return started, err
}
```

```go
// pkg/scheduler/scheduler.go:261-275
workerCtx := ctx
if s.WorkerContext != nil {
    workerCtx = s.WorkerContext
}
for i, leasedAssignment := range leased {
    allocation := allocator.AllocateFrom(leasedAssignment.workerID, availableThreads, len(leased))
    ...
    s.register(assignment)
    s.workerWG.Add(1)
    go s.runWorker(workerCtx, assignment)
}
```

```go
// pkg/scheduler/scheduler.go:280-285
func (s *Scheduler) workerContextError(ctx context.Context) error {
    workerCtx := ctx
    if s.WorkerContext != nil {
        workerCtx = s.WorkerContext
    }
    return workerCtx.Err()
}
```

Existing tests already cover nearby behavior:

- `pkg/scheduler/scheduler_test.go:167-211` — cancellation releases a lease when no separate worker context is involved.
- `pkg/scheduler/scheduler_test.go:213-247` — a canceled worker context releases leases.
- `pkg/scheduler/scheduler_test.go:249-288` — already-dispatched workers can outlive scheduler cancellation.

Repo conventions to match:

- Keep cancellation deterministic; avoid sleeps in tests when a hook can make the race explicit.
- Release leased jobs with `context.WithoutCancel(ctx)` when cleanup must outlive a canceled scheduler context.
- Return joined errors rather than logging and swallowing release failures.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Targeted scheduler tests | `go test ./pkg/scheduler -run 'TestScheduleAvailable|TestWorkerContext'` | exit 0 |
| Race check for scheduler | `go test -race ./pkg/scheduler` | exit 0 |
| Format | `make fmt` | exit 0 |
| Tests | `make test` | exit 0 |
| Lint | `make lint` | exit 0, no issues |

## Suggested executor toolkit

- Use `golang-context`, `golang-error-handling`, and `golang-testing` if available.
- Model new tests after the existing scheduler tests listed above.

## Scope

**In scope** (the only source files you should modify):

- `pkg/scheduler/scheduler.go`
- `pkg/scheduler/scheduler_test.go`
- `plans/README.md` status row when done

**Read-only reference file**:

- `cmd/anvild/main.go`

**Out of scope** (do NOT touch):

- `cmd/anvild` shutdown policy or `daemonContexts`.
- Worker cancellation semantics. Already-dispatched workers must still drain when shutdown policy is `drain`.
- Store lease schema or SQL.
- Any retry/recovery behavior outside the scheduler dispatch boundary.

## Git workflow

- Branch: `release-leased-jobs`
- Commit message: `fix(scheduler): release leases on shutdown dispatch race`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Release leases when scheduler context is canceled after leasing

In `pkg/scheduler/scheduler.go`, after `leaseAvailable` returns and before checking `workerContextError`, check `ctx.Err()` directly.

Target behavior:

- If `ctx.Err()` is non-nil:
  - call `s.releaseLeased(context.WithoutCancel(ctx), leased)`;
  - return `0` started workers and `errors.Join(err, ctx.Err(), releaseErr)`.
- Order matters: check scheduler `ctx.Err()` before the existing
  `workerContextError` path, since in drain mode the worker context is
  deliberately still live and would let dispatch proceed.

This closes the deterministic case where `leaseAvailable` returns leased jobs plus `context.Canceled` or the context is canceled immediately after leasing.

**Verify**: `go test ./pkg/scheduler` → exit 0 or compile/test failures directly related to the next step.

### Step 2: Guard dispatch itself against scheduler cancellation

There is still a tiny window between the post-lease check and each worker registration. Close it by changing `dispatchLeased` from returning only `int` to returning `(int, error)`. Verified at `4149ac0`: `dispatchLeased` (defined at `pkg/scheduler/scheduler.go:257`) has exactly one call site, in `scheduleAvailable` at line 132, so the signature change is contained.

Implement this shape:

- Keep `workerCtx` selection as today so already-started workers use `WorkerContext` when configured.
- Track `started := 0`.
- Before registering each leased assignment, check `ctx.Err()`.
- If canceled:
  - release only the remaining unstarted leased assignments using `context.WithoutCancel(ctx)`;
  - return `started` plus `errors.Join(ctx.Err(), releaseErr)`.
- After successfully starting a worker, increment `started`.
- Return `started, nil` at the end.

Update `scheduleAvailable` accordingly:

```go
started, dispatchErr := s.dispatchLeased(ctx, leased, allocator, availableThreads, active.count)
if err != nil || dispatchErr != nil {
    return started, errors.Join(err, dispatchErr)
}
return started, nil
```

Do not release jobs that have already been registered/dispatched; those workers own their lifecycle.

**Verify**: `go test ./pkg/scheduler` → exit 0.

### Step 3: Add a deterministic regression test

In `pkg/scheduler/scheduler_test.go`, add a test near the existing cancellation tests:

`TestScheduleAvailableReleasesLeaseWhenSchedulerCanceledBeforeDrainDispatch`

Test structure:

- Create `schedulerCtx, stopScheduling := context.WithCancel(context.Background())`.
- Create a separate `workerCtx, stopWorker := context.WithCancel(context.Background())`; defer `stopWorker()`.
- Use `fakeScheduleStore` with one pending job and `afterLease: stopScheduling` so cancellation happens immediately after the lease is acquired.
- Use `newBlockingWorker()` and defer `worker.releaseAll()`.
- Configure `Scheduler{WorkerContext: workerCtx, WorkerCount: 1, LeaseDuration: time.Minute, ...}`.
- Call `started, err := s.ScheduleAvailable(schedulerCtx)`.
- Assert:
  - `errors.Is(err, context.Canceled)` is true;
  - `started == 0`;
  - `len(store.released) == 1`;
  - released job has empty `LeaseOwner` and state `pending`;
  - `s.ActiveCount() == 0`.
- Assert no worker started. Use a non-blocking select on `worker.started`:

```go
select {
case assignment := <-worker.started:
    t.Fatalf("worker started after scheduler cancellation: %+v", assignment)
default:
}
```

Do not use sleeps or timers in this test.

**Verify**: `go test ./pkg/scheduler -run 'TestScheduleAvailableReleasesLeaseWhenSchedulerCanceledBeforeDrainDispatch|TestWorkerContextCanOutliveSchedulerContext'` → exit 0.

### Step 4: Run final verification

Run:

```sh
go test -race ./pkg/scheduler
make fmt
make test
make lint
```

Expected: all exit 0, with `make lint` reporting no issues.

## Test plan

- Add one deterministic scheduler regression test for scheduler cancellation with a still-live worker context.
- Existing drain behavior test must continue to pass, proving already-dispatched workers can still drain.
- Race-check the scheduler package after changing dispatch/active registration paths.

## Done criteria

All must hold:

- [ ] Scheduler context cancellation after leasing releases unstarted leases even when `WorkerContext` is still active.
- [ ] Cancellation during dispatch releases only unstarted leases.
- [ ] Already-dispatched workers still drain under `WorkerContext`.
- [ ] Release failures are returned via `errors.Join`, not swallowed.
- [ ] `go test -race ./pkg/scheduler` exits 0.
- [ ] `make fmt`, `make test`, and `make lint` exit 0.
- [ ] No files outside the in-scope list are modified, except `plans/README.md` status.

## STOP conditions

Stop and report back if:

- Any current-state excerpt above no longer matches the live code.
- The fix requires changing `cmd/anvild` drain/cancel policy.
- A worker can be both started and have its lease released on the same path.
- Release failures are hidden to make tests pass.
- Tests require timing sleeps to reproduce the race.

## Maintenance notes

- Reviewers should focus on ownership boundaries: scheduler owns unstarted leased jobs; workers own dispatched jobs.
- Future scheduler batching changes must preserve the invariant that unstarted leases are released on scheduler cancellation.
- This does not replace stale lease recovery; it avoids creating unnecessary stale leases during normal shutdown.

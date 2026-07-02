# Plan 002: Skip resume-checkpoint writes after non-resumable pipeline steps

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 4149ac0..HEAD -- pkg/pipeline/pipeline.go pkg/worker/resume.go pkg/worker/worker.go pkg/worker/worker_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `4149ac0`, 2026-07-02

## Why this matters

Anvil persists pipeline context so retry attempts can skip expensive, safe-to-reuse steps such as probe, crop detection, and CRF search. The same persistence hook currently runs after every successful step, including irreversible steps such as `replace`, `handoff`, and `cleanup`. If saving resume context fails after one of those steps, the attempt can be marked failed even though media may already have been published or staging already removed. Resume checkpoints should not turn successful non-resumable side effects into retryable failures.

Scope of the behavior change: the fix skips persistence for **every**
non-resumable step — `subtitle-cleanup`, `stage`, `encode`, `dovi-fix`,
`validate`, `replace`, `handoff`, and `cleanup` — not just the irreversible
trio. This is safe (verified at `4149ac0`): `ResumeStep` in
`pkg/worker/resume.go:63-99` gates on `resumableStep(step)` and only ever
consumes `Probe`, `Audio`, `Crop`, and `Search` from a cached snapshot, so
persisted context for other steps is never read back. Since all resumable
steps run before the first non-resumable one in the default flows, no useful
checkpoint data is lost.

## Current state

Relevant files:

- `pkg/pipeline/pipeline.go` — generic pipeline runner; currently treats `StepPersistence.StepSucceeded` errors as fatal.
- `pkg/worker/resume.go` — worker-specific resume-context persistence and resumable-step policy.
- `pkg/worker/worker.go` — wires resume persistence into the default worker pipeline.
- `pkg/worker/worker_test.go` — worker and resume-context tests.

Current excerpts to confirm before editing:

```go
// pkg/pipeline/pipeline.go:135-140
slog.Info("pipeline step finished", ...)
if err := r.stepSucceeded(ctx, step.Name, job); err != nil {
    return err
}
if err := r.record(ctx, job.Attempt.ID, domain.AttemptEventBlockFinished, step.Name, "", map[string]any{"step_index": index}); err != nil {
    return err
}
```

```go
// pkg/worker/resume.go:100-105
func (p *pipelineContextPersistence) StepSucceeded(ctx context.Context, step string, job *pipeline.JobContext) error {
    if p == nil || p.store == nil || job == nil || job.Job.ID == 0 {
        return nil
    }
    p.current = p.capture(step, job)
    return p.store.SaveJobPipelineContext(ctx, job.Job.ID, p.current, p.timestamp())
}
```

```go
// pkg/worker/resume.go:203-209
func resumableStep(step string) bool {
    switch step {
    case "probe", "audio-cleanup", "crop-detect", "crf-search":
        return true
    default:
        return false
    }
}
```

```go
// pkg/worker/worker.go:177-189
Registry: pipeline.NewRegistry(
    probe.Block{Prober: prober},
    crop.Block{},
    audio.Block{},
    subtitle.Block{},
    staging.StageBlock{Manager: stageManager},
    search.Block{},
    ffmpeg.Block{},
    ffmpeg.DolbyVisionBlock{},
    validate.Block{Validator: validate.Validator{Prober: prober}},
    replacepkg.ReplaceBlock{},
    replacepkg.HandoffBlock{},
    staging.CleanupBlock{Manager: stageManager},
),
```

Repo conventions to match:

- Keep the generic pipeline runner generic; worker-specific policy belongs in `pkg/worker`.
- Errors from actual block execution must remain fatal.
- Use small fake implementations in tests; do not require external media tools.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Targeted worker tests | `go test ./pkg/worker -run 'TestPipelineContextPersistence|TestRunner'` | exit 0 |
| Pipeline tests | `go test ./pkg/pipeline ./pkg/worker` | exit 0 |
| Format | `make fmt` | exit 0 |
| Tests | `make test` | exit 0 |
| Lint | `make lint` | exit 0, no issues |

## Suggested executor toolkit

- Use `golang-error-handling`, `golang-context`, and `golang-testing` if available.
- Read the existing resume tests in `pkg/worker/worker_test.go` around `TestRunnerResumesPersistedPipelineContext` before adding new tests.

## Scope

**In scope** (the only source files you should modify):

- `pkg/worker/resume.go`
- `pkg/worker/worker_test.go`
- `plans/README.md` status row when done

**Read-only reference files**:

- `pkg/pipeline/pipeline.go`
- `pkg/worker/worker.go`

**Out of scope** (do NOT touch):

- `pkg/pipeline/pipeline.go` behavior. The runner should still treat persistence errors as fatal when the configured persistence reports them.
- `pkg/replace`, `pkg/staging`, or any filesystem mutation behavior.
- Making `replace`, `handoff`, `validate`, `encode`, or `cleanup` resumable.
- Swallowing `FinishAttempt`, `TransitionJob`, `RecordJobFileSizes`, or actual block execution errors.

## Git workflow

- Branch: `resume-checkpoints`
- Commit message: `fix(worker): skip checkpoints for nonresumable steps`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Make worker resume checkpoints write only for resumable steps

In `pkg/worker/resume.go`, change `(*pipelineContextPersistence).StepSucceeded` so it still captures the latest in-memory state, but only writes to the store for steps where `resumableStep(step)` is true.

Target shape:

```go
p.current = p.capture(step, job)
if !resumableStep(step) {
    return nil
}
return p.store.SaveJobPipelineContext(ctx, job.Job.ID, p.current, p.timestamp())
```

Add a short comment above the non-resumable branch explaining the safety rule: job pipeline context is a resume checkpoint for reusable work, not the authoritative completion record for irreversible side effects.

Do not change `resumableStep` in this plan.

**Verify**: `go test ./pkg/worker` → exit 0 or fail only because new tests are not added yet. Fix compile errors before continuing.

### Step 2: Add direct persistence-policy tests

In `pkg/worker/worker_test.go`, add tests near the existing resume-context tests.

Add or extend the existing fake store to support:

- counting calls to `SaveJobPipelineContext`;
- returning a configured save error.

Add these tests:

1. `TestPipelineContextPersistenceSkipsSaveForNonResumableStep`
   - Build a minimal `pipeline.JobContext` with non-zero `Job.ID` and `Attempt.ID`.
   - Use a fake store that would fail if `SaveJobPipelineContext` were called.
   - Call `StepSucceeded(context.Background(), "replace", job)`.
   - Assert no error and zero save calls.
   - Repeat or table-drive for `"handoff"` and `"cleanup"` if it stays readable.

2. `TestPipelineContextPersistenceReturnsSaveErrorForResumableStep`
   - Use a fake store whose `SaveJobPipelineContext` returns a sentinel error.
   - Call `StepSucceeded(context.Background(), "probe", job)`.
   - Assert `errors.Is(err, sentinel)`.
   - Assert exactly one save call.

Keep the tests deterministic and in-memory; no external processes or filesystem fixtures are needed.

Audit existing assertions: `fakeWorkerStore.SaveJobPipelineContext`
(`pkg/worker/worker_test.go:588`) overwrites `f.pipelineContext` with the
latest snapshot. After this change, the stored snapshot at the end of a full
run reflects the last **resumable** step (e.g. `crf-search`), not the final
pipeline step. Any existing test that asserts the persisted context contains
later steps must be updated to match the new policy — update the assertion,
do not weaken the fix.

**Verify**: `go test ./pkg/worker -run TestPipelineContextPersistence` → exit 0 and the new tests pass.

### Step 3: Add a worker-runner regression test for post-mutation persistence failures

Add one higher-level regression test in `pkg/worker/worker_test.go` proving `Runner.Run` does not fail solely because checkpoint persistence would fail after a non-resumable step.

Suggested structure:

- Use the existing `fakeWorkerStore` and `staticMetadataResolver` patterns.
- Configure the fake store to return an error from `SaveJobPipelineContext` when the latest snapshot includes a non-resumable step such as `"replace"` but not when saving a resumable step.
- Build a custom pipeline with:
  - one resumable step such as `"probe"` that sets enough context and saves successfully;
  - one non-resumable step named `"replace"` that sets `job.FinalPath` and succeeds.
- Use a flow with those exact step names.
- Run `Runner.Run`.
- Assert:
  - `Run` returns nil;
  - the attempt state is `succeeded`;
  - the last job transition is `complete`;
  - the non-resumable step ran.

If this test becomes too awkward because existing fakes are too narrow, prefer the direct tests from Step 2 and add a short comment in the test explaining the policy boundary. Do not redesign worker fakes broadly.

**Verify**: `go test ./pkg/worker -run 'TestPipelineContextPersistence|TestRunner'` → exit 0.

### Step 4: Run final verification

Run:

```sh
make fmt
make test
make lint
```

Expected: all exit 0, with `make lint` reporting no issues.

## Test plan

- Unit coverage in `pkg/worker/worker_test.go` for non-resumable step persistence skip.
- Unit coverage for resumable step persistence errors remaining fatal.
- Existing resume tests must still prove `probe`, `audio-cleanup`, `crop-detect`, and `crf-search` can be reused.

## Done criteria

All must hold:

- [ ] `StepSucceeded` does not call `SaveJobPipelineContext` for any non-resumable step (`subtitle-cleanup`, `stage`, `encode`, `dovi-fix`, `validate`, `replace`, `handoff`, `cleanup`).
- [ ] `StepSucceeded` still calls `SaveJobPipelineContext` and returns errors for resumable steps.
- [ ] Generic pipeline block errors remain fatal.
- [ ] Existing resume behavior for `probe`, `audio-cleanup`, `crop-detect`, and `crf-search` still passes tests.
- [ ] `make fmt`, `make test`, and `make lint` exit 0.
- [ ] No files outside the in-scope list are modified, except `plans/README.md` status.

## STOP conditions

Stop and report back if:

- Any current-state excerpt above no longer matches the live code.
- The fix appears to require changing `pkg/pipeline.Runner`.
- A proposed change would swallow actual block execution errors.
- You discover `replace`, `handoff`, or `cleanup` are intentionally meant to be resumable.
- The bug persists because `FinishAttempt` or `TransitionJob` fails after a publish; that is a separate lifecycle-persistence problem, not this checkpoint plan.

## Maintenance notes

- Reviewers should ensure this plan only narrows checkpoint writes; it must not weaken job lifecycle persistence or block error handling.
- If future steps become safely resumable, add them to `resumableStep` and add tests showing their persisted state is sufficient to skip execution.
- Treat resume context as an optimization. Job, attempt, and event tables remain the authoritative lifecycle record.

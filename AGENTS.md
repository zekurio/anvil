# Repository Guidelines

- Anvil is a Linux-only Go daemon that orchestrates AV1 encodes across
  user-defined media libraries. `anvild` (`cmd/anvild`) owns config, the SQLite
  store, scanning, scheduling, encoding, and publication; `anvilctl`
  (`cmd/anvilctl`) is the operator client and talks to the daemon over a Unix
  socket. The scanner uses recursive inotify watches, including close-write and
  moved-in completion signals for download libraries. Orchestration packages
  live under `pkg/*`; `internal/textout` holds shared operator output helpers.
- Data flow: `pkg/scanner` enqueues jobs into `pkg/store`, `pkg/scheduler`
  leases them under a thread budget (`pkg/resources`), `pkg/worker` runs a
  `pkg/pipeline` of named blocks (`probe`, `crop-detect`, `audio-cleanup`,
  `subtitle-cleanup`, `stage`, `crf-search`, `encode`,
  `validate`, `publish`, `cleanup`). Each block lives
  in its own package and registers itself in `worker.DefaultPipeline`.
- Deployment is Linux-only, but the dev shell and `go build` must keep working
  on darwin: platform corners carry build tags (`pkg/scanner/filesystem_*`,
  `pkg/process/kill_unix.go`, `cmd/anvild/ownership_*`), with inotify stubbed
  out off Linux and `flake.nix` exposing `devShells` for darwin while
  `packages`/`apps` stay Linux-only.
- The default branch is `main` and it is the only long-lived branch; use `main`
  or `origin/main` for diffs.
- Go `1.26.4` with a deliberately small dependency set (`BurntSushi/toml`,
  `doublestar/v4`, `golang.org/x/sys`, `modernc.org/sqlite`).
  SQLite must stay pure-Go: no CGo, ever. No CLI framework — stdlib `flag`
  parsing in both binaries.
- `make fmt` (`go fmt ./...`) and `make lint` (`golangci-lint run ./...` inside
  `nix develop`) must pass before a coding task is complete. Add `make build`
  (`bin/anvild`, `bin/anvilctl`) when entrypoints, package wiring, command
  construction, or build tags changed. `make lint-fix` applies autofixes.
- Lint policy is `.golangci.yml`: `errcheck` (including blank assignments),
  `govet`, `ineffassign`, `staticcheck`, `unused`. Format with `gofmt` only;
  never hand-format.
- External tools (`ffmpeg`/`ffprobe`, `ab-av1`) are
  provided by the Nix dev shell (`flake.nix`, `devenv.nix`, `direnv allow`).
  Bump `vendorHash` in `flake.nix` whenever Go dependencies change.
- There is no automated test suite. Verification is `gofmt`, `golangci-lint`,
  building both binaries, and reading the code paths you touched.
- Early WIP; sweeping changes that improve long-term maintainability are
  encouraged. Priorities in order: reliability, data safety (replace, handoff,
  cleanup), predictable behavior under cancellation and retries, then
  performance. Prefer correctness, debuggability, and operator trust.

## Branch Names

Use a short branch name of at most three words, separated by hyphens. Do not use slashes or type prefixes such as `feat/` or `fix/`.

Examples: `session-recovery`, `fix-scroll-state`, `publish-journal`.

## Commits and PR Titles

Use conventional commit-style messages and PR titles: `type(scope): summary`.

Valid types are `feat`, `fix`, `docs`, `chore`, `refactor`, and `test`. Scopes are optional; use the affected package or area, e.g. `cmd`, `config`, `store`, `scanner`, `scheduler`, `worker`, `pipeline`, `ffmpeg`, `control`, `nix`, or `docs`.

Examples: `fix(worker): tolerate a raced cancel`, `feat(control): expose stream selection`, `docs(nix): clarify control client installation`.

## Style Guide

### General Principles

- Early returns, no unnecessary `else`. `switch` over long if-chains.
- Inline single-use logic instead of extracting a helper preemptively; extract
  only when the name describes a real concept or the logic is genuinely reused.
- Keep the exported surface minimal. Grouped imports, no dot imports, no
  aliased imports except to disambiguate a real collision (`replacepkg`).
- No goroutines unless the operation is genuinely concurrent.
- Comment non-obvious constraints and surprising behavior, not obvious control
  flow. Every package carries a `doc.go` where the package name alone is not
  self-explanatory.
- Prefer changing existing code over bolting a local fix onto it; extract
  shared logic into the owning package rather than duplicating it.

### Errors

Wrap with `%w`, lowercase, no trailing punctuation. Use sentinels for expected conditions and check them with `errors.Is`; merge cleanup failures with `errors.Join`.

```go
// Good
if err := s.publish(ctx, job); err != nil {
    return fmt.Errorf("publish job %d: %w", job.ID, err)
}

// Bad
if err := s.publish(ctx, job); err != nil {
    slog.Error("Publish failed.", "error", err)
    return err
}
```

Log **or** return, never both. Logging happens at process and job boundaries (`cmd/*`, `pkg/worker`, `pkg/scheduler`), not in leaf packages.

### Context

`ctx context.Context` is the first parameter on anything that blocks, spawns a process, or touches the store. Never store a context in a struct. Use `context.WithoutCancel(ctx)` deliberately where cleanup must outlive cancellation, and say why.

```go
// Good — the lease must be released even though ctx is already canceled
releaseErr := s.releaseLeased(context.WithoutCancel(ctx), leased)
```

### Dependency Injection

Constructors return concrete structs; behavior is injected through fields. No functional-options APIs. A nil dependency field falls back to the real implementation so the zero value is usable.

```go
// Good
type FFProbe struct {
    Runner process.Runner
}

func (p FFProbe) Probe(ctx context.Context, path string) (domain.ProbeResult, error) {
    runner := p.Runner
    if runner == nil {
        runner = process.OSRunner{}
    }
    ...
}

// Bad
func NewFFProbe(opts ...Option) *FFProbe { ... }
```

### Interfaces

Interfaces are defined consumer-side, in the package that needs them, and stay as small as that consumer requires (`scheduler.Store`, `worker.Store`, `scanner.Store`, `pipeline.EventRecorder`, `probe.Prober`). Concrete persistence lives only in `pkg/store`.

### External Processes

`pkg/process` is the only place external commands run. Build the argument list in a pure function and execute it separately, so command construction is inspectable without spawning anything.

```go
// Good
func Args(plan domain.EncodePlan) []string { ... }
result, err := runner.Run(ctx, process.Command{Name: "ffmpeg", Args: Args(plan)})
```

Always respect `ctx` cancellation, and capture enough metadata (command, exit code, duration, byte counts) to diagnose a failure after the fact.

### Logging

Use `log/slog` with stable messages and variable data as attributes. Large
process output goes to per-attempt log files plus artifact events
(`pkg/worker/process_logs.go`), never into the log stream.

## Repo Patterns

- `pkg/domain` holds core job/attempt/media/profile/encode types and must
  stay free of persistence, ffmpeg, and CLI concerns.
- `pkg/config` is the only place raw TOML is parsed. `Load` decodes into the
  pointer-bearing raw structs in `pkg/config/raw.go` and `resolve.go` fills
  defaults in one pass, so downstream code always sees effective values via
  `cfg.ResolveForLibrary`; never reparse config elsewhere. Keep the field doc
  comments in `pkg/config/schema.go` and `examples/anvil-reference.toml` in
  sync. The Nix module passes TOML-shaped settings to Go.
- Pipeline step order is fixed in `worker.DefaultPipeline`. A block's `Name()`
  is its event and checkpoint name. The publish block selects replace or
  handoff from the library kind.
- `pkg/store` is SQLite via `modernc.org/sqlite` with `SetMaxOpenConns(1)`, WAL,
  and `foreign_keys` pragmas, split into `sqlite_*.go` by concern. Schema
  changes are versioned in-code migrations in `pkg/store/migrations.go`; bump
  `currentSchemaVersion` and add a migration entry, never edit an old one.
- `pkg/worker/resume.go` checkpoints only reusable steps (`probe`,
  `audio-cleanup`, `crop-detect`, `crf-search`) as JSON in
  `jobs.pipeline_context_json`, guarded by an input/config fingerprint.
  Attempt-local output is never resumed except through the publish journal.
- `validate` is observational: `validate.Block.Run` logs and returns `nil` even
  on `ErrValidationFailed`. `ab-av1` search is the encode acceptance authority.
- Publication (`pkg/replace`) goes through a durable journal
  (`prepared → published → source_cleaned → committed`, or `conflict`). Never
  overwrite an existing destination, and record intent before mutating the
  filesystem. The `stage` step plans the destination (`replace.PlanDestination`)
  and the artifact is written next to it as `<name>.job-<id>.anvil-part`, so publish is
  fsync + hardlink + unlink, never a bulk copy; `pkg/staging` keeps only
  scratch (search samples) under `temp_dir`.
  `pkg/store/protection.go` defines the jobs maintenance must not disturb;
  staging cleanup and job pruning both depend on it.
- `anvild` takes an exclusive `flock` on `<store_path>.lock`, then claims the
  control socket, and only then opens the store, recovers jobs, and sweeps
  staging. Nothing with a side effect may run ahead of those two claims.
- The control protocol (`pkg/control`) is private and versioned per frame; the
  stable contract is `anvilctl` syntax, `--json` shapes, error codes, and exit
  status (`0` ok, `1` failed, `2` usage, `3` unreachable, `4` not found). No
  HTTP, JSON-RPC, or `net/rpc`. Operator commands that mutate live state belong
  in `anvilctl`; only local read-only commands (`check-config`, `preflight`)
  stay on `anvild`.
- Operator output goes through `internal/textout` (error-carrying writer,
  tables, JSON, byte/percent formatting) so both binaries stay consistent.
- Filesystem mutation is conservative by default: explicit safety checks and
  clear logs over implicit behavior.

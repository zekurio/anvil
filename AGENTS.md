# Repository Guidelines

## Project Overview

Anvil is a Linux-first Go daemon that orchestrates AV1 encodes across user-defined media libraries. It delegates quality search to `ab-av1 crf-search` but owns final `ffmpeg` command construction and the full scan → schedule → stage → encode → validate → replace/handoff workflow. External tools: `ffmpeg`/`ffprobe`, `ab-av1`, `dovi_tool`, `mkvtoolnix`.

Early WIP. Priorities, in order: reliability, data safety (especially replace/handoff/cleanup paths), predictable behavior under cancellation/retries/partial progress, then performance. When in doubt choose correctness, debuggability, and operator trust.

## Architecture & Data Flow

```
scanner ──enqueue──▶ store (SQLite) ◀──lease── scheduler ──dispatch──▶ worker ──▶ pipeline blocks
                                                                          │
                     probe → crop-detect → audio-cleanup → subtitle-cleanup → stage
                     → crf-search → encode → dovi-fix → validate → replace|handoff → cleanup
```

- **Scan** — `pkg/scanner` (`Scanner.Scan`/`ScanLibrary`) discovers candidates, upserts `domain.MediaSource`/`MediaAsset`, enqueues `domain.Job` through a consumer-side `Store` interface. `monitor.go` adds fsnotify-driven rescans with debounce.
- **Persist** — `pkg/store` is SQLite via `modernc.org/sqlite` (CGo-free), `SetMaxOpenConns(1)`, WAL + `foreign_keys` pragmas, versioned in-code migrations (`pkg/store/migrations.go`). Job states: `pending → leased → running → validating → replacing → complete` (plus `failed`, `retrying`, `skipped`) defined in `pkg/domain/job.go`.
- **Schedule** — `pkg/scheduler` leases jobs in priority order (`LeaseNextJobForLibraries`), enforces library concurrency and thread budgets (`pkg/resources.Allocator`), one goroutine per assignment.
- **Execute** — `pkg/worker.Runner.Run` resolves library/flow/profile (`cfg.ResolveForLibrary`), starts an attempt, builds `pipeline.JobContext`, runs the pipeline, records sizes, transitions the job. `worker.DefaultPipeline(tempDir)` in `pkg/worker/worker.go` is the composition root that registers every block.
- **Pipeline** — `pkg/pipeline.Runner` executes `job.Flow.Steps` by name from a `Registry`; step order comes from config flows (`pkg/config/defaults.go`), not code. Emits `block_started`/`block_finished`/`block_failed` attempt events.
- **Resume** — `pkg/worker/resume.go` checkpoints reusable steps only (`probe`, `audio-cleanup`, `crop-detect`, `crf-search`) as JSON in `jobs.pipeline_context_json`; resume validity is fingerprint-checked (input path, source/asset, resolved config).
- **Validate is observational** — `validate.Block.Run` logs a warning and returns `nil` even on `ErrValidationFailed`; it records `domain.ValidationResult` but does not gate. `pkg/replace.Manager` sets `job.FinalPath` only after safe file operations succeed.

## Key Directories

- `cmd/anvild` — daemon + operational CLI (default `daemon` mode; `scan`, `jobs`, `stats`, `retry`, `recover`, `cleanup`, `check-config`, `inspect`, `preflight`).
- `cmd/anvil-mockarr` — fake Radarr/Sonarr HTTP server for the local smoke fixture.
- `pkg/domain` — core job/attempt/media/profile/flow/encode types. Must stay free of persistence, ffmpeg, and CLI concerns.
- `pkg/config` — TOML loading, defaults, validation. Downstream packages receive resolved settings; never reparse raw config elsewhere.
- `pkg/store` — SQLite persistence (split into `sqlite_*.go` by concern: lifecycle, attempts, queries, media, scan, job context).
- `pkg/scanner`, `pkg/scheduler`, `pkg/worker`, `pkg/pipeline` — orchestration layers as described above.
- `pkg/probe`, `pkg/crop`, `pkg/audio`, `pkg/subtitle`, `pkg/search`, `pkg/ffmpeg`, `pkg/validate`, `pkg/replace`, `pkg/staging` — the pipeline blocks (ffprobe/DV detection, cropdetect, stream selection, ab-av1 search, encode plan + execution + Dolby Vision repair, output validation, safe replace/handoff, staging dirs).
- `pkg/process` — the only place external commands run (`Command` + `OSRunner` via `exec.CommandContext`, captured stdout/stderr/exit code); `context.go` carries process logger/step metadata.
- `pkg/marker`, `pkg/metadata`, `pkg/language`, `pkg/video`, `pkg/resources` — Anvil output tags, Arr metadata resolution, language normalization, codec/crop helpers, thread budgeting.
- `nix/`, `flake.nix`, `devenv.nix` — packaging, NixOS module (`services.anvil`), dev shell.
- `plans/` — numbered implementation-plan docs with a status index in `plans/README.md`.
- `scripts/mock-library.sh` — end-to-end smoke fixture builder/runner.

## Development Commands

```sh
make fmt                # go fmt ./...
make test               # go test ./...
make lint               # golangci-lint run ./... (wrapped in `nix develop`)
make lint-fix           # golangci-lint run --fix ./...
make build              # go build -o bin/anvild ./cmd/anvild
make mock-library       # scripts/mock-library.sh setup (build disposable fixture)
make mock-config-check  # generate fixture config + anvild check-config
make mock-smoke         # scripts/mock-library.sh run (full daemon smoke)
```

**Task completion requirements** for any Go change: `make fmt && make test && make lint`. Add `make build` when entrypoints, package wiring, command construction, or build tags changed. Run `make mock-smoke` only when scanner/scheduler/worker/pipeline behavior changed and the external tools are available.

## Code Conventions & Common Patterns

- **Formatting**: `gofmt` only; never hand-format. Grouped imports, no dot imports. Minimal exported surface.
- **Commits/PRs**: conventional style `type(scope): summary`; types `feat|fix|docs|chore|refactor|test`; useful scopes: `cmd`, `config`, `store`, `scanner`, `scheduler`, `worker`, `pipeline`, `ffmpeg`, `nix`, `docs`. Branches: ≤3 hyphenated words, no slashes or type prefixes (`session-recovery`, `fix-scroll-state`).
- **Errors**: wrap with `%w`, lowercase, no trailing punctuation. Sentinels for expected conditions (`store.ErrNotFound`, `validate.ErrValidationFailed`) checked via `errors.Is`; `errors.Join` for merged cleanup failures. Log **or** return, never both — log at process/job boundaries.
- **Context**: `ctx context.Context` first param on anything that blocks, spawns processes, or touches the store. Never stored in structs. `context.WithoutCancel(ctx)` is used deliberately where cleanup must outlive cancellation (lease release in `pkg/scheduler`, process-output events in `pkg/worker/process_logs.go`).
- **Logging**: `log/slog` structured logging with stable messages and variable data as attributes. Large process output goes to per-attempt log files + artifact events (`pkg/worker/process_logs.go`), never dumped into logs.
- **Interfaces are consumer-side**: each consumer defines the contract it needs (`scheduler.Store`/`Worker`, `worker.Store`/`MetadataResolver`, `scanner.Store`, `pipeline.EventRecorder`/`StepPersistence`, `probe.Prober`). Concrete persistence lives in `pkg/store`.
- **DI is struct composition**: constructors return concrete structs; behavior is injected via fields (`probe.FFProbe{Runner: ...}`, `search.ABAV1{Runner: ...}`, `ffmpeg.Encoder{Runner: ...}`). No functional-options APIs.
- **External processes**: build commands separately from execution so builders are testable without spawning; always respect ctx cancellation; capture enough metadata to diagnose failures.
- **Filesystem mutation**: be conservative around replace/handoff/cleanup — explicit safety checks and clear logs over implicit behavior.
- **General style**: early returns, no unnecessary `else`; `switch` over long if-chains; inline single-use logic instead of premature helpers; no goroutines unless the operation is genuinely concurrent; comments for non-obvious constraints only.
- **Maintainability**: prefer extracting shared logic to the owning package over duplicating locally; changing existing code beats bolt-on local fixes.

## Important Files

- `cmd/anvild/main.go` — CLI entrypoint: config load, logging setup, subcommand dispatch; daemon mode splits service vs worker cancellation contexts and runs scan/reload/recovery/scheduler loops with configurable drain/cancel shutdown.
- `cmd/anvild/preflight.go` — read-only planner: reports candidate/staging/publish plans and warnings without mutating anything.
- `pkg/worker/worker.go` — `DefaultPipeline`, attempt lifecycle, job transitions.
- `pkg/config/defaults.go` — default daemon settings, flows, profiles (canonical step order lives here).
- `pkg/config/schema.go` — typed TOML schema.
- `pkg/store/migrations.go` — versioned schema migrations and pragmas.
- `examples/anvil.toml` — reference config: `[daemon]`, `[flows.*]` (named `steps` arrays), `[profiles.*]` (video/audio/subtitles/validation/metadata subtrees), `[arrs.*]`, `[libraries.*]` (media vs download kinds).
- `nix/modules/anvil.nix` — NixOS module: renders `/etc/anvil/anvil.toml`, hardened systemd unit.
- `Makefile`, `.golangci.yml` — dev commands and lint policy.

## Runtime/Tooling Preferences

- Go `1.26.4` (`go.mod`). Small dependency set: `BurntSushi/toml`, `bmatcuk/doublestar/v4`, `fsnotify`, `modernc.org/sqlite` (pure-Go, no CGo). No CLI framework — stdlib flag parsing in `cmd/anvild`.
- Dev environment is Nix/devenv: `.envrc`/`devenv.yaml` use `use flake . --impure`. The shell provides `go`, `golangci-lint`, `gopls`, `ffmpeg` (jellyfin-ffmpeg on Linux), `ab-av1`, `dovi-tool`, `mkvtoolnix`, `sqlite`. `make lint` wraps golangci-lint in `nix develop --no-pure-eval --command`.
- Flake outputs: `packages.default` (buildGoModule, `cmd/anvild` + `cmd/anvil-mockarr`, binary wrapped with runtime tool PATH), `nixosModules.anvil`, dev shell. Bump `vendorHash` in `flake.nix` when Go deps change.
- Default branch is `main`; use `main`/`origin/main` for diffs.

## Testing & QA

- **Framework**: stdlib `testing` only — no testify, no mock libraries. Table-driven tests with named subtests (see `pkg/config/config_test.go`, `pkg/ffmpeg/ffmpeg_test.go`).
- **Fixtures**: `t.TempDir()` for filesystem tests; temp-file or `:memory:` SQLite (`pkg/store/sqlite_test.go` `openTestStore`); hand-written fakes for boundaries (`fakeWorkerStore` in `pkg/worker/worker_test.go`, `fakeRunner`/`captureCommandRunner` in `pkg/search/search_test.go`, `fakeProber` in `pkg/validate/validate_test.go`). Test observable behavior and public contracts; never duplicate production logic into assertions. No real external services or credentials.
- **Running**: `go test ./...` (or `make test`); targeted `go test ./pkg/<pkg>/` while iterating. No race/coverage flags in the default target.
- **Lint**: golangci-lint v2 config with `linters.default: none` and only `errcheck` (`check-blank: true`), `govet`, `ineffassign`, `staticcheck`, `unused` enabled.
- **Smoke**: `scripts/mock-library.sh` (`setup`, `run`, `config`, `serve-arrs`, `reset`, `paths`) builds a disposable fixture under `tmp/mock-library` with ffmpeg-generated sample media, starts `anvil-mockarr` (fake Radarr/Sonarr, `X-Api-Key` auth) and `anvild`, and waits for three complete jobs in `tmp/mock-library/state/anvil.db`. Use only when scanner/scheduler/worker/pipeline behavior changed and tools are available.

# AGENTS.md

Repo-specific context for AI agents working in Anvil.

- The default branch in this repo is `main`. Use `main` or `origin/main` for diffs.
- Commit scopes when helpful: `cmd`, `config`, `store`, `scanner`, `scheduler`, `worker`, `pipeline`, `ffmpeg`, `nix`, `docs`. Example: `fix(worker): clean staging after failed attempt`.

## Repo-Specific Style

- Be conservative around filesystem mutation, replacement, handoff, and cleanup paths. Prefer explicit safety checks and clear logs over implicit behavior.
- External process execution must respect context cancellation and should capture enough metadata to diagnose failures.
- Keep domain concepts in `pkg/domain` and avoid leaking persistence, ffmpeg, or CLI concerns into domain types.
- Keep config parsing/defaulting in `pkg/config`; downstream packages should receive resolved settings instead of reparsing raw config.
- Keep external command construction and execution separated: command builders should be easy to test without spawning processes.
- Store interfaces belong near the consumers when that keeps packages decoupled; concrete persistence belongs in `pkg/store`.

## Testing

- Use integration-style smoke tests only when the relevant external tools or mock fixtures are available.
- The mock library fixture under `tmp/mock-library` is disposable; use `scripts/mock-library.sh setup` and `scripts/mock-library.sh run` for end-to-end daemon smoke testing when warranted.

## Task Completion Requirements

Before considering a Go coding task completed, run:

```sh
make fmt
make test
make lint
```

Run `make build` when entrypoints, package wiring, command construction, or build tags changed. Run `make mock-smoke` only when the change affects scanner/scheduler/worker/pipeline behavior and the required external tools are available.

## Package Roles

- `cmd/anvild` - daemon and operational CLI entrypoint for scanning, jobs, stats, retries, recovery, cleanup, and config validation.
- `cmd/anvil-mockarr` - mock Arr server used by the local mock library smoke fixture.
- `pkg/audio` - audio stream selection and cleanup policy.
- `pkg/config` - TOML config loading, defaults, validation, and resolved runtime settings.
- `pkg/crop` - crop detection and crop-related video decisions.
- `pkg/domain` - core media, job, attempt, profile, flow, and encode concepts.
- `pkg/dovi` - Dolby Vision detection/repair workflow helpers.
- `pkg/ffmpeg` - final ffmpeg command construction and stream mapping.
- `pkg/language` - language normalization and original-language selection helpers.
- `pkg/marker` - Anvil metadata marker handling for processed/encoded outputs.
- `pkg/metadata` - media metadata preservation, stripping, and standardization policies.
- `pkg/pipeline` - composable orchestration blocks for job attempts.
- `pkg/probe` - media probing and ffprobe integration.
- `pkg/process` - external process execution helpers and captured process logs.
- `pkg/replace` - safe replacement and handoff workflows.
- `pkg/resources` - host resource accounting and worker thread budgeting.
- `pkg/scanner` - media library discovery, filesystem watching, and candidate enqueueing.
- `pkg/scheduler` - job planning, leasing, retry, and dispatch coordination.
- `pkg/search` - `ab-av1` CRF search integration and search-result policy.
- `pkg/staging` - temporary output staging and cleanup.
- `pkg/store` - SQLite persistence interfaces and implementation.
- `pkg/subtitle` - subtitle stream selection and cleanup policy.
- `pkg/validate` - output diagnostics and validation observations.
- `pkg/video` - video profile resolution, encoder/backend selection, and video action policy.
- `pkg/worker` - encode worker coordination and attempt lifecycle.
- `nix` - Nix package and NixOS module support.
- `scripts` - local development and mock-library helper scripts.

## Project Snapshot

Anvil is a Linux-first Go daemon for orchestrating AV1 encodes across user-defined media libraries. It uses `ab-av1 crf-search` for encode search while owning final `ffmpeg` command construction and the surrounding scan, schedule, stage, validate, replace, and handoff workflow.

This repository is a VERY EARLY WIP. Proposing sweeping changes that improve long-term maintainability is encouraged.

## Core Priorities

1. Reliability first.
2. Data safety first, especially around replace/handoff/cleanup paths.
3. Keep daemon behavior predictable during cancellation, shutdown, retries, external process failures, filesystem events, and partial pipeline progress.
4. Performance matters, but correctness and operator trust matter more than short-term throughput.

If a tradeoff is required, choose correctness, debuggability, and robustness over short-term convenience.

## Shared Conventions

<!-- Shared across repos; sync deliberate changes to the other repos' AGENTS.md. -->

### Branch Names

Use a short branch name of at most three words, separated by hyphens. Do not use slashes or type prefixes such as `feat/` or `fix/`. Examples: `session-recovery`, `fix-scroll-state`.

### Commits and PR Titles

Use conventional commit-style messages and PR titles: `type(scope): summary`.

Valid types are `feat`, `fix`, `docs`, `chore`, `refactor`, and `test`. Scopes are optional; useful scopes are listed at the top of this file.

### Style: General Principles

- Keep related logic in one function unless extracting it makes the behavior easier to reuse, test, or reason about.
- Do not extract single-use helpers preemptively. Inline the logic at the call site unless the helper is reused, hides a genuinely complex boundary, or has a clear independent name that improves the caller.
- Keep the happy path readable: handle validation, missing resources, and errors early with early returns; avoid unnecessary `else`.
- Reduce total variable count by inlining values that are only used once, but keep named intermediates when they explain business logic.
- Prefer boring, explicit code over clever abstractions.
- Keep synchronous parsing, validation, and option building synchronous. Do not introduce async control flow or concurrency unless the operation is actually asynchronous.
- Add comments for non-obvious constraints and surprising behavior, not for obvious assignments or control flow.

### Testing

- Avoid mocks as much as possible; prefer real temporary directories, in-memory fixtures, and small fake implementations.
- Test observable behavior and public contracts; do not duplicate production logic into tests.
- Run targeted checks while iterating, then run the completion checks listed above before calling a coding task done.

### Task Completion

- Coding tasks: the completion checks listed above must pass before the task is considered done.
- Nix tasks: run appropriate checks for the changed surface; issue builds only when actually warranted.
- Documentation or planning tasks: verification can be limited to reading the changed files unless the user asks for more. Still keep examples and commands accurate.

### Maintainability

Long-term maintainability is a core priority. When adding functionality, first check if there is shared logic that can be extracted to a separate module or package, or an existing module that owns it. Duplicate logic across multiple files is a code smell. Don't be afraid to change existing code; don't take shortcuts by adding isolated local logic to solve a problem.

## Go Style

<!-- Shared across repos; sync deliberate changes to the other repos' AGENTS.md. -->

### Formatting and Organization

- Use `gofmt`/`go fmt`; do not hand-format Go code. Keep imports grouped and let Go tooling order them.
- Avoid dot imports. Blank imports should be limited to entrypoints or tests where side effects are obvious.
- Keep related declarations together: constants, types, constructors, methods, then helpers. Keep helpers close to the code they support, usually below the main exported function/type that uses them.
- Minimize public surface area. Export only what is used across packages or is part of a deliberate package API.
- A little copying is better than a little dependency.

### Variables and Data Structures

- Use `:=` for non-zero values and `var` for intentional zero-value initialization. Prefer `const` where possible.
- Initialize slices and maps explicitly when they may be returned, serialized, or mutated; avoid surprising nil slices/maps. Preallocate only when there is a clear expected size.
- Use named fields in composite literals for structs from the repo and for external structs whose shape may change.

### Control Flow

- Prefer early returns for errors and edge cases; avoid unnecessary `else` after `return`, `break`, or `continue`.
- Prefer `switch` over long `if`/`else if` chains when comparing the same value or expressing mutually exclusive modes.
- Extract complex conditions into named booleans when they encode multiple business rules.
- Do not introduce goroutines or async-style orchestration unless the operation is actually concurrent.

### Context and Cancellation

- Pass `context.Context` as the first parameter, named `ctx`, on operations that can block, call external services or processes, access persistence, or participate in shutdown.
- Do not store contexts in structs; pass them explicitly. Do not create `context.Background()` in the middle of a request/job path; propagate the caller's context.
- Always call cancel functions on every control-flow path unless ownership is explicitly returned or transferred.
- External calls and processes must respect context cancellation and capture enough metadata to diagnose failures without leaking secrets.

### Errors and Logging

- Returned errors must be checked; do not discard errors with `_` unless there is a documented, safe reason.
- Wrap errors with useful context using `%w`; keep error strings lowercase and without trailing punctuation.
- Errors should be either logged or returned, not both. Log at process/job boundaries where the error is handled.
- Use `errors.Is`/`errors.As` for sentinel or typed error handling.
- Use structured logging (`slog` or the repo's helpers) for operator diagnostics. Keep log messages stable and attach variable data as attributes.
- Avoid `panic` for expected operational failures. Reserve it for impossible programmer errors or startup invariants that cannot be recovered.

### Package Boundaries

- Keep executable entrypoints thin; application behavior belongs in library packages.
- Avoid import cycles by pushing shared concepts down into focused packages rather than creating broad utility packages.

### Go Testing

- Prefer table-driven tests with named subtests for behavior matrices.
- Avoid mocks unless they clarify a package boundary. Use `t.TempDir()` for filesystem tests and keep tests independent of execution order.
- Do not rely on real external services or credentials in tests.

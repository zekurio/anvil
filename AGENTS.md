# AGENTS.md

This file gives AI agents the repo-specific context they need when working in Anvil.

- The default branch in this repo is `main`.
- Use `main` or `origin/main` for diffs.

## Branch Names

Use a short branch name of at most three words, separated by hyphens. Do not use slashes or type prefixes such as `feat/` or `fix/`.

Examples: `resume-pipeline`, `fix-staging-cleanup`, `add-preflight-check`.

## Commits and PR Titles

Use conventional commit-style messages and PR titles: `type(scope): summary`.

Valid types are `feat`, `fix`, `docs`, `chore`, `refactor`, and `test`. Scopes are optional; use the affected package or area when helpful, e.g. `cmd`, `config`, `store`, `scanner`, `scheduler`, `worker`, `pipeline`, `ffmpeg`, `nix`, or `docs`.

Examples: `fix(worker): clean staging after failed attempt`, `docs: update daemon config guide`, `chore(nix): refresh package inputs`.

## Workflows and subagents (pi)

Pi is the harness this repo is worked on with. It provides two delegation mechanisms: the `Agent` tool for spawning individual subagents, and the `workflow` tool for deterministic multi-agent orchestration. Use them to parallelize independent work, protect the main context window from large search/read results, and route work to cheaper or better-fitting models (see model routing below). Do not delegate when a direct tool call is enough — a known file path is a `read`, a known symbol is a `grep`.

### Subagents (`Agent` tool)

Agent types currently available:

- `general-purpose` — full tool access; for complex multi-step tasks, open-ended research, and implementation work.
- `Explore` — fast read-only search agent (read/bash/grep/find/ls) for locating code, files, and references. Runs on Haiku, so use it only for locating things, never for review or analysis. Specify breadth: "quick", "medium", or "very thorough".
- `Plan` — read-only software architect agent that returns step-by-step implementation plans.
- Custom agents can be defined in `.pi/agents/<name>.md` (project) or `~/.pi/agent/agents/<name>.md` (global); project-level overrides global, and a file named after a default agent overrides it.

Key mechanics:

- `model` overrides the agent's default model (`provider/modelId` or fuzzy, e.g. "opus"); `thinking` sets the extended-thinking level.
- `run_in_background: true` returns an agent ID immediately; pi notifies on completion — never poll or sleep waiting. Use `get_subagent_result` to fetch results and `steer_subagent` to redirect a running background agent. To run agents in parallel, launch them in a single message with multiple background calls.
- `resume: <agent-id>` continues a previous agent with its context; a fresh call has no memory of prior runs.
- `isolation: "worktree"` runs the agent in a temporary git worktree so parallel agents can modify files safely; changes land on a branch.
- `inherit_context: true` forks the parent conversation into the agent; the default is a fresh context.
- Subagent results are invisible to the user and describe intent, not outcome — verify actual file changes before reporting delegated work as done.

### Workflows (`workflow` tool)

A workflow is a raw JavaScript script that deterministically orchestrates subagents. Prefer it for decomposable work: repository-wide inspection, independent research or checks, multi-perspective review, and fan-out/fan-in synthesis. Skip it for single quick reads/edits.

- First statement must be `export const meta = { name: 'short_snake_case', description: '...' }`, and the script must call `agent()` at least once.
- Available globals: `agent(prompt, opts)`, `parallel(thunks)`, `pipeline(items, ...stages)`, `phase(title)`, `log(message)`, `args`, `cwd`, `budget`. Plain JavaScript only — no TypeScript, imports, `fs`, `Date`, or `Math.random()`.
- `parallel()` takes thunks, not promises: `await parallel(items.map((item) => () => agent(...)))`. Results return in input order.
- `pipeline(items, ...stages)` runs each item through stages sequentially while items run concurrently; stages receive `(previousValue, originalItem, index)`.
- Give every `agent()` call a unique short `label` (2-5 words) for readable live status. Pass `opts.model` to route to a specific Claude model and `opts.agentType` to pick an agent type.
- For machine-readable output, pass a plain JSON Schema via `opts.schema`; `agent()` returns the validated object.
- Failed branches return `null` and log the failure — check for nulls before synthesizing. When combining multiple subagent results, end with a synthesis/assertion agent that returns a compact JSON value with an ok/verdict.
- Call `phase(title)` when a new group of work starts; don't predeclare speculative phases.

### Delegate prompts

Prompts for workflow/subagent delegates must be fully self-contained: repo path, relevant rules from this file, exact files or search targets, expected output shape, and verification commands. Delegates do not share the parent session's context — brief them like a smart colleague who just walked in. Never delegate understanding ("based on your findings, fix it"); do the synthesis yourself and hand over concrete instructions.

## Model routing

Rankings, higher = better. Cost reflects what I actually pay (OpenAI has really generous limits), not list price. Intelligence is how hard a problem you can hand the model unsupervised. Coding is sheer coding capability (based on Deep SWE). UI taste covers UI/UX, visual design, API ergonomics, and copy.

| model    | cost | intelligence | coding | ui taste |
| -------- | ---- | ------------ | ------ | -------- |
| gpt-5.5  | 9    | 8            | 7      | 5        |
| sonnet-5 | 5    | 5            | 4      | 7        |
| opus-4.8 | 4    | 7            | 6      | 8        |
| fable-5  | 2    | 9            | 9      | 9        |

How to apply:

- These are defaults, not limits. You have standing permission to override them: if a cheaper model's output doesn't meet the bar, rerun or redo the work with a smarter model without asking. Judge the output, not the price tag. Escalating costs less than shipping mediocre work.
- Cost is a tie-breaker only; when axes conflict for anything that ships, intelligence > coding > ui taste > cost. For operator-facing CLI behavior, config shape, docs, and diagnostics, ui taste/API ergonomics matter.
- Bulk/mechanical work (clear-spec implementation, data analysis, migrations): gpt-5.5 - it's effectively free.
- Anything user-facing or operator-facing (CLI UX, config ergonomics, docs, logs, diagnostics, API design) needs ui taste >= 7 - never gpt-5.5 as the only perspective.
- Reviews of plans/implementations: fable-5 or opus-4.8, optionally gpt-5.5 as an extra independent perspective.
- Never use Haiku for anything beyond `Explore`-style code location.

### Reaching each model in pi

All four models are directly available to subagents and workflows via the model parameter — no CLI handoffs or wrapper agents needed:

- `anthropic/claude-fable-5` (fable-5)
- `anthropic/claude-opus-4-8` (opus-4.8)
- `anthropic/claude-sonnet-5` (sonnet-5)
- `openai-codex/gpt-5.5` (gpt-5.5)

Pass `model` on the `Agent` tool or `opts.model` on `agent()` inside a workflow; fuzzy names like "opus" or "gpt-5.5" also resolve. For long-running gpt-5.5 tasks, run them as background subagents instead of blocking the main session.

## Go Skills

For Go coding, review, debugging, or setup work, use the Go skills orchestrator and load the relevant specializations before changing code. Commonly relevant skills for this repo include:

- `golang-code-style`
- `golang-context`
- `golang-design-patterns`
- `golang-error-handling`
- `golang-lint`
- `golang-project-layout`
- `golang-structs-interfaces`
- `golang-testing`

## Style Guide

### General Principles

- Keep related logic in one function unless extracting it makes the behavior easier to reuse, test, or reason about.
- Do not extract single-use helpers preemptively. Inline the logic at the call site unless the helper is reused, hides a genuinely complex boundary, or has a clear independent name that improves the caller.
- Keep the happy path readable and handle validation, missing resources, and errors early.
- Prefer boring, explicit Go over clever abstractions. A little copying is better than a little dependency.
- Minimize public surface area. Export only what is used across packages or is part of a deliberate package API.
- Add comments for non-obvious constraints, process-safety concerns, and surprising behavior; avoid comments that restate simple assignments or control flow.

### Go Formatting and Organization

- Use `gofmt`/`go fmt`; do not hand-format Go code.
- Keep imports grouped and let Go tooling order them.
- Avoid dot imports. Blank imports should be limited to entrypoints or tests where side effects are obvious.
- Prefer one primary type per file when the type has significant methods.
- Keep related declarations together: constants, types, constructors, methods, then helpers.
- Keep helpers close to the code they support, usually below the main exported function/type that uses them.

### Variables and Data Structures

- Use `:=` for non-zero values and `var` for intentional zero-value initialization.
- Prefer `const` where possible.
- Initialize slices and maps explicitly when they may be returned, serialized, or mutated. Avoid surprising nil slices/maps.
- Preallocate slices/maps when there is a clear expected size; do not preallocate speculatively.
- Use named fields in composite literals for structs from this repo and for external structs whose shape may change.
- Reduce total variable count by inlining values that are only used once, but keep named booleans or intermediate values when they explain business logic.

### Control Flow

- Avoid unnecessary `else` after `return`, `break`, or `continue`.
- Prefer early returns for errors and edge cases.
- Prefer `switch` over long `if`/`else if` chains when comparing the same value or expressing mutually exclusive modes.
- Extract complex conditions into named booleans when the condition has multiple business rules.
- Keep synchronous parsing, validation, and option building synchronous. Do not introduce goroutines or async-style orchestration unless the operation is actually concurrent.

### Context, Cancellation, and Processes

- Pass `context.Context` as the first parameter, named `ctx`, on operations that can block, run external commands, use the store, scan files, or participate in daemon shutdown.
- Do not store contexts in structs. Pass them explicitly.
- Do not create `context.Background()` in the middle of a request/job path; propagate the caller's context.
- Always call cancel functions on every control-flow path unless ownership is explicitly returned or transferred.
- External process execution must respect context cancellation and should capture enough metadata to diagnose failures.
- Be conservative around filesystem mutation, replacement, handoff, and cleanup paths. Prefer explicit safety checks and clear logs over implicit behavior.

### Errors and Logging

- Returned errors must be checked; do not discard errors with `_` unless there is a documented, safe reason.
- Wrap errors with useful context using `%w`; keep error strings lowercase and without trailing punctuation.
- Errors should be either logged or returned, not both. Log at process/job boundaries where the error is handled.
- Use `errors.Is`/`errors.As` for sentinel or typed error handling.
- Use structured logging (`slog`) for daemon/operator diagnostics. Keep log messages stable and attach variable data as attributes.
- Avoid `panic` for expected operational failures. Reserve it for impossible programmer errors or startup invariants that cannot be recovered.

### Package Boundaries

- Keep domain concepts in `pkg/domain` and avoid leaking persistence, ffmpeg, or CLI concerns into domain types.
- Keep config parsing/defaulting in `pkg/config`; downstream packages should receive resolved settings instead of reparsing raw config.
- Keep external command construction and execution separated: command builders should be easy to test without spawning processes.
- Store interfaces belong near the consumers when that keeps packages decoupled; concrete persistence belongs in `pkg/store`.
- Avoid import cycles by pushing shared concepts down into focused packages rather than creating broad utility packages.

## Testing

- Prefer table-driven tests with named subtests for behavior matrices.
- Test observable behavior and public/package contracts; do not duplicate production logic into tests.
- Avoid mocks unless they clarify a package boundary. Prefer real temporary directories, in-memory fixtures, and small fake implementations.
- Use `t.TempDir()` for filesystem tests and keep tests independent of execution order.
- Use integration-style smoke tests only when the relevant external tools or mock fixtures are available.
- The mock library fixture under `tmp/mock-library` is disposable; use `scripts/mock-library.sh setup` and `scripts/mock-library.sh run` for end-to-end daemon smoke testing when warranted.

## Task Completion Requirements

### Coding Tasks

Before considering a Go coding task completed, run:

```sh
make fmt
make test
make lint
```

Run `make build` when entrypoints, package wiring, command construction, or build tags changed. Run `make mock-smoke` only when the change affects scanner/scheduler/worker/pipeline behavior and the required external tools are available.

### Nix Tasks

If updating Nix packaging, flakes, dev shell, or the NixOS module, run appropriate Nix checks or builds for the changed surface. Builds should only be issued when actually warranted.

### Documentation or Planning Tasks

If the task only changes docs or plans, verification can be limited to reading the rendered/changed files unless the user asks for more. Still keep examples and commands accurate.

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

## Maintainability

Long-term maintainability is a core priority. If you add new functionality, first check if there is shared logic that can be extracted to a separate module or package. Duplicate logic across multiple files is a code smell and should be avoided. Don't be afraid to change existing code. Don't take shortcuts by just adding local logic to solve a problem.

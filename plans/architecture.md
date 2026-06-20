# Architecture Plan

## Product Shape

Anvil orchestrates encoding work across user-defined media libraries. Each library points at a path, chooses a flow, chooses an encode profile, and eventually contributes discovered media files to a persistent job queue.

The core job lifecycle should look like this:

```text
scan library
  -> enqueue candidate
  -> lease job to worker
  -> probe media
  -> stage input/output as needed
  -> run CRF search
  -> build final ffmpeg command
  -> encode to worker temp output
  -> validate result
  -> replace original safely
  -> cleanup
```

## Decisions So Far

- Runtime language: Go.
- Target runtime: Linux.
- Layout: `pkg/`, not `internal/`, so components can grow as reusable packages.
- Primary surface: `anvild` daemon.
- No API or web server in v1.
- Config format: TOML.
- Development shell: Nix flake plus devenv.
- Search strategy: use `ab-av1 crf-search` first.
- Final encode strategy: Anvil builds and owns the final `ffmpeg` command.
- Persistence direction: SQLite, but not implemented yet.

## Package Boundaries

```text
cmd/anvild/       daemon entrypoint
pkg/config/       TOML config loading, defaults, validation
pkg/domain/       core media, job, library, flow, and encode concepts
pkg/store/        SQLite-backed state and job leases later
pkg/scanner/      library traversal and candidate discovery
pkg/scheduler/    job selection, priorities, and dispatch
pkg/resources/    CPU/thread accounting and worker resource budgets
pkg/worker/       job lifecycle coordination
pkg/pipeline/     composable flow block execution
pkg/probe/        ffprobe integration and media metadata
pkg/search/       CRF search backends, starting with ab-av1
pkg/ffmpeg/       final ffmpeg command construction
pkg/staging/      temp directories and disk-friendly staging
pkg/replace/      safe original replacement workflow
pkg/validate/     encoded output validation
pkg/process/      external process execution, logs, cancellation
```

## Core Abstractions

### Library

A configured media root with scheduling metadata.

Planned fields:

- `name`
- `path`
- `priority`
- `flow`
- `profile`
- include/exclude globs
- per-library concurrency limit
- staging policy

### Flow

An ordered set of pipeline blocks. Flow configuration should remain declarative at first, then become richer as blocks need typed config.

Example:

```toml
[[flows]]
name = "av1-crf-search"
steps = ["probe", "stage", "crf-search", "encode", "validate", "replace"]
```

### Profile

Encoding intent shared by libraries.

Initial video fields:

- codec
- preset
- pixel format
- CRF search bounds
- target VMAF
- minimum encode percentage / size policy later

### Job

A durable unit of work for one media file. Jobs should be stored in SQLite so the daemon can recover from crashes and restarts.

Planned states:

```text
pending -> leased -> running -> validating -> replacing -> complete
                            \-> failed
                            \-> retrying
                            \-> skipped
```

## Command Ownership

Anvil should avoid becoming a collection of ad hoc command strings. The daemon should create an internal encode plan, then render backend commands from that plan.

```text
config + probe result
  -> encode plan
  -> ab-av1 search command
  -> search result
  -> final ffmpeg command
```

This keeps search, encode, validation, and later audio/HDR work aligned.

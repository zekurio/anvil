# Architecture Plan

## Product Shape

Anvil orchestrates encoding work across user-defined media and download libraries. Each library points at a path, chooses a flow, chooses an encode profile, and contributes discovered sources/assets to a persistent job queue.

Library kinds:

- `media`: process existing media library files in place.
- `download`: process stable completed-download packages, then hand off staged output to an import path watched by external tools.

The core job lifecycle should look like this:

```text
scan library
  -> upsert source/assets
  -> enqueue candidate job
  -> lease job to worker
  -> resolve latest config
  -> probe media
  -> stage input/output as needed
  -> run CRF search
  -> build final ffmpeg command
  -> encode to worker temp output
  -> validate result
  -> replace original or hand off package safely
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
- Persistence: SQLite, implemented for media sources, media assets, jobs, attempts, leases, heartbeats, and stale recovery.
- Scanner: implemented for recursive media discovery, download package grouping, include/exclude globs, ignorable download globs, size/mtime fingerprints, and idempotent enqueueing.

## Package Boundaries

```text
cmd/anvild/       daemon entrypoint
pkg/config/       TOML config loading, defaults, validation
pkg/domain/       core media, job, library, flow, and encode concepts
pkg/store/        SQLite-backed sources, assets, jobs, attempts, leases, recovery
pkg/scanner/      library traversal, package grouping, candidate discovery
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

A configured media or download root with scheduling metadata.

Current fields:

- `name`
- `kind`
- `path`
- `priority`
- `flow`
- `profile`
- include/exclude globs
- per-library concurrency limit
- media replacement policy
- download handoff, stability, cleanup, and ignorable-file policy

Download cleanup is intentionally separate from handoff. Handoff delivers staged output. Source cleanup must be explicit and asset-scoped first, with directory pruning only when future cleanup code proves directories contain no wanted assets.

### Flow

An ordered set of pipeline blocks. Flow configuration should remain declarative at first, then become richer as blocks need typed config.

Example:

```toml
[[flows]]
name = "av1-replace"
steps = ["probe", "stage", "crf-search", "encode", "validate", "replace", "cleanup"]
```

### Profile

Encoding intent shared by libraries.

Initial fields:

- container
- video codec, preset, pixel format, CRF search bounds, target VMAF
- expandable audio policy
- expandable subtitle policy
- metadata, attachment, and chapter policy
- minimum encode percentage / size policy later

### Source And Asset

A source is either a single file or a download package root. An asset is a file within that source. Jobs target a source/asset pair so nested download releases can be processed one asset at a time without implying recursive source deletion.

### Job

A durable unit of work for one media source or asset. Jobs are stored in SQLite so the daemon can recover from crashes and restarts. Jobs resolve latest library/flow/profile config when leased; attempts keep room for resolved snapshots.

Current states:

```text
pending -> leased -> running -> validating -> replacing -> complete
                            \-> failed
                            \-> retrying
                            \-> skipped
```

The current store allows one job per source/asset target until explicit change-detection and requeue semantics are added.

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

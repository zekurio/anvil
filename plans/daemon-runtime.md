# Daemon Runtime Plan

## Current State

`cmd/anvild` can load TOML config, validate it, open the SQLite store, recover stale jobs, run an initial scanner pass, print startup details, and stay alive until `SIGINT` or `SIGTERM`.

Supported flags:

```text
--config <path>
--daemon
--check-config
```

`--daemon` currently means "run the daemon loop and stay active"; it does not fork into the background yet.

## Runtime Goals

The daemon should be a simple, reliable process:

```text
start
  -> load config
  -> open store
  -> recover stale jobs
  -> run initial scan
  -> start scanner loop
  -> start scheduler loop
  -> start workers
  -> handle shutdown signal
  -> stop accepting work
  -> cancel or drain workers according to policy
  -> persist final state
  -> exit
```

## Config Reload

Later, `SIGHUP` can reload config.

Reload should be conservative:

- validate the full new config before applying it
- update library/profile/flow definitions
- avoid killing running jobs unless required
- let removed libraries stop contributing new work
- keep old job metadata readable

## Persistence

SQLite is now the source of truth for:

- discovered sources and assets
- jobs and attempts
- job leases and heartbeats

SQLite should later also store:

- libraries as last-seen config snapshots
- block-level progress
- process logs or log references
- probe/search/encode/validation summaries
- replacement and handoff state

This avoids in-memory queues becoming the real state of the system.

## Worker Leases

Workers lease jobs from SQLite rather than receive jobs only through memory channels.

Implemented lease behavior:

- worker claims a pending job
- job records worker id, lease deadline, and heartbeat time
- worker heartbeats while search/encode is running
- daemon startup recovers stale leases
- stale jobs return to pending or failed based on max attempts

Remaining lease work:

- expose retry/max-attempt policy in config
- persist block-level progress and process logs
- decide graceful shutdown drain versus cancel semantics

## Resource Allocation

The first CPU allocator can be simple:

```text
total_threads = configured value or runtime.NumCPU()
active_workers = number of running encode jobs
threads_per_job = max(1, floor(total_threads / active_workers))
```

The important part is that thread allocation is centralized in `pkg/resources`, not scattered through ffmpeg command construction.

Later allocator inputs:

- per-library concurrency
- per-flow concurrency
- search versus encode cost
- encoder-specific thread behavior
- cgroup CPU limits
- manual thread caps
- `nice` / `ionice`

## Logging

Early logging uses the standard library `slog`. Before real encode work starts, decide whether to keep the current logging surface or add richer structured fields around jobs and external processes.

Useful log fields later:

- job id
- library
- file path
- flow
- profile
- worker id
- external command name
- attempt number

## Non-Goals For V1

- No HTTP API.
- No web UI.
- No live flow editing.
- No distributed workers.
- No background forking until the foreground daemon behavior is solid.

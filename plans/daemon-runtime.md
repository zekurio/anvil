# Daemon Runtime Plan

## Current State

`cmd/anvild` can load TOML config, validate it, print startup details, and stay alive until `SIGINT` or `SIGTERM`.

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

SQLite should become the source of truth for:

- libraries as last-seen config snapshots
- discovered files
- jobs and attempts
- job leases and heartbeats
- process logs or log references
- output validation summaries
- replacement state

This avoids in-memory queues becoming the real state of the system.

## Worker Leases

Workers should lease jobs from SQLite rather than receive jobs only through memory channels.

Planned lease behavior:

- worker claims a pending job
- job records worker id, lease deadline, and heartbeat time
- worker heartbeats while search/encode is running
- daemon startup recovers stale leases
- retry policy decides whether a stale job returns to pending or failed

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

Early logging can use the standard library. Before real encode work starts, decide whether to keep stdlib logging or move to structured logs.

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

# Daemon Runtime Plan

## Current State

`cmd/anvild` can load and validate config, open SQLite, recover stale jobs, run an initial scan, and then keep scanner, recovery, and scheduler loops active until `SIGINT` or `SIGTERM`.

Supported flags:

```text
--config <path>
--daemon
--check-config
```

`--daemon` still means foreground daemon mode. It does not fork into the background.

## Runtime Flow

```text
start
  -> load config
  -> open store
  -> recover stale jobs
  -> run initial scan
  -> start scanner loop
  -> start stale recovery loop
  -> start scheduler loop
  -> scheduler leases pending jobs for eligible libraries
  -> workers resolve latest config and run pipeline blocks
  -> handle shutdown signal
  -> exit foreground process
```

## Config Resolution

Workers resolve the latest library, flow, and profile after a job is leased, then snapshot the resolved config onto the attempt. This matches the desired Tdarr/FileFlows-style behavior where queued work does not freeze old config until execution starts.

Later, `SIGHUP` can reload config. Reload should validate the whole file before applying it, stop removed libraries from contributing new work, and avoid killing running jobs unless a future policy explicitly asks for that.

## Persistence

SQLite is the source of truth for:

- discovered sources and assets
- jobs, leases, attempts, and attempt events
- stale lease recovery state

Likely next persistence additions:

- structured probe/search/encode/validation summaries
- external process log references
- retained staging/artifact records
- last-seen library/profile/flow snapshots outside attempts

## Resource Allocation

The first allocator splits configured `total_threads` across active workers:

```text
threads_per_job = max(1, floor(total_threads / active_workers))
```

The allocation is passed through the worker context into search and encode planning. Later allocator inputs can include flow cost, library cost, encoder-specific thread behavior, cgroup CPU limits, and `nice` / `ionice`.

## Shutdown And Cleanup

Current shutdown cancels daemon loops through context cancellation. Remaining policy work:

- decide drain versus cancel behavior for active workers
- decide whether failed attempts retain staging directories for debugging
- add stale temp cleanup for abandoned staging directories
- improve process log capture around cancellation and failures

## Non-Goals For V1

- No HTTP API.
- No web UI.
- No distributed workers.
- No background forking until foreground daemon behavior is solid.

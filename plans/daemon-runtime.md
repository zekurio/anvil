# Daemon Runtime Plan

## Current State

`cmd/anvild` can load and validate config, open SQLite, recover stale jobs, run an initial scan, and then keep scanner, recovery, and scheduler loops active until `SIGINT` or `SIGTERM`. It also exposes a small operational CLI for one-shot scan, job inspection, retry, and stale recovery.

Supported legacy run flags:

```text
--config <path>
--daemon
--check-config
--shutdown-policy drain|cancel
--shutdown-timeout <duration>
```

Supported commands:

```text
run
check-config
scan
jobs
retry
recover
cleanup-staging
help
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
  -> handle shutdown signal according to configured policy
  -> exit foreground process
```

## Config Resolution

Workers resolve the latest library, flow, and profile after a job is leased, then snapshot the resolved config onto the attempt. This matches the desired Tdarr/FileFlows-style behavior where queued work does not freeze old config until execution starts.

`SIGHUP` reload is implemented for runtime-safe settings. It reloads and validates the config file, then swaps the in-memory config for future scans, leases, and worker resolution. Changes to `daemon.store_path` and `daemon.temp_dir` are rejected because the open SQLite handle and staging manager own those process paths.

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

Shutdown is now explicit:

- `drain`: stop scanner/recovery/scheduler loops, stop leasing new jobs, and wait for active workers to finish.
- `cancel`: stop loops and cancel active workers immediately.
- `shutdown_timeout`: with `drain`, a positive timeout cancels active workers after the timeout; `0s` waits indefinitely.
- a second shutdown signal cancels active workers even under `drain`.
- `cleanup-staging`: removes old Anvil staging directories by age, with `--dry-run` support.
- `daemon.staging_cleanup_age`: optional startup cleanup age; `0s` disables automatic cleanup.

Remaining cleanup work:

- decide whether failed attempts retain staging directories by default or opt into immediate best-effort cleanup
- improve process log capture around cancellation and failures

## Non-Goals For V1

- No HTTP API.
- No web UI.
- No distributed workers.
- No background forking until foreground daemon behavior is solid.

# Roadmap

## Implemented

### Foundation

- Go daemon entrypoint, TOML config, defaults, validation, example config, and Nix/devenv setup.
- Durable domain model for libraries, flows, profiles, sources, assets, jobs, attempts, attempt events, probes, searches, encode plans, validation results, and resource allocations.
- SQLite store with migrations, source/asset upserts, idempotent job enqueueing, library-scoped leasing, attempts, event recording, heartbeats, state transitions, and stale lease recovery.

### Discovery And Scheduling

- Scanner walks media and download libraries with `**` include/exclude support.
- Download libraries apply `ignorable_globs`, stability windows, and package grouping for nested releases such as `SomeShowS01/SomeShowS01E01/episode_1.mkv`.
- Scheduler fills available worker slots, respects worker count and per-library concurrency, leases only eligible libraries, and passes thread allocations into workers.
- Daemon now runs initial scan, scan loop, stale recovery loop, and scheduler loop.

### Worker Pipeline

- Workers resolve the latest library, flow, and profile after leasing and snapshot that config on the attempt.
- Static block registry executes configured flows with block start/finish/failure attempt events.
- Built-in blocks cover probe, crop detection, audio cleanup, stage, `ab-av1 crf-search`, final `ffmpeg` encode, validation, media replacement, download handoff, and staging cleanup.
- External process execution is cancellable and testable through a small process runner.
- Final encodes write Anvil-owned video stream markers. Compatible reruns can skip crop detection, CRF search, and video re-encode, then remux/copy the marked video while applying safe stream cleanup.

### File Completion

- Validation checks output existence, non-empty output, ffprobe readability, and duration tolerance.
- Media replacement uses a backup workflow and refuses existing backup destinations.
- Handoff copy/move refuses existing destinations and keeps source cleanup explicit.
- Download cleanup removes only the processed source media, then prunes upward only while directories contain no kept files or only configured ignorable leftovers.

## Remaining Tracks

### Stream Policy Depth

- Implement subtitle retention and cleanup from the expandable profile section.
- Preserve or strip chapters, attachments, metadata, and HDR data intentionally instead of with the current conservative first pass.
- Deepen Sonarr/Radarr-aware track decisions beyond original-language audio cleanup.

### Encode Quality And Validation

- Persist probe/search/encode/validation summaries beyond attempt events.
- Add minimum savings or max encoded percentage policy.
- Add post-encode spot checks, richer stream-layout validation, and better parse support for structured `ab-av1` output.
- Reconcile final `ffmpeg` settings with `ab-av1` search settings as profiles become more expressive.
- Decide how profile changes should invalidate or reuse existing Anvil video markers.

### Runtime Operations

- Define graceful shutdown semantics: drain, cancel, or resume leases.
- Add stale temp/staging cleanup policy, including whether failed attempts keep artifacts for debugging.
- Add config reload via `SIGHUP`.
- Add structured process logs, systemd packaging, `nice` / `ionice`, and basic operational CLI commands.

### Reprocessing And Integrations

- Detect file changes after terminal jobs and decide when to requeue.
- Expand metadata providers beyond the first Arr original-language resolver, including path conventions, `.nfo`, or richer APIs.
- Decide if future management UI edits config files, SQLite state, or a separate flow model.

## Open Questions

- Should failed attempts keep staging directories by default for debugging, or should cleanup run in a best-effort defer?
- Should profile container always decide output extension, or should media libraries preserve original extensions unless explicitly changed?
- How aggressive should retries be per library/profile, beyond the daemon-wide max attempts?
- What is the first useful CLI surface: inspect jobs, retry failed jobs, rescan libraries, or dry-run flows?

# Roadmap

## Implemented

### Foundation

- Go daemon entrypoint, TOML config, defaults, validation, example config, and Nix/devenv setup.
- Devenv shell now carries Go, ffmpeg/ffprobe, ab-av1, SQLite tooling, jq, curl, and make for local unit and smoke testing.
- Durable domain model for libraries, flows, profiles, sources, assets, jobs, attempts, attempt events, probes, searches, encode plans, validation results, and resource allocations.
- SQLite store with migrations, source/asset upserts, idempotent job enqueueing, library-scoped leasing, attempts, event recording, heartbeats, state transitions, and stale lease recovery.
- Operational CLI commands for config checks, one-shot scans, job listing, failed-job retry, stale lease recovery, and stale staging cleanup.

### Discovery And Scheduling

- Scanner walks media and download libraries with `**` include/exclude support.
- Download libraries apply `ignorable_globs`, stability windows, and package grouping for nested releases such as `SomeShowS01/SomeShowS01E01/episode_1.mkv`.
- Scheduler fills available worker slots, respects worker count and per-library concurrency, leases only eligible libraries, and passes thread allocations into workers.
- Daemon now runs initial scan, scan loop, stale recovery loop, and scheduler loop, with explicit `drain` or `cancel` shutdown policy and SIGHUP config reload for runtime-safe settings.

### Worker Pipeline

- Workers resolve the latest library, flow, and profile after leasing and snapshot that config on the attempt.
- Static block registry executes configured flows with block start/finish/failure attempt events.
- Built-in blocks cover probe, crop detection, audio cleanup, stage, `ab-av1 crf-search`, final `ffmpeg` encode, validation, media replacement, download handoff, and staging cleanup.
- External process execution is cancellable and testable through a small process runner.
- Final encodes write Anvil-owned video stream markers. Compatible reruns can skip crop detection, CRF search, and video re-encode, then remux/copy the marked video while applying safe stream cleanup.
- Audio cleanup preserves all streams when no language cleanup policy is configured, when required metadata is unavailable, or when Arr lookup does not match the source.

### File Completion

- Validation checks output existence, non-empty output, ffprobe readability, and duration tolerance.
- Media replacement uses a backup workflow and refuses existing backup destinations.
- Handoff copy/move refuses existing destinations and keeps source cleanup explicit.
- Download cleanup removes only the processed source media, then prunes upward only while directories contain no kept files or only configured ignorable leftovers.
- Local mock smoke tests generate tiny media, run mock Radarr/Sonarr responses, exercise media sidecar output, Arr parse fallback for download handoff, and repeatable SQLite state cleanup.

## Remaining Tracks

### Stream Policy Depth

- Implement subtitle retention and cleanup from the expandable profile section.
- Preserve or strip chapters, attachments, metadata, and HDR data intentionally instead of with the current conservative first pass.
- Deepen Sonarr/Radarr-aware track decisions beyond original-language audio cleanup.
- Harden download-intake metadata beyond parse fallback: handle ambiguous parses, no-match cases, history/queue lookups, `.nfo`, sidecars, or explicit metadata handoff.

### Encode Quality And Validation

- Persist probe/search/encode/validation summaries beyond attempt events.
- Add minimum savings or max encoded percentage policy.
- Add post-encode spot checks, richer stream-layout validation, and better parse support for structured `ab-av1` output.
- Reconcile final `ffmpeg` settings with `ab-av1` search settings as profiles become more expressive.
- Decide how profile changes should invalidate or reuse existing Anvil video markers.

### Runtime Operations

- Decide whether failed attempts keep staging artifacts by default or opt into immediate best-effort cleanup.
- Add structured process logs, systemd packaging, `nice` / `ionice`, and broader operational CLI commands.
- Decide which smoke tests should become CI-friendly and which should remain local/manual because they invoke ffmpeg encodes.

### Reprocessing And Integrations

- Detect file changes after terminal jobs and decide when to requeue.
- Expand metadata providers beyond the first Arr original-language resolver, including path conventions, `.nfo`, or richer APIs.
- Decide if future management UI edits config files, SQLite state, or a separate flow model.

## Open Questions

- Should failed attempts keep staging directories by default for debugging, or should cleanup run in a best-effort defer?
- Should profile container always decide output extension, or should media libraries preserve original extensions unless explicitly changed?
- How aggressive should retries be per library/profile, beyond the daemon-wide max attempts?
- What is the first useful CLI surface: inspect jobs, retry failed jobs, rescan libraries, or dry-run flows?

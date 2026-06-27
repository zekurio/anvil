# Anvil Plans

This directory tracks the working plans for Anvil. These documents are not hard specs; they are the current design direction and should be updated as implementation teaches us more.

## Documents

- [Architecture](architecture.md): high-level decisions, boundaries, and package responsibilities.
- [Daemon Runtime](daemon-runtime.md): daemon-first execution model, lifecycle, config, and persistence.
- [Flows And Pipeline Blocks](flows-and-pipeline.md): expandable flow model and command-building strategy.
- [Roadmap](roadmap.md): completed foundation work, next implementation phases, and open questions.

## Current Direction

Anvil is a Linux-first Go daemon for orchestrating AV1 encodes across user-defined media and download libraries. Media libraries process files in place. Download libraries act as intake roots for completed downloader output, encode stable package assets, and later hand off staged results to paths watched by Sonarr or Radarr.

The foundation now exists: config loading, domain types, SQLite state, leases, attempts, stale recovery, scanner discovery, source/asset records, and initial job enqueueing. The next major work is scheduler/resources, worker flow execution, probing/search, final ffmpeg command construction, validation, replacement, and download handoff.

The first practical encode version should use `ab-av1 crf-search` to find quality settings, then let Anvil own the final `ffmpeg` command plan and execution.

The v1 surface is the daemon. A separate CLI, API, or web UI can come later once the state model and flow engine are real.

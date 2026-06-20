# Anvil Plans

This directory tracks the working plans for Anvil. These documents are not hard specs; they are the current design direction and should be updated as implementation teaches us more.

## Documents

- [Architecture](architecture.md): high-level decisions, boundaries, and package responsibilities.
- [Daemon Runtime](daemon-runtime.md): daemon-first execution model, lifecycle, config, and future persistence.
- [Flows And Pipeline Blocks](flows-and-pipeline.md): expandable flow model and command-building strategy.
- [Roadmap](roadmap.md): staged implementation plan and open questions.

## Current Direction

Anvil is a Linux-first Go daemon for orchestrating AV1 encodes across user-defined media libraries. The first practical version should use `ab-av1 crf-search` to find quality settings, then let Anvil own the final `ffmpeg` command plan and execution.

The v1 surface is the daemon. A separate CLI, API, or web UI can come later once the state model and flow engine are real.

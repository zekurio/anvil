# Anvil Plans

This directory tracks Anvil's working design direction. The docs are intentionally light: implementation has started teaching us the real boundaries, so plans should stay current rather than exhaustive.

## Documents

- [Architecture](architecture.md): package boundaries and high-level daemon shape.
- [Daemon Runtime](daemon-runtime.md): lifecycle, scheduling, leases, and operations.
- [Flows And Pipeline Blocks](flows-and-pipeline.md): block model and media pipeline strategy.
- [Roadmap](roadmap.md): completed work and remaining implementation tracks.

## Current Direction

Anvil is a Go daemon for orchestrating AV1 encodes across media libraries and download libraries. Media libraries process files in place. Download libraries act as intake roots for completed downloader output, encode stable package assets, then hand completed files to paths watched by Sonarr or Radarr.

The current implementation has the core loop in place: scan, enqueue, lease, resolve latest config, run a flow, validate output, replace or hand off safely, and recover stale leases. A disposable mock library fixture now exercises media sidecars, download handoff, generated media, SQLite state, mock Radarr/Sonarr library metadata, and Arr parse fallback for completed-download paths. The next work should deepen media policy, validation, and operations rather than add another broad skeleton.

The first practical encode path is:

```text
probe -> crop-detect -> audio-cleanup -> stage -> crf-search -> encode -> validate -> replace/handoff -> cleanup
```

The profile shape is deliberately expandable so subtitles, metadata, chapters, attachments, HDR, and deeper Arr-aware cleanup can be added without replacing the worker model. Audio cleanup is intentionally conservative for v1: if no language policy is configured, or if Arr metadata is unavailable or does not match the source, Anvil preserves streams instead of making destructive guesses while still marking only the video stream for rerun detection.

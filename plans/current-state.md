# Current State

## Working Core

The daemon can load TOML config, open SQLite, recover stale jobs, scan configured libraries, enqueue candidate media, lease work by library, run configured pipeline blocks, validate output, replace or hand off files, and clean staging.

The implemented default path is:

```text
probe -> crop-detect -> audio-cleanup -> stage -> crf-search -> encode -> validate -> replace/handoff -> cleanup
```

The mock library smoke test exercises generated media, mock Radarr/Sonarr metadata, media sidecar output, download handoff, SQLite state, staging cleanup, and captured process logs.

## Durable State

SQLite stores sources, assets, jobs, attempts, leases, attempt events, and process-output artifact records. Jobs resolve the latest library, flow, and profile after leasing, and attempts snapshot that resolved config.

## Implemented Safety

- Replacement uses an original-file backup before installing the candidate output.
- Handoff refuses existing destinations.
- Download source cleanup is explicit and asset-scoped.
- Failed attempts trigger best-effort staging cleanup.
- Stale leases can be recovered on startup or with the CLI.
- Audio cleanup is conservative when metadata is missing or unsafe.

## Known Shallow Areas

- Validation is still basic: file exists, non-empty, ffprobe-readable, and duration is close.
- Subtitle policy is parsed but not deeply enforced.
- Metadata, chapters, attachments, and HDR are only handled through coarse preserve/strip command shaping.
- Probe parsing captures core stream fields, not rich HDR/chapter/attachment details.
- Completed jobs do not automatically requeue when a source file changes.
- Process logs are captured, but operator-friendly attempt/artifact inspection is not yet exposed in the CLI.
- `daemon.log_level` is accepted by config but not currently wired into slog setup.
- The NixOS module exists, but daemon packaging and service hardening are not complete.

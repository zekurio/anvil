# Current State

## Working Core

The daemon can load TOML config, open SQLite, recover stale jobs, scan configured libraries, enqueue candidate media, lease work by library, run configured pipeline blocks, validate output, replace or hand off files, and clean staging.

The implemented default path is:

```text
probe -> crop-detect -> audio-cleanup -> stage -> crf-search -> encode -> dovi-fix -> validate -> replace/handoff -> cleanup
```

The mock library smoke test exercises generated media, mock Radarr/Sonarr metadata, media copy output, download handoff, SQLite state, staging cleanup, and captured process logs.

## Durable State

SQLite stores sources, assets, jobs, attempts, leases, attempt events, and process-output artifact records. Jobs resolve the latest library, flow, and profile after leasing, and attempts snapshot that resolved config.

## Implemented Safety

- Replacement uses an original-file backup before installing the candidate output.
- Handoff refuses existing destinations.
- Download source cleanup is explicit and asset-scoped.
- Failed attempts trigger best-effort staging cleanup.
- Stale leases can be recovered on startup or with the CLI.
- Audio cleanup is conservative when metadata is missing or unsafe.
- Preflight and inspect expose scan plans, resolved behavior, attempts, and captured process-output artifacts.
- Staged and published outputs are always MKV, including non-MKV sources.
- HDR color fields and Dolby Vision side data are probed; Dolby Vision can select a dedicated HEVC encoder when `dovi_tool` is available, then repair RPU/crop compatibility before validation.
- The flake builds an Anvil package and the NixOS module includes baseline systemd hardening.

## Known Shallow Areas

- Subtitle policy is parsed but not deeply enforced.
- Metadata, chapters, and attachments are only handled through coarse preserve/strip command shaping.
- Dolby Vision RPU repair follows the HEVC/MKV workflow from FileFlows. AV1 Dolby Vision remains unsupported until `dovi_tool` supports it.
- Probe parsing still does not capture rich chapter/attachment details.
- Completed jobs do not automatically requeue when a source file changes.
- NixOS service hardening is present, but real deployments may still need host-specific device and path allowances for hardware encoders.

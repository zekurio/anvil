# Flows And Pipeline Blocks Plan

## Why Flows Exist

Anvil needs to grow media behavior without rewriting the worker every time. Video encode, audio policy, subtitle cleanup, HDR handling, metadata preservation, validation, replacement, and download handoff are separate blocks over one shared job context.

Current default media flow:

```text
probe -> crop-detect -> audio-cleanup -> stage -> crf-search -> encode -> validate -> replace -> cleanup
```

Download libraries use the same shape with `handoff` instead of `replace`.

## Implemented Shape

The worker now executes configured flow steps through a static block registry:

- `probe`: ffprobe JSON wrapper
- `crop-detect`: ffmpeg cropdetect wrapper, skipped on compatible Anvil-marked reruns
- `audio-cleanup`: original-language-aware audio selection, with cleanup disabled when no language policy is configured, required metadata is unavailable, or Arr lookup does not match the source
- `stage`: per-job temp output directory
- `crf-search`: `ab-av1 crf-search`, skipped on compatible Anvil-marked reruns
- `encode`: Anvil-owned final `ffmpeg` command; writes Anvil video stream markers and can copy already-marked video during reruns
- `validate`: output existence, non-empty file, ffprobe readability, and duration tolerance
- `replace`: media-library backup and install workflow
- `handoff`: download-library copy/move into Arr watch paths
- `cleanup`: staging cleanup after successful full flow completion

Attempts record resolved config snapshots and block start/finish/failure events.

## Job Context

The current context carries:

- job, attempt, source, and asset metadata
- resolved library, flow, and profile
- input path, staging dir, output path, and final path
- probe result, audio selection, crop result, search result, encode plan, validation result
- metadata safety state, original language, crop filters, and Anvil video marker tags
- resource allocation

This is intentionally broad enough for future audio/subtitle/HDR policy without changing the worker contract.

## Search And Encode Strategy

`ab-av1 crf-search` remains the first search backend. The wrapper uses the profile CRF range, target VMAF, encoder, preset, pixel format, and resource allocation.

Anvil owns the final `ffmpeg` args so stream mapping, metadata, chapters, attachments, subtitles, audio, and replacement semantics stay predictable as profile policy gets deeper.

Anvil writes operational markers to the output video stream, including whether the file was encoded by Anvil, the profile name, configured video codec/pixel format, CRF when known, crop filter, and marker version. On rerun, a compatible marker lets the pipeline skip crop detection, CRF search, and video encode; the final command copies video and can still apply safe stream remux work.

## Current Limits

- Audio cleanup currently preserves all streams unless `languages_to_keep` expands to at least one concrete language. With an explicit policy, it keeps matching non-commentary tracks, expands `orig` from Arr original-language metadata, and disables cleanup if required metadata is unavailable or Arr lookup does not match the source.
- Subtitle profile sections are parsed and carried, but detailed retention/cleanup is not implemented yet.
- Metadata, chapter, and attachment policy is only the first conservative command-shaping pass.
- Probe parsing captures core format and stream fields, not full HDR, chapter, or attachment details.
- Validation is intentionally modest and does not yet enforce required stream layout, savings, or post-encode VMAF.
- Cleanup is a normal success-path block, and failed attempts also trigger immediate best-effort staging cleanup. Old staging directories are still possible after hard crashes and are handled by startup/manual stale cleanup.
- The mock Arr server follows the response fields Anvil consumes and now covers `/api/v3/parse` fallback for completed-download paths that do not match final Sonarr/Radarr library paths.

## Next Blocks

- subtitle forced/SDH/commentary cleanup based on kept languages
- attachment and chapter preservation policy
- HDR metadata preservation and tonemapping
- post-encode VMAF spot checks
- notification hooks
- richer Arr-aware track and naming decisions
- harden download-intake metadata around ambiguous Arr parse results, no-match cases, history/queue lookups, and optional sidecar metadata

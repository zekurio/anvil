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
- `audio-cleanup`: original-language-aware audio selection, with cleanup disabled when required metadata is unavailable
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

- Audio cleanup currently keeps all non-commentary tracks whose language is in `languages_to_keep`, with `orig` expanded from Arr original-language metadata. If that metadata is unavailable for an `orig`-based profile, stream cleanup is disabled and streams are preserved.
- Subtitle profile sections are parsed and carried, but detailed retention/cleanup is not implemented yet.
- Metadata, chapter, and attachment policy is only the first conservative command-shaping pass.
- Probe parsing captures core format and stream fields, not full HDR, chapter, or attachment details.
- Validation is intentionally modest and does not yet enforce required stream layout, savings, or post-encode VMAF.
- Cleanup is a normal success-path block; failed attempts can leave staging artifacts for inspection until a cleanup policy is chosen.

## Next Blocks

- subtitle forced/SDH/commentary cleanup based on kept languages
- attachment and chapter preservation policy
- HDR metadata preservation and tonemapping
- post-encode VMAF spot checks
- notification hooks
- richer Arr-aware track and naming decisions

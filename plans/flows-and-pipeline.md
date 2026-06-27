# Flows And Pipeline Blocks Plan

## Why Flows Exist

Anvil needs to grow media behavior without rewriting the worker every time. Video encode, audio policy, subtitle cleanup, HDR handling, metadata preservation, validation, replacement, and download handoff are separate blocks over one shared job context.

Current default media flow:

```text
probe -> stage -> crf-search -> encode -> validate -> replace -> cleanup
```

Download libraries use the same shape with `handoff` instead of `replace`.

## Implemented Shape

The worker now executes configured flow steps through a static block registry:

- `probe`: ffprobe JSON wrapper
- `stage`: per-job temp output directory
- `crf-search`: `ab-av1 crf-search`
- `encode`: Anvil-owned final `ffmpeg` command
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
- probe result, search result, encode plan, validation result
- resource allocation

This is intentionally broad enough for future audio/subtitle/HDR policy without changing the worker contract.

## Search And Encode Strategy

`ab-av1 crf-search` remains the first search backend. The wrapper uses the profile CRF range, target VMAF, encoder, preset, pixel format, and resource allocation.

Anvil owns the final `ffmpeg` args so stream mapping, metadata, chapters, attachments, subtitles, audio, and replacement semantics stay predictable as profile policy gets deeper.

## Current Limits

- Audio and subtitle profile sections are parsed and carried, but detailed retention/cleanup is not implemented yet.
- Metadata, chapter, and attachment policy is only the first conservative command-shaping pass.
- Probe parsing captures core format and stream fields, not full HDR, chapter, or attachment details.
- Validation is intentionally modest and does not yet enforce required stream layout, savings, or post-encode VMAF.
- Cleanup is a normal success-path block; failed attempts can leave staging artifacts for inspection until a cleanup policy is chosen.

## Next Blocks

- audio language and commentary/descriptive-audio retention
- subtitle forced/SDH/commentary cleanup
- attachment and chapter preservation policy
- HDR metadata preservation and tonemapping
- crop detection or filter planning
- post-encode VMAF spot checks
- notification hooks
- Arr-aware track and naming decisions

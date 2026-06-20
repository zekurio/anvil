# Flows And Pipeline Blocks Plan

## Why Flows Exist

Anvil should be expandable without rewriting the worker every time we add a media operation. Video encode, audio policy, remuxing, HDR handling, metadata preservation, validation, and replacement should be separate pipeline blocks that operate on a shared job context.

The worker should execute a configured flow:

```text
flow: ["probe", "stage", "crf-search", "encode", "validate", "replace", "cleanup"]
```

Each step contributes to or consumes a shared `JobContext`.

## Planned Job Context

The exact Go shape can evolve, but the context should carry:

- job metadata
- library config snapshot
- flow config snapshot
- profile config snapshot
- original input path
- staged input path
- temp output path
- final candidate path
- probe result
- search result
- encode plan
- validation result
- resource allocation
- logger/process hooks

## Block Interface Direction

A block should be small and explicit.

Possible shape:

```go
type Block interface {
    Name() string
    Run(ctx context.Context, job *JobContext) error
}
```

The first implementation can use a static registry:

```text
probe       -> pkg/probe
stage       -> pkg/staging
crf-search  -> pkg/search
encode      -> pkg/ffmpeg
validate    -> pkg/validate
replace     -> pkg/replace
cleanup     -> pkg/staging
```

Avoid dynamic plugins until the built-in block model has proven itself.

## Search Strategy

Use `ab-av1 crf-search` first. It already handles the hardest early part: searching for a CRF that meets target quality.

Anvil should wrap it behind an interface:

```go
type CRFSearchBackend interface {
    Search(ctx context.Context, plan EncodePlan) (SearchResult, error)
}
```

This leaves room for a native ffmpeg/libvmaf search backend later.

## Final Encode Strategy

Anvil should own the final `ffmpeg` command builder.

Reason:

- audio policy will become Anvil-specific
- stream mapping must be predictable
- subtitles, chapters, metadata, and attachments need explicit policy
- HDR handling will require careful command construction
- replacement and validation need to understand exactly what was produced

The search command and final command must come from the same `EncodePlan`. If they diverge, the CRF found during search may not represent the final encode.

## Initial Encode Plan Fields

Likely first fields:

- input path
- output path
- video codec
- preset
- pixel format
- CRF
- target VMAF
- thread budget
- stream map policy
- metadata policy
- container/output format

## Future Blocks

Potential later blocks:

- audio copy/transcode policy
- audio language preference
- subtitle retention/filtering
- attachment retention
- chapter preservation
- HDR metadata preservation
- tonemapping
- crop detection
- post-encode VMAF spot check
- manual review/quarantine
- notification hooks

## Validation Policy

Validation should start modest and become stricter.

Initial checks:

- output file exists
- output file is non-empty
- ffprobe can read it
- duration is close to source duration
- required streams are present
- output passes minimum savings / encode percentage policy

Later checks:

- compare stream layout against policy
- confirm metadata/chapter retention
- spot-check VMAF
- detect severe bitrate or duration anomalies

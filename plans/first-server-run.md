# First Server Run

## Goal

Run Anvil against the real server in a way that is observable, reversible, and tightly scoped.

## Do Not Start Broadly Yet

Do not run replace mode across a real media library until these are done:

1. Operator visibility for attempts and process-output artifacts.
2. Stronger validation gates for stream layout, encode intent, markers, and size policy.
3. Dry-run/preflight output for scan candidates and planned writes.

## Pilot Configuration

The first real run should use:

- one library or one narrow include path
- one worker
- conservative thread count
- media replacement mode `copy`, or download handoff mode `copy`
- source cleanup disabled for downloads
- `shutdown_policy = "drain"`
- staging cleanup age disabled or set long enough for inspection
- explicit excludes for generated `.anvil` outputs and temporary directories

## Preflight Checklist

Before starting the daemon:

- `check-config` passes on the server config.
- A dry-run scan shows only expected candidates.
- Arr metadata lookup behavior is known for the selected paths.
- The temp directory and store path are on disks with enough space.
- Jellyfin ffmpeg/ffprobe, `ab-av1`, `dovi_tool`, and MKVToolNix are available to the service user.
- Logs and process-output artifact paths are easy to inspect.
- Backups or snapshots exist for any path where replacement mode may be tested later.

## Rollout Shape

1. Run scan-only or preflight once and inspect candidates.
2. Run one copy-only encode.
3. Inspect the output manually with `ffprobe` and playback.
4. Inspect the job, attempt, block events, and process logs.
5. Expand to a tiny batch only after the first output is boring.
6. Move to replace mode only after validation and operator visibility are strong enough to catch bad outputs before install.

## Stop Conditions

Stop immediately if:

- a job fails without a clear process log path
- output duration differs unexpectedly
- audio/subtitle layout is surprising
- destination naming or handoff path is wrong
- Anvil tries to process generated `.anvil` outputs or previous outputs
- disk usage grows unexpectedly in staging or process logs

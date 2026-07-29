# Anvil

Anvil is a Linux-first Go daemon for orchestrating AV1 encodes across user-defined media libraries.

The initial implementation uses `ab-av1 crf-search` for encode search, while Anvil owns the final `ffmpeg` command builder and the surrounding orchestration. The daemon is the primary v1 surface, with a small operational CLI for scanning, inspecting jobs and stats, retrying failed work, and recovering stale leases.

## Design Direction

- Daemon-first, with no API or web server in v1.
- Expandable flow/pipeline blocks for scanning, validation, search, staging, replacement, and process execution.
- Package layout under `pkg/` so orchestration components can grow as reusable building blocks.
- Linux is the target runtime, even though development may happen elsewhere.

## Repository Layout

```text
cmd/anvild/       daemon entrypoint
pkg/config/       configuration loading and defaults
pkg/domain/       core media, job, and encode concepts
pkg/store/        persistence interfaces and implementations
pkg/scanner/      media library discovery
pkg/scheduler/    job planning and dispatch
pkg/resources/    host resource accounting
pkg/worker/       encode worker coordination
pkg/pipeline/     composable orchestration blocks
pkg/probe/        media probing
pkg/search/       AV1 search integration
pkg/ffmpeg/       final ffmpeg command construction
pkg/staging/      temporary output staging
pkg/replace/      safe replacement workflow
pkg/validate/     output diagnostics
pkg/process/      external process execution helpers
```

## Development

Requirements:

- Go 1.26 or newer
- Linux target environment for daemon runtime behavior
- Jellyfin ffmpeg/ffprobe, `ab-av1`, `dovi_tool`, and MKVToolNix for encode and Dolby Vision repair workflows
- Optional: Nix with flakes and direnv for the checked-in development shell

Common commands:

```sh
make test
make fmt
make build
make mock-library
make mock-smoke
```

Run the daemon in the foreground with default settings:

```sh
go run ./cmd/anvild
```

Run with a TOML config:

```sh
go run ./cmd/anvild --config examples/anvil.toml
```

Validate a config without starting the daemon loop:

```sh
go run ./cmd/anvild --config examples/anvil.toml --check-config
go run ./cmd/anvild check-config --config examples/anvil.toml
```

Run in daemon mode:

```sh
go run ./cmd/anvild --config examples/anvil.toml --daemon
```

Daemon mode currently stays in-process and waits for `SIGINT` or `SIGTERM`. It does not fork into the background yet. On shutdown, the default policy is `drain`: Anvil stops scanning/scheduling new work and waits for active workers. Use `--shutdown-policy cancel` or `daemon.shutdown_policy = "cancel"` to cancel active workers too. `shutdown_timeout = "0s"` waits indefinitely; a positive timeout cancels active workers after that wait. Set `daemon.log_level` to `debug`, `info`, `warn`, or `error` to control structured stderr logs. Failed attempts trigger best-effort staging cleanup immediately; `cleanup-staging` and `daemon.staging_cleanup_age` are for hard-crash leftovers and manual maintenance.

Use `daemon.worker_count` to cap simultaneous jobs and `daemon.total_threads` to cap the thread budget shared by active workers. The scheduler leases pending jobs in batches and splits currently free threads across that batch: for example, with `worker_count = 4`, `total_threads = 8`, and two pending jobs, each worker gets four threads. Once all eight threads are reserved by running workers, new jobs wait for a worker to finish even if worker slots remain.

The daemon runs an initial full scan, then each library gets its own repeated scan timer. Set `libraries.<name>.scan_interval` to override the global `daemon.scan_interval`; leaving it blank uses the daemon interval. Anvil also watches configured library roots with filesystem events and coalesces create/write/rename activity with `daemon.filesystem_event_debounce`. Download libraries use `libraries.<name>.download.stable_for` before enqueueing new files; when a filesystem-triggered scan still finds unstable downloads, Anvil schedules the next scan around the earliest skipped file's stable time instead of waiting for the full scan interval.

`ffmpeg`, `ab-av1`, `dovi_tool`, and MKVToolNix stdout/stderr are captured under `daemon.temp_dir/process-logs/job-<job_id>-attempt-<attempt_id>/` and recorded as attempt artifact events with command, exit code, duration, byte counts, and log paths.

Anvil also persists reusable per-job pipeline context in SQLite after successful steps. If a job retries after a daemon restart or interrupted process, the next attempt can best-effort resume saved probe, audio selection, crop detection, and CRF search results when the input fingerprint, resolved config, and initial metadata still match. Attempt-local staging/output files are normally not reused. The exception is a prepared replacement or handoff: its publish journal owns that exact staged artifact until the operation commits or is resolved as a conflict, and recovery runs before ordinary pipeline work.

Replacement and handoff use a durable publish journal with `prepared`, `published`, `source_cleaned`, `committed`, and `conflict` stages. The intended paths, cleanup policy, and file identity are recorded before file mutation. Publication never overwrites an existing destination. Recovery accepts an existing destination only when its size and identity match the journaled artifact; when the filesystem identity cannot prove the match, recovery streams both files through SHA-256. Normal successful publication does not hash the media. A mismatch is recorded as a distinct conflict and leaves the destination and remaining source artifacts untouched.

Filesystem updates and SQLite commits cannot be one atomic transaction. Anvil closes that boundary by syncing a newly published file and its directory before advancing SQLite, and by advancing the journal to `published` before removing the staged artifact or original media. A crash can therefore leave the filesystem one step ahead of SQLite, but retries can prove and replay that step without overwriting. SQLite uses full synchronous mode for these journal commits. This schema is part of the clean database bootstrap; deployments from the superseded schema must reset the database rather than expect an in-place publish-journal migration.

Profiles choose a target bitstream with `profiles.<name>.video.codec` (`av1`, `hevc`/`h265`, or `h264`/`avc`), an implementation family with `profiles.<name>.video.accelerator` (`software`, `qsv`, `vaapi`, or `amf`), and `profiles.<name>.video.bit_depth` (`8` or `10`). Anvil resolves those into concrete ffmpeg encoders and backend formats such as `libsvtav1`, `av1_qsv`, `hevc_vaapi`, or `h264_amf`; concrete ffmpeg encoder names and hardware pixel formats are intentionally not profile values. With QSV, Anvil also uses QSV input decode when the source codec has a QSV decoder and routes the final encode through `vpp_qsv`, including no-crop cases, so bit depth maps to QSV formats like `nv12` or `p010le` internally.

Profiles can require encodes to save space during `ab-av1 crf-search` with `profiles.<name>.video.min_savings_percent`. Anvil maps this to `ab-av1 --max-encoded-percent`, so `min_savings_percent = 20` requires the fitted encode to be no larger than 80% of the input. If ab-av1 cannot find a CRF that satisfies the configured VMAF and savings policy, Anvil treats that as a non-fatal video-copy/remux path by default: audio, subtitle, metadata, attachment, chapter, validation, replacement, and handoff steps still run, but no CRF encode is applied. Set `profiles.<name>.video.force_encode_on_no_fit = true` to force an encode instead; when ab-av1 reports no suitable CRF, Anvil uses the lowest tested CRF from the search output, falling back to `crf_min` if the no-fit output does not include a CRF sample.

Anvil always stages and publishes MKV outputs. Non-MKV sources are remuxed into `.mkv`; in replace mode the original source path is removed after the new `.mkv` target is installed, and in copy/handoff modes the destination extension follows the staged MKV output.

Profiles can pass extra video encoder/search options with `profiles.<name>.video.ffmpeg_args` and `profiles.<name>.video.ab_av1_args`. Dolby Vision sources can use a separate override under `profiles.<name>.video.dolby_vision`: when ffprobe sees Dolby Vision side data and `dovi_tool` is available, Anvil switches to the configured Dolby Vision codec, accelerator, preset, bit depth, and extra args. After encode, `dovi-fix` extracts the original RPU with `dovi_tool --crop --mode 2`, injects it into the encoded HEVC bitstream, and remuxes the fixed video with MKVToolNix before validation. Set `remove_hdr10plus = true` to pass `--drop-hdr10plus`; set `mode = "require"` to fail Dolby Vision jobs if the override cannot be used, or `mode = "off"` to leave Dolby Vision handling to the normal encoder path.

Anvil writes `anvil.processed=true` to outputs it processes. Outputs with a newly encoded video also keep the compatibility marker `anvil.encoded=true`; remux-only outputs use `anvil.video.action=copy` and `anvil.process.reason` instead of claiming a new AV1 encode.

Audio and subtitle cleanup use the same language-selection model. Set `profiles.<name>.audio.languages_to_keep` or `profiles.<name>.subtitles.languages_to_keep` to keep specific languages; `orig` expands to the Arr-derived original language. Leaving the list empty preserves all streams of that type. Both policies support `fallback = "keep_all"`, `"keep_first"`, or `"fail_job"` when no stream matches, plus `unknown_as_original` to treat `und`/unknown language tags as the original language. Audio can drop commentary tracks with `keep_commentary = false`; subtitles can also control forced, SDH/caption/descriptive, and commentary tracks with `keep_forced`, `keep_sdh`, and `keep_commentary`.

Profiles strip per-stream `title` tags by default with `profiles.<name>.metadata.track_titles = "strip"` so release-group or tool branding is not carried into output tracks. Set it to `"preserve"` to keep source titles, or `"standardize"` to replace them with feature-based titles such as `1080p HDR10 AV1`, `English E-AC-3 5.1 640 kb/s`, or `English Forced PGS Subtitle`. This is separate from `profiles.<name>.metadata.mode`, so useful language and disposition metadata can still be preserved.

Before replace or handoff, Anvil probes the staged output and records diagnostics against the resolved job context: probe success, duration tolerance, video codec/pixel-format intent, Anvil encoded or processed markers, audio/subtitle stream counts, HDR color metadata preservation, Dolby Vision preservation when enabled, and observed size savings. These observations are useful for inspection, but ab-av1 search is the video-encode acceptance authority; Anvil does not reject an encode because its own diagnostic checks disagree. `profiles.<name>.validation.duration_tolerance_seconds` can override the default two-second duration tolerance.

`anvild` exposes a versioned JSON control API on `daemon.control_socket`; the NixOS module uses `/run/anvil/anvild.sock`. `anvilctl` is a pure client of that socket and never opens SQLite directly:

```sh
anvilctl status --json
anvilctl job list --library sonarr-anime-downloads --path 'Release/Episode.mkv' --current-only --json
anvilctl job list --absolute-path '/mnt/downloads/complete/Release/Episode.mkv' --current-only --json
anvilctl job cancel --library sonarr-anime-downloads --state pending,running
anvilctl job cancel --absolute-path '/mnt/downloads/complete/Release/Episode.mkv' --reason 'queued by mistake'
anvilctl job cancel --library sonarr-anime-downloads 167 168 --json
```

`job cancel` accepts the same selector vocabulary as `job list`, so it can never target a broader set than the equivalent listing; explicit job ids only narrow that selection further. It requires at least one narrowing selector, so a bare `anvilctl job cancel` — or one that only passes the `--current-only` refinement — is rejected instead of canceling the queue. Cancellation moves `pending`, `leased`, `running`, `validating`, `replacing`, and `retrying` jobs to the terminal `canceled` state, records the reason, cancels the running attempt, and signals the worker so `ffmpeg`/`ab-av1` and the child processes they spawn are killed and the staging directory with any partial output is removed. `canceled` is distinct from `skipped`, which still means Anvil decided a job needs no work. Cancelling an already terminal job is a reported no-op, not an error, and `anvild retry` can requeue a canceled job.

A matched job that cannot be canceled is reported with `canceled = false` and a machine-readable `skip_reason`: `already_terminal`, `state_changed` when the job left the state the selector asked for, `not_found`, or `publish_in_progress`. The last one is a data-safety rule: once the replace or handoff step has journaled a publish, the destination is being written and only that job can finish it, so Anvil refuses the cancel instead of stranding a half-published destination, an orphaned `.anvil-backup`, and an unrecoverable journal row. Wait for it to finish, or `anvild retry` it afterwards. Because a publish is journaled before anything touches the destination, an accepted cancel never leaves a partially published destination behind.

Job path filters are exact. `--path` is library-relative and requires `--library`; `--absolute-path` resolves exact source, asset, planned handoff, journaled destination, and package destination-directory paths across configured libraries without requiring the path to still exist. Zero, one, or multiple jobs are returned without fuzzy selection. The API reports factual daemon, occurrence, job, heartbeat, lease, and publication state only; it does not emit workflow policy such as `wait_recommended`.

Other operator commands:

```sh
go run ./cmd/anvild scan --config examples/anvil.toml
go run ./cmd/anvild scan --config examples/anvil.toml --library movies
go run ./cmd/anvild preflight --config examples/anvil.toml --library movies --limit 20
go run ./cmd/anvild preflight --config examples/anvil.toml --json
go run ./cmd/anvild jobs --config examples/anvil.toml --state pending,failed
go run ./cmd/anvild jobs --config examples/anvil.toml --json
go run ./cmd/anvild stats --config examples/anvil.toml
go run ./cmd/anvild stats --config examples/anvil.toml --json
go run ./cmd/anvild inspect --config examples/anvil.toml 42
go run ./cmd/anvild inspect --config examples/anvil.toml --json 42
go run ./cmd/anvild retry --config examples/anvil.toml 42
go run ./cmd/anvild retry --config examples/anvil.toml --failed --library movies
go run ./cmd/anvild recover --config examples/anvil.toml
go run ./cmd/anvild cleanup-staging --config examples/anvil.toml --older-than 24h --dry-run
go run ./cmd/anvild backup --config examples/anvil.toml /srv/backups/anvil-$(date +%F).db
go run ./cmd/anvild prune-jobs --config examples/anvil.toml --library movies --state complete,failed,canceled
go run ./cmd/anvild prune-jobs --config examples/anvil.toml --library movies --state complete,failed,canceled --apply
go run ./cmd/anvild force-occurrence --config examples/anvil.toml --library usenet-tv 'Release/Episode.mkv'
```

`backup` creates a consistent SQLite snapshot with `VACUUM INTO`, so committed data still present in the live WAL is included. The destination directory must already exist. Anvil writes a private sibling temporary database, verifies it with `PRAGMA integrity_check`, and installs it without replacement; it refuses URI destinations, the live database path, and every destination that already exists.

`prune-jobs` only considers terminal jobs (`complete`, `failed`, `skipped`, or `canceled`) whose source occurrence is already marked `missing`. It preserves active jobs, jobs for present sources, and the source records themselves. The command is a dry run unless `--apply` is given, and reports matched jobs, affected sources, deletions, and per-state counts. Use `--library` and `--state` to narrow the transaction before applying it.

`force-occurrence` resolves one exact library-relative media path through the configured scan rules, then explicitly creates and enqueues the next occurrence. It refuses absolute or escaping paths, missing or ambiguous targets, ignored media, unstable downloads, non-enqueueable assets, and any target with active work. File targets advance the source generation; package targets advance only the selected asset generation. Use this only when cleanup is disabled and an unchanged retained path intentionally represents new content that automatic discovery cannot distinguish.

Daemon scans emit a structured `WARN` with `event=queue_stall_detected`, `metric=anvil_queue_stalled`, and `value=1` when a scan finds sources and existing jobs but enqueues nothing while no workers are active. The warning is suppressed while work is active and while downloads are still waiting for their stability window.

Send `SIGHUP` to reload config without restarting. Reload can update libraries, flows, profiles, Arr settings, worker count, thread count, daemon and library scan intervals, filesystem event debounce, retry policy, shutdown policy, and log level. Changes to `daemon.store_path`, `daemon.temp_dir`, or `daemon.control_socket` are rejected and require a restart.

`preflight` is read-only: it does not migrate or mutate SQLite and does not create, copy, move, delete, or write media, staging, or log files. It reports scan candidates, current source and asset generations, whether a scan would retain an occurrence or create the next generation, existing job status, resolved flow/profile steps, staging/output paths with `job-<new>-attempt-<new>` placeholders where needed, planned publish and cleanup actions, and warnings for destructive settings. Search policy output is described as `ab-av1`/CRF-search driven; when search decides AV1 fitting is not worthwhile, the preflight plan shows whether Anvil will continue as video-copy/remux/metadata processing or force an encode with the lowest tested CRF.

The SQLite store is bootstrapped at schema version 6. Databases from an earlier released schema version are migrated forward in place on open; unrecognized schemas still fail closed, so start with a fresh store path when intentionally crossing an incompatible schema boundary. Version 6 rebuilds the `jobs` table so its state constraint accepts `canceled`.

With Nix:

```sh
nix develop --no-pure-eval
nix build .#default
nix run .#anvild -- --help
nix run .#anvilctl -- status --json
```

The flake exposes `packages.default`, `apps.default`, `apps.anvild`, `apps.anvilctl`, and `nixosModules.anvil`. The package and dev shell prefer `jellyfin-ffmpeg` on Linux when nixpkgs provides it, and fall back to stock ffmpeg elsewhere. The NixOS module adds Jellyfin ffmpeg, `ab-av1`, `dovi-tool`, and MKVToolNix to the service PATH by default, writes the generated TOML to `/etc/anvil/anvil.toml`, creates `/var/lib/anvil/tmp` with `StateDirectory`, exposes the control socket from `RuntimeDirectory`, hardens the systemd service with a writable path allowlist, and exposes `services.anvil.service.*` knobs for nice/IO/CPU weighting and extra writable paths.

With direnv:

```sh
direnv allow
```

The Nix shell is defined by `flake.nix` and `devenv.nix`. It enables Go tooling and includes useful development/runtime tools such as `gopls`, `golangci-lint`, SQLite tooling, Jellyfin ffmpeg when available, `ab-av1`, `dovi_tool`, and MKVToolNix.

## Mock Library Smoke Test

The mock library fixture creates a complete local playground under `tmp/mock-library`: generated movie and TV media, a completed-download package, Radarr/Sonarr API key files, an Anvil config, logs, imports, temp space, and SQLite state.

Set it up:

```sh
scripts/mock-library.sh setup
```

Run the mock Arr server and Anvil until all fixture jobs complete:

```sh
scripts/mock-library.sh run
```

To inspect the mock Arr endpoints manually:

```sh
scripts/mock-library.sh serve-arrs
```

The fixture is intentionally disposable. Reset it with:

```sh
scripts/mock-library.sh reset
```

The daemon can scan configured libraries, persist jobs in SQLite, schedule workers, run media pipeline blocks, record output diagnostics, and publish replacements or handoffs. The mock fixture is the quickest way to exercise that path locally before testing against real libraries.

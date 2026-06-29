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

Profiles choose a target bitstream with `profiles.<name>.video.codec` (`av1`, `hevc`/`h265`, or `h264`/`avc`) and an implementation family with `profiles.<name>.video.accelerator` (`software`, `qsv`, `vaapi`, or `amf`). Anvil resolves those into concrete ffmpeg encoders such as `libsvtav1`, `av1_qsv`, `hevc_vaapi`, or `h264_amf`; concrete ffmpeg encoder names are intentionally not profile codecs. With QSV, Anvil also uses QSV input decode when the source codec has a QSV decoder, drops full-frame no-op crop filters, and maps real crop filters to `vpp_qsv` for the final encode.

Profiles can require encodes to save space during `ab-av1 crf-search` with `profiles.<name>.video.min_savings_percent`. Anvil maps this to `ab-av1 --max-encoded-percent`, so `min_savings_percent = 20` requires the fitted encode to be no larger than 80% of the input. If ab-av1 cannot find a CRF that satisfies the configured VMAF and savings policy, Anvil treats that as a non-fatal video-copy/remux path by default: audio, subtitle, metadata, attachment, chapter, validation, replacement, and handoff steps still run, but no CRF encode is applied. Set `profiles.<name>.video.force_encode_on_no_fit = true` to force an encode instead; when ab-av1 reports no suitable CRF, Anvil uses the lowest tested CRF from the search output, falling back to `crf_min` if the no-fit output does not include a CRF sample.

Anvil always stages and publishes MKV outputs. Non-MKV sources are remuxed into `.mkv`; in replace mode the original source path is removed after the new `.mkv` target is installed, and in copy/handoff modes the destination extension follows the staged MKV output.

Profiles can pass extra video encoder/search options with `profiles.<name>.video.ffmpeg_args` and `profiles.<name>.video.ab_av1_args`. Dolby Vision sources can use a separate override under `profiles.<name>.video.dolby_vision`: when ffprobe sees Dolby Vision side data and `dovi_tool` is available, Anvil switches to the configured Dolby Vision codec, accelerator, preset, pixel format, and extra args. After encode, `dovi-fix` extracts the original RPU with `dovi_tool --crop --mode 2`, injects it into the encoded HEVC bitstream, and remuxes the fixed video with MKVToolNix before validation. Set `remove_hdr10plus = true` to pass `--drop-hdr10plus`; set `mode = "require"` to fail Dolby Vision jobs if the override cannot be used, or `mode = "off"` to leave Dolby Vision handling to the normal encoder path.

Anvil writes `anvil.processed=true` to outputs it processes. Outputs with a newly encoded video also keep the compatibility marker `anvil.encoded=true`; remux-only outputs use `anvil.video.action=copy` and `anvil.process.reason` instead of claiming a new AV1 encode.

Profiles strip per-stream `title` tags by default with `profiles.<name>.metadata.track_titles = "strip"` so release-group or tool branding is not carried into output tracks. Set it to `"preserve"` to keep source titles, or `"standardize"` to replace them with feature-based titles such as `1080p HDR10 AV1`, `English E-AC-3 5.1 640 kb/s`, or `English Forced PGS Subtitle`. This is separate from `profiles.<name>.metadata.mode`, so useful language and disposition metadata can still be preserved.

Before replace or handoff, Anvil probes the staged output and records diagnostics against the resolved job context: probe success, duration tolerance, video codec/pixel-format intent, Anvil encoded or processed markers, audio/subtitle stream counts, HDR color metadata preservation, Dolby Vision preservation when enabled, and observed size savings. These observations are useful for inspection, but ab-av1 search is the video-encode acceptance authority; Anvil does not reject an encode because its own diagnostic checks disagree. `profiles.<name>.validation.duration_tolerance_seconds` can override the default two-second duration tolerance.

Useful operator commands:

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
```

Send `SIGHUP` to reload config without restarting. Reload can update libraries, flows, profiles, Arr settings, worker count, thread count, daemon and library scan intervals, filesystem event debounce, retry policy, shutdown policy, and log level. Changes to `daemon.store_path` or `daemon.temp_dir` are rejected and require a restart.

`preflight` is read-only: it does not migrate or mutate SQLite and does not create, copy, move, delete, or write media, staging, or log files. It reports scan candidates, existing job status, resolved flow/profile steps, staging/output paths with `job-<new>-attempt-<new>` placeholders where needed, planned publish and cleanup actions, and warnings for destructive settings. Search policy output is described as `ab-av1`/CRF-search driven; when search decides AV1 fitting is not worthwhile, the preflight plan shows whether Anvil will continue as video-copy/remux/metadata processing or force an encode with the lowest tested CRF.

With Nix:

```sh
nix develop --no-pure-eval
nix build .#default
nix run .#anvild -- --help
```

The flake exposes `packages.default`, `apps.default`, `apps.anvild`, and `nixosModules.anvil`. The package and dev shell prefer `jellyfin-ffmpeg` on Linux when nixpkgs provides it, and fall back to stock ffmpeg elsewhere. The NixOS module adds Jellyfin ffmpeg, `ab-av1`, `dovi-tool`, and MKVToolNix to the service PATH by default, writes the generated TOML to `/etc/anvil/anvil.toml`, creates `/var/lib/anvil/tmp` with `StateDirectory`, hardens the systemd service with a writable path allowlist, and exposes `services.anvil.service.*` knobs for nice/IO/CPU weighting and extra writable paths.

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

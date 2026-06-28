# Anvil

Anvil is a Linux-first Go daemon for orchestrating AV1 encodes across user-defined media libraries.

The initial implementation uses `ab-av1 crf-search` for encode search, while Anvil owns the final `ffmpeg` command builder and the surrounding orchestration. The daemon is the primary v1 surface, with a small operational CLI for scanning, inspecting jobs, retrying failed work, and recovering stale leases.

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
pkg/validate/     output validation
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

`ffmpeg`, `ab-av1`, `dovi_tool`, and MKVToolNix stdout/stderr are captured under `daemon.temp_dir/process-logs/job-<job_id>-attempt-<attempt_id>/` and recorded as attempt artifact events with command, exit code, duration, byte counts, and log paths.

Profiles can require AV1 encodes to save space during `ab-av1 crf-search` with `profiles.<name>.video.min_savings_percent`. Anvil maps this to `ab-av1 --max-encoded-percent`, so `min_savings_percent = 20` requires the fitted encode to be no larger than 80% of the input. If ab-av1 cannot find a CRF that satisfies the configured VMAF and savings policy, Anvil treats that as a non-fatal video-copy/remux path: audio, subtitle, metadata, attachment, chapter, validation, replacement, and handoff steps still run, but no AV1 CRF encode is applied.

Anvil always stages and publishes MKV outputs. Non-MKV sources are remuxed into `.mkv`; in replace mode the original source path is removed after the new `.mkv` target is installed, and in copy/handoff modes the destination extension follows the staged MKV output.

Profiles can pass extra video encoder/search options with `profiles.<name>.video.ffmpeg_args` and `profiles.<name>.video.ab_av1_args`. Dolby Vision sources can use a separate override under `profiles.<name>.video.dolby_vision`: when ffprobe sees Dolby Vision side data and `dovi_tool` is available, Anvil switches to the configured Dolby Vision codec, preset, pixel format, and extra args. After encode, `dovi-fix` extracts the original RPU with `dovi_tool --crop --mode 2`, injects it into the encoded HEVC bitstream, and remuxes the fixed video with MKVToolNix before validation. Set `remove_hdr10plus = true` to pass `--drop-hdr10plus`; set `mode = "require"` to fail Dolby Vision jobs if the override cannot be used, or `mode = "off"` to leave Dolby Vision handling to the normal encoder path.

Anvil writes `anvil.processed=true` to outputs it processes. Outputs with a newly encoded video also keep the compatibility marker `anvil.encoded=true`; remux-only outputs use `anvil.video.action=copy` and `anvil.process.reason` instead of claiming a new AV1 encode.

Before replace or handoff, Anvil validates the staged output against the resolved job context: probe success, duration tolerance, video codec/pixel-format intent, Anvil encoded or processed markers, audio/subtitle stream counts, HDR color metadata preservation, Dolby Vision preservation when enabled, and observed size savings. `profiles.<name>.validation.duration_tolerance_seconds` can override the default two-second duration tolerance. Validation records larger outputs in its size metrics but does not reject them solely for being larger than the source.

Useful operator commands:

```sh
go run ./cmd/anvild scan --config examples/anvil.toml
go run ./cmd/anvild scan --config examples/anvil.toml --library movies
go run ./cmd/anvild preflight --config examples/anvil.toml --library movies --limit 20
go run ./cmd/anvild preflight --config examples/anvil.toml --json
go run ./cmd/anvild jobs --config examples/anvil.toml --state pending,failed
go run ./cmd/anvild jobs --config examples/anvil.toml --json
go run ./cmd/anvild inspect --config examples/anvil.toml 42
go run ./cmd/anvild inspect --config examples/anvil.toml --json 42
go run ./cmd/anvild retry --config examples/anvil.toml 42
go run ./cmd/anvild retry --config examples/anvil.toml --failed --library movies
go run ./cmd/anvild recover --config examples/anvil.toml
go run ./cmd/anvild cleanup-staging --config examples/anvil.toml --older-than 24h --dry-run
```

Send `SIGHUP` to reload config without restarting. Reload can update libraries, flows, profiles, Arr settings, worker count, thread count, intervals, retry policy, shutdown policy, and log level. Changes to `daemon.store_path` or `daemon.temp_dir` are rejected and require a restart.

`preflight` is read-only: it does not migrate or mutate SQLite and does not create, copy, move, delete, or write media, staging, or log files. It reports scan candidates, existing job status, resolved flow/profile steps, staging/output paths with `job-<new>-attempt-<new>` placeholders where needed, planned publish and cleanup actions, and warnings for destructive settings. Search policy output is described as `ab-av1`/CRF-search driven; when search decides AV1 fitting is not worthwhile, the preflight plan represents the remaining configured actions as video-copy/remux/metadata processing without applying an AV1 CRF encode.

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

The daemon can scan configured libraries, persist jobs in SQLite, schedule workers, run media pipeline blocks, validate output, and publish replacements or handoffs. The mock fixture is the quickest way to exercise that path locally before testing against real libraries.

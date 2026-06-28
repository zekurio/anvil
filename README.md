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
- `ffmpeg` and `ab-av1` for future encode workflows
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

`ffmpeg` and `ab-av1` stdout/stderr are captured under `daemon.temp_dir/process-logs/job-<job_id>-attempt-<attempt_id>/` and recorded as attempt artifact events with command, exit code, duration, byte counts, and log paths.

Useful operator commands:

```sh
go run ./cmd/anvild scan --config examples/anvil.toml
go run ./cmd/anvild scan --config examples/anvil.toml --library movies
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

With Nix:

```sh
nix develop --no-pure-eval
```

With direnv:

```sh
direnv allow
```

The Nix shell is defined by `flake.nix` and `devenv.nix`. It enables Go tooling and includes useful development/runtime tools such as `gopls`, `golangci-lint`, SQLite tooling, `ffmpeg`, and `ab-av1` when that package is available in the selected nixpkgs.

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

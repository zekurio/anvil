# Anvil

A Linux-only daemon that keeps media libraries encoded in AV1. Anvil relies on
Linux inotify, `flock`, Unix-domain sockets, and POSIX process groups for safe
filesystem monitoring, singleton ownership, operator control, and process-tree
cancellation. It scans configured libraries, searches CRF using its own FFmpeg sample
encodes, owns the final `ffmpeg` command, and publishes results by
replacing the source in place or handing off finished downloads to an import
directory.

`anvild` is the service; `anvilctl` is the operator client, the way `systemctl`
is for systemd. It opens no database and runs no media tools — it asks the
running daemon over a Unix socket, because a second writer on a live store is
how half-published files happen.

This project is early and under active development. Expect sharp edges.

### Deployment

The NixOS module is the preferred deployment path. Add Anvil to your flake
inputs:

```nix
inputs.anvil.url = "github:zekurio/anvil";
```

Then import and configure the module:

```nix
{
  imports = [ inputs.anvil.nixosModules.anvil ];

  services.anvil = {
    enable = true;
    package = inputs.anvil.packages.${pkgs.system}.anvild;
    group = "anvil";

    controlClient = {
      install = true;
      package = inputs.anvil.packages.${pkgs.system}.anvilctl;
    };

    settings.libraries.movies.path = "/srv/media/movies";
  };

  users.users.alice.extraGroups = [ "anvil" ];
}
```

`services.anvil.settings` uses the same snake_case keys as the reference TOML.
The module renders `/etc/anvil/anvil.toml`, creates `/var/lib/anvil`, exposes
the control socket from `RuntimeDirectory`, and adds Jellyfin ffmpeg
to the service PATH. The socket is `0660` inside a
`0750` directory owned by `services.anvil.group`, so group membership — not
having `anvilctl` installed — is what grants operator access.

Without Nix, install `ffmpeg`/`ffprobe` with the selected encoder and
`libvmaf` or `xpsnr` filter,
then run `anvild --config /etc/anvil/anvil.toml`.
[`examples/anvil.toml`](examples/anvil.toml) is the minimal quick-start;
[`examples/anvil-reference.toml`](examples/anvil-reference.toml) documents every
setting, including daemon, profiles, Arrs, and media and download
libraries. Validate a config before starting anything:

```sh
anvild check-config --config examples/anvil.toml
anvild preflight --config examples/anvil.toml --library movies --limit 20
```

`anvild check-config --config PATH --show` prints the effective config with
defaults applied and secrets redacted.

Both are local and read-only; every command that touches live state lives in
`anvilctl`. `SIGHUP` reloads libraries, profiles, and most daemon
settings in place; `store_path`, `temp_dir`, and `control_socket` require a
restart.

### CRF search

Anvil searches integer CRFs between `video.crf_min` and `video.crf_max`.
It measures the upper endpoint first, then bisects the range. Once the interval
has halved, it estimates the next CRF from the last two measured scores, and any
estimate that fails to halve the interval again hands the next step back to
bisection. It finds the highest CRF meeting `video.target`. Quality and size are assumed to decrease
as CRF increases; only measured candidates can be accepted. The chosen result
must also meet `video.min_savings_percent`. If none fits, Anvil copies the video.
With `force_encode_on_no_fit = true`, it searches the size boundary and chooses
the best measured quality within the size limit, or the best measured quality
overall when no candidate fits the size limit. Process and scoring failures
always fail the attempt, including in forced mode.

Samples are evenly spaced. `samples = 0` uses one sample per 12 minutes of
input, rounded up; `sample_duration = "20s"` controls their length. If the
requested samples cover at least 85% of the input, Anvil uses the whole video
once. Clips are copied from seekable keyframes and may include preroll. The
same clips are reused for every candidate, with the same encoder settings as
the final encode. Sample timestamps are regenerated at the reported frame rate,
or 25 fps when unknown, to avoid gaps from cutting reordered GOPs. Final encodes
keep the source timeline and pass every source frame to the encoder, so
duplicate-timestamp and VFR sources are not silently shortened. Each encoded
clip must decode to the reference frame count.

VMAF matches ab-av1's default model and scaling: sources smaller than 1728x972
are bicubic scaled up to 1080p, sources larger than 2560x1440 use the
`vmaf_4k_v0.6.1` model, and those smaller than 3456x1944 are scaled up to 4K.
This keeps a VMAF target's meaning consistent across resolutions. XPSNR uses the
minimum of the Y/U/V plane averages reported by FFmpeg, then the arithmetic mean
across samples. Comparisons pair frames by index at fixed analysis rates of
25 fps for VMAF and 60 fps for XPSNR. Infinite XPSNR for identical images is
recorded as 100 to keep results valid JSON. No HDR tone mapping or HDR-specific
metric model is applied.

Savings compare the total encoded clip bytes against the reference clip bytes.
These are video-only Matroska clips, including their container overhead; audio,
subtitles, and attachments are excluded. Sample estimates do not guarantee
whole-file quality or final file size. Candidate CRFs, scores, and size ratios
are saved in the pipeline checkpoint, and commands have per-attempt process
logs. Interrupted searches restart; completed searches resume from checkpoints.

The native implementation replaces ab-av1 entirely. Remove `ab_av1_args` from
base profiles and overrides, even when empty. Move encoder options to
`ffmpeg_args` as option/value pairs, for example
`["-svtav1-params", "tune=0:film-grain=8"]`. These now apply to both search and
final encoding. Anvil manages codec, preset, CRF, pixel format, threads, stream
mapping, filters, and timing; those command-line overrides are rejected,
including `crf=`, `qp=`, `preset=`, and `rc=` keys inside `-svtav1-params`,
`-x265-params`, or `-x264-params`. VMAF targets keep their ab-av1 calibration
because search uses the same resolution-dependent scaling and 4k model.
Use `samples` and `sample_duration` for sampling. Other ab-av1-specific options
have no automatic translation. Existing checkpoints are invalidated on upgrade.

### Operating

```
anvilctl status                                  daemon state, workers, queue counts
anvilctl version                                 client, daemon, and protocol versions
anvilctl jobs [SELECTORS]                        list jobs
anvilctl show JOB                                show one job
anvilctl cancel [JOB...] [SELECTORS]             cancel jobs
anvilctl retry [JOB...] [--failed [--library N]] requeue failed jobs
anvilctl prune [--library N] [--state S,...] [--apply]
anvilctl recover
anvilctl scan [LIBRARY]
anvilctl stats [LIBRARY]
anvilctl requeue --library NAME PATH
anvilctl staging cleanup [--older-than D] [--dry-run] [--legacy-parts]
anvilctl backup DESTINATION
anvilctl help [COMMAND]
```

Jobs are addressed by numeric id or slug. `--json` (or `-j`) works globally
and per command. Exit status is `0` success, `1` command failed, `2` usage error, `3`
daemon unreachable or protocol mismatch, `4` not found. `--socket` or
`ANVIL_CONTROL_SOCKET` overrides the default `/run/anvil/anvild.sock`.

```sh
anvilctl jobs --state pending,failed --json
anvilctl jobs --absolute-path '/mnt/media/converted/Release/Episode.mkv'
anvilctl cancel --library usenet-tv --state pending,running
anvilctl retry --failed --library movies
anvilctl prune --library movies --state complete,failed,canceled --apply
anvilctl staging cleanup --older-than 24h --dry-run
anvilctl backup /srv/backups/anvil-$(date +%F).db
```

Legacy output artifacts are cleaned only on request. Use
`anvilctl staging cleanup --older-than 24h --legacy-parts --dry-run` to preview
old unscoped `.mkv.anvil-part` files in configured output libraries. Remove
`--dry-run` to delete eligible files. Current job parts and artifacts held by
publish journals are preserved. Job startup no longer lists destination
directories for this maintenance.

The scheduler wakes when work arrives or a worker finishes. Its timer remains
a fallback. `daemon.max_threads_per_job = 0` divides the thread budget across
worker slots so later arrivals can start. Set a positive value to choose a
per-job cap. Allocations remain fixed for each running job.

`cancel` requires a narrowing selector, and refuses a job whose publish is
already journaled — only the daemon can finish that destination safely. Job
pruning and staging cleanup likewise skip anything active or holding an
unresolved publish journal, and report it under `protected_jobs`.

Encodes are written next to their publish destination as
`<name>.mkv.job-<id>.anvil-part` and linked into place under the final name
once validated, so publishing never copies the file across filesystems and
the destination only ever appears complete. A part file left behind by a
crashed attempt is removed when the job retries or is canceled; media
scanners and Arrs ignore the suffix. `temp_dir` only holds scratch that
never publishes: search samples and process logs.

### Development

Deployment targets Linux only (the scanner relies on inotify), but day-to-day
development — building, linting, `gopls`, running `anvilctl` — also works on
macOS. With [Nix](https://nixos.org/) and [direnv](https://direnv.net/)
(provides Go, `golangci-lint`, `gopls`, ffmpeg, and SQLite):

```sh
direnv allow
make build
```

Without Nix: install Go 1.26 or newer plus the media tools above, then use the
same `make` targets. `make lint` wraps `golangci-lint` in `nix develop`.

```sh
make fmt      # go fmt ./...
make lint     # golangci-lint run ./...
make build    # bin/anvild and bin/anvilctl
go run ./cmd/anvild --config examples/anvil.toml
```

Flake outputs: `packages.default` (both binaries), `packages.anvild` (wrapped
with its media tools), `packages.anvilctl` (standalone, deliberately unwrapped),
`apps.*`, and `nixosModules.anvil`. Bump `vendorHash` in `flake.nix` when Go
dependencies change.

Run `make fmt && make lint` before opening a pull request; add `make build` when
entrypoints or package wiring changed. [`AGENTS.md`](AGENTS.md) covers branch,
commit, and code conventions.

### Contributing

Found a bug or have an idea?
[Open an issue](https://github.com/zekurio/anvil/issues/new).

### License

[MIT](LICENSE)

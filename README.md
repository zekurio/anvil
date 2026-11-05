# Anvil

Anvil is a Linux daemon that keeps media libraries encoded in AV1. It scans
the libraries you configure, picks a CRF for each file by encoding and scoring
samples with FFmpeg, runs the final `ffmpeg` encode itself, and publishes the
result. In a media library the encode replaces the source in place. In a
download library it goes to an import directory.

Linux is a hard requirement. Anvil watches the filesystem with inotify, holds a
`flock` so a second daemon can't open the same store, takes operator commands
over a Unix socket, and cancels work by killing whole process groups.

There are two binaries. `anvild` is the service. `anvilctl` is the operator
client, the way `systemctl` is for systemd. `anvilctl` never opens the database
or runs a media tool. It asks the running daemon instead, because a second
writer on a live store is how half-published files happen.

Anvil is early and changes often. Expect sharp edges.

## Deployment

The NixOS module is the supported way to deploy. Add Anvil to your flake
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

`services.anvil.settings` takes the same snake_case keys as the reference TOML.
The module renders `/etc/anvil/anvil.toml`, creates `/var/lib/anvil`, puts the
control socket in `RuntimeDirectory`, and adds Jellyfin's ffmpeg to the service
PATH. The socket is `0660` inside a `0750` directory owned by
`services.anvil.group`. Membership in that group is what grants operator
access. Installing `anvilctl` grants nothing.

Without Nix, install `ffmpeg` and `ffprobe` built with your encoder and with
the `libvmaf` or `xpsnr` filter, then run
`anvild --config /etc/anvil/anvil.toml`.
[`examples/anvil.toml`](examples/anvil.toml) is a minimal starting point.
[`examples/anvil-reference.toml`](examples/anvil-reference.toml) documents
every setting for the daemon, profiles, Arrs, and media and download libraries.

Check a config before you start anything:

```sh
anvild check-config --config examples/anvil.toml
anvild preflight --config examples/anvil.toml --library movies --limit 20
```

`anvild check-config --config PATH --show` prints the effective config with
defaults filled in and secrets redacted.

Both commands are local and read-only. Anything that touches live state is in
`anvilctl`. `SIGHUP` reloads libraries, profiles, and most daemon settings in
place. Changing `store_path`, `temp_dir`, or `control_socket` needs a restart.

## CRF search

Anvil looks for the highest integer CRF between `video.crf_min` and
`video.crf_max` that still meets `video.target`. It measures the upper
endpoint first, then the midpoint. After that it estimates the next CRF from
the last two scores. Estimates get a fixed budget, the number of bisections the
original range would need. A flat or reversed slope, or a spent budget, falls
back to plain bisection. Good estimates converge fast, and a bad score curve
still can't drag the search through every CRF.

The search assumes quality and size both drop as CRF rises, and it only
accepts CRFs it actually measured. The winner also has to meet
`video.min_savings_percent`. If nothing fits, Anvil copies the video stream.
With `force_encode_on_no_fit = true` it also searches the size boundary, then
picks the best measured quality within the size limit, or the best measured
quality overall if no candidate fits the limit. A failed process or score always
fails the attempt, in forced mode too.

Samples are evenly spaced. `samples = 0` takes one sample per 12 minutes of
input, rounded up, and `sample_duration = "20s"` sets their length. When the
samples would cover 85% or more of the input, Anvil scores the whole video once
instead. It cuts clips at seekable keyframes, so a clip can include preroll.
Every candidate reuses the same clips and the same encoder settings as the
final encode. Anvil regenerates sample timestamps at the reported frame rate,
or 25 fps when that is unknown, so cutting reordered GOPs leaves no gaps. Final
encodes keep the source timeline and pass every source frame to the encoder,
so duplicate-timestamp and VFR sources come out full length. Each encoded clip
must decode to the same frame count as its reference.

VMAF uses ab-av1's default model and scaling. Sources smaller than 1728x972
get bicubic upscaling to 1080p. Sources larger than 2560x1440 use the
`vmaf_4k_v0.6.1` model, and those smaller than 3456x1944 get upscaled to 4K.
That way a VMAF target means the same thing at every resolution. XPSNR takes
the lowest of the Y, U, and V plane averages FFmpeg reports, then the
arithmetic mean across samples. Comparisons pair frames by index at fixed
analysis rates, 25 fps for VMAF and 60 fps for XPSNR. FFmpeg reports infinite
XPSNR for identical images, and Anvil records that as 100 so results stay valid
JSON. There is no HDR tone mapping and no HDR-specific metric model.

Savings compare the total bytes of the encoded clips with the reference clips.
Both are video-only Matroska clips, container overhead included. Audio,
subtitles, and attachments don't count. Sample estimates guarantee neither
whole-file quality nor final file size. The pipeline checkpoint stores every
candidate CRF, score, and size ratio, and each command writes a per-attempt
process log. An interrupted search starts over. A finished one resumes from its
checkpoint.

### Moving off ab-av1

The native search replaces ab-av1 completely. Remove `ab_av1_args` from base
profiles and overrides, even where it is empty. Move encoder options to
`ffmpeg_args` as option/value pairs, for example
`["-svtav1-params", "tune=0:film-grain=8"]`. They now apply to both the search
and the final encode. Anvil owns codec, preset, CRF, pixel format, threads,
stream mapping, filters, and timing, and rejects overrides for any of them.
That includes `crf=`, `qp=`, `preset=`, and `rc=` keys inside
`-svtav1-params`, `-x265-params`, or `-x264-params`. VMAF targets keep their
ab-av1 calibration, since the search uses the same resolution-dependent scaling
and 4K model. Use `samples` and `sample_duration` for sampling. Other ab-av1
options have no automatic translation. Upgrading invalidates existing
checkpoints.

## Operating

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

Name a job by its numeric id or its slug. `--json`, or `-j`, works globally and
per command. Exit status is `0` on success, `1` when the command failed, `2`
for a usage error, `3` when the daemon is unreachable or speaks a different
protocol version, and `4` when something was not found. `--socket` or
`ANVIL_CONTROL_SOCKET` overrides the default `/run/anvil/anvild.sock`.

Human output is styled with Lip Gloss. You get color on a terminal and plain
text when the output is redirected or `NO_COLOR=1` is set. Job listings group
by library, with a count and one entry per job. Each entry shows the job's
numeric id, state, update time, input filename, and last error if it has one.
Long filenames and errors wrap to the terminal width. `show JOB` prints full
paths and slugs, and `--json` is the format for scripts. JSON fields and exit
codes don't change with the human presentation. Listings print times in the
client's local timezone, while JSON keeps the daemon's timestamps.

```sh
anvilctl jobs --state pending,failed --json
anvilctl jobs --absolute-path '/mnt/media/converted/Release/Episode.mkv'
anvilctl cancel --library usenet-tv --state pending,running
anvilctl retry --failed --library movies
anvilctl prune --library movies --state complete,failed,canceled --apply
anvilctl staging cleanup --older-than 24h --dry-run
anvilctl backup /srv/backups/anvil-$(date +%F).db
```

Anvil only cleans up legacy output artifacts when you ask.
`anvilctl staging cleanup --older-than 24h --legacy-parts --dry-run` lists old
unscoped `.mkv.anvil-part` files in configured output libraries. Run it again
without `--dry-run` to delete the eligible ones. It keeps current job parts and
anything a publish journal still holds. Job startup no longer lists destination
directories to do this.

The scheduler wakes when work arrives or a worker finishes. Its timer is only a
fallback. `daemon.max_threads_per_job = 0` splits the thread budget across
worker slots, so jobs that arrive later can still start. A positive value sets
a fixed per-job cap. A running job keeps its allocation until it ends.

`cancel` needs a selector that narrows the set. It refuses a job whose publish
is already journaled, because only the daemon can finish that destination
safely. Job pruning and staging cleanup skip anything active or holding an
unresolved publish journal too, and list it under `protected_jobs`.

Anvil writes each encode on the destination filesystem as
`<name>.mkv.job-<id>.anvil-part`. After the validate step it links the file into
place under its final name. Publishing never copies across filesystems, and the
destination never appears half-written. Media libraries put the part file next
to its destination. Download libraries put it under
`<handoff_path>/.anvil-work` and create the package directory only at publish
time. An importer that sweeps a package directory after taking one episode
can't delete a sibling job's unpublished artifact that way. When a crashed
attempt leaves a part file behind, Anvil removes it once the job retries or is
canceled. Media scanners and Arrs ignore the suffix in the meantime. `temp_dir`
only holds scratch that never gets published, which means search samples and
process logs.

## Development

Deployment needs Linux because the scanner depends on inotify. Everyday work
runs on macOS as well: building, linting, `gopls`, and running `anvilctl`. With
[Nix](https://nixos.org/) and [direnv](https://direnv.net/), the dev shell
brings Go, `golangci-lint`, `gopls`, ffmpeg, and SQLite:

```sh
direnv allow
make build
```

Without Nix, install Go 1.26 or newer plus the media tools above and use the
same `make` targets. `make lint` runs `golangci-lint` inside `nix develop`.

```sh
make fmt      # go fmt ./...
make lint     # golangci-lint run ./...
make build    # bin/anvild and bin/anvilctl
go run ./cmd/anvild --config examples/anvil.toml
```

The flake exposes `packages.default` with both binaries, `packages.anvild`
wrapped with its media tools, `packages.anvilctl` standalone and deliberately
unwrapped, `apps.*`, and `nixosModules.anvil`. Bump `vendorHash` in `flake.nix`
when Go dependencies change.

Run `make fmt && make lint` before opening a pull request. Add `make build`
when you changed entrypoints or package wiring. [`AGENTS.md`](AGENTS.md) covers
branch, commit, and code conventions.

## Contributing

Found a bug or have an idea?
[Open an issue](https://github.com/zekurio/anvil/issues/new).

## License

[MIT](LICENSE)

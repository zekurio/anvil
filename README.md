# Anvil

A Linux-only daemon that keeps media libraries encoded in AV1. Anvil relies on
Linux inotify, `flock`, Unix-domain sockets, and POSIX process groups for safe
filesystem monitoring, singleton ownership, operator control, and process-tree
cancellation. It scans configured libraries, delegates quality search to
`ab-av1 crf-search`, owns the final `ffmpeg` command, and publishes results by
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
  };

  users.users.alice.extraGroups = [ "anvil" ];
}
```

The module renders `/etc/anvil/anvil.toml`, creates `/var/lib/anvil`, exposes
the control socket from `RuntimeDirectory`, and adds Jellyfin ffmpeg, `ab-av1`,
`dovi-tool`, and MKVToolNix to the service PATH. The socket is `0660` inside a
`0750` directory owned by `services.anvil.group`, so group membership — not
having `anvilctl` installed — is what grants operator access.

Without Nix, install `ffmpeg`/`ffprobe`, `ab-av1`, `dovi_tool`, and MKVToolNix,
then run `anvild --config /etc/anvil/anvil.toml`.
[`examples/anvil.toml`](examples/anvil.toml) is the minimal quick-start;
[`examples/anvil-reference.toml`](examples/anvil-reference.toml) documents every
setting, including daemon, flows, profiles, Arrs, and media and download
libraries. Validate a config before starting anything:

```sh
anvild check-config --config examples/anvil.toml
anvild preflight --config examples/anvil.toml --library movies --limit 20
```

`anvild check-config --config PATH --show` prints the effective config with
defaults applied and secrets redacted.

Both are local and read-only; every command that touches live state lives in
`anvilctl`. `SIGHUP` reloads libraries, flows, profiles, and most daemon
settings in place; `store_path`, `temp_dir`, and `control_socket` require a
restart.

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
anvilctl staging cleanup [--older-than D] [--dry-run]
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

`cancel` requires a narrowing selector, and refuses a job whose publish is
already journaled — only the daemon can finish that destination safely. Job
pruning and staging cleanup likewise skip anything active or holding an
unresolved publish journal, and report it under `protected_jobs`.

Encodes are written next to their publish destination as
`<name>.mkv.anvil-part` and linked into place under the final name once
validated, so publishing never copies the file across filesystems and the
destination only ever appears complete. A part file left behind by a crashed
attempt is removed when the job retries; media scanners and Arrs ignore the
suffix. `temp_dir` only holds scratch that never publishes: search samples,
Dolby Vision intermediates, and process logs.

### Development

Deployment targets Linux only (the scanner relies on inotify), but day-to-day
development — building, linting, `gopls`, running `anvilctl` — also works on
macOS. With [Nix](https://nixos.org/) and [direnv](https://direnv.net/)
(provides Go, `golangci-lint`, `gopls`, ffmpeg, `ab-av1`, `dovi-tool`,
MKVToolNix, and SQLite):

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

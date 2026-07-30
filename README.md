# Anvil

A Linux-first daemon that keeps media libraries encoded in AV1. Anvil scans
configured libraries, delegates quality search to `ab-av1 crf-search`, owns the
final `ffmpeg` command, and publishes results by replacing the source in place
or handing off finished downloads to an import directory.

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
then run `anvild --config /etc/anvil/anvil.toml`. See
[`examples/anvil.toml`](examples/anvil.toml) for the full reference config:
`[daemon]`, `[flows.*]`, `[profiles.*]`, `[arrs.*]`, and `[libraries.*]` for
media and download libraries. Validate a config before starting anything:

```sh
anvild check-config --config examples/anvil.toml
anvild preflight --config examples/anvil.toml --library movies --limit 20
```

Both are local and read-only; every command that touches live state lives in
`anvilctl`. `SIGHUP` reloads libraries, flows, profiles, and most daemon
settings in place; `store_path`, `temp_dir`, and `control_socket` require a
restart.

### Operating

```
anvilctl status                                  daemon state, workers, queue counts
anvilctl job list|show|cancel|retry|prune|recover
anvilctl library scan|stats
anvilctl occurrence force --library NAME PATH
anvilctl staging cleanup
anvilctl store backup DESTINATION
```

Jobs are addressed by numeric id or slug. `--json` works globally and per
command. Exit status is `0` success, `1` command failed, `2` usage error, `3`
daemon unreachable or protocol mismatch, `4` not found. `--socket` or
`ANVIL_CONTROL_SOCKET` overrides the default `/run/anvil/anvild.sock`.

```sh
anvilctl job list --state pending,failed --json
anvilctl job list --absolute-path '/mnt/media/converted/Release/Episode.mkv'
anvilctl job cancel --library usenet-tv --state pending,running
anvilctl job retry --failed --library movies
anvilctl job prune --library movies --state complete,failed,canceled --apply
anvilctl staging cleanup --older-than 24h --dry-run
anvilctl store backup /srv/backups/anvil-$(date +%F).db
```

`job cancel` requires a narrowing selector, and refuses a job whose publish is
already journaled — only the daemon can finish that destination safely. Job
pruning and staging cleanup likewise skip anything active or holding an
unresolved publish journal, and report it under `protected_jobs`.

The old `anvild` subcommands (`scan`, `jobs`, `stats`, `inspect`, `retry`,
`recover`, `cleanup-staging`, `backup`, `prune-jobs`, `force-occurrence`) still
work as `anvilctl` aliases and no longer take `--config`: they act on the
configuration the daemon actually accepted.

### Development

With [Nix](https://nixos.org/) and [direnv](https://direnv.net/) (provides Go,
`golangci-lint`, `gopls`, ffmpeg, `ab-av1`, `dovi-tool`, MKVToolNix, and
SQLite):

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

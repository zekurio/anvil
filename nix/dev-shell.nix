# The development shell: Go, its linters, Node.js for the web UI, ffmpeg, and
# SQLite from the same nixpkgs the packages build with. `nix develop` enters
# it; `.envrc` does the same through direnv.
{
  lib,
  stdenv,
  mkShell,
  go,
  golangci-lint,
  gopls,
  gotools,
  nodejs_24,
  ffmpeg,
  jellyfin-ffmpeg ? null,
  sqlite-interactive,
  gnumake,
  git,
  curl,
  jq,
  nixd,
  nil,
}:
let
  # jellyfin-ffmpeg does not build on darwin; the shell falls back to plain
  # ffmpeg there. The packaged daemon only ships on Linux and always gets
  # jellyfin-ffmpeg.
  ffmpegPackage = if stdenv.isLinux && jellyfin-ffmpeg != null then jellyfin-ffmpeg else ffmpeg;
in
mkShell {
  packages = [
    go
    golangci-lint
    gopls
    gotools
    # Keep in step with the Node.js the flake's web package uses.
    nodejs_24
    ffmpegPackage
    sqlite-interactive
    gnumake
    git
    curl
    jq
    nixd
    nil
  ];
}

{ pkgs, lib, ... }:

let
  goPackage = pkgs.go;
  # jellyfin-ffmpeg does not build on darwin; the dev shell falls back to
  # plain ffmpeg there. The packaged daemon only ships on Linux and always
  # gets jellyfin-ffmpeg.
  ffmpegPackage =
    if pkgs.stdenv.isLinux then
      (pkgs.jellyfin-ffmpeg or pkgs.ffmpeg)
    else
      pkgs.ffmpeg;
  devPackages =
    with pkgs;
    [
      goPackage
      ab-av1
      coreutils
      curl
      ffmpegPackage
      git
      gnumake
      golangci-lint
      gopls
      gotools
      jq
      sqlite-interactive
    ];
in

{
  languages.go = {
    enable = true;
    package = goPackage;
  };

  packages = devPackages;

  enterShell = ''
    export PATH="${lib.makeBinPath devPackages}:$PATH"
    echo "Anvil development shell"
    go version
  '';
}

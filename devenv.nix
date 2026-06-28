{ pkgs, lib, ... }:

let
  goPackage = pkgs.go;
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
      dovi-tool
      ffmpegPackage
      git
      gnumake
      golangci-lint
      gopls
      gotools
      jq
      mkvtoolnix
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

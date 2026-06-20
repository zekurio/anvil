{ pkgs, lib, ... }:

{
  languages.go = {
    enable = true;
    package = pkgs.go;
  };

  packages =
    with pkgs;
    [
      ffmpeg
      git
      golangci-lint
      gopls
      gotools
      sqlite
    ]
    ++ lib.optionals (lib.hasAttr "ab-av1" pkgs) [
      pkgs.ab-av1
    ];

  enterShell = ''
    echo "Anvil development shell"
    go version
  '';
}

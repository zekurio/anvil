{
  description = "Media-library AV1 encoding daemon";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    devenv.url = "github:cachix/devenv";
  };

  outputs =
    inputs@{ self, nixpkgs, devenv, ... }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];

      forEachSystem =
        f:
        nixpkgs.lib.genAttrs systems (
          system:
          f {
            inherit system;
            pkgs = nixpkgs.legacyPackages.${system};
          }
        );
    in
    {
      nixosModules.default = import ./nix/modules/anvil.nix;
      nixosModules.anvil = import ./nix/modules/anvil.nix;

      packages = forEachSystem (
        { pkgs, ... }:
        let
          ffmpegPackage =
            if pkgs.stdenv.isLinux then
              (pkgs.jellyfin-ffmpeg or pkgs.ffmpeg)
            else
              pkgs.ffmpeg;
          runtimePackages = [
            ffmpegPackage
            pkgs.ab-av1
            pkgs.dovi-tool
            pkgs.mkvtoolnix
          ];
        in
        {
          default = pkgs.buildGoModule {
            pname = "anvil";
            version = "0.1.0";
            src = ./.;
            vendorHash = "sha256-0j1IhNahvM035aJzfog14r2RVWS7i1LQb/1MQ9/tIog=";
            nativeBuildInputs = [
              pkgs.makeWrapper
            ];

            subPackages = [
              "cmd/anvild"
              "cmd/anvil-mockarr"
            ];

            ldflags = [
              "-s"
              "-w"
            ];

            postInstall = ''
              wrapProgram "$out/bin/anvild" \
                --prefix PATH : "${pkgs.lib.makeBinPath runtimePackages}"
            '';

            meta = {
              description = "Media-library AV1 encoding daemon";
              mainProgram = "anvild";
              platforms = pkgs.lib.platforms.unix;
            };
          };
        }
      );

      apps = forEachSystem (
        { system, ... }:
        {
          default = {
            type = "app";
            program = "${self.packages.${system}.default}/bin/anvild";
          };
          anvild = {
            type = "app";
            program = "${self.packages.${system}.default}/bin/anvild";
          };
        }
      );

      devShells = forEachSystem (
        { pkgs, ... }:
        {
          default = devenv.lib.mkShell {
            inherit inputs pkgs;
            modules = [
              ./devenv.nix
            ];
          };
        }
      );
    };
}

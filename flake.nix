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
          version = "0.1.0";
          vendorHash = "sha256-0j1IhNahvM035aJzfog14r2RVWS7i1LQb/1MQ9/tIog=";
          ldflags = [
            "-s"
            "-w"
            "-X github.com/zekurio/anvil/pkg/controlapi.BuildVersion=${version}"
          ];
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
        rec {
          default = anvil;

          # anvil is the full build: the daemon wrapped with its runtime tools,
          # the control client, and the smoke-test Arr stub.
          anvil = pkgs.buildGoModule {
            pname = "anvil";
            inherit version vendorHash ldflags;
            src = ./.;
            nativeBuildInputs = [
              pkgs.makeWrapper
            ];

            subPackages = [
              "cmd/anvild"
              "cmd/anvilctl"
              "cmd/anvil-mockarr"
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

          # anvild is the service binary on its own, still wrapped with the
          # media tools it actually runs.
          anvild = pkgs.buildGoModule {
            pname = "anvild";
            inherit version vendorHash ldflags;
            src = ./.;
            nativeBuildInputs = [ pkgs.makeWrapper ];
            subPackages = [ "cmd/anvild" ];

            postInstall = ''
              wrapProgram "$out/bin/anvild" \
                --prefix PATH : "${pkgs.lib.makeBinPath runtimePackages}"
            '';

            meta = {
              description = "Anvil AV1 encoding daemon";
              mainProgram = "anvild";
              platforms = pkgs.lib.platforms.unix;
            };
          };

          # anvilctl is deliberately standalone: it talks to the daemon over a
          # Unix socket and never opens SQLite or runs ffmpeg, so wrapping it
          # with the media toolchain would pull hundreds of megabytes into every
          # operator's profile for nothing.
          anvilctl = pkgs.buildGoModule {
            pname = "anvilctl";
            inherit version vendorHash ldflags;
            src = ./.;
            subPackages = [ "cmd/anvilctl" ];

            meta = {
              description = "Operator control client for the Anvil daemon";
              mainProgram = "anvilctl";
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
            program = "${self.packages.${system}.anvild}/bin/anvild";
          };
          anvild = {
            type = "app";
            program = "${self.packages.${system}.anvild}/bin/anvild";
          };
          anvilctl = {
            type = "app";
            program = "${self.packages.${system}.anvilctl}/bin/anvilctl";
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

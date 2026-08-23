{
  description = "Media-library AV1 encoding daemon";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    devenv.url = "github:cachix/devenv";
  };

  outputs =
    inputs@{ self, nixpkgs, devenv, ... }:
    let
      # The daemon and its packages only support Linux, but the development
      # shell also evaluates on darwin so maintainers on macOS can build,
      # lint, and run the operator tooling.
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      devSystems = systems ++ [
        "x86_64-darwin"
        "aarch64-darwin"
      ];

      forEachSystem =
        systemList: f:
        nixpkgs.lib.genAttrs systemList (
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

      packages = forEachSystem systems (
        { pkgs, ... }:
        let
          version = "0.1.0";
          vendorHash = "sha256-MQjXQsq+k6OmLMZLNwGGC8K5pu1tNxo7uIXjIPGLPIo=";
          ldflags = [
            "-s"
            "-w"
            "-X github.com/zekurio/anvil/pkg/control.BuildVersion=${version}"
          ];
          ffmpegPackage = pkgs.jellyfin-ffmpeg or pkgs.ffmpeg;
          runtimePackages = [
            ffmpegPackage
            pkgs.ab-av1
          ];
        in
        rec {
          default = anvil;

          # anvil is the full build: the daemon wrapped with its runtime tools
          # together with the standalone control client.
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
            ];

            postInstall = ''
              wrapProgram "$out/bin/anvild" \
                --prefix PATH : "${pkgs.lib.makeBinPath runtimePackages}"
            '';

            meta = {
              description = "Media-library AV1 encoding daemon";
              mainProgram = "anvild";
              platforms = pkgs.lib.platforms.linux;
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
              platforms = pkgs.lib.platforms.linux;
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
              platforms = pkgs.lib.platforms.linux;
            };
          };
        }
      );

      apps = forEachSystem systems (
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

      devShells = forEachSystem devSystems (
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

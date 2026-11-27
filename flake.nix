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
          inherit (pkgs) lib;
          fs = lib.fileset;

          version = "0.1.0";
          vendorHash = "sha256-uv+9IeDHGzjfCaAVUQpERaBFw/E4M1MNyn7ubmWIyKU=";
          ldflags = [
            "-s"
            "-w"
            "-X github.com/zekurio/anvil/pkg/control.BuildVersion=${version}"
          ];
          ffmpegPackage = pkgs.jellyfin-ffmpeg or pkgs.ffmpeg;
          runtimePackages = [
            ffmpegPackage
          ];
          # Keep in step with the Node.js in devenv.nix so the packaged UI and
          # a local `make web` come from the same toolchain.
          nodejs = pkgs.nodejs_24;

          # Two filtered sources. Editing docs, examples, or the Nix module
          # rebuilds nothing, a frontend edit leaves anvilctl alone, and a Go
          # edit reuses the built frontend. goSrc is an allow-list, so add any
          # new top-level Go directory to it.
          goSrc = fs.toSource {
            root = ./.;
            fileset = fs.unions [
              ./go.mod
              ./go.sum
              ./cmd
              ./internal
              ./pkg
              # From web/, Go needs only the package that embeds web/dist. The
              # placeholder keeps that package compiling where no UI is
              # copied in.
              (fs.fileFilter (file: file.hasExt "go") ./web)
              ./web/dist/.gitkeep
            ];
          };
          webSrc = fs.toSource {
            root = ./web;
            # Everything under web/ except what cannot affect the bundle. A
            # checkout that is not a git tree may carry a local build and
            # node_modules, and neither belongs in the sandbox.
            fileset = fs.difference ./web (
              fs.unions [
                (fs.fileFilter (file: file.hasExt "go" || file.hasExt "md") ./web)
                (fs.maybeMissing ./web/dist)
                (fs.maybeMissing ./web/node_modules)
              ]
            );
          };

          # web is the built frontend, exactly the tree the daemon embeds.
          # importNpmLock reads web/package-lock.json and fetches each tarball
          # against the lockfile's integrity hash, so a JS dependency change
          # needs no hash bump here.
          web = pkgs.stdenvNoCC.mkDerivation {
            pname = "anvil-web";
            inherit version;
            src = webSrc;

            npmDeps = pkgs.importNpmLock { npmRoot = ./web; };
            nativeBuildInputs = [
              nodejs
              pkgs.importNpmLock.npmConfigHook
            ];

            buildPhase = ''
              runHook preBuild
              npm run build
              runHook postBuild
            '';

            installPhase = ''
              runHook preInstall
              cp -r dist "$out"
              runHook postInstall
            '';

            meta = {
              description = "Web UI embedded in the Anvil daemon";
              platforms = lib.platforms.linux;
            };
          };

          # web/embed.go embeds web/dist, so the built frontend replaces the
          # placeholder before Go compiles. This runs in preConfigure because
          # buildGoModule copies preBuild and postPatch into the vendoring
          # derivation, which would then wait on the frontend for nothing.
          embedWeb = ''
            rm -rf web/dist
            cp -r --no-preserve=mode ${web} web/dist
            test -f web/dist/index.html
          '';
          wrapDaemon = ''
            wrapProgram "$out/bin/anvild" \
              --prefix PATH : "${lib.makeBinPath runtimePackages}"
          '';
        in
        rec {
          default = anvil;
          inherit web;

          # anvil is the full build: the daemon wrapped with its runtime tools
          # together with the standalone control client.
          anvil = pkgs.buildGoModule {
            pname = "anvil";
            inherit version vendorHash ldflags;
            src = goSrc;
            nativeBuildInputs = [
              pkgs.makeWrapper
            ];

            subPackages = [
              "cmd/anvild"
              "cmd/anvilctl"
            ];

            preConfigure = embedWeb;
            postInstall = wrapDaemon;

            meta = {
              description = "Media-library AV1 encoding daemon";
              mainProgram = "anvild";
              platforms = lib.platforms.linux;
            };
          };

          # anvild is the service binary on its own, still wrapped with the
          # media tools it actually runs.
          anvild = pkgs.buildGoModule {
            pname = "anvild";
            inherit version vendorHash ldflags;
            src = goSrc;
            nativeBuildInputs = [ pkgs.makeWrapper ];
            subPackages = [ "cmd/anvild" ];

            preConfigure = embedWeb;
            postInstall = wrapDaemon;

            meta = {
              description = "Anvil AV1 encoding daemon";
              mainProgram = "anvild";
              platforms = lib.platforms.linux;
            };
          };

          # anvilctl is deliberately standalone: it talks to the daemon over a
          # Unix socket and never opens SQLite or runs ffmpeg, so wrapping it
          # with the media toolchain would pull hundreds of megabytes into every
          # operator's profile for nothing. It does not serve the web UI
          # either, so it must not depend on the web derivation.
          anvilctl = pkgs.buildGoModule {
            pname = "anvilctl";
            inherit version vendorHash ldflags;
            src = goSrc;
            subPackages = [ "cmd/anvilctl" ];

            meta = {
              description = "Operator control client for the Anvil daemon";
              mainProgram = "anvilctl";
              platforms = lib.platforms.linux;
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

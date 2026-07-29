# Evaluation check for the NixOS module's two boundaries.
#
# Both are silent when they break. Installing services.anvil.package as "the
# control client" puts the ffmpeg-wrapped daemon in every operator's PATH and
# nothing about it looks wrong; and the group the control socket grants access
# through has to exist before systemd can run the service as it, which only
# shows up as a permission error long after deployment.
#
# The packages here are stand-ins: the check is about which package the module
# selects, not about building Anvil.
{
  pkgs,
  module,
}:

let
  inherit (pkgs) lib;
  daemonPackage = pkgs.hello;
  clientPackage = pkgs.coreutils;

  evalSystem =
    settings:
    import (pkgs.path + "/nixos/lib/eval-config.nix") {
      system = "x86_64-linux";
      modules = [
        module
        {
          boot.loader.grub.enable = false;
          fileSystems."/" = {
            device = "/dev/null";
            fsType = "ext4";
          };
          system.stateVersion = "24.05";
          nixpkgs.hostPlatform = "x86_64-linux";
        }
        { services.anvil = settings; }
      ];
    };

  defaults = evalSystem {
    enable = true;
    package = daemonPackage;
    group = "anvil";
  };

  deliberate = evalSystem {
    enable = true;
    package = daemonPackage;
    group = "anvil";
    createGroup = true;
    controlClient.install = true;
    controlClient.package = clientPackage;
  };

  installWithoutAPackage = builtins.tryEval (
    builtins.deepSeq
      (evalSystem {
        enable = true;
        package = daemonPackage;
        controlClient.install = true;
      }).config.environment.systemPackages
      null
  );

  expectations = {
    "the defaults install no Anvil package system-wide" =
      !(lib.elem daemonPackage defaults.config.environment.systemPackages)
      && !(lib.elem clientPackage defaults.config.environment.systemPackages);
    "the defaults create no group" = !(defaults.config.users.groups ? anvil);
    "naming the client installs it" =
      lib.elem clientPackage deliberate.config.environment.systemPackages;
    "the daemon package is never installed as the client" =
      !(lib.elem daemonPackage deliberate.config.environment.systemPackages);
    "createGroup creates the access group" = deliberate.config.users.groups ? anvil;
    "the control socket directory stays 0750" =
      deliberate.config.systemd.services.anvil.serviceConfig.RuntimeDirectoryMode == "0750";
    "installing the client without naming a package is refused" = !installWithoutAPackage.success;
  };

  failures = lib.attrNames (lib.filterAttrs (_name: held: !held) expectations);
in
if failures == [ ] then
  pkgs.runCommand "anvil-module-boundaries" { } "touch $out"
else
  throw "Anvil NixOS module check failed: ${lib.concatStringsSep "; " failures}"

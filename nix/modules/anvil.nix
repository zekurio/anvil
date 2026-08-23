{ config, lib, pkgs, ... }:

let
  cfg = config.services.anvil;
  inherit (lib)
    literalExpression
    mkEnableOption
    mkIf
    mkOption
    optional
    optionalAttrs
    types
    ;

  format = pkgs.formats.toml { };
  daemonDefaults = {
    temp_dir = "/var/lib/anvil/tmp";
    store_path = "/var/lib/anvil/anvil.db";
    control_socket = "/run/anvil/anvild.sock";
  };
  daemon = daemonDefaults // (cfg.settings.daemon or { });
  libraries = cfg.settings.libraries or { };
  generatedConfig = cfg.settings // { inherit daemon; };
  generatedConfigFile = format.generate "anvil.toml" generatedConfig;

  packageExe = if cfg.package == null then "${pkgs.coreutils}/bin/false" else lib.getExe cfg.package;
  controlClientPackage = cfg.controlClient.package;
  ffmpegPackage = pkgs.jellyfin-ffmpeg or pkgs.ffmpeg;
  storeDirectory = builtins.dirOf daemon.store_path;
  controlSocketDirectory = builtins.dirOf daemon.control_socket;
  daemonDirectoryPaths = lib.unique [
    daemon.temp_dir
    storeDirectory
    controlSocketDirectory
  ];
  tmpfilesUser = if cfg.user == null then "root" else cfg.user;
  tmpfilesGroup = if cfg.group == null then "root" else cfg.group;
  libraryWritePaths = lib.flatten (
    lib.mapAttrsToList (
      _name: library:
      optional (library ? path && library.path != "") library.path
    ) libraries
  );
  handoffWritePaths = lib.flatten (
    lib.mapAttrsToList (
      _name: library:
      let
        download = library.download or { };
      in
      optional (
        (library.kind or "media") == "download"
        && download ? handoff_path
        && download.handoff_path != ""
      ) download.handoff_path
    ) libraries
  );
  readWritePaths = lib.unique (
    daemonDirectoryPaths
    ++ libraryWritePaths
    ++ handoffWritePaths
    ++ cfg.service.extraReadWritePaths
  );
in
{
  options.services.anvil = {
    enable = mkEnableOption "Anvil AV1 encoding daemon";

    package = mkOption {
      type = types.nullOr types.package;
      default = null;
      description = "Anvil package providing anvild. Required when the service is enabled.";
    };

    controlClient = {
      install = mkOption {
        type = types.bool;
        default = false;
        description = ''
          Install the anvilctl control client into environment.systemPackages.
          Access is granted by control-socket permissions, not by installing
          the client. Enable this together with controlClient.package.
        '';
      };

      package = mkOption {
        type = types.nullOr types.package;
        default = null;
        example = literalExpression "inputs.anvil.packages.\${pkgs.system}.anvilctl";
        description = ''
          Standalone package providing anvilctl. Required when install is true.
          The daemon package is not a fallback because it includes the media tools.
        '';
      };

      setEnvironment = mkOption {
        type = types.bool;
        default = true;
        description = "Set ANVIL_CONTROL_SOCKET system-wide when installing anvilctl.";
      };
    };

    runtimePackages = mkOption {
      type = types.listOf types.package;
      default = [
        ffmpegPackage
        pkgs.ab-av1
      ];
      description = "Packages added to the service PATH for media tools.";
    };

    user = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "User that runs the daemon. Null uses the systemd default.";
    };

    group = mkOption {
      type = types.nullOr types.str;
      default = null;
      example = "anvil";
      description = ''
        Group that runs the daemon. The control socket uses this group as its
        access boundary. Null uses the systemd default.
      '';
    };

    createGroup = mkOption {
      type = types.bool;
      default = false;
      description = "Create services.anvil.group as a system group.";
    };

    configFile = mkOption {
      type = types.path;
      readOnly = true;
      default = generatedConfigFile;
      description = "Generated Anvil TOML configuration.";
    };

    settings = mkOption {
      inherit (format) type;
      default = { };
      example = literalExpression ''
        {
          daemon.log_level = "info";
          libraries.movies = {
            path = "/srv/media/movies";
            profile = "default-av1";
          };
        }
      '';
      description = ''
        Anvil settings written as TOML. Use the snake_case keys from the Anvil
        reference config. Anvil validates this data. Prefer api_key_file to
        api_key because literal secrets in Nix enter the Nix store.
      '';
    };

    service = {
      extraReadWritePaths = mkOption {
        type = types.listOf types.str;
        default = [ ];
        description = "More paths that the hardened service can write.";
      };
      extraServiceConfig = mkOption {
        type = types.attrsOf types.anything;
        default = { };
        description = "More systemd serviceConfig attributes for Anvil.";
      };
    };
  };

  config = mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.package != null;
        message = "services.anvil.package must be set when services.anvil.enable is true.";
      }
      {
        assertion = !cfg.controlClient.install || controlClientPackage != null;
        message = ''
          services.anvil.controlClient.package must be set when controlClient.install is true.
          Use the standalone anvilctl package. The daemon package is not a fallback.
        '';
      }
      {
        assertion = !cfg.createGroup || cfg.group != null;
        message = "services.anvil.createGroup requires services.anvil.group.";
      }
    ];

    environment.etc."anvil/anvil.toml".source = cfg.configFile;
    environment.systemPackages = optional cfg.controlClient.install controlClientPackage;
    environment.variables = optionalAttrs (cfg.controlClient.install && cfg.controlClient.setEnvironment) {
      ANVIL_CONTROL_SOCKET = daemon.control_socket;
    };
    users.groups = optionalAttrs (cfg.createGroup && cfg.group != null) {
      ${cfg.group} = { };
    };
    systemd.tmpfiles.rules = map (path: "d ${path} 0750 ${tmpfilesUser} ${tmpfilesGroup} - -") daemonDirectoryPaths;

    warnings = optional (cfg.controlClient.install && cfg.group == null) ''
      services.anvil.controlClient.install is enabled but services.anvil.group is null.
      The control socket ${daemon.control_socket} uses the service's default group.
      Set services.anvil.group and add operators to it.
    '';

    systemd.services.anvil = {
      description = "Anvil AV1 encoding daemon";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      path = cfg.runtimePackages;
      serviceConfig =
        {
          ExecStart = "${packageExe} --config /etc/anvil/anvil.toml";
          Environment = [
            "TEMP=${daemon.temp_dir}"
            "TMP=${daemon.temp_dir}"
            "TMPDIR=${daemon.temp_dir}"
            "XDG_CACHE_HOME=${daemon.temp_dir}/cache"
          ];
          Restart = "on-failure";
          StateDirectory = [ "anvil" "anvil/tmp" ];
          RuntimeDirectory = "anvil";
          RuntimeDirectoryMode = "0750";
          UMask = "0027";
          ReadWritePaths = readWritePaths;
          NoNewPrivileges = true;
          PrivateTmp = true;
          ProtectSystem = "strict";
          ProtectControlGroups = true;
          ProtectKernelLogs = true;
          ProtectKernelModules = true;
          ProtectKernelTunables = true;
          RestrictRealtime = true;
          RestrictSUIDSGID = true;
          LockPersonality = true;
          CapabilityBoundingSet = "";
          SystemCallArchitectures = "native";
        }
        // optionalAttrs (cfg.user != null) { User = cfg.user; }
        // optionalAttrs (cfg.group != null) { Group = cfg.group; }
        // cfg.service.extraServiceConfig;
    };
  };
}

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
  # The store holds the operator's libraries, flows, and Arr connections next
  # to the job history, so it lives in the persistent StateDirectory. temp_dir
  # is scratch and stays in its own subdirectory.
  daemonDefaults = {
    temp_dir = "/var/lib/anvil/tmp";
    store_path = "/var/lib/anvil/anvil.db";
    control_socket = "/run/anvil/anvild.sock";
  };
  declaredMediaRoots = map (path: lib.removePrefix "-" path) cfg.mediaPaths;
  daemon = daemonDefaults
    // optionalAttrs (!((cfg.settings.daemon or { }) ? media_roots)) {
      media_roots = declaredMediaRoots;
    }
    // (cfg.settings.daemon or { });

  # The typed web options render first; settings.web overrides them key by
  # key. Everything below reads the merged table, so the assertion and the
  # firewall rule see what the daemon will see.
  webDefaults =
    {
      enabled = cfg.web.enable;
      listen =
        if lib.hasInfix ":" cfg.web.listenAddress then
          "[${cfg.web.listenAddress}]:${toString cfg.web.port}"
        else
          "${cfg.web.listenAddress}:${toString cfg.web.port}";
      allow_unauthenticated = cfg.web.allowUnauthenticated;
    }
    // optionalAttrs (cfg.web.passwordFile != null) {
      password_file = toString cfg.web.passwordFile;
    };
  web = webDefaults // (cfg.settings.web or { });
  # web-listen.nix reads the address exactly as the daemon does; see there.
  listenAddress = import ./web-listen.nix { inherit lib; };
  webListen = listenAddress.parse web.listen;
  webPort = webListen.port;
  webLoopback = listenAddress.isLoopback webListen.host;
  webHasPassword = (web.password_file or "") != "" || (web.password or "") != "";

  libraries = cfg.settings.libraries or { };
  legacyTables = lib.filter (name: (cfg.settings.${name} or { }) != { }) [
    "libraries"
    "profiles"
    "arrs"
  ];
  generatedConfig = cfg.settings // { inherit daemon web; };
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
  # The store must not live here. /run and /tmp do not survive a reboot or,
  # under PrivateTmp, a service stop, and temp_dir is scratch the operator may
  # wipe.
  storeUnsafeDirectories = [
    "/run"
    "/tmp"
    "/var/tmp"
    "/dev/shm"
    daemon.temp_dir
  ];
  tmpfilesUser = if cfg.user == null then "root" else cfg.user;
  tmpfilesGroup = if cfg.group == null then "root" else cfg.group;
  # Operators add libraries in the web UI at runtime, so the unit cannot learn
  # their paths from the config and the operator declares them in mediaPaths.
  # The legacy settings.libraries tables still contribute theirs, so a config
  # from before the web UI keeps its access.
  legacyLibraryPaths = lib.flatten (
    lib.mapAttrsToList (
      _name: library:
      optional (library ? path && library.path != "") library.path
    ) libraries
  );
  legacyHandoffPaths = lib.flatten (
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
    ++ cfg.mediaPaths
    ++ legacyLibraryPaths
    ++ legacyHandoffPaths
    ++ cfg.service.extraReadWritePaths
  );
in
{
  options.services.anvil = {
    enable = mkEnableOption "Anvil AV1 encoding daemon";

    package = mkOption {
      type = types.nullOr types.package;
      default = null;
      description = ''
        Anvil package providing anvild. Required when the service is enabled.
        The flake's anvild and anvil packages carry the web UI; a daemon built
        without it serves the API only.
      '';
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

    web = {
      enable = mkOption {
        type = types.bool;
        default = true;
        description = ''
          Serve the web UI and its HTTP API. Libraries, flows, and Arr
          connections are managed there. Rendered as web.enabled.
        '';
      };

      listenAddress = mkOption {
        type = types.str;
        default = "127.0.0.1";
        example = "0.0.0.0";
        description = ''
          Address the web UI listens on. Rendered with web.port as web.listen;
          an IPv6 address gets its brackets. Anything other than a loopback
          address needs web.passwordFile, or web.allowUnauthenticated.
        '';
      };

      port = mkOption {
        type = types.port;
        default = 8484;
        description = "TCP port the web UI listens on.";
      };

      passwordFile = mkOption {
        type = types.nullOr types.path;
        default = null;
        example = "/run/secrets/anvil-web-password";
        description = ''
          File holding the single shared web UI password. Rendered as
          web.password_file. The module passes the path through as written and
          the daemon reads the file as the service user, so that user or its
          group must be able to read it. With sops-nix or agenix, set the
          secret's owner or group to services.anvil.user or
          services.anvil.group. Keep the file out of /tmp and /var/tmp, which
          the unit cannot see, and out of the Nix store. Null leaves the web
          UI without a password, which the daemon accepts on a loopback
          address only.
        '';
      };

      allowUnauthenticated = mkOption {
        type = types.bool;
        default = false;
        description = ''
          Allow listening on a non-loopback address without a password.
          Rendered as web.allow_unauthenticated. Anyone who can reach the port
          can then change libraries and flows, so use this only behind a
          reverse proxy that authenticates.
        '';
      };

      openFirewall = mkOption {
        type = types.bool;
        default = false;
        description = "Open the web UI's TCP port in the firewall.";
      };
    };

    mediaPaths = mkOption {
      type = types.listOf types.str;
      default = [ ];
      example = [
        "/srv/media"
        "/srv/downloads"
      ];
      description = ''
        Directories the daemon may write to. List every library root and every
        download library's handoff directory that is or will be configured in
        the web UI. A parent directory covers everything below it.

        The unit runs with ProtectSystem=strict, which leaves the rest of the
        filesystem read-only to it, and the module cannot see what the
        operator adds in the web UI. A library outside these paths can still
        be added and scanned, but its jobs fail with a read-only file system
        or permission error as soon as Anvil writes the encode beside the
        source. The service user also needs ordinary write permission on the
        directories. Changing this list restarts the service.

        Each path must exist when the service starts, or the unit fails.
        Prefix a path with "-" to let it be missing. The module adds the path
        and download.handoff_path of every legacy settings.libraries entry to
        this list.
      '';
    };

    runtimePackages = mkOption {
      type = types.listOf types.package;
      default = [
        ffmpegPackage
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
          daemon.worker_count = 2;
        }
      '';
      description = ''
        Anvil settings written as TOML. Use the snake_case keys from the Anvil
        reference config. Anvil validates this data.

        daemon holds the process settings. Its store_path defaults to
        /var/lib/anvil/anvil.db and must stay on persistent storage, because
        the store holds the libraries, flows, and Arr connections made in the
        web UI. temp_dir defaults to /var/lib/anvil/tmp and only holds scratch.

        The module fills web from services.anvil.web. Keys set here override
        those options one by one.

        libraries, profiles, and arrs are legacy. They still pass through, and
        Anvil imports them into its store once, on the first start. After that
        it ignores them, so later changes here have no effect. Manage
        libraries, flows, and Arr connections in the web UI.

        Prefer web.passwordFile and api_key_file to literal secrets, because
        literal secrets in Nix enter the Nix store.
      '';
    };

    service = {
      extraReadWritePaths = mkOption {
        type = types.listOf types.str;
        default = [ ];
        description = ''
          More paths that the hardened service can write. Library and handoff
          directories belong in services.anvil.mediaPaths.
        '';
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
      {
        assertion = !lib.any (
          directory: lib.hasPrefix "${directory}/" daemon.store_path
        ) storeUnsafeDirectories;
        message = ''
          services.anvil.settings.daemon.store_path is ${daemon.store_path}. That is on
          volatile storage or inside temp_dir, which is ${daemon.temp_dir}. The store holds
          the libraries, flows, and Arr connections made in the web UI. Keep store_path on
          persistent storage and temp_dir separate from it. The defaults are
          ${daemonDefaults.store_path} and ${daemonDefaults.temp_dir}.
        '';
      }
      {
        assertion = !web.enabled || webListen != null;
        message = ''
          The web UI listen address "${web.listen}" is not host:port with a
          port from 0 to 65535 (an IPv6 host needs brackets: "[::1]:8484").
          Set services.anvil.web.listenAddress and web.port, or fix
          services.anvil.settings.web.listen.
        '';
      }
      {
        # Mirrors the daemon's own startup rule so the mistake surfaces at
        # evaluation time rather than as a failed unit.
        assertion =
          !web.enabled
          || webListen == null
          || webLoopback
          || webHasPassword
          || web.allow_unauthenticated;
        message = ''
          The Anvil web UI listens on ${web.listen}, which is not a loopback address, without a password.
          Set services.anvil.web.passwordFile, or set services.anvil.web.allowUnauthenticated
          to accept that anyone who can reach the port controls Anvil.
        '';
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
    networking.firewall.allowedTCPPorts = optional (
      cfg.web.openFirewall && web.enabled && webListen != null
    ) webPort;

    warnings =
      optional (cfg.controlClient.install && cfg.group == null) ''
        services.anvil.controlClient.install is enabled but services.anvil.group is null.
        The control socket ${daemon.control_socket} uses the service's default group.
        Set services.anvil.group and add operators to it.
      ''
      ++ optional (legacyTables != [ ]) ''
        services.anvil.settings sets the legacy ${lib.concatStringsSep ", " legacyTables} tables.
        Anvil imports them into its store once, on the first start, and ignores them
        after that, so later changes to them here have no effect. Manage libraries,
        flows, and Arr connections in the web UI, list the directories they use in
        services.anvil.mediaPaths, and remove these tables once they are imported.
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
          # The store can hold Arr API keys, so the state directory is as
          # closed as the tmpfiles rule and the runtime directory.
          StateDirectoryMode = "0750";
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
          # The web UI binds an unprivileged TCP port, which needs no
          # capability, and the unit sets no PrivateNetwork or IPAddressDeny
          # that would cut it off.
          CapabilityBoundingSet = "";
          # AF_UNIX for the control socket, AF_INET and AF_INET6 for the web
          # UI and Arr calls, AF_NETLINK for the resolver's address lookup.
          RestrictAddressFamilies = [
            "AF_UNIX"
            "AF_INET"
            "AF_INET6"
            "AF_NETLINK"
          ];
          SystemCallArchitectures = "native";
        }
        // optionalAttrs (cfg.user != null) { User = cfg.user; }
        // optionalAttrs (cfg.group != null) { Group = cfg.group; }
        // cfg.service.extraServiceConfig;
    };
  };
}

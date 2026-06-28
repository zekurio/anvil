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

  defaultFlowSteps = [
    "probe"
    "crop-detect"
    "audio-cleanup"
    "stage"
    "crf-search"
    "encode"
    "dovi-fix"
    "validate"
    "replace"
    "cleanup"
  ];

  profileToToml =
    _name: profile:
    {
      inherit (profile) container;
      video = {
        inherit (profile.video) codec preset;
        pixel_format = profile.video.pixelFormat;
        crf_min = profile.video.crfMin;
        crf_max = profile.video.crfMax;
        target_vmaf = profile.video.targetVmaf;
        min_savings_percent = profile.video.minSavingsPercent;
        ffmpeg_args = profile.video.ffmpegArgs;
        ab_av1_args = profile.video.abAv1Args;
        dolby_vision = {
          inherit (profile.video.dolbyVision) mode codec preset;
          pixel_format = profile.video.dolbyVision.pixelFormat;
          ffmpeg_args = profile.video.dolbyVision.ffmpegArgs;
          ab_av1_args = profile.video.dolbyVision.abAv1Args;
          remove_hdr10plus = profile.video.dolbyVision.removeHDR10Plus;
        };
      };
      audio = {
        languages_to_keep = profile.audio.languagesToKeep;
        keep_commentary = profile.audio.keepCommentary;
        fallback = profile.audio.fallback;
        unknown_as_original = profile.audio.unknownAsOriginal;
      };
      subtitles = {
        inherit (profile.subtitles) mode fallback;
        preferred_languages = profile.subtitles.preferredLanguages;
        keep_forced = profile.subtitles.keepForced;
        keep_sdh = profile.subtitles.keepSdh;
        keep_commentary = profile.subtitles.keepCommentary;
        keep_external = profile.subtitles.keepExternal;
        max_tracks = profile.subtitles.maxTracks;
      };
      validation.duration_tolerance_seconds = profile.validation.durationToleranceSeconds;
      metadata.mode = profile.metadataMode;
      attachments.mode = profile.attachmentsMode;
      chapters.mode = profile.chaptersMode;
    };

  libraryToToml =
    _name: library:
    {
      inherit (library) kind path flow profile priority include exclude;
      scan_interval = library.scanInterval;
      concurrency_limit = library.concurrencyLimit;
    }
    // optionalAttrs (library.arr != null) {
      arr = library.arr;
    }
    // optionalAttrs (library.kind == "media") {
      media.replacement_mode = library.media.replacementMode;
    }
    // optionalAttrs (library.kind == "download") {
      download = {
        handoff_path = library.download.handoffPath;
        stable_for = library.download.stableFor;
        package_mode = library.download.packageMode;
        handoff_mode = library.download.handoffMode;
        preserve_relative_path = library.download.preserveRelativePath;
        cleanup_source_media = library.download.cleanupSourceMedia;
        prune_empty_dirs = library.download.pruneEmptyDirs;
        ignorable_globs = library.download.ignorableGlobs;
      };
    };

  arrToToml =
    _name: arr:
    {
      type = arr.type;
    }
    // optionalAttrs (arr.baseUrl != null) { base_url = arr.baseUrl; }
    // optionalAttrs (arr.apiKeyFile != null) { api_key_file = arr.apiKeyFile; };

  generatedConfig = {
    daemon = {
      temp_dir = cfg.daemon.tempDir;
      store_path = cfg.daemon.storePath;
      worker_count = cfg.daemon.workerCount;
      total_threads = cfg.daemon.totalThreads;
      max_attempts = cfg.daemon.maxAttempts;
      scan_interval = cfg.daemon.scanInterval;
      scheduler_interval = cfg.daemon.schedulerInterval;
      lease_duration = cfg.daemon.leaseDuration;
      shutdown_policy = cfg.daemon.shutdownPolicy;
      shutdown_timeout = cfg.daemon.shutdownTimeout;
      staging_cleanup_age = cfg.daemon.stagingCleanupAge;
      log_level = cfg.daemon.logLevel;
    };
    arrs = lib.mapAttrs arrToToml cfg.arrs;
    flows = lib.mapAttrs (_name: flow: { inherit (flow) steps; }) cfg.flows;
    profiles = lib.mapAttrs profileToToml cfg.profiles;
    libraries = lib.mapAttrs libraryToToml cfg.libraries;
  };

  configFile = format.generate "anvil.toml" generatedConfig;
  packageExe = if cfg.package == null then "${pkgs.coreutils}/bin/false" else lib.getExe cfg.package;
  ffmpegPackage =
    if pkgs.stdenv.isLinux then
      (pkgs.jellyfin-ffmpeg or pkgs.ffmpeg)
    else
      pkgs.ffmpeg;
  storeDirectory = builtins.dirOf cfg.daemon.storePath;
  daemonDirectoryPaths = lib.unique [
    cfg.daemon.tempDir
    storeDirectory
  ];
  tmpfilesUser = if cfg.user == null then "root" else cfg.user;
  tmpfilesGroup = if cfg.group == null then "root" else cfg.group;
  libraryWritePaths = lib.mapAttrsToList (_name: library: library.path) cfg.libraries;
  handoffWritePaths = lib.flatten (
    lib.mapAttrsToList (
      _name: library:
      optional (library.kind == "download" && library.download.handoffPath != "") library.download.handoffPath
    ) cfg.libraries
  );
  readWritePaths = lib.unique (
    [
      cfg.daemon.tempDir
      storeDirectory
    ]
    ++ libraryWritePaths
    ++ handoffWritePaths
    ++ cfg.service.extraReadWritePaths
  );

  arrAssertions = lib.flatten (
    lib.mapAttrsToList (
      name: arr:
      [
        {
          assertion = arr.baseUrl != null;
          message = "services.anvil.arrs.${name}.baseUrl is required.";
        }
        {
          assertion = arr.apiKeyFile != null;
          message = "services.anvil.arrs.${name}.apiKeyFile is required.";
        }
      ]
    ) cfg.arrs
  );

  libraryAssertions = lib.flatten (
    lib.mapAttrsToList (
      name: library:
      (optional (library.arr != null) {
        assertion = builtins.hasAttr library.arr cfg.arrs;
        message = "services.anvil.libraries.${name}.arr references unknown arr ${library.arr}.";
      })
      ++ (optional (library.kind == "download") {
        assertion = library.download.handoffPath != "";
        message = "services.anvil.libraries.${name}.download.handoffPath is required for download libraries.";
      })
    ) cfg.libraries
  );

  arrModule = types.submodule {
    options = {
      type = mkOption {
        type = types.enum [
          "radarr"
          "sonarr"
        ];
        description = "Arr provider type.";
      };

      baseUrl = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "http://radarr:7878";
        description = "Base URL for this Arr instance.";
      };

      apiKeyFile = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = literalExpression "config.sops.secrets.radarr-api-key.path";
        description = "Runtime file containing this Arr instance's API key.";
      };
    };
  };

  profileModule = types.submodule {
    options = {
      container = mkOption {
        type = types.enum [ "mkv" ];
        default = "mkv";
        description = "Output container extension. Anvil outputs MKV only.";
      };

      video = {
        codec = mkOption {
          type = types.str;
          default = "libsvtav1";
          description = "ffmpeg video encoder.";
        };
        preset = mkOption {
          type = types.str;
          default = "6";
          description = "Encoder preset.";
        };
        pixelFormat = mkOption {
          type = types.str;
          default = "yuv420p10le";
          description = "Output pixel format.";
        };
        crfMin = mkOption {
          type = types.int;
          default = 18;
          description = "Minimum CRF to test during CRF search.";
        };
        crfMax = mkOption {
          type = types.int;
          default = 40;
          description = "Maximum CRF to test during CRF search.";
        };
        targetVmaf = mkOption {
          type = types.number;
          default = 95;
          description = "Target VMAF for CRF search.";
        };
        minSavingsPercent = mkOption {
          type = types.number;
          default = 20;
          description = "Minimum input-size savings percentage required during CRF search. Written as ab-av1 --max-encoded-percent = 100 - this value.";
        };
        ffmpegArgs = mkOption {
          type = types.listOf types.str;
          default = [ ];
          example = [
            "-svtav1-params"
            "film-grain=8"
          ];
          description = "Extra ffmpeg video encoder arguments appended to Anvil's generated ffmpeg command.";
        };
        abAv1Args = mkOption {
          type = types.listOf types.str;
          default = [ ];
          example = [
            "--enc"
            "lookahead=120"
          ];
          description = "Extra ab-av1 crf-search arguments appended to Anvil's generated search command.";
        };
        dolbyVision = {
          mode = mkOption {
            type = types.enum [
              "auto"
              "off"
              "require"
            ];
            default = "auto";
            description = "How to handle Dolby Vision sources. Auto uses this override only when Dolby Vision is detected and dovi_tool is available.";
          };
          codec = mkOption {
            type = types.str;
            default = "";
            example = "hevc_qsv";
            description = "ffmpeg video encoder used for Dolby Vision sources. Empty disables encoder switching unless mode is require.";
          };
          preset = mkOption {
            type = types.str;
            default = "";
            description = "Dolby Vision encoder preset. Empty keeps the normal video preset.";
          };
          pixelFormat = mkOption {
            type = types.str;
            default = "";
            example = "p010le";
            description = "Dolby Vision output pixel format. Empty keeps the normal video pixel format.";
          };
          ffmpegArgs = mkOption {
            type = types.listOf types.str;
            default = [ ];
            example = [
              "-global_quality"
              "24"
            ];
            description = "Extra ffmpeg video encoder arguments appended only when the Dolby Vision override is selected.";
          };
          abAv1Args = mkOption {
            type = types.listOf types.str;
            default = [ ];
            example = [
              "--enc"
              "low_power=1"
            ];
            description = "Extra ab-av1 crf-search arguments appended only when the Dolby Vision override is selected.";
          };
          removeHDR10Plus = mkOption {
            type = types.bool;
            default = false;
            description = "Pass dovi_tool --drop-hdr10plus during Dolby Vision RPU extraction/injection.";
          };
        };
      };

      audio = {
        languagesToKeep = mkOption {
          type = types.listOf types.str;
          default = [ ];
          example = [
            "orig"
            "deu"
          ];
          description = "Audio languages to keep. The special value \"orig\" uses Arr-derived original language.";
        };
        keepCommentary = mkOption {
          type = types.bool;
          default = false;
          description = "Keep audio tracks detected as commentary.";
        };
        fallback = mkOption {
          type = types.enum [
            "keep_all"
            "keep_first"
            "fail_job"
          ];
          default = "keep_all";
          description = "Fallback when no audio stream matches the configured policy.";
        };
        unknownAsOriginal = mkOption {
          type = types.bool;
          default = false;
          description = "Treat und/unknown track language as the original language.";
        };
      };

      subtitles = {
        mode = mkOption {
          type = types.enum [
            "preserve"
            "prefer"
            "cleanup"
          ];
          default = "preserve";
          description = "Subtitle stream policy.";
        };
        preferredLanguages = mkOption {
          type = types.listOf types.str;
          default = [ ];
          description = "Preferred subtitle languages.";
        };
        keepForced = mkOption {
          type = types.bool;
          default = false;
          description = "Keep forced subtitles.";
        };
        keepSdh = mkOption {
          type = types.bool;
          default = false;
          description = "Keep SDH subtitles.";
        };
        keepCommentary = mkOption {
          type = types.bool;
          default = false;
          description = "Keep commentary subtitles.";
        };
        keepExternal = mkOption {
          type = types.bool;
          default = false;
          description = "Keep external subtitles.";
        };
        maxTracks = mkOption {
          type = types.int;
          default = 0;
          description = "Maximum subtitle tracks to keep. Zero means unlimited.";
        };
        fallback = mkOption {
          type = types.enum [
            "keep_all"
            "keep_first"
            "fail_job"
          ];
          default = "keep_all";
          description = "Subtitle fallback behavior.";
        };
      };

      validation = {
        durationToleranceSeconds = mkOption {
          type = types.number;
          default = 0;
          description = "Allowed source/output duration delta in seconds before validation fails. Zero uses Anvil's default.";
        };
      };

      metadataMode = mkOption {
        type = types.enum [
          "preserve"
          "strip"
        ];
        default = "preserve";
        description = "Metadata retention policy.";
      };
      attachmentsMode = mkOption {
        type = types.enum [
          "preserve"
          "strip"
        ];
        default = "preserve";
        description = "Attachment retention policy.";
      };
      chaptersMode = mkOption {
        type = types.enum [
          "preserve"
          "strip"
        ];
        default = "preserve";
        description = "Chapter retention policy.";
      };
    };
  };

  libraryModule = types.submodule {
    options = {
      kind = mkOption {
        type = types.enum [
          "media"
          "download"
        ];
        default = "media";
        description = "Library kind.";
      };
      path = mkOption {
        type = types.str;
        description = "Library root path.";
      };
      flow = mkOption {
        type = types.str;
        default = "av1-crf-search";
        description = "Flow name used for this library.";
      };
      profile = mkOption {
        type = types.str;
        default = "default-av1";
        description = "Profile name used for this library.";
      };
      priority = mkOption {
        type = types.int;
        default = 0;
        description = "Job priority for this library.";
      };
      scanInterval = mkOption {
        type = types.str;
        default = "";
        example = "5m";
        description = "Optional scan interval for this library. Empty falls back to services.anvil.daemon.scanInterval.";
      };
      include = mkOption {
        type = types.listOf types.str;
        default = [ ];
        description = "Include glob patterns.";
      };
      exclude = mkOption {
        type = types.listOf types.str;
        default = [ ];
        description = "Exclude glob patterns.";
      };
      concurrencyLimit = mkOption {
        type = types.int;
        default = 0;
        description = "Maximum active jobs for this library. Zero means unlimited.";
      };
      arr = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "main-radarr";
        description = "Name of the Arr instance used to derive metadata for this library.";
      };
      media.replacementMode = mkOption {
        type = types.enum [
          "replace"
          "copy"
        ];
        default = "replace";
        description = "Completion behavior for media libraries.";
      };
      download = {
        handoffPath = mkOption {
          type = types.str;
          default = "";
          description = "Destination path for completed download-library encodes.";
        };
        stableFor = mkOption {
          type = types.str;
          default = "5m";
          description = "How long a download must be unchanged before scanning.";
        };
        packageMode = mkOption {
          type = types.enum [
            "auto"
            "directory"
            "file"
          ];
          default = "auto";
          description = "How download packages are grouped.";
        };
        handoffMode = mkOption {
          type = types.enum [
            "move"
            "copy"
          ];
          default = "copy";
          description = "How completed encodes are handed off.";
        };
        preserveRelativePath = mkOption {
          type = types.bool;
          default = false;
          description = "Preserve source relative path under the handoff path.";
        };
        cleanupSourceMedia = mkOption {
          type = types.bool;
          default = false;
          description = "Remove source media after successful handoff.";
        };
        pruneEmptyDirs = mkOption {
          type = types.bool;
          default = false;
          description = "Prune empty source directories after handoff cleanup.";
        };
        ignorableGlobs = mkOption {
          type = types.listOf types.str;
          default = [
            "**/samples/**"
            "**/sample*/**"
            "**/*sample*"
            "**/*.txt"
            "**/*.url"
            "**/*.sfv"
            "**/*.srr"
            "**/*.nzb"
            "**/__MACOSX/**"
            "**/.DS_Store"
            "**/.nfs*"
          ];
          description = "Globs ignored while handling download packages.";
        };
      };
    };
  };
in
{
  options.services.anvil = {
    enable = mkEnableOption "Anvil AV1 encoding daemon";

    package = mkOption {
      type = types.nullOr types.package;
      default = null;
      description = "Anvil package to run. Required when the service is enabled.";
    };

    runtimePackages = mkOption {
      type = types.listOf types.package;
      default = [
        ffmpegPackage
        pkgs.ab-av1
        pkgs.dovi-tool
        pkgs.mkvtoolnix
      ];
      description = "Packages added to the Anvil service PATH for probe, crop detection, CRF search, Dolby Vision checks/repair, MKV remuxing, and encoding.";
    };

    user = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "User to run the daemon as. Null leaves systemd's default user.";
    };

    group = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "Group to run the daemon as. Null leaves systemd's default group.";
    };

    configFile = mkOption {
      type = types.path;
      readOnly = true;
      default = configFile;
      description = "Generated Anvil TOML configuration.";
    };

    daemon = {
      tempDir = mkOption {
        type = types.str;
        default = "/var/lib/anvil/tmp";
        description = "Temporary working directory.";
      };
      storePath = mkOption {
        type = types.str;
        default = "/var/lib/anvil/anvil.db";
        description = "SQLite store path.";
      };
      workerCount = mkOption {
        type = types.int;
        default = 1;
        description = "Number of encode workers.";
      };
      totalThreads = mkOption {
        type = types.int;
        default = 0;
        description = "Total thread budget. Zero lets Anvil use its default.";
      };
      maxAttempts = mkOption {
        type = types.int;
        default = 3;
        description = "Maximum attempts per job.";
      };
      scanInterval = mkOption {
        type = types.str;
        default = "30m";
        description = "Library scan interval.";
      };
      schedulerInterval = mkOption {
        type = types.str;
        default = "5s";
        description = "Scheduler tick interval.";
      };
      leaseDuration = mkOption {
        type = types.str;
        default = "30m";
        description = "Worker job lease duration.";
      };
      shutdownPolicy = mkOption {
        type = types.enum [
          "drain"
          "cancel"
        ];
        default = "drain";
        description = "Shutdown behavior after SIGINT or SIGTERM.";
      };
      shutdownTimeout = mkOption {
        type = types.str;
        default = "0s";
        description = "How long drain shutdown waits before canceling active work. Zero waits indefinitely.";
      };
      stagingCleanupAge = mkOption {
        type = types.str;
        default = "0s";
        description = "Age threshold for automatic staging cleanup. Zero disables age-based cleanup.";
      };
      logLevel = mkOption {
        type = types.str;
        default = "info";
        description = "Daemon stderr log level: debug, info, warn, or error.";
      };
    };

    flows = mkOption {
      type = types.attrsOf (types.submodule {
        options.steps = mkOption {
          type = types.listOf types.str;
          default = defaultFlowSteps;
          description = "Pipeline block names for this flow.";
        };
      });
      default = {
        av1-crf-search.steps = defaultFlowSteps;
      };
      description = "Named pipeline flows.";
    };

    profiles = mkOption {
      type = types.attrsOf profileModule;
      default = {
        default-av1 = { };
      };
      description = "Named encode profiles.";
    };

    arrs = mkOption {
      type = types.attrsOf arrModule;
      default = { };
      example = literalExpression ''
        {
          main-radarr = {
            type = "radarr";
            baseUrl = "http://radarr:7878";
            apiKeyFile = config.sops.secrets.radarr-api-key.path;
          };
        }
      '';
      description = "Arr instances keyed by local name.";
    };

    libraries = mkOption {
      type = types.attrsOf libraryModule;
      default = { };
      example = literalExpression ''
        {
          movies = {
            path = "/srv/media/movies";
            arr = "main-radarr";
          };
        }
      '';
      description = "Libraries keyed by library name.";
    };

    service = {
      nice = mkOption {
        type = types.nullOr types.int;
        default = null;
        example = 10;
        description = "Optional systemd Nice value for the Anvil service.";
      };
      ioSchedulingClass = mkOption {
        type = types.nullOr (types.enum [
          "idle"
          "best-effort"
          "realtime"
        ]);
        default = null;
        example = "best-effort";
        description = "Optional systemd IOSchedulingClass value.";
      };
      ioSchedulingPriority = mkOption {
        type = types.nullOr types.int;
        default = null;
        example = 7;
        description = "Optional systemd IOSchedulingPriority value.";
      };
      cpuWeight = mkOption {
        type = types.nullOr types.int;
        default = null;
        example = 50;
        description = "Optional systemd CPUWeight value.";
      };
      ioWeight = mkOption {
        type = types.nullOr types.int;
        default = null;
        example = 50;
        description = "Optional systemd IOWeight value.";
      };
      extraReadWritePaths = mkOption {
        type = types.listOf types.str;
        default = [ ];
        description = "Additional paths writable by the hardened service.";
      };
      extraServiceConfig = mkOption {
        type = types.attrsOf types.anything;
        default = { };
        description = "Additional systemd serviceConfig attributes merged into the Anvil service.";
      };
    };
  };

  config = mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.package != null;
        message = "services.anvil.package must be set when services.anvil.enable is true.";
      }
    ] ++ arrAssertions ++ libraryAssertions;

    environment.etc."anvil/anvil.toml".source = cfg.configFile;
    systemd.tmpfiles.rules = map (path: "d ${path} 0750 ${tmpfilesUser} ${tmpfilesGroup} - -") daemonDirectoryPaths;

    systemd.services.anvil = {
      description = "Anvil AV1 encoding daemon";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      path = cfg.runtimePackages;
      serviceConfig =
        {
          ExecStart = "${packageExe} --config /etc/anvil/anvil.toml";
          Restart = "on-failure";
          StateDirectory = [ "anvil" "anvil/tmp" ];
          RuntimeDirectory = "anvil";
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
        // optionalAttrs (cfg.service.nice != null) { Nice = cfg.service.nice; }
        // optionalAttrs (cfg.service.ioSchedulingClass != null) { IOSchedulingClass = cfg.service.ioSchedulingClass; }
        // optionalAttrs (cfg.service.ioSchedulingPriority != null) { IOSchedulingPriority = cfg.service.ioSchedulingPriority; }
        // optionalAttrs (cfg.service.cpuWeight != null) { CPUWeight = cfg.service.cpuWeight; }
        // optionalAttrs (cfg.service.ioWeight != null) { IOWeight = cfg.service.ioWeight; }
        // optionalAttrs (cfg.user != null) { User = cfg.user; }
        // optionalAttrs (cfg.group != null) { Group = cfg.group; }
        // cfg.service.extraServiceConfig;
    };
  };
}

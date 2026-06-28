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
      metadata.mode = profile.metadataMode;
      attachments.mode = profile.attachmentsMode;
      chapters.mode = profile.chaptersMode;
    };

  libraryToToml =
    _name: library:
    {
      inherit (library) kind path flow profile priority include exclude;
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
      log_level = cfg.daemon.logLevel;
    };
    arrs = lib.mapAttrs arrToToml cfg.arrs;
    flows = lib.mapAttrs (_name: flow: { inherit (flow) steps; }) cfg.flows;
    profiles = lib.mapAttrs profileToToml cfg.profiles;
    libraries = lib.mapAttrs libraryToToml cfg.libraries;
  };

  configFile = format.generate "anvil.toml" generatedConfig;
  packageExe = if cfg.package == null then "${pkgs.coreutils}/bin/false" else lib.getExe cfg.package;

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
        type = types.str;
        default = "mkv";
        description = "Output container extension.";
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
          "sidecar"
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
        pkgs.ffmpeg
        pkgs.ab-av1
      ];
      description = "Packages added to the Anvil service PATH for probe, crop detection, CRF search, and encoding.";
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
        default = "/var/tmp/anvil";
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
  };

  config = mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.package != null;
        message = "services.anvil.package must be set when services.anvil.enable is true.";
      }
    ] ++ arrAssertions ++ libraryAssertions;

    environment.etc."anvil/anvil.toml".source = cfg.configFile;

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
          StateDirectory = "anvil";
          RuntimeDirectory = "anvil";
        }
        // optionalAttrs (cfg.user != null) { User = cfg.user; }
        // optionalAttrs (cfg.group != null) { Group = cfg.group; };
    };
  };
}

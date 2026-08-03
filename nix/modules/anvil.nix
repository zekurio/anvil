{ config, lib, pkgs, ... }:

let
  cfg = config.services.anvil;
  inherit (lib)
    literalExpression
    mkDefault
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
    "subtitle-cleanup"
    "stage"
    "crf-search"
    "encode"
    "dovi-fix"
    "track-stats"
    "validate"
    "replace"
    "cleanup"
  ];

  defaultDownloadFlowSteps = [
    "probe"
    "crop-detect"
    "audio-cleanup"
    "subtitle-cleanup"
    "stage"
    "crf-search"
    "encode"
    "dovi-fix"
    "track-stats"
    "validate"
    "handoff"
    "cleanup"
  ];

  # Module-managed values are compacted by each serializer below. extraConfig
  # accepts arbitrary future schema, so clean it recursively before merging to
  # keep nulls and empty structural noise out of TOML.
  hasTomlValue = value:
    if value == null then
      false
    else if builtins.isAttrs value then
      value != { }
    else if builtins.isList value then
      value != [ ]
    else
      true;

  cleanExtraConfig = value:
    if builtins.isAttrs value then
      lib.filterAttrs (_name: hasTomlValue) (lib.mapAttrs (_name: cleanExtraConfig) value)
    else if builtins.isList value then
      builtins.filter hasTomlValue (map cleanExtraConfig value)
    else
      value;

  compact = lib.filterAttrs (_name: hasTomlValue);

  videoOverrideToToml =
    _key: videoOverride:
    compact {
      inherit (videoOverride) codec accelerator preset metric target;
      bit_depth = videoOverride.bitDepth;
      crf_min = videoOverride.crfMin;
      crf_max = videoOverride.crfMax;
      min_savings_percent = videoOverride.minSavingsPercent;
      force_encode_on_no_fit = videoOverride.forceEncodeOnNoFit;
      skip_encode = videoOverride.skipEncode;
      ffmpeg_args = videoOverride.ffmpegArgs;
      ab_av1_args = videoOverride.abAv1Args;
    };

  profileToToml =
    _name: profile:
    let
      dolbyVision = compact {
        inherit (profile.video.dolbyVision) mode;
        remove_hdr10plus = profile.video.dolbyVision.removeHdr10plus;
      };
      overrides = compact (lib.mapAttrs videoOverrideToToml profile.video.overrides);
      video = compact (
        {
          inherit (profile.video) codec accelerator preset metric target;
          bit_depth = profile.video.bitDepth;
          crf_min = profile.video.crfMin;
          crf_max = profile.video.crfMax;
          min_savings_percent = profile.video.minSavingsPercent;
          force_encode_on_no_fit = profile.video.forceEncodeOnNoFit;
          skip_encode = profile.video.skipEncode;
          ffmpeg_args = profile.video.ffmpegArgs;
          ab_av1_args = profile.video.abAv1Args;
        }
        // optionalAttrs (overrides != { }) { inherit overrides; }
        // optionalAttrs (dolbyVision != { }) { dolby_vision = dolbyVision; }
      );
      audio = compact {
        languages_to_keep = profile.audio.languagesToKeep;
        keep_commentary = profile.audio.keepCommentary;
        inherit (profile.audio) fallback;
        unknown_as_original = profile.audio.unknownAsOriginal;
      };
      subtitles = compact {
        languages_to_keep = profile.subtitles.languagesToKeep;
        keep_forced = profile.subtitles.keepForced;
        keep_sdh = profile.subtitles.keepSdh;
        keep_commentary = profile.subtitles.keepCommentary;
        inherit (profile.subtitles) fallback;
        unknown_as_original = profile.subtitles.unknownAsOriginal;
      };
      validation = compact {
        duration_tolerance_seconds = profile.validation.durationToleranceSeconds;
      };
      metadata = compact {
        inherit (profile.metadata) mode;
        track_titles = profile.metadata.trackTitles;
      };
      attachments = compact { inherit (profile.attachments) mode; };
      chapters = compact { inherit (profile.chapters) mode; };
    in
    compact (
      { inherit (profile) container; }
      // optionalAttrs (video != { }) { inherit video; }
      // optionalAttrs (audio != { }) { inherit audio; }
      // optionalAttrs (subtitles != { }) { inherit subtitles; }
      // optionalAttrs (validation != { }) { inherit validation; }
      // optionalAttrs (metadata != { }) { inherit metadata; }
      // optionalAttrs (attachments != { }) { inherit attachments; }
      // optionalAttrs (chapters != { }) { inherit chapters; }
    );

  libraryToToml =
    _name: library:
    let
      media = compact {
        replacement_mode = library.media.replacementMode;
      };
      download = compact {
        handoff_path = library.download.handoffPath;
        stable_for = library.download.stableFor;
        package_mode = library.download.packageMode;
        handoff_mode = library.download.handoffMode;
        preserve_relative_path = library.download.preserveRelativePath;
        cleanup_source_media = library.download.cleanupSourceMedia;
        prune_empty_dirs = library.download.pruneEmptyDirs;
        ignorable_globs = library.download.ignorableGlobs;
      };
    in
    compact (
      {
        inherit (library) kind path flow profile priority include exclude;
        scan_interval = library.scanInterval;
        ignore_regex = library.ignoreRegex;
        concurrency_limit = library.concurrencyLimit;
        inherit (library) arr;
      }
      // optionalAttrs (library.kind == "media" && media != { }) { inherit media; }
      // optionalAttrs (library.kind == "download" && download != { }) { inherit download; }
    );

  arrToToml =
    _name: arr:
    compact {
      inherit (arr) type;
      base_url = arr.baseUrl;
      api_key_file = arr.apiKeyFile;
    };

  daemonToToml = compact {
    temp_dir = cfg.daemon.tempDir;
    store_path = cfg.daemon.storePath;
    control_socket = cfg.daemon.controlSocket;
    worker_count = cfg.daemon.workerCount;
    total_threads = cfg.daemon.totalThreads;
    max_attempts = cfg.daemon.maxAttempts;
    scan_interval = cfg.daemon.scanInterval;
    filesystem_event_debounce = cfg.daemon.filesystemEventDebounce;
    scheduler_interval = cfg.daemon.schedulerInterval;
    lease_duration = cfg.daemon.leaseDuration;
    shutdown_policy = cfg.daemon.shutdownPolicy;
    shutdown_timeout = cfg.daemon.shutdownTimeout;
    staging_cleanup_age = cfg.daemon.stagingCleanupAge;
    log_level = cfg.daemon.logLevel;
  };

  moduleConfig =
    { daemon = daemonToToml; }
    // optionalAttrs (cfg.arrs != { }) { arrs = lib.mapAttrs arrToToml cfg.arrs; }
    // optionalAttrs (cfg.flows != { }) {
      flows = lib.mapAttrs (_name: flow: { inherit (flow) steps; }) cfg.flows;
    }
    // optionalAttrs (cfg.profiles != { }) { profiles = lib.mapAttrs profileToToml cfg.profiles; }
    // optionalAttrs (cfg.libraries != { }) { libraries = lib.mapAttrs libraryToToml cfg.libraries; };

  generatedConfig = lib.recursiveUpdate moduleConfig (cleanExtraConfig cfg.extraConfig);
  configFile = format.generate "anvil.toml" generatedConfig;

  packageExe = if cfg.package == null then "${pkgs.coreutils}/bin/false" else lib.getExe cfg.package;
  # The control client is a separate package on purpose: it speaks to the daemon
  # over the control socket and needs none of the media toolchain, so installing
  # it system-wide must not drag ffmpeg into every user's profile. There is
  # deliberately no fallback to services.anvil.package.
  controlClientPackage = cfg.controlClient.package;
  ffmpegPackage = pkgs.jellyfin-ffmpeg or pkgs.ffmpeg;
  storeDirectory = builtins.dirOf cfg.daemon.storePath;
  controlSocketDirectory = builtins.dirOf cfg.daemon.controlSocket;
  daemonDirectoryPaths = lib.unique [
    cfg.daemon.tempDir
    storeDirectory
    controlSocketDirectory
  ];
  tmpfilesUser = if cfg.user == null then "root" else cfg.user;
  tmpfilesGroup = if cfg.group == null then "root" else cfg.group;
  libraryWritePaths = lib.mapAttrsToList (_name: library: library.path) cfg.libraries;
  handoffWritePaths = lib.flatten (
    lib.mapAttrsToList (
      _name: library:
      optional (
        library.kind == "download"
        && library.download.handoffPath != null
        && library.download.handoffPath != ""
      ) library.download.handoffPath
    ) cfg.libraries
  );
  readWritePaths = lib.unique (
    [
      cfg.daemon.tempDir
      storeDirectory
      controlSocketDirectory
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
        assertion = library.download.handoffPath != null && library.download.handoffPath != "";
        message = "services.anvil.libraries.${name}.download.handoffPath is required for download libraries.";
      })
    ) cfg.libraries
  );

  profileAssertions = lib.flatten (
    lib.mapAttrsToList (
      name: profile:
      let
        # Go defaults an unset metric to vmaf, so a null base metric is vmaf
        # for the override comparison below.
        baseMetric = if profile.video.metric == null then "vmaf" else profile.video.metric;
      in
      [
        {
          assertion = profile.video.metric != "xpsnr" || profile.video.target != null;
          message = "services.anvil.profiles.${name}.video.target must be set when video.metric is \"xpsnr\" (typical targets are 35-50).";
        }
      ]
      ++ lib.mapAttrsToList (overrideName: override: {
        assertion = override.metric == null || override.metric == baseMetric || override.target != null;
        message = "services.anvil.profiles.${name}.video.overrides.${overrideName}.metric changes the quality metric, so its target must be set too.";
      }) profile.video.overrides
    ) cfg.profiles
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
        description = ''
          Runtime file containing this Arr instance's API key. The apiKey TOML
          field is deliberately not exposed because secrets do not belong in
          the Nix store; use a runtime secret path here instead.
        '';
      };
    };
  };

  videoOverrideModule = types.submodule {
    options = {
      codec = mkOption {
        type = types.nullOr (types.enum [
          "av1"
          "hevc"
          "h265"
          "h264"
          "avc"
        ]);
        default = null;
        description = "Target codec for this source condition. Null inherits the base video codec.";
      };
      accelerator = mkOption {
        type = types.nullOr (types.enum [
          "software"
          "qsv"
          "vaapi"
          "amf"
        ]);
        default = null;
        description = "Acceleration backend for this source condition. Null inherits the base setting.";
      };
      preset = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Encoder preset for this source condition. Null inherits the base setting.";
      };
      bitDepth = mkOption {
        type = types.nullOr (types.enum [
          8
          10
        ]);
        default = null;
        description = "Output bit depth for this source condition. Null inherits the base setting.";
      };
      crfMin = mkOption {
        type = types.nullOr types.int;
        default = null;
        description = "Minimum CRF for this source condition. Null inherits the base setting.";
      };
      crfMax = mkOption {
        type = types.nullOr types.int;
        default = null;
        description = "Maximum CRF for this source condition. Null inherits the base setting.";
      };
      metric = mkOption {
        type = types.nullOr (types.enum [
          "vmaf"
          "xpsnr"
        ]);
        default = null;
        description = "Quality metric for this source condition. Null inherits the base metric.";
      };
      target = mkOption {
        type = types.nullOr types.number;
        default = null;
        description = "Quality target for this source condition. Null inherits the base target; required when this override switches the metric.";
      };
      minSavingsPercent = mkOption {
        type = types.nullOr types.number;
        default = null;
        description = "Minimum input-size savings percentage. Null inherits the base setting.";
      };
      forceEncodeOnNoFit = mkOption {
        type = types.nullOr types.bool;
        default = null;
        description = "Whether to encode when CRF search finds no fit. Null inherits the base setting.";
      };
      skipEncode = mkOption {
        type = types.nullOr types.bool;
        default = null;
        description = "Whether to copy video instead of searching and encoding. Null inherits the base setting.";
      };
      ffmpegArgs = mkOption {
        type = types.listOf types.str;
        default = [ ];
        description = "ffmpeg arguments appended to the base arguments. An empty list is omitted.";
      };
      abAv1Args = mkOption {
        type = types.listOf types.str;
        default = [ ];
        description = "ab-av1 arguments appended to the base arguments. An empty list is omitted.";
      };
    };
  };

  profileModule = types.submodule {
    options = {
      container = mkOption {
        type = types.nullOr (types.enum [ "mkv" ]);
        default = null;
        description = "Output container. Null defers to Anvil's default.";
      };

      video = {
        codec = mkOption {
          type = types.nullOr (types.enum [
            "av1"
            "hevc"
            "h265"
            "h264"
            "avc"
          ]);
          default = null;
          description = "Target video codec. Null defers to Anvil's default.";
        };
        accelerator = mkOption {
          type = types.nullOr (types.enum [
            "software"
            "qsv"
            "vaapi"
            "amf"
          ]);
          default = null;
          description = "Encoder acceleration backend. Null defers to Anvil's default.";
        };
        preset = mkOption {
          type = types.nullOr types.str;
          default = null;
          description = "Encoder preset. Null defers to Anvil's default.";
        };
        bitDepth = mkOption {
          type = types.nullOr (types.enum [
            8
            10
          ]);
          default = null;
          description = "Output video bit depth. Null defers to Anvil's default.";
        };
        crfMin = mkOption {
          type = types.nullOr types.int;
          default = null;
          description = "Minimum CRF to test. Null defers to Anvil's default.";
        };
        crfMax = mkOption {
          type = types.nullOr types.int;
          default = null;
          description = "Maximum CRF to test. Null defers to Anvil's default.";
        };
        metric = mkOption {
          type = types.nullOr (types.enum [
            "vmaf"
            "xpsnr"
          ]);
          default = null;
          description = "CRF-search quality metric. Null defers to Anvil's default.";
        };
        target = mkOption {
          type = types.nullOr types.number;
          default = null;
          description = "Quality target. VMAF uses 0-100; typical XPSNR targets are 35-50. Null defers to Anvil's VMAF default (95); a target is required when metric is \"xpsnr\".";
        };
        minSavingsPercent = mkOption {
          type = types.nullOr types.number;
          default = null;
          description = "Minimum input-size savings percentage. Null defers to Anvil's default.";
        };
        forceEncodeOnNoFit = mkOption {
          type = types.nullOr types.bool;
          default = null;
          description = "Force an encode when CRF search finds no fit. Null defers to Anvil's default.";
        };
        skipEncode = mkOption {
          type = types.nullOr types.bool;
          default = null;
          description = "Copy video instead of searching and encoding. Null defers to Anvil's default.";
        };
        ffmpegArgs = mkOption {
          type = types.listOf types.str;
          default = [ ];
          description = "Extra ffmpeg encoder arguments. An empty list is omitted.";
        };
        abAv1Args = mkOption {
          type = types.listOf types.str;
          default = [ ];
          description = "Extra ab-av1 CRF-search arguments. An empty list is omitted.";
        };
        overrides = mkOption {
          type = types.attrsOf videoOverrideModule;
          default = { };
          description = ''
            Per-source overrides keyed by a canonical codec family or
            dolby_vision. Nullable fields inherit base video settings; argument
            lists append and are omitted when empty.
          '';
        };
        dolbyVision = {
          mode = mkOption {
            type = types.nullOr (types.enum [
              "auto"
              "off"
              "require"
            ]);
            default = null;
            description = "Dolby Vision handling policy. Null defers to Anvil's default.";
          };
          removeHdr10plus = mkOption {
            type = types.nullOr types.bool;
            default = null;
            description = "Pass dovi_tool --drop-hdr10plus during RPU extraction and injection.";
          };
        };
      };

      audio = {
        languagesToKeep = mkOption {
          type = types.listOf types.str;
          default = [ ];
          description = "Audio languages to keep. The special value orig uses Arr-derived original language.";
        };
        keepCommentary = mkOption {
          type = types.nullOr types.bool;
          default = null;
          description = "Whether to keep commentary audio. Null defers to Anvil's default.";
        };
        fallback = mkOption {
          type = types.nullOr (types.enum [
            "keep_all"
            "keep_first"
            "fail_job"
          ]);
          default = null;
          description = "Behavior when no audio stream matches. Null defers to Anvil's default.";
        };
        unknownAsOriginal = mkOption {
          type = types.nullOr types.bool;
          default = null;
          description = "Treat unknown audio language as original. Null defers to Anvil's default.";
        };
      };

      subtitles = {
        languagesToKeep = mkOption {
          type = types.listOf types.str;
          default = [ ];
          description = "Subtitle languages to keep. The special value orig uses Arr-derived original language.";
        };
        keepForced = mkOption {
          type = types.nullOr types.bool;
          default = null;
          description = "Whether to keep forced subtitles. Null defers to Anvil's default.";
        };
        keepSdh = mkOption {
          type = types.nullOr types.bool;
          default = null;
          description = "Whether to keep SDH subtitles. Null defers to Anvil's default.";
        };
        keepCommentary = mkOption {
          type = types.nullOr types.bool;
          default = null;
          description = "Whether to keep commentary subtitles. Null defers to Anvil's default.";
        };
        fallback = mkOption {
          type = types.nullOr (types.enum [
            "keep_all"
            "keep_first"
            "fail_job"
          ]);
          default = null;
          description = "Behavior when no subtitle stream matches. Null defers to Anvil's default.";
        };
        unknownAsOriginal = mkOption {
          type = types.nullOr types.bool;
          default = null;
          description = "Treat unknown subtitle language as original. Null defers to Anvil's default.";
        };
      };

      validation.durationToleranceSeconds = mkOption {
        type = types.nullOr types.number;
        default = null;
        description = "Allowed source/output duration delta in seconds. Null defers to Anvil's default.";
      };

      metadata = {
        mode = mkOption {
          type = types.nullOr (types.enum [
            "preserve"
            "strip"
          ]);
          default = null;
          description = "Metadata retention policy. Null defers to Anvil's default.";
        };
        trackTitles = mkOption {
          type = types.nullOr (types.enum [
            "preserve"
            "strip"
            "standardize"
          ]);
          default = null;
          description = "Stream-title metadata policy. Null defers to Anvil's default.";
        };
      };

      attachments.mode = mkOption {
        type = types.nullOr (types.enum [
          "preserve"
          "strip"
        ]);
        default = null;
        description = "Attachment retention policy. Null defers to Anvil's default.";
      };

      chapters.mode = mkOption {
        type = types.nullOr (types.enum [
          "preserve"
          "strip"
        ]);
        default = null;
        description = "Chapter retention policy. Null defers to Anvil's default.";
      };
    };
  };

  libraryModule = types.submodule ({ config, ... }: {
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
        description = "Flow name. The module selects a kind-specific default when unset.";
      };
      profile = mkOption {
        type = types.str;
        default = "default-av1";
        description = "Profile name used for this library.";
      };
      priority = mkOption {
        type = types.nullOr types.int;
        default = null;
        description = "Job priority. Null defers to Anvil's default.";
      };
      scanInterval = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Library-specific scan interval. Null uses the daemon interval.";
      };
      include = mkOption {
        type = types.listOf types.str;
        default = [ ];
        description = "Include glob patterns. An empty list is omitted.";
      };
      exclude = mkOption {
        type = types.listOf types.str;
        default = [ ];
        description = "Exclude glob patterns. An empty list is omitted.";
      };
      ignoreRegex = mkOption {
        type = types.listOf types.str;
        default = [ ];
        description = "Regular expressions matched against slash-normalized library-relative paths.";
      };
      concurrencyLimit = mkOption {
        type = types.nullOr types.int;
        default = null;
        description = "Maximum active jobs for this library. Null defers to Anvil's default.";
      };
      arr = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Arr instance used to derive metadata for this library.";
      };
      media.replacementMode = mkOption {
        type = types.nullOr (types.enum [
          "replace"
          "copy"
        ]);
        default = null;
        description = "Media-library completion behavior. Null defers to Anvil's default.";
      };
      download = {
        handoffPath = mkOption {
          type = types.nullOr types.str;
          default = null;
          description = "Destination path for completed download-library encodes.";
        };
        stableFor = mkOption {
          type = types.nullOr types.str;
          default = null;
          description = "Fallback quiet period for files without an observed close-write or moved-in completion. Null defers to Anvil's default.";
        };
        packageMode = mkOption {
          type = types.nullOr (types.enum [
            "auto"
            "directory"
            "file"
          ]);
          default = null;
          description = "Download package grouping policy. Null defers to Anvil's default.";
        };
        handoffMode = mkOption {
          type = types.nullOr (types.enum [
            "move"
            "copy"
          ]);
          default = null;
          description = "Completed encode handoff behavior. Null defers to Anvil's default.";
        };
        preserveRelativePath = mkOption {
          type = types.nullOr types.bool;
          default = null;
          description = "Preserve source-relative paths at handoff. Null defers to Anvil's default.";
        };
        cleanupSourceMedia = mkOption {
          type = types.nullOr types.bool;
          default = null;
          description = "Remove source media after handoff. Null defers to Anvil's default.";
        };
        pruneEmptyDirs = mkOption {
          type = types.nullOr types.bool;
          default = null;
          description = "Prune empty source directories after cleanup. Null defers to Anvil's default.";
        };
        ignorableGlobs = mkOption {
          type = types.listOf types.str;
          default = [ ];
          description = ''
            Globs excluded from download-package discovery and stability
            handling. An empty list is omitted, causing Anvil to apply its
            canonical default list. A non-empty list replaces that default.
          '';
        };
      };
    };

    config.flow = mkDefault (
      if config.kind == "download" then
        "download-av1-handoff"
      else
        "av1-crf-search"
    );
  });
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
          Standalone package providing anvilctl. Required when install is true;
          services.anvil.package is deliberately not used as a fallback because
          it includes the daemon's media toolchain.
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
        pkgs.dovi-tool
        pkgs.mkvtoolnix
      ];
      description = "Packages added to the service PATH for media inspection, search, repair, remuxing, and encoding.";
    };

    user = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "User to run the daemon as. Null leaves systemd's default user.";
    };

    group = mkOption {
      type = types.nullOr types.str;
      default = null;
      example = "anvil";
      description = ''
        Group to run the daemon as. The control socket and its runtime directory
        make this group the operator access boundary. Null leaves systemd's
        default group.
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
      default = configFile;
      description = "Generated Anvil TOML configuration.";
    };

    extraConfig = mkOption {
      type = types.attrsOf types.anything;
      default = { };
      description = ''
        Raw TOML-shaped attributes deep-merged into the generated configuration
        after all module-managed settings. This can override module-managed
        keys and is the forward-compatibility escape hatch for schema keys the
        module does not expose yet. Use TOML snake_case key names.
      '';
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
      controlSocket = mkOption {
        type = types.str;
        default = "/run/anvil/anvild.sock";
        description = "Unix socket used by anvilctl; its group permissions are the operator access boundary.";
      };
      workerCount = mkOption {
        type = types.nullOr types.int;
        default = null;
        description = "Encode worker count. Null lets Anvil use the host CPU count.";
      };
      totalThreads = mkOption {
        type = types.nullOr types.int;
        default = null;
        description = "Total thread budget. Null lets Anvil use the host CPU count.";
      };
      maxAttempts = mkOption {
        type = types.nullOr types.int;
        default = null;
        description = "Maximum attempts per job. Null defers to Anvil's default.";
      };
      scanInterval = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Library scan interval. Null defers to Anvil's default.";
      };
      filesystemEventDebounce = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Filesystem-event coalescing delay. Null defers to Anvil's default.";
      };
      schedulerInterval = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Scheduler tick interval. Null defers to Anvil's default.";
      };
      leaseDuration = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Worker job lease duration. Null defers to Anvil's default.";
      };
      shutdownPolicy = mkOption {
        type = types.nullOr (types.enum [
          "drain"
          "cancel"
        ]);
        default = null;
        description = "Shutdown behavior after SIGINT or SIGTERM. Null defers to Anvil's default.";
      };
      shutdownTimeout = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Drain timeout before canceling active work. Null defers to Anvil's default.";
      };
      stagingCleanupAge = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Age threshold for staging cleanup. Null defers to Anvil's default.";
      };
      logLevel = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Daemon stderr log level. Null defers to Anvil's default.";
      };
    };

    flows = mkOption {
      type = types.attrsOf (types.submodule {
        options.steps = mkOption {
          type = types.listOf types.str;
          description = "Pipeline block names for this flow.";
        };
      });
      default = {
        av1-crf-search.steps = defaultFlowSteps;
        download-av1-handoff.steps = defaultDownloadFlowSteps;
      };
      description = "Named pipeline flows.";
    };

    profiles = mkOption {
      type = types.attrsOf profileModule;
      default = {
        default-av1 = { };
      };
      description = "Named encode profiles. Unset fields are omitted so Anvil applies canonical defaults.";
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
        description = "Optional systemd Nice value.";
      };
      ioSchedulingClass = mkOption {
        type = types.nullOr (types.enum [
          "idle"
          "best-effort"
          "realtime"
        ]);
        default = null;
        description = "Optional systemd IOSchedulingClass value.";
      };
      ioSchedulingPriority = mkOption {
        type = types.nullOr types.int;
        default = null;
        description = "Optional systemd IOSchedulingPriority value.";
      };
      cpuWeight = mkOption {
        type = types.nullOr types.int;
        default = null;
        description = "Optional systemd CPUWeight value.";
      };
      ioWeight = mkOption {
        type = types.nullOr types.int;
        default = null;
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
      {
        assertion = !cfg.controlClient.install || controlClientPackage != null;
        message = ''
          services.anvil.controlClient.package must be set when controlClient.install is true.
          Use the standalone anvilctl package; services.anvil.package is not a fallback because
          it wraps anvild with the whole media toolchain.
        '';
      }
      {
        assertion = !cfg.createGroup || cfg.group != null;
        message = "services.anvil.createGroup requires services.anvil.group to name the group to create.";
      }
    ] ++ arrAssertions ++ libraryAssertions ++ profileAssertions;

    environment.etc."anvil/anvil.toml".source = cfg.configFile;
    environment.systemPackages = optional cfg.controlClient.install controlClientPackage;
    environment.variables = optionalAttrs (cfg.controlClient.install && cfg.controlClient.setEnvironment) {
      ANVIL_CONTROL_SOCKET = cfg.daemon.controlSocket;
    };
    users.groups = optionalAttrs (cfg.createGroup && cfg.group != null) {
      ${cfg.group} = { };
    };
    # The socket is 0660 inside a 0750 directory, making the service group the
    # access boundary for anvilctl.
    systemd.tmpfiles.rules = map (path: "d ${path} 0750 ${tmpfilesUser} ${tmpfilesGroup} - -") daemonDirectoryPaths;

    warnings = optional (cfg.controlClient.install && cfg.group == null) ''
      services.anvil.controlClient.install is enabled but services.anvil.group is null,
      so the control socket ${cfg.daemon.controlSocket} is owned by the service's default
      group. Set services.anvil.group, ensure it exists, and add operators to it.
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
            "TEMP=${cfg.daemon.tempDir}"
            "TMP=${cfg.daemon.tempDir}"
            "TMPDIR=${cfg.daemon.tempDir}"
            "XDG_CACHE_HOME=${cfg.daemon.tempDir}/cache"
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

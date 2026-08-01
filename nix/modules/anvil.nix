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

  videoOverrideToToml =
    _key: videoOverride:
    optionalAttrs (videoOverride.codec != null) { inherit (videoOverride) codec; }
    // optionalAttrs (videoOverride.accelerator != null) { inherit (videoOverride) accelerator; }
    // optionalAttrs (videoOverride.preset != null) { inherit (videoOverride) preset; }
    // optionalAttrs (videoOverride.bitDepth != null) { bit_depth = videoOverride.bitDepth; }
    // optionalAttrs (videoOverride.crfMin != null) { crf_min = videoOverride.crfMin; }
    // optionalAttrs (videoOverride.crfMax != null) { crf_max = videoOverride.crfMax; }
    // optionalAttrs (videoOverride.targetVmaf != null) { target_vmaf = videoOverride.targetVmaf; }
    // optionalAttrs (videoOverride.minSavingsPercent != null) {
      min_savings_percent = videoOverride.minSavingsPercent;
    }
    // optionalAttrs (videoOverride.forceEncodeOnNoFit != null) {
      force_encode_on_no_fit = videoOverride.forceEncodeOnNoFit;
    }
    // optionalAttrs (videoOverride.skipEncode != null) { skip_encode = videoOverride.skipEncode; }
    // optionalAttrs (videoOverride.ffmpegArgs != [ ]) { ffmpeg_args = videoOverride.ffmpegArgs; }
    // optionalAttrs (videoOverride.abAv1Args != [ ]) { ab_av1_args = videoOverride.abAv1Args; };

  profileToToml =
    _name: profile:
    {
      inherit (profile) container;
      video = {
        inherit (profile.video) codec accelerator preset;
        bit_depth = profile.video.bitDepth;
        crf_min = profile.video.crfMin;
        crf_max = profile.video.crfMax;
        target_vmaf = profile.video.targetVmaf;
        min_savings_percent = profile.video.minSavingsPercent;
        force_encode_on_no_fit = profile.video.forceEncodeOnNoFit;
        skip_encode = profile.video.skipEncode;
        ffmpeg_args = profile.video.ffmpegArgs;
        ab_av1_args = profile.video.abAv1Args;
        dolby_vision = {
          inherit (profile.video.dolbyVision) mode;
          remove_hdr10plus = profile.video.dolbyVision.removeHDR10Plus;
        };
      }
      // optionalAttrs (profile.video.overrides != { }) {
        overrides = lib.mapAttrs videoOverrideToToml profile.video.overrides;
      };
      audio = {
        languages_to_keep = profile.audio.languagesToKeep;
        keep_commentary = profile.audio.keepCommentary;
        fallback = profile.audio.fallback;
        unknown_as_original = profile.audio.unknownAsOriginal;
      };
      subtitles = {
        fallback = profile.subtitles.fallback;
        languages_to_keep = profile.subtitles.languagesToKeep;
        keep_forced = profile.subtitles.keepForced;
        keep_sdh = profile.subtitles.keepSdh;
        keep_commentary = profile.subtitles.keepCommentary;
        unknown_as_original = profile.subtitles.unknownAsOriginal;
      };
      validation.duration_tolerance_seconds = profile.validation.durationToleranceSeconds;
      metadata = {
        mode = profile.metadataMode;
        track_titles = profile.trackTitleMode;
      };
      attachments.mode = profile.attachmentsMode;
      chapters.mode = profile.chaptersMode;
    };

  libraryToToml =
    _name: library:
    {
      inherit (library) kind path profile priority include exclude;
      flow =
        if library.flow != "" then
          library.flow
        else if library.kind == "download" then
          "download-av1-handoff"
        else
          "av1-crf-search";
      scan_interval = library.scanInterval;
      ignore_regex = library.ignoreRegex;
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
    arrs = lib.mapAttrs arrToToml cfg.arrs;
    flows = lib.mapAttrs (_name: flow: { inherit (flow) steps; }) cfg.flows;
    profiles = lib.mapAttrs profileToToml cfg.profiles;
    libraries = lib.mapAttrs libraryToToml cfg.libraries;
  };

  configFile = format.generate "anvil.toml" generatedConfig;
  packageExe = if cfg.package == null then "${pkgs.coreutils}/bin/false" else lib.getExe cfg.package;
  # The control client is a separate package on purpose: it speaks to the daemon
  # over the control socket and needs none of the media toolchain, so installing
  # it system-wide must not drag ffmpeg into every user's profile. There is
  # deliberately no fallback to services.anvil.package: that package wraps
  # anvild with ffmpeg, ab-av1, dovi_tool, and MKVToolNix, and installing it as
  # "the lightweight client" is exactly the mistake this option exists to avoid.
  # The assertion below asks for the choice rather than guessing at it.
  controlClientPackage = cfg.controlClient.package;
  ffmpegPackage =
    if pkgs.stdenv.isLinux then
      (pkgs.jellyfin-ffmpeg or pkgs.ffmpeg)
    else
      pkgs.ffmpeg;
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
      optional (library.kind == "download" && library.download.handoffPath != "") library.download.handoffPath
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
          type = types.enum [
            "av1"
            "hevc"
            "h265"
            "h264"
            "avc"
          ];
          default = "av1";
          description = "Target video bitstream codec. Anvil maps this with accelerator to a concrete ffmpeg encoder.";
        };
        accelerator = mkOption {
          type = types.enum [
            "software"
            "qsv"
            "vaapi"
            "amf"
          ];
          default = "software";
          description = "Hardware encoder/acceleration backend. Codec selects the target bitstream; this selects the implementation family.";
        };
        preset = mkOption {
          type = types.str;
          default = "6";
          description = "Encoder preset.";
        };
        bitDepth = mkOption {
          type = types.enum [
            8
            10
          ];
          default = 10;
          description = "Output video bit depth. Anvil maps this to backend-specific pixel formats.";
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
        forceEncodeOnNoFit = mkOption {
          type = types.bool;
          default = false;
          description = "When ab-av1 cannot find a CRF satisfying search constraints, force an encode with the lowest tested CRF instead of falling back to video-copy/remux.";
        };
        skipEncode = mkOption {
          type = types.bool;
          default = false;
          description = "Skip CRF search and video encoding entirely and copy the video stream. Audio, subtitle, metadata, and publish handling still run. Usually set through video.overrides.<codec>.skipEncode to exempt specific source codecs.";
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
        overrides = mkOption {
          type = types.attrsOf (types.submodule {
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
                example = "hevc";
                description = "Target video bitstream codec for this source condition. Null inherits the base video codec. For dolby_vision, this must be set for Dolby Vision handling to activate.";
              };
              accelerator = mkOption {
                type = types.nullOr (types.enum [
                  "software"
                  "qsv"
                  "vaapi"
                  "amf"
                ]);
                default = null;
                example = "qsv";
                description = "Hardware encoder/acceleration backend for this source condition. Null inherits the base video accelerator.";
              };
              preset = mkOption {
                type = types.nullOr types.str;
                default = null;
                example = "6";
                description = "Encoder preset for this source condition. Null inherits the base video preset.";
              };
              bitDepth = mkOption {
                type = types.nullOr (types.enum [
                  8
                  10
                ]);
                default = null;
                example = 10;
                description = "Output video bit depth for this source condition. Null inherits the base video bit depth.";
              };
              crfMin = mkOption {
                type = types.nullOr types.int;
                default = null;
                example = 18;
                description = "Minimum CRF for this source condition. Null inherits the base video minimum CRF.";
              };
              crfMax = mkOption {
                type = types.nullOr types.int;
                default = null;
                example = 45;
                description = "Maximum CRF for this source condition. Null inherits the base video maximum CRF.";
              };
              targetVmaf = mkOption {
                type = types.nullOr types.number;
                default = null;
                example = 96;
                description = "Target VMAF from 0 to 100 for this source condition. Null inherits the base video target VMAF.";
              };
              minSavingsPercent = mkOption {
                type = types.nullOr types.number;
                default = null;
                example = 40;
                description = "Minimum input-size savings percentage from 0 to 100 for this source condition. Null inherits the base video minimum savings percentage.";
              };
              forceEncodeOnNoFit = mkOption {
                type = types.nullOr types.bool;
                default = null;
                example = true;
                description = "Whether to force an encode when CRF search finds no fit for this source condition. Null inherits the base video setting.";
              };
              skipEncode = mkOption {
                type = types.nullOr types.bool;
                default = null;
                example = true;
                description = "Whether to skip CRF search and video encoding entirely for this source condition and copy the video stream instead. Audio, subtitle, metadata, and publish handling still run. Null inherits the base video setting.";
              };
              ffmpegArgs = mkOption {
                type = types.listOf types.str;
                default = [ ];
                example = [
                  "-global_quality"
                  "24"
                ];
                description = "Extra ffmpeg video encoder arguments appended to the base arguments for this source condition. An empty list emits no override field.";
              };
              abAv1Args = mkOption {
                type = types.listOf types.str;
                default = [ ];
                example = [
                  "--enc"
                  "low_power=1"
                ];
                description = "Extra ab-av1 crf-search arguments appended to the base arguments for this source condition. An empty list emits no override field.";
              };
            };
          });
          default = { };
          description = ''
            Per-source video overrides keyed by canonical source codec family,
            such as hevc, h264, or av1, or by the reserved dolby_vision key.
            Unset nullable fields inherit their base video settings; argument
            lists append to the base arguments and are omitted when empty.
            Dolby Vision handling requires overrides.dolby_vision.codec to be set.
          '';
        };
        dolbyVision = {
          mode = mkOption {
            type = types.enum [
              "auto"
              "off"
              "require"
            ];
            default = "auto";
            description = ''
              How to handle Dolby Vision sources. Encoder settings belong in
              video.overrides.dolby_vision, and Dolby Vision handling activates
              only when that override's codec is set. Auto uses the override
              when Dolby Vision is detected and dovi_tool is available.
            '';
          };
          removeHDR10Plus = mkOption {
            type = types.bool;
            default = false;
            description = "Pass dovi_tool --drop-hdr10plus during Dolby Vision RPU extraction/injection. Encoder settings belong in video.overrides.dolby_vision.";
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
        languagesToKeep = mkOption {
          type = types.listOf types.str;
          default = [ ];
          example = [
            "orig"
            "deu"
          ];
          description = "Subtitle languages to keep. The special value \"orig\" uses Arr-derived original language.";
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
        fallback = mkOption {
          type = types.enum [
            "keep_all"
            "keep_first"
            "fail_job"
          ];
          default = "keep_all";
          description = "Fallback when no subtitle stream matches the configured policy.";
        };
        unknownAsOriginal = mkOption {
          type = types.bool;
          default = false;
          description = "Treat und/unknown subtitle language as the original language.";
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
      trackTitleMode = mkOption {
        type = types.enum [
          "preserve"
          "strip"
          "standardize"
        ];
        default = "strip";
        description = "Stream title metadata policy.";
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
        default = "";
        example = "av1-crf-search";
        description = "Flow name used for this library. Empty uses the kind-specific default.";
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
      ignoreRegex = mkOption {
        type = types.listOf types.str;
        default = [ ];
        example = [ "(^|/)_UNPACK[^/]*(/|$)" ];
        description = "Regular expressions matched against slash-normalized library-relative paths. Matching directories are skipped recursively before stability checks.";
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
          description = ''
            Globs excluded from download-package discovery and stability
            handling. Setting this option replaces the default list. During
            successful handoff source cleanup, matching paths may be deleted
            when cleanupSourceMedia and pruneEmptyDirs are enabled. External
            subtitle sidecars are preserved by default; add
            **/[Ss][Uu][Bb][Ss]/** alongside the defaults to opt in to cleaning
            release-provided Subs/ directories.
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

          anvilctl is a userspace client: it talks to the daemon over
          daemon.control_socket and never opens the SQLite store or runs
          ffmpeg. Access is granted by socket permissions, not by installing
          the binary, so installing it is safe and being able to run it is not
          the same as being allowed to use it.

          It is off by default because installing it requires naming a package,
          and the only right answer is the standalone anvilctl build. Enable it
          together with services.anvil.controlClient.package.
        '';
      };

      package = mkOption {
        type = types.nullOr types.package;
        default = null;
        example = literalExpression "inputs.anvil.packages.\${pkgs.system}.anvilctl";
        description = ''
          Package providing anvilctl. Required when controlClient.install is
          true; there is no fallback to services.anvil.package.

          Use the standalone anvilctl package. services.anvil.package wraps
          anvild with ffmpeg, ab-av1, dovi_tool, and MKVToolNix, none of which
          an operator's shell has any use for, and installing it system-wide
          puts that entire toolchain in every user's PATH.
        '';
      };

      setEnvironment = mkOption {
        type = types.bool;
        default = true;
        description = ''
          Set ANVIL_CONTROL_SOCKET system-wide so anvilctl finds a socket at a
          non-default path without --socket on every invocation.
        '';
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
      example = "anvil";
      description = ''
        Group to run the daemon as. Null leaves systemd's default group.

        This group is the operator access boundary: the control socket is
        created 0660 inside a 0750 runtime directory, both owned by it, so its
        members are exactly the users who can run anvilctl.
      '';
    };

    createGroup = mkOption {
      type = types.bool;
      default = false;
      description = ''
        Create services.anvil.group as a system group.

        Leave this off when the group is defined elsewhere, which is the usual
        case: the group only matters because operators are added to it, and
        whoever adds them normally declares it. Turning it on is the shortcut
        for a host where Anvil is the only reason the group exists.
      '';
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
      controlSocket = mkOption {
        type = types.str;
        default = "/run/anvil/anvild.sock";
        description = "Unix socket anvilctl connects to. It is created 0660, owned by the service user and group, inside a 0750 runtime directory, so group membership is the operator access boundary.";
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
      filesystemEventDebounce = mkOption {
        type = types.str;
        default = "2s";
        description = "Delay used to coalesce filesystem events before scanning a library.";
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
        download-av1-handoff.steps = defaultDownloadFlowSteps;
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
      {
        assertion = !cfg.controlClient.install || controlClientPackage != null;
        message = ''
          services.anvil.controlClient.package must be set when controlClient.install is true.
          Use the standalone anvilctl package, for example
          inputs.anvil.packages.''${pkgs.system}.anvilctl. services.anvil.package is not used as a
          fallback because it wraps anvild with the whole media toolchain.
        '';
      }
      {
        assertion = !cfg.createGroup || cfg.group != null;
        message = "services.anvil.createGroup requires services.anvil.group to name the group to create.";
      }
    ] ++ arrAssertions ++ libraryAssertions;

    environment.etc."anvil/anvil.toml".source = cfg.configFile;
    environment.systemPackages = optional cfg.controlClient.install controlClientPackage;
    environment.variables = optionalAttrs (cfg.controlClient.install && cfg.controlClient.setEnvironment) {
      ANVIL_CONTROL_SOCKET = cfg.daemon.controlSocket;
    };
    # Only when asked for. A group systemd is told to run as has to exist, and
    # silently creating one that the administrator also declares elsewhere
    # would make two definitions of the same access boundary.
    users.groups = optionalAttrs (cfg.createGroup && cfg.group != null) {
      ${cfg.group} = { };
    };
    # The control socket directory keeps mode 0750: the socket itself is 0660,
    # so its directory group is the access boundary for anvilctl. Widening
    # either would silently hand queue control to every local user.
    systemd.tmpfiles.rules = map (path: "d ${path} 0750 ${tmpfilesUser} ${tmpfilesGroup} - -") daemonDirectoryPaths;

    # Installing anvilctl does not grant access to the daemon. The socket is
    # created 0660 owned by the service user and group inside a 0750 directory,
    # so with no explicit group every non-root operator is locked out, and the
    # failure looks like a permission error long after deployment.
    warnings = optional (cfg.controlClient.install && cfg.group == null) ''
      services.anvil.controlClient.install is enabled but services.anvil.group is null,
      so the control socket ${cfg.daemon.controlSocket} is owned by the service's default
      group. Set services.anvil.group, make sure that group exists (declare it yourself or
      set services.anvil.createGroup), and add operators to it. Otherwise anvilctl is
      installed but every non-root operator is refused with a permission error.
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
          # 0750 with the socket's own 0660 is the access contract: membership
          # in the service group is what lets an operator run anvilctl.
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

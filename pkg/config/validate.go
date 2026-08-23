package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/video"
)

// Validate checks that the configuration is internally consistent.
func (c Config) Validate() error {
	var problems []string

	if strings.TrimSpace(c.Daemon.TempDir) == "" {
		problems = append(problems, "daemon.temp_dir is required")
	}
	if strings.TrimSpace(c.Daemon.StorePath) == "" {
		problems = append(problems, "daemon.store_path is required")
	}
	if strings.TrimSpace(c.Daemon.ControlSocket) == "" {
		problems = append(problems, "daemon.control_socket is required")
	} else if !filepath.IsAbs(c.Daemon.ControlSocket) {
		problems = append(problems, "daemon.control_socket must be an absolute path")
	}
	if c.Daemon.WorkerCount < 1 {
		problems = append(problems, "daemon.worker_count must be at least 1")
	}
	if c.Daemon.TotalThreads < 0 {
		problems = append(problems, "daemon.total_threads must be non-negative")
	}
	if c.Daemon.MaxAttempts < 1 {
		problems = append(problems, "daemon.max_attempts must be at least 1")
	}
	validatePositiveDuration(&problems, "daemon.scan_interval", c.Daemon.ScanInterval)
	validateNonNegativeDuration(&problems, "daemon.filesystem_event_debounce", c.Daemon.FSDebounce)
	validatePositiveDuration(&problems, "daemon.scheduler_interval", c.Daemon.SchedulerInterval)
	validatePositiveDuration(&problems, "daemon.lease_duration", c.Daemon.LeaseDuration)
	validateNonNegativeDuration(&problems, "daemon.shutdown_timeout", c.Daemon.ShutdownTimeout)
	validateNonNegativeDuration(&problems, "daemon.staging_cleanup_age", c.Daemon.StagingCleanupAge)
	if !validShutdownPolicy(c.Daemon.ShutdownPolicy) {
		problems = append(problems, fmt.Sprintf("daemon.shutdown_policy %q is invalid", c.Daemon.ShutdownPolicy))
	}
	if _, ok := NormalizeLogLevel(c.Daemon.LogLevel); !ok {
		problems = append(problems, fmt.Sprintf("daemon.log_level %q is invalid (must be debug, info, warn, or error)", c.Daemon.LogLevel))
	}

	profiles := make(map[string]struct{}, len(c.Profiles))
	for _, name := range sortedKeys(c.Profiles) {
		profile := c.Profiles[name]
		name = strings.TrimSpace(name)
		if name == "" {
			problems = append(problems, "profile name is required")
			continue
		}
		profiles[name] = struct{}{}

		if !validContainer(profile.Container) {
			problems = append(problems, fmt.Sprintf("profile %q container %q is invalid; Anvil outputs MKV only", name, profile.Container))
		}
		if !validVideoCodec(profile.Video.Codec) {
			problems = append(problems, fmt.Sprintf("profile %q video.codec %q is invalid (must be av1, hevc, h265, h264, or avc)", name, profile.Video.Codec))
		}
		if !validAccelerator(profile.Video.Accelerator) {
			problems = append(problems, fmt.Sprintf("profile %q video.accelerator %q is invalid (must be software, qsv, vaapi, or amf)", name, profile.Video.Accelerator))
		}
		if !video.ValidBitDepth(profile.Video.BitDepth) {
			problems = append(problems, fmt.Sprintf("profile %q video.bit_depth %d is invalid (must be 8 or 10)", name, profile.Video.BitDepth))
		}
		if profile.Video.CRFMin < 0 || profile.Video.CRFMax < 0 {
			problems = append(problems, fmt.Sprintf("profile %q CRF values must be non-negative", name))
		}
		if profile.Video.CRFMin > profile.Video.CRFMax {
			problems = append(problems, fmt.Sprintf("profile %q crf_min must be less than or equal to crf_max", name))
		}
		if !validQualityMetric(profile.Video.Metric) {
			problems = append(problems, fmt.Sprintf("profile %q video.metric %q is invalid (must be vmaf or xpsnr)", name, profile.Video.Metric))
		} else if !validQualityTarget(profile.Video.Metric, profile.Video.Target) {
			problems = append(problems, fmt.Sprintf("profile %q video.target must be between 0 and 100 for metric %q", name, profile.Video.Metric))
		} else if profile.Video.Metric == "xpsnr" && profile.Video.Target == 0 {
			// There is no sensible XPSNR default: without --min-xpsnr ab-av1
			// falls back to its VMAF default, silently ignoring the metric.
			problems = append(problems, fmt.Sprintf("profile %q video.target must be set when metric is \"xpsnr\" (typical targets are 35-50)", name))
		}
		if profile.Video.MinSavingsPercent < 0 || profile.Video.MinSavingsPercent > 100 {
			problems = append(problems, fmt.Sprintf("profile %q min_savings_percent must be between 0 and 100", name))
		}
		problems = append(problems, videoOverrideKeyProblems(name, profile.Video.Overrides)...)
		for _, key := range sortedKeys(profile.Video.Overrides) {
			override := profile.Video.Overrides[key]
			prefix := fmt.Sprintf("profile %q video.overrides.%s", name, key)
			if override.Codec != nil {
				if strings.TrimSpace(*override.Codec) == "" {
					problems = append(problems, prefix+".codec must not be empty")
				} else if !validVideoCodec(*override.Codec) {
					problems = append(problems, fmt.Sprintf("%s.codec %q is invalid (must be av1, hevc, h265, h264, or avc)", prefix, *override.Codec))
				}
			}
			if override.Accelerator != nil && !validAccelerator(*override.Accelerator) {
				problems = append(problems, fmt.Sprintf("%s.accelerator %q is invalid (must be software, qsv, vaapi, or amf)", prefix, *override.Accelerator))
			}
			if override.BitDepth != nil && !video.ValidBitDepth(*override.BitDepth) {
				problems = append(problems, fmt.Sprintf("%s.bit_depth %d is invalid (must be 8 or 10)", prefix, *override.BitDepth))
			}
			effectiveMetric := profile.Video.Metric
			if override.Metric != nil {
				effectiveMetric = *override.Metric
				if !validQualityMetric(effectiveMetric) {
					problems = append(problems, fmt.Sprintf("%s.metric %q is invalid (must be vmaf or xpsnr)", prefix, effectiveMetric))
				} else if effectiveMetric != profile.Video.Metric && override.Target == nil {
					// Inheriting the base target across metrics is never right:
					// a VMAF 95 becomes an unattainable XPSNR 95 dB, and an
					// XPSNR 42 becomes a uselessly low VMAF 42.
					problems = append(problems, fmt.Sprintf("%s.metric changes the quality metric, so its target must be set too", prefix))
				}
			}
			if override.Target != nil && validQualityMetric(effectiveMetric) && !validQualityTarget(effectiveMetric, *override.Target) {
				problems = append(problems, fmt.Sprintf("%s.target must be between 0 and 100 for metric %q", prefix, effectiveMetric))
			} else if override.Target != nil && *override.Target == 0 && effectiveMetric == "xpsnr" {
				// A zero target omits --min-xpsnr, so ab-av1 would silently
				// search against its VMAF default instead of XPSNR.
				problems = append(problems, fmt.Sprintf("%s.target must be positive for metric \"xpsnr\" (typical targets are 35-50)", prefix))
			}
			if override.MinSavingsPercent != nil && (*override.MinSavingsPercent < 0 || *override.MinSavingsPercent > 100) {
				problems = append(problems, prefix+".min_savings_percent must be between 0 and 100")
			}
			if override.CRFMin != nil && *override.CRFMin < 0 {
				problems = append(problems, prefix+".crf_min must be non-negative")
			}
			if override.CRFMax != nil && *override.CRFMax < 0 {
				problems = append(problems, prefix+".crf_max must be non-negative")
			}
			effectiveCRFMin := profile.Video.CRFMin
			if override.CRFMin != nil {
				effectiveCRFMin = *override.CRFMin
			}
			effectiveCRFMax := profile.Video.CRFMax
			if override.CRFMax != nil {
				effectiveCRFMax = *override.CRFMax
			}
			if effectiveCRFMin > effectiveCRFMax {
				problems = append(problems, prefix+" effective crf_min must be less than or equal to crf_max")
			}
		}
		if !validStreamFallback(profile.Audio.Fallback) {
			problems = append(problems, fmt.Sprintf("profile %q audio.fallback %q is invalid", name, profile.Audio.Fallback))
		}
		if !validStreamFallback(profile.Subtitles.Fallback) {
			problems = append(problems, fmt.Sprintf("profile %q subtitles.fallback %q is invalid", name, profile.Subtitles.Fallback))
		}
		if profile.Validation.DurationToleranceSeconds < 0 {
			problems = append(problems, fmt.Sprintf("profile %q validation.duration_tolerance_seconds must be non-negative", name))
		}
		if !validMetadataMode(profile.Metadata.Mode) {
			problems = append(problems, fmt.Sprintf("profile %q metadata.mode %q is invalid", name, profile.Metadata.Mode))
		}
		if !validTrackTitleMode(profile.Metadata.TrackTitles) {
			problems = append(problems, fmt.Sprintf("profile %q metadata.track_titles %q is invalid", name, profile.Metadata.TrackTitles))
		}
		if !validMetadataMode(profile.Attachments.Mode) {
			problems = append(problems, fmt.Sprintf("profile %q attachments.mode %q is invalid", name, profile.Attachments.Mode))
		}
		if !validMetadataMode(profile.Chapters.Mode) {
			problems = append(problems, fmt.Sprintf("profile %q chapters.mode %q is invalid", name, profile.Chapters.Mode))
		}
	}

	arrs := make(map[string]struct{}, len(c.Arrs))
	for _, name := range sortedKeys(c.Arrs) {
		arr := c.Arrs[name]
		name = strings.TrimSpace(name)
		if name == "" {
			problems = append(problems, "arr name is required")
			continue
		}
		arrs[name] = struct{}{}
		provider := arrProvider(arr)
		if !validArrProvider(provider) {
			problems = append(problems, fmt.Sprintf("arr %q type %q is invalid", name, provider))
		}
		if strings.TrimSpace(arr.BaseURL) == "" {
			problems = append(problems, fmt.Sprintf("arr %q base_url is required", name))
		}
		if strings.TrimSpace(arr.APIKey) == "" && strings.TrimSpace(arr.APIKeyFile) == "" {
			problems = append(problems, fmt.Sprintf("arr %q api_key or api_key_file is required", name))
		}
	}

	for _, name := range sortedKeys(c.Libraries) {
		library := c.Libraries[name]
		name = strings.TrimSpace(name)
		if name == "" {
			problems = append(problems, "library name is required")
			continue
		}
		if !validLibraryKind(library.Kind) {
			problems = append(problems, fmt.Sprintf("library %q kind %q is invalid", name, library.Kind))
		}
		if strings.TrimSpace(library.Path) == "" {
			problems = append(problems, fmt.Sprintf("library %q path is required", name))
		}
		if _, exists := profiles[library.Profile]; !exists {
			problems = append(problems, fmt.Sprintf("library %q references unknown profile %q", name, library.Profile))
		}
		if library.ConcurrencyLimit < 0 {
			problems = append(problems, fmt.Sprintf("library %q concurrency_limit must be non-negative", name))
		}
		if strings.TrimSpace(library.ScanInterval) != "" {
			validatePositiveDuration(&problems, fmt.Sprintf("library %q scan_interval", name), library.ScanInterval)
		}
		validateRegexps(&problems, fmt.Sprintf("library %q ignore_regex", name), library.IgnoreRegex)
		if strings.TrimSpace(library.Arr) != "" {
			if _, exists := arrs[library.Arr]; !exists {
				problems = append(problems, fmt.Sprintf("library %q references unknown arr %q", name, library.Arr))
			}
		}
		if library.Kind != "download" {
			if !validReplacementMode(library.Media.ReplacementMode) {
				problems = append(problems, fmt.Sprintf("library %q media.replacement_mode %q is invalid", name, library.Media.ReplacementMode))
			}
			continue
		}

		if strings.TrimSpace(library.Media.ReplacementMode) != "" {
			problems = append(problems, fmt.Sprintf("download library %q media.replacement_mode must not be set", name))
		}
		if strings.TrimSpace(library.Download.HandoffPath) == "" {
			problems = append(problems, fmt.Sprintf("download library %q download.handoff_path is required", name))
		}
		if stableFor, err := time.ParseDuration(library.Download.StableFor); err != nil {
			problems = append(problems, fmt.Sprintf("download library %q download.stable_for is invalid: %v", name, err))
		} else if stableFor < 0 {
			problems = append(problems, fmt.Sprintf("download library %q download.stable_for must be non-negative", name))
		}
		if !validPackageMode(library.Download.PackageMode) {
			problems = append(problems, fmt.Sprintf("download library %q download.package_mode %q is invalid", name, library.Download.PackageMode))
		}
		if !validHandoffMode(library.Download.HandoffMode) {
			problems = append(problems, fmt.Sprintf("download library %q download.handoff_mode %q is invalid", name, library.Download.HandoffMode))
		}
	}

	if len(problems) > 0 {
		return errors.New("invalid config: " + strings.Join(problems, "; "))
	}

	return nil
}

func videoOverrideKeyProblems(profileName string, overrides map[string]VideoOverrideConfig) []string {
	var problems []string
	canonicalKeys := make(map[string]string, len(overrides))
	for _, key := range sortedKeys(overrides) {
		canonicalKey := canonicalVideoOverrideKey(key)
		if strings.TrimSpace(key) == "" {
			problems = append(problems, fmt.Sprintf("profile %q video.overrides key must not be empty", profileName))
			continue
		}
		// Dolby Vision support was removed; its reserved override key must
		// fail loudly instead of silently encoding with base settings.
		if canonicalKey == "dolby-vision" {
			problems = append(problems, fmt.Sprintf("profile %q video.overrides.%s targets removed Dolby Vision handling; delete the override (and any video.dolby_vision table)", profileName, key))
			continue
		}
		if previousKey, exists := canonicalKeys[canonicalKey]; exists {
			problems = append(problems, fmt.Sprintf("profile %q video.overrides.%s collides with video.overrides.%s after canonicalization to %q", profileName, key, previousKey, canonicalKey))
			continue
		}
		canonicalKeys[canonicalKey] = key
	}
	return problems
}

func validLibraryKind(kind string) bool {
	return kind == "media" || kind == "download"
}

func validatePositiveDuration(problems *[]string, name string, value string) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s is invalid: %v", name, err))
		return
	}
	if duration <= 0 {
		*problems = append(*problems, fmt.Sprintf("%s must be greater than zero", name))
	}
}

func validateNonNegativeDuration(problems *[]string, name string, value string) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s is invalid: %v", name, err))
		return
	}
	if duration < 0 {
		*problems = append(*problems, fmt.Sprintf("%s must be non-negative", name))
	}
}

func validateRegexps(problems *[]string, name string, values []string) {
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			*problems = append(*problems, fmt.Sprintf("%s[%d] must not be empty", name, i))
			continue
		}
		if _, err := regexp.Compile(value); err != nil {
			*problems = append(*problems, fmt.Sprintf("%s[%d] is invalid: %v", name, i, err))
		}
	}
}

func validShutdownPolicy(policy string) bool {
	return policy == "drain" || policy == "cancel"
}

// NormalizeLogLevel trims and canonicalizes a configured daemon log level.
func NormalizeLogLevel(level string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(level))
	switch normalized {
	case "debug", "info", "warn", "error":
		return normalized, true
	default:
		return "", false
	}
}

func validReplacementMode(mode string) bool {
	return mode == "replace" || mode == "copy"
}

func validPackageMode(mode string) bool {
	return mode == "auto" || mode == "directory" || mode == "file"
}

func validHandoffMode(mode string) bool {
	return mode == "move" || mode == "copy"
}

func validArrProvider(provider string) bool {
	return provider == "radarr" || provider == "sonarr"
}

func arrProvider(arr ArrConfig) string {
	return arr.Type
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validQualityMetric(metric string) bool {
	return metric == "vmaf" || metric == "xpsnr"
}

func validQualityTarget(metric string, target float64) bool {
	switch metric {
	case "vmaf", "xpsnr":
		return target >= 0 && target <= 100
	default:
		return false
	}
}

func normalizeContainer(container string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(container), "."))
}

func validContainer(container string) bool {
	return normalizeContainer(container) == "mkv"
}

func validVideoCodec(codec string) bool {
	switch video.NormalizeCodec(codec) {
	case "av1", "hevc", "h265", "h264", "avc":
		return true
	default:
		return false
	}
}

func normalizeConfigVideoCodec(codec string) string {
	switch video.NormalizeCodec(codec) {
	case "h265":
		return "hevc"
	case "avc":
		return "h264"
	default:
		return video.NormalizeCodec(codec)
	}
}

func validAccelerator(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "software", "qsv", "vaapi", "amf":
		return true
	default:
		return false
	}
}

func validStreamFallback(fallback string) bool {
	return fallback == "keep_all" || fallback == "keep_first" || fallback == "fail_job"
}

func validMetadataMode(mode string) bool {
	return mode == "preserve" || mode == "strip"
}

func validTrackTitleMode(mode string) bool {
	return mode == "preserve" || mode == "strip" || mode == "standardize"
}

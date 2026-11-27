package config

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/ffmpeg"
	"github.com/zekurio/anvil/pkg/video"
)

// Accepted values for enumerated settings. Keep these in sync with the field
// docs in schema.go and examples/anvil-reference.toml.
var (
	libraryKinds     = []string{"media", "download"}
	shutdownPolicies = []string{"drain", "cancel"}
	logLevels        = []string{"debug", "info", "warn", "error"}
	replacementModes = []string{"replace", "copy"}
	packageModes     = []string{"auto", "directory", "file"}
	handoffModes     = []string{"move", "copy"}
	arrProviders     = []string{"radarr", "sonarr"}
	qualityMetrics   = []string{"vmaf", "xpsnr"}
	videoCodecs      = []string{"av1", "hevc", "h265", "h264", "avc"}
	accelerators     = []string{"software", "qsv", "vaapi", "amf"}
	streamFallbacks  = []string{"keep_all", "keep_first", "fail_job"}
	metadataModes    = []string{"preserve", "strip"}
	trackTitleModes  = []string{"preserve", "strip", "standardize"}
)

const (
	codecChoices       = "must be av1, hevc, h265, h264, or avc"
	acceleratorChoices = "must be software, qsv, vaapi, or amf"
)

// problems collects every configuration error so one check-config run reports
// all of them instead of the first.
type problems []string

func (p *problems) addf(format string, args ...any) {
	*p = append(*p, fmt.Sprintf(format, args...))
}

// Validate checks the process settings and, so that check-config still says
// what is wrong with a file that has them, the legacy sections.
func (c Config) Validate() error {
	var p problems
	c.Daemon.validate(&p)
	c.Web.validate(&p)

	profiles := make(map[string]struct{}, len(c.Legacy.Profiles))
	for _, key := range sortedKeys(c.Legacy.Profiles) {
		name := strings.TrimSpace(key)
		if name == "" {
			p.addf("profile name is required")
			continue
		}
		profiles[name] = struct{}{}
		c.Legacy.Profiles[key].validate(&p, name)
	}

	arrs := make(map[string]struct{}, len(c.Legacy.Arrs))
	for _, key := range sortedKeys(c.Legacy.Arrs) {
		name := strings.TrimSpace(key)
		if name == "" {
			p.addf("arr name is required")
			continue
		}
		arrs[name] = struct{}{}
		c.Legacy.Arrs[key].validate(&p, name)
	}

	for _, key := range sortedKeys(c.Legacy.Libraries) {
		name := strings.TrimSpace(key)
		if name == "" {
			p.addf("library name is required")
			continue
		}
		c.Legacy.Libraries[key].validate(&p, name, profiles, arrs)
	}

	if len(p) > 0 {
		return errors.New("invalid config:\n- " + strings.Join(p, "\n- "))
	}
	return nil
}

func (d DaemonConfig) validate(p *problems) {
	seenRoots := make(map[string]bool, len(d.MediaRoots))
	for i, root := range d.MediaRoots {
		switch {
		case !filepath.IsAbs(root):
			p.addf("daemon.media_roots[%d] %q must be absolute", i, root)
		case filepath.Clean(root) != root:
			p.addf("daemon.media_roots[%d] %q must be cleaned (use %q)", i, root, filepath.Clean(root))
		case seenRoots[root]:
			p.addf("daemon.media_roots contains duplicate path %q", root)
		default:
			seenRoots[root] = true
		}
	}
	if strings.TrimSpace(d.TempDir) == "" {
		p.addf("daemon.temp_dir is required")
	}
	if strings.TrimSpace(d.StorePath) == "" {
		p.addf("daemon.store_path is required")
	}
	switch {
	case strings.TrimSpace(d.ControlSocket) == "":
		p.addf("daemon.control_socket is required")
	case !filepath.IsAbs(d.ControlSocket):
		p.addf("daemon.control_socket must be an absolute path")
	}
	for _, problem := range d.tunableProblems() {
		p.addf("daemon.%s", problem.text)
	}
}

func (profile ProfileConfig) validate(p *problems, name string) {
	if normalizeContainer(profile.Container) != "mkv" {
		p.addf("profile %q container %q is invalid; Anvil outputs MKV only", name, profile.Container)
	}
	profile.Crop.validate(p, name)
	profile.Video.validate(p, name)
	if !slices.Contains(streamFallbacks, profile.Audio.Fallback) {
		p.addf("profile %q audio.fallback %q is invalid", name, profile.Audio.Fallback)
	}
	if !slices.Contains(streamFallbacks, profile.Subtitles.Fallback) {
		p.addf("profile %q subtitles.fallback %q is invalid", name, profile.Subtitles.Fallback)
	}
	if profile.Validation.DurationToleranceSeconds < 0 {
		p.addf("profile %q validation.duration_tolerance_seconds must be non-negative", name)
	}
	if !slices.Contains(metadataModes, profile.Metadata.Mode) {
		p.addf("profile %q metadata.mode %q is invalid", name, profile.Metadata.Mode)
	}
	if !slices.Contains(trackTitleModes, profile.Metadata.TrackTitles) {
		p.addf("profile %q metadata.track_titles %q is invalid", name, profile.Metadata.TrackTitles)
	}
	if !slices.Contains(metadataModes, profile.Attachments.Mode) {
		p.addf("profile %q attachments.mode %q is invalid", name, profile.Attachments.Mode)
	}
	if !slices.Contains(metadataModes, profile.Chapters.Mode) {
		p.addf("profile %q chapters.mode %q is invalid", name, profile.Chapters.Mode)
	}
}

// Problem is one rule a profile section breaks. Validate reports it in
// check-config wording; other front ends, such as the flow editor, attribute
// it to Field and phrase Rule their own way.
type Problem struct {
	// Field is the TOML key at fault within its section, e.g. "crf_min".
	Field string
	// Rule says what a valid value looks like, e.g. "must be 8 or 10".
	Rule string
	// text is the check-config message without its `profile "name" ` prefix.
	text string
}

func problemf(field, rule, format string, args ...any) Problem {
	return Problem{Field: field, Rule: rule, text: fmt.Sprintf(format, args...)}
}

func (crop CropConfig) validate(p *problems, profile string) {
	for _, problem := range crop.Problems() {
		p.addf("profile %q %s", profile, problem.text)
	}
}

// Problems reports every rule the crop settings break, in the order Validate
// lists them.
func (crop CropConfig) Problems() []Problem {
	var found []Problem
	if crop.Samples < 0 || crop.Samples == 1 {
		found = append(found, problemf("samples", "must be 0 (automatic) or at least 2", "crop.samples must be 0 (automatic) or at least 2"))
	}
	if crop.FrameCount < 1 {
		found = append(found, problemf("frame_count", "must be at least 1", "crop.frame_count must be at least 1"))
	}
	// An empty list selects spread sampling, so only explicit offsets are checked.
	for i, offset := range crop.SeekOffsets {
		if offset.Duration < 0 {
			found = append(found, problemf("seek_offsets", "must not contain negative durations", "crop.seek_offsets[%d] must be non-negative", i))
		}
	}
	if crop.Limit < 1 || crop.Limit > 255 {
		found = append(found, problemf("limit", "must be between 1 and 255", "crop.limit must be between 1 and 255"))
	}
	if crop.Round < 1 {
		found = append(found, problemf("round", "must be at least 1", "crop.round must be at least 1"))
	}
	if crop.ResetCount < 0 {
		found = append(found, problemf("reset_count", "must be non-negative", "crop.reset_count must be non-negative"))
	}
	if !finite(crop.MinRetainedAreaPct) || crop.MinRetainedAreaPct <= 0 || crop.MinRetainedAreaPct > 100 {
		found = append(found, problemf("min_retained_area_percent", "must be finite, greater than 0, and at most 100", "crop.min_retained_area_percent must be finite, greater than 0, and at most 100"))
	}
	if crop.MinWidth < 1 {
		found = append(found, problemf("min_width", "must be at least 1", "crop.min_width must be at least 1"))
	}
	if crop.MinHeight < 1 {
		found = append(found, problemf("min_height", "must be at least 1", "crop.min_height must be at least 1"))
	}
	if crop.RequiredAlignment < 1 {
		found = append(found, problemf("required_alignment", "must be at least 1", "crop.required_alignment must be at least 1"))
	}
	return found
}

func (v VideoConfig) validate(p *problems, profile string) {
	for _, problem := range v.Problems() {
		p.addf("profile %q %s", profile, problem.text)
	}
	*p = append(*p, videoOverrideKeyProblems(profile, v.Overrides)...)
	for _, key := range sortedKeys(v.Overrides) {
		v.Overrides[key].validate(p, fmt.Sprintf("profile %q video.overrides.%s", profile, key), v)
	}
}

// Problems reports every rule the base video settings break, in the order
// Validate lists them. Overrides are checked separately, against this base.
func (v VideoConfig) Problems() []Problem {
	var found []Problem
	if !validVideoCodec(v.Codec) {
		found = append(found, problemf("codec", codecChoices, "video.codec %q is invalid (%s)", v.Codec, codecChoices))
	}
	if !validAccelerator(v.Accelerator) {
		found = append(found, problemf("accelerator", acceleratorChoices, "video.accelerator %q is invalid (%s)", v.Accelerator, acceleratorChoices))
	}
	if !video.ValidBitDepth(v.BitDepth) {
		found = append(found, problemf("bit_depth", "must be 8 or 10", "video.bit_depth %d is invalid (must be 8 or 10)", v.BitDepth))
	}
	switch {
	case v.CRFMin < 0:
		found = append(found, problemf("crf_min", "must be non-negative", "CRF values must be non-negative"))
	case v.CRFMax < 0:
		found = append(found, problemf("crf_max", "must be non-negative", "CRF values must be non-negative"))
	}
	if v.CRFMin > v.CRFMax {
		found = append(found, problemf("crf_min", "must be less than or equal to crf_max", "crf_min must be less than or equal to crf_max"))
	}
	if v.SampleDuration.Duration <= 0 {
		found = append(found, problemf("sample_duration", "must be positive", "video.sample_duration must be positive"))
	}
	if err := ffmpeg.ValidateEncoderArgs(v.FFmpegArgs); err != nil {
		found = append(found, problemf("ffmpeg_args", strings.TrimPrefix(err.Error(), "ffmpeg_args "), "video.%s", err))
	}
	if v.Samples < 0 {
		found = append(found, problemf("samples", "must be non-negative", "video.samples must be non-negative"))
	}
	switch {
	case !slices.Contains(qualityMetrics, v.Metric):
		found = append(found, problemf("metric", "must be vmaf or xpsnr", "video.metric %q is invalid (must be vmaf or xpsnr)", v.Metric))
	case !validQualityTarget(v.Target):
		found = append(found, problemf("target", "must be between 0 and 100", "video.target must be between 0 and 100 for metric %q", v.Metric))
	case v.Metric == "xpsnr" && v.Target == 0:
		found = append(found, problemf("target", "must be set when metric is \"xpsnr\" (typical targets are 35-50)", "video.target must be set when metric is \"xpsnr\" (typical targets are 35-50)"))
	}
	if !validPercent(v.MinSavingsPercent) {
		found = append(found, problemf("min_savings_percent", "must be between 0 and 100", "min_savings_percent must be between 0 and 100"))
	}
	return found
}

// validate checks one override against the base video settings it inherits
// from, because several constraints only hold for the effective values.
func (o VideoOverrideConfig) validate(p *problems, prefix string, base VideoConfig) {
	if err := ffmpeg.ValidateEncoderArgs(o.FFmpegArgs); err != nil {
		p.addf("%s.%s", prefix, err)
	}
	if o.Codec != nil {
		switch {
		case strings.TrimSpace(*o.Codec) == "":
			p.addf("%s.codec must not be empty", prefix)
		case !validVideoCodec(*o.Codec):
			p.addf("%s.codec %q is invalid (%s)", prefix, *o.Codec, codecChoices)
		}
	}
	if o.Accelerator != nil && !validAccelerator(*o.Accelerator) {
		p.addf("%s.accelerator %q is invalid (%s)", prefix, *o.Accelerator, acceleratorChoices)
	}
	if o.BitDepth != nil && !video.ValidBitDepth(*o.BitDepth) {
		p.addf("%s.bit_depth %d is invalid (must be 8 or 10)", prefix, *o.BitDepth)
	}
	metric := valueOr(o.Metric, base.Metric)
	metricValid := slices.Contains(qualityMetrics, metric)
	if o.Metric != nil {
		switch {
		case !metricValid:
			p.addf("%s.metric %q is invalid (must be vmaf or xpsnr)", prefix, metric)
		case metric != base.Metric && o.Target == nil:
			// Inheriting the base target across metrics is never right: a
			// VMAF 95 becomes an unattainable XPSNR 95 dB, and an XPSNR 42
			// becomes a uselessly low VMAF 42.
			p.addf("%s.metric changes the quality metric, so its target must be set too", prefix)
		}
	}
	if o.Target != nil {
		switch {
		case metricValid && !validQualityTarget(*o.Target):
			p.addf("%s.target must be between 0 and 100 for metric %q", prefix, metric)
		case *o.Target == 0 && metric == "xpsnr":
			p.addf("%s.target must be positive for metric \"xpsnr\" (typical targets are 35-50)", prefix)
		}
	}
	if o.MinSavingsPercent != nil && !validPercent(*o.MinSavingsPercent) {
		p.addf("%s.min_savings_percent must be between 0 and 100", prefix)
	}
	if o.CRFMin != nil && *o.CRFMin < 0 {
		p.addf("%s.crf_min must be non-negative", prefix)
	}
	if o.CRFMax != nil && *o.CRFMax < 0 {
		p.addf("%s.crf_max must be non-negative", prefix)
	}
	if valueOr(o.CRFMin, base.CRFMin) > valueOr(o.CRFMax, base.CRFMax) {
		p.addf("%s effective crf_min must be less than or equal to crf_max", prefix)
	}
}

func (arr ArrConfig) validate(p *problems, name string) {
	if !slices.Contains(arrProviders, arr.Type) {
		p.addf("arr %q type %q is invalid", name, arr.Type)
	}
	if strings.TrimSpace(arr.BaseURL) == "" {
		p.addf("arr %q base_url is required", name)
	}
	if strings.TrimSpace(arr.APIKey) == "" && strings.TrimSpace(arr.APIKeyFile) == "" {
		p.addf("arr %q api_key or api_key_file is required", name)
	}
}

func (library LegacyLibraryConfig) validate(p *problems, name string, profiles, arrs map[string]struct{}) {
	if !slices.Contains(libraryKinds, library.Kind) {
		p.addf("library %q kind %q is invalid", name, library.Kind)
	}
	if strings.TrimSpace(library.Path) == "" {
		p.addf("library %q path is required", name)
	}
	if _, exists := profiles[library.Profile]; !exists {
		p.addf("library %q references unknown profile %q", name, library.Profile)
	}
	if library.ConcurrencyLimit < 0 {
		p.addf("library %q concurrency_limit must be non-negative", name)
	}
	if library.ScanInterval.Duration < 0 {
		p.addf("library %q scan_interval must be greater than zero", name)
	}
	validateRegexps(p, fmt.Sprintf("library %q ignore_regex", name), library.IgnoreRegex)
	if strings.TrimSpace(library.Arr) != "" {
		if _, exists := arrs[library.Arr]; !exists {
			p.addf("library %q references unknown arr %q", name, library.Arr)
		}
	}
	if library.Kind != string(domain.LibraryKindDownload) {
		if !slices.Contains(replacementModes, library.Media.ReplacementMode) {
			p.addf("library %q media.replacement_mode %q is invalid", name, library.Media.ReplacementMode)
		}
		return
	}

	download := library.Download
	if strings.TrimSpace(library.Media.ReplacementMode) != "" {
		p.addf("download library %q media.replacement_mode must not be set", name)
	}
	if strings.TrimSpace(download.HandoffPath) == "" {
		p.addf("download library %q download.handoff_path is required", name)
	}
	if download.StableFor.Duration < 0 {
		p.addf("download library %q download.stable_for must be non-negative", name)
	}
	if !slices.Contains(packageModes, download.PackageMode) {
		p.addf("download library %q download.package_mode %q is invalid", name, download.PackageMode)
	}
	if !slices.Contains(handoffModes, download.HandoffMode) {
		p.addf("download library %q download.handoff_mode %q is invalid", name, download.HandoffMode)
	}
}

func videoOverrideKeyProblems(profileName string, overrides map[string]VideoOverrideConfig) []string {
	var p problems
	canonicalKeys := make(map[string]string, len(overrides))
	for _, key := range sortedKeys(overrides) {
		canonicalKey := canonicalVideoOverrideKey(key)
		if strings.TrimSpace(key) == "" {
			p.addf("profile %q video.overrides key must not be empty", profileName)
			continue
		}
		// Dolby Vision support was removed; its reserved override key must
		// fail loudly instead of silently encoding with base settings.
		if canonicalKey == "dolby-vision" {
			p.addf("profile %q video.overrides.%s targets removed Dolby Vision handling; delete the override (and any video.dolby_vision table)", profileName, key)
			continue
		}
		if previousKey, exists := canonicalKeys[canonicalKey]; exists {
			p.addf("profile %q video.overrides.%s collides with video.overrides.%s after canonicalization to %q", profileName, key, previousKey, canonicalKey)
			continue
		}
		canonicalKeys[canonicalKey] = key
	}
	return p
}

func canonicalVideoOverrideKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return key
	}
	return video.CanonicalCodec(key)
}

func validateRegexps(p *problems, name string, values []string) {
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			p.addf("%s[%d] must not be empty", name, i)
			continue
		}
		if _, err := regexp.Compile(value); err != nil {
			p.addf("%s[%d] is invalid: %v", name, i, err)
		}
	}
}

// NormalizeLogLevel trims and canonicalizes a configured daemon log level.
func NormalizeLogLevel(level string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(level))
	if !slices.Contains(logLevels, normalized) {
		return "", false
	}
	return normalized, true
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// validPercent reports whether value is a finite percentage in [0, 100].
func validPercent(value float64) bool {
	return finite(value) && value >= 0 && value <= 100
}

// validQualityTarget is the shared range for VMAF and XPSNR targets; the
// caller checks the metric itself.
func validQualityTarget(target float64) bool {
	return target >= 0 && target <= 100
}

func normalizeContainer(container string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(container), "."))
}

func validVideoCodec(codec string) bool {
	return slices.Contains(videoCodecs, video.NormalizeCodec(codec))
}

func normalizeConfigVideoCodec(codec string) string {
	switch normalized := video.NormalizeCodec(codec); normalized {
	case "h265":
		return "hevc"
	case "avc":
		return "h264"
	default:
		return normalized
	}
}

func validAccelerator(value string) bool {
	return slices.Contains(accelerators, video.NormalizeAccelerator(value))
}

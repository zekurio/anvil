package webapi

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/store"
)

func durationText(value time.Duration) string {
	return config.Duration{Duration: value}.String()
}

// optionalDuration renders a duration that has "use the default" as its zero
// value, which travels as an empty string.
func optionalDuration(value time.Duration) string {
	if value <= 0 {
		return ""
	}
	return durationText(value)
}

// parseDuration reads a duration field. An empty string means "use the
// default" and returns fallback.
func parseDuration(field, text string, fallback time.Duration) (time.Duration, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(text)
	if err != nil {
		return 0, invalidf(field, "%q is not a duration. Write one such as \"30m\", \"90s\", or \"2h\".", text)
	}
	if value < 0 {
		return 0, invalidf(field, "The duration must not be negative.")
	}
	return value, nil
}

// libraryConfig converts a request body into the library the store keeps,
// with defaults filled in. It reports the first unreadable duration; every
// other rule is checked by config.LibraryConfig.Problems.
func libraryConfig(write LibraryWrite) (config.LibraryConfig, error) {
	scanInterval, err := parseDuration("scan_interval", write.ScanInterval, 0)
	if err != nil {
		return config.LibraryConfig{}, err
	}
	stableFor, err := parseDuration("download.stable_for", write.Download.StableFor, config.DefaultStableFor)
	if err != nil {
		return config.LibraryConfig{}, err
	}
	rules := make([]config.Rule, 0, len(write.Rules))
	for _, rule := range write.Rules {
		rules = append(rules, config.Rule{
			Action: strings.TrimSpace(rule.Action), Field: strings.TrimSpace(rule.Field),
			Op: strings.TrimSpace(rule.Op), Value: strings.TrimSpace(rule.Value),
		})
	}
	library := config.LibraryConfig{
		Name:             write.Name,
		Kind:             strings.TrimSpace(write.Kind),
		Path:             cleanPath(write.Path),
		FlowID:           write.FlowID,
		Paused:           write.Paused,
		ScanInterval:     config.Duration{Duration: scanInterval},
		Priority:         write.Priority,
		ConcurrencyLimit: write.ConcurrencyLimit,
		Arr:              strings.TrimSpace(write.Arr),
		Rules:            rules,
		RuleFallback:     strings.TrimSpace(write.RuleFallback),
		Media:            config.MediaLibraryConfig{ReplacementMode: strings.TrimSpace(write.Media.ReplacementMode)},
		Download: config.DownloadLibraryConfig{
			HandoffPath:          cleanPath(write.Download.HandoffPath),
			StableFor:            config.Duration{Duration: stableFor},
			PackageMode:          strings.TrimSpace(write.Download.PackageMode),
			HandoffMode:          strings.TrimSpace(write.Download.HandoffMode),
			PreserveRelativePath: write.Download.PreserveRelativePath,
			CleanupSourceMedia:   write.Download.CleanupSourceMedia,
			PruneEmptyDirs:       write.Download.PruneEmptyDirs,
			IgnorableGlobs:       write.Download.IgnorableGlobs,
		},
	}
	return library.WithDefaults(), nil
}

// cleanPath trims and cleans a path the operator typed. An empty path stays
// empty, so the validation can say it is missing.
func cleanPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return path.Clean(value)
}

// libraryWrite is the stored library in the shape requests carry it, which
// is also the editable half of the Library response.
func libraryWrite(library config.LibraryConfig) LibraryWrite {
	rules := make([]Rule, 0, len(library.Rules))
	for _, rule := range library.Rules {
		rules = append(rules, Rule{Action: rule.Action, Field: rule.Field, Op: rule.Op, Value: rule.Value})
	}
	write := LibraryWrite{
		Name:             library.Name,
		Kind:             library.Kind,
		Path:             library.Path,
		FlowID:           library.FlowID,
		Paused:           library.Paused,
		Priority:         library.Priority,
		ConcurrencyLimit: library.ConcurrencyLimit,
		ScanInterval:     optionalDuration(library.ScanInterval.Duration),
		Arr:              library.Arr,
		Rules:            rules,
		RuleFallback:     config.NormalizeRuleFallback(library.RuleFallback),
		Media:            MediaOptions{ReplacementMode: library.Media.ReplacementMode},
		Download: DownloadOptions{
			HandoffPath:          library.Download.HandoffPath,
			PackageMode:          library.Download.PackageMode,
			HandoffMode:          library.Download.HandoffMode,
			PreserveRelativePath: library.Download.PreserveRelativePath,
			CleanupSourceMedia:   library.Download.CleanupSourceMedia,
			PruneEmptyDirs:       library.Download.PruneEmptyDirs,
			IgnorableGlobs:       append([]string{}, library.Download.IgnorableGlobs...),
		},
	}
	if library.IsDownload() {
		write.Download.StableFor = durationText(library.Download.StableFor.Duration)
	}
	return write
}

func libraryStats(overview store.LibraryOverview) LibraryStats {
	stats := LibraryStats{Files: overview.Files, TotalBytes: overview.TotalBytes, SavedBytes: overview.SavedBytes}
	for state, count := range overview.Jobs {
		switch store.FileStatusOf(state) {
		case store.FileStatusQueued:
			stats.Pending += count
		case store.FileStatusRunning:
			stats.Running += count
		case store.FileStatusDone:
			stats.Done += count
		case store.FileStatusFailed:
			stats.Failed += count
		case store.FileStatusSkipped:
			stats.Skipped += count
		}
	}
	return stats
}

// fileFacts describes a probe in the contract's words. The resolution class,
// HDR kind, and bitrate come from flow.Describe, so what the UI shows about a
// file is what a condition node would branch on.
func fileFacts(probe domain.ProbeResult) FileFacts {
	description := flow.Describe(probe)
	facts := FileFacts{
		Path:            probe.Path,
		SizeBytes:       probe.SizeBytes,
		DurationSeconds: probe.DurationSeconds,
		Container:       containerLabel(probe),
		HDR:             description.HDR,
		BitrateKbps:     description.BitrateKbps,
	}
	if video, found := domain.PrimaryVideoStream(probe.Streams); found {
		facts.VideoCodec, facts.Width, facts.Height = video.Codec, video.Width, video.Height
	}
	facts.Audio, facts.Subtitles = streamFacts(probe, "audio"), streamFacts(probe, "subtitle")
	return facts
}

func streamFacts(probe domain.ProbeResult, kind string) []StreamFacts {
	facts := []StreamFacts{}
	for _, stream := range probe.Streams {
		if stream.Type != kind {
			continue
		}
		fact := StreamFacts{
			Index: stream.Index, Codec: stream.Codec, Language: stream.Language, Title: stream.Title,
			Default: stream.Disposition["default"], Forced: stream.Disposition["forced"],
		}
		if kind == "audio" {
			fact.Channels = stream.Channels
		}
		facts = append(facts, fact)
	}
	return facts
}

// listedProbe turns the probe facts the store keeps for a file list back
// into a probe, so a list labels a file with the same functions that label
// it everywhere else. It holds nothing but those facts.
func listedProbe(path string, facts store.ProbeFacts) domain.ProbeResult {
	probe := domain.ProbeResult{Path: path, FormatName: facts.FormatName}
	if facts.VideoCodec != "" || facts.Width > 0 || facts.Height > 0 {
		probe.Streams = append(probe.Streams, domain.MediaStream{Type: "video", Codec: facts.VideoCodec, Width: facts.Width, Height: facts.Height})
	}
	if facts.AudioCodec != "" || facts.AudioChannels > 0 {
		probe.Streams = append(probe.Streams, domain.MediaStream{Type: "audio", Codec: facts.AudioCodec, Channels: facts.AudioChannels})
	}
	return probe
}

// containerLabel names a container by the extension people know it under.
// ffprobe reports demuxer names, several of which cover a family of formats.
func containerLabel(probe domain.ProbeResult) string {
	format := strings.ToLower(probe.FormatName)
	extension := strings.ToLower(strings.TrimPrefix(path.Ext(probe.Path), "."))
	switch {
	case format == "":
		return ""
	case strings.Contains(format, "matroska"):
		if extension == "webm" {
			return "webm"
		}
		return "mkv"
	case strings.Contains(format, "mp4"):
		if extension == "mov" || extension == "m4v" {
			return extension
		}
		return "mp4"
	case format == "mpegts":
		return "ts"
	}
	name, _, _ := strings.Cut(format, ",")
	return name
}

// audioLabel describes the first audio track, such as "eac3 5.1".
func audioLabel(probe domain.ProbeResult) string {
	for _, stream := range probe.Streams {
		if stream.Type != "audio" {
			continue
		}
		return strings.TrimSpace(stream.Codec + " " + channelLabel(stream.Channels))
	}
	return ""
}

func channelLabel(channels int) string {
	switch channels {
	case 0:
		return ""
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	default:
		return fmt.Sprintf("%dch", channels)
	}
}

// flowCheck derives everything the editor shows about a graph.
func flowCheck(graph flow.Graph) FlowCheck {
	check := FlowCheck{Valid: true, Issues: flow.Validate(graph), Nodes: make(map[string]NodeCheck, len(graph.Nodes))}
	invalid := make(map[string]bool)
	for _, issue := range check.Issues {
		if issue.Severity == flow.SeverityError {
			check.Valid = false
			invalid[issue.NodeID] = true
		}
	}
	for id, summary := range flow.Summaries(graph) {
		check.Nodes[id] = NodeCheck{Summary: summary, Invalid: invalid[id]}
	}
	return check
}

// normalizeGraph makes the lists of a graph non-nil, so it encodes as [] and
// not null.
func normalizeGraph(graph flow.Graph) flow.Graph {
	if graph.Nodes == nil {
		graph.Nodes = []flow.Node{}
	}
	if graph.Edges == nil {
		graph.Edges = []flow.Edge{}
	}
	return graph
}

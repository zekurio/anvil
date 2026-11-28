package flow

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/video"
)

// Facts is everything evaluating a flow may look at: the probe of the input
// file and the job metadata gathered before it.
type Facts struct {
	Probe domain.ProbeResult
	// Metadata supplies OriginalLanguage for the "orig" language token.
	Metadata domain.JobMetadata
}

// Description is a probe in the terms the condition nodes compare, so what
// the UI shows about a file agrees with how a flow branches on it.
type Description struct {
	// Resolution is the class check.resolution works with, such as "1080p".
	// Video below 480p is named by its height ("360p"); it is empty without
	// a video stream.
	Resolution string
	// HDR is "", "hdr10", "hlg", or "dolby_vision". Dolby Vision wins over
	// the HDR10 or HLG base layer it rides on, although check.hdr matches
	// such a file under either name.
	HDR string
	// BitrateKbps is the overall bitrate from size and duration; 0 when the
	// duration is unknown.
	BitrateKbps int64
}

// Describe summarizes a probe the way the condition nodes read it.
func Describe(probe domain.ProbeResult) Description {
	var description Description
	if stream, ok := domain.PrimaryVideoStream(probe.Streams); ok {
		description.Resolution = resolutionName(stream.Width, stream.Height)
		if kinds := hdrKinds(stream); len(kinds) > 0 {
			description.HDR = kinds[0]
		}
	}
	description.BitrateKbps, _ = overallBitrateKbps(probe)
	return description
}

type resolutionTier struct {
	name          string
	width, height int
}

// resolutionTiers are the classes check.resolution offers, smallest first. A
// frame reaches a tier by width or by height, so 1920x800 video with its
// letterbox cropped away still counts as 1080p.
var resolutionTiers = []resolutionTier{
	{"480p", 854, 480},
	{"720p", 1280, 720},
	{"1080p", 1920, 1080},
	{"1440p", 2560, 1440},
	{"2160p", 3840, 2160},
}

// resolutionRank counts the tiers a frame reaches: 0 is below 480p and
// len(resolutionTiers) is 2160p or more.
func resolutionRank(width, height int) int {
	rank := 0
	for i, tier := range resolutionTiers {
		if width >= tier.width || height >= tier.height {
			rank = i + 1
		}
	}
	return rank
}

func resolutionName(width, height int) string {
	switch rank := resolutionRank(width, height); {
	case rank > 0:
		return resolutionTiers[rank-1].name
	case height > 0:
		return strconv.Itoa(height) + "p"
	default:
		return ""
	}
}

// hdrKinds names what a video stream carries, most specific first. The
// transfer names are ffprobe's, the same ones pkg/ffmpeg tags HDR encodes by.
func hdrKinds(stream domain.MediaStream) []string {
	var kinds []string
	if stream.DolbyVision != nil {
		kinds = append(kinds, "dolby_vision")
	}
	switch strings.ToLower(strings.TrimSpace(stream.ColorTransfer)) {
	case "smpte2084":
		kinds = append(kinds, "hdr10")
	case "arib-std-b67":
		kinds = append(kinds, "hlg")
	}
	return kinds
}

func overallBitrateKbps(probe domain.ProbeResult) (int64, bool) {
	if probe.DurationSeconds <= 0 || probe.SizeBytes <= 0 {
		return 0, false
	}
	return int64(math.Round(float64(probe.SizeBytes) * 8 / probe.DurationSeconds / 1000)), true
}

// codecFamilies reads the codec list of check.video_codec as a set of
// families: sorted, without blanks or repeats. The entries and the codec of
// a file go through the same codecFamily, so "h265" matches hevc video and
// "mpeg2" matches what ffprobe calls mpeg2video.
func codecFamilies(entries []string) []string {
	var families []string
	for _, entry := range entries {
		if family := codecFamily(entry); family != "" && !slices.Contains(families, family) {
			families = append(families, family)
		}
	}
	slices.Sort(families)
	return families
}

// codecFamily names a video codec the way check.video_codec compares it. It
// builds on video.CanonicalCodec, which folds encoder and alias names, and
// drops the "video" suffix ffprobe puts on the MPEG-1 and MPEG-2 decoders.
// An empty name stays empty rather than becoming CanonicalCodec's default.
func codecFamily(codec string) string {
	if strings.TrimSpace(codec) == "" {
		return ""
	}
	switch family := video.CanonicalCodec(codec); family {
	case "mpeg1video":
		return "mpeg1"
	case "mpeg2video":
		return "mpeg2"
	default:
		return family
	}
}

func streamsOfType(probe domain.ProbeResult, kind string) []domain.MediaStream {
	var streams []domain.MediaStream
	for _, stream := range probe.Streams {
		if stream.Type == kind {
			streams = append(streams, stream)
		}
	}
	return streams
}

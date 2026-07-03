package marker

import (
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	videocodec "github.com/zekurio/anvil/pkg/video"
)

const (
	Version = "1"

	TagProcessed        = "anvil.processed"
	TagEncoded          = "anvil.encoded"
	TagProcessReason    = "anvil.process.reason"
	TagVersion          = "anvil.version"
	TagProfile          = "anvil.profile"
	TagVideoAction      = "anvil.video.action"
	TagVideoCodec       = "anvil.video.codec"
	TagVideoPixelFormat = "anvil.video.pixel_format"
	TagVideoCRF         = "anvil.video.crf"
	TagCrop             = "anvil.crop"

	VideoActionEncode = "encode"
	VideoActionCopy   = "copy"
	VideoActionRemux  = "remux"
)

type Match struct {
	Compatible bool
	Tags       map[string]string
	CropFilter string
}

func Detect(probe domain.ProbeResult, profile domain.Profile) Match {
	return DetectVideo(probe, profile.Name, profile.Video.Codec, videocodec.SoftwarePixelFormat(profile.Video.BitDepth))
}

func DetectVideo(probe domain.ProbeResult, profileName domain.ProfileName, videoCodec string, pixelFormat string) Match {
	for _, stream := range probe.Streams {
		if stream.Type != "video" || stream.AttachedPic() {
			continue
		}
		tags := NormalizeTags(stream.Tags)
		if !truthy(tags[TagEncoded]) {
			continue
		}
		if !compatibleVideo(tags, profileName, videoCodec, pixelFormat) {
			return Match{Tags: tags, CropFilter: tags[TagCrop]}
		}
		return Match{Compatible: true, Tags: tags, CropFilter: tags[TagCrop]}
	}
	return Match{}
}

func DetectProcessed(probe domain.ProbeResult, profile domain.Profile) Match {
	for _, stream := range probe.Streams {
		if stream.Type != "video" || stream.AttachedPic() {
			continue
		}
		tags := NormalizeTags(stream.Tags)
		if !truthy(tags[TagProcessed]) {
			continue
		}
		if profile.Name != "" && tags[TagProfile] != string(profile.Name) {
			return Match{Tags: tags, CropFilter: tags[TagCrop]}
		}
		return Match{Compatible: true, Tags: tags, CropFilter: tags[TagCrop]}
	}
	return Match{}
}

func NormalizeTags(tags map[string]string) map[string]string {
	normalized := make(map[string]string, len(tags))
	for key, value := range tags {
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			normalized[key] = value
		}
	}
	return normalized
}

func OutputTags(plan domain.EncodePlan) map[string]string {
	tags := make(map[string]string, len(plan.AnvilTags)+9)
	for key, value := range plan.AnvilTags {
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			tags[key] = value
		}
	}
	tags[TagProcessed] = "true"
	tags[TagVersion] = Version
	if plan.ProfileName != "" {
		tags[TagProfile] = string(plan.ProfileName)
	}

	if plan.VideoCopy {
		tags[TagVideoAction] = VideoActionCopy
		if plan.VideoCopyReason != "" {
			tags[TagProcessReason] = plan.VideoCopyReason
		}
		return tags
	}

	tags[TagEncoded] = "true"
	tags[TagVideoAction] = VideoActionEncode
	if plan.VideoCodec != "" {
		tags[TagVideoCodec] = plan.VideoCodec
	}
	if pixelFormat := outputPixelFormat(plan); pixelFormat != "" {
		tags[TagVideoPixelFormat] = pixelFormat
	}
	if plan.CRF > 0 {
		tags[TagVideoCRF] = strconv.Itoa(plan.CRF)
	}
	if plan.CropFilter != "" {
		tags[TagCrop] = plan.CropFilter
	}
	return tags
}

func outputPixelFormat(plan domain.EncodePlan) string {
	if strings.TrimSpace(plan.PixelFormat) != "" {
		return plan.PixelFormat
	}
	if plan.BitDepth != 0 {
		return videocodec.SoftwarePixelFormat(plan.BitDepth)
	}
	return ""
}

func compatibleVideo(tags map[string]string, profileName domain.ProfileName, videoCodec string, pixelFormat string) bool {
	if profileName != "" && tags[TagProfile] != string(profileName) {
		return false
	}
	if !compatibleTag(tags[TagVideoCodec], videoCodec) {
		return false
	}
	if !compatibleTag(tags[TagVideoPixelFormat], pixelFormat) {
		return false
	}
	return true
}

func compatibleTag(actual string, expected string) bool {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return true
	}
	if videocodec.CanonicalCodec(actual) == videocodec.CanonicalCodec(expected) {
		return true
	}
	return actual == expected
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "t", "true", "yes", "y":
		return true
	default:
		return false
	}
}

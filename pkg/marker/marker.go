package marker

import (
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
)

const (
	Version = "1"

	TagEncoded          = "anvil.encoded"
	TagProcessed        = "anvil.processed"
	TagProcessReason    = "anvil.process.reason"
	TagVersion          = "anvil.version"
	TagProfile          = "anvil.profile"
	TagVideoAction      = "anvil.video.action"
	TagVideoCodec       = "anvil.video.codec"
	TagVideoPixelFormat = "anvil.video.pixel_format"
	TagVideoCRF         = "anvil.video.crf"
	TagCrop             = "anvil.crop"
)

type Match struct {
	Compatible bool
	Tags       map[string]string
	CropFilter string
}

func Detect(probe domain.ProbeResult, profile domain.Profile) Match {
	for _, stream := range probe.Streams {
		if stream.Type != "video" {
			continue
		}
		tags := NormalizeTags(stream.Tags)
		if !truthy(tags[TagEncoded]) {
			continue
		}
		if !compatibleProfile(tags, profile) {
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
		tags[TagVideoAction] = "copy"
		if plan.VideoCopyReason != "" {
			tags[TagProcessReason] = plan.VideoCopyReason
		}
		return tags
	}

	tags[TagEncoded] = "true"
	tags[TagVideoAction] = "encode"
	if plan.VideoCodec != "" {
		tags[TagVideoCodec] = plan.VideoCodec
	}
	if plan.PixelFormat != "" {
		tags[TagVideoPixelFormat] = plan.PixelFormat
	}
	if plan.CRF > 0 {
		tags[TagVideoCRF] = strconv.Itoa(plan.CRF)
	}
	if plan.CropFilter != "" {
		tags[TagCrop] = plan.CropFilter
	}
	return tags
}

func compatibleProfile(tags map[string]string, profile domain.Profile) bool {
	if profile.Name != "" && tags[TagProfile] != string(profile.Name) {
		return false
	}
	if !compatibleTag(tags[TagVideoCodec], profile.Video.Codec) {
		return false
	}
	if !compatibleTag(tags[TagVideoPixelFormat], profile.Video.PixelFormat) {
		return false
	}
	return true
}

func compatibleTag(actual string, expected string) bool {
	expected = strings.TrimSpace(expected)
	if expected == "" {
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

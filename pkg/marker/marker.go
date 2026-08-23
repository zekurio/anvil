package marker

import (
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
)

const TagProcessed = "anvil.processed"

func Processed(probe domain.ProbeResult) bool {
	for _, stream := range probe.Streams {
		if stream.Type != "video" || stream.AttachedPic() {
			continue
		}
		for key, value := range stream.Tags {
			if strings.EqualFold(strings.TrimSpace(key), TagProcessed) && strings.EqualFold(strings.TrimSpace(value), "true") {
				return true
			}
		}
	}
	return false
}

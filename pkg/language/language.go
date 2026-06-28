// Package language normalizes language names and codes used across metadata and streams.
package language

import "strings"

func Normalize(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	if IsUnknown(value) {
		return ""
	}
	if i := strings.Index(value, "-"); i >= 0 {
		value = value[:i]
	}
	switch value {
	case "de", "ger", "deu", "german", "deutsch":
		return "deu"
	case "en", "eng", "english":
		return "eng"
	case "ja", "jpn", "jp", "japanese":
		return "jpn"
	case "fr", "fre", "fra", "french", "francais":
		return "fra"
	case "es", "spa", "spanish", "espanol":
		return "spa"
	case "it", "ita", "italian":
		return "ita"
	case "ko", "kor", "korean":
		return "kor"
	case "zh", "chi", "zho", "chinese":
		return "zho"
	default:
		return value
	}
}

func IsUnknown(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "" || value == "und" || value == "unknown" || value == "undefined"
}

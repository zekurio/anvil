package video

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type CropSpec struct {
	Width  int
	Height int
	X      int
	Y      int
}

func ParseCropFilter(filter string) (CropSpec, bool) {
	matches := cropFilterPattern.FindStringSubmatch(strings.TrimSpace(filter))
	if len(matches) != 5 {
		return CropSpec{}, false
	}
	values := make([]int, 0, 4)
	for _, match := range matches[1:] {
		value, err := strconv.Atoi(match)
		if err != nil {
			return CropSpec{}, false
		}
		values = append(values, value)
	}
	return CropSpec{Width: values[0], Height: values[1], X: values[2], Y: values[3]}, true
}

func NoOpCrop(filter string, width int, height int) bool {
	crop, ok := ParseCropFilter(filter)
	if !ok || width <= 0 || height <= 0 {
		return false
	}
	return crop.Width == width && crop.Height == height && crop.X == 0 && crop.Y == 0
}

func QSVCropFilter(filter string, format string) (string, bool) {
	crop, ok := ParseCropFilter(filter)
	if !ok {
		return "", false
	}
	options := []string{
		fmt.Sprintf("cw=%d", crop.Width),
		fmt.Sprintf("ch=%d", crop.Height),
		fmt.Sprintf("cx=%d", crop.X),
		fmt.Sprintf("cy=%d", crop.Y),
	}
	if strings.TrimSpace(format) != "" {
		options = append(options, "format="+strings.TrimSpace(format))
	}
	return "vpp_qsv=" + strings.Join(options, ":"), true
}

func QSVFormatFilter(format string) string {
	format = strings.TrimSpace(format)
	if format == "" {
		return ""
	}
	return "vpp_qsv=format=" + format
}

var cropFilterPattern = regexp.MustCompile(`^crop=(\d+):(\d+):(\d+):(\d+)$`)

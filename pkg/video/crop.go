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

func QSVCropFilter(filter string) (string, bool) {
	crop, ok := ParseCropFilter(filter)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("vpp_qsv=cw=%d:ch=%d:cx=%d:cy=%d", crop.Width, crop.Height, crop.X, crop.Y), true
}

var cropFilterPattern = regexp.MustCompile(`^crop=(\d+):(\d+):(\d+):(\d+)$`)

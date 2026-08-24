package video

import (
	"errors"
	"fmt"
	"math"
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

// ValidateCropFilter checks a crop against source geometry and encoder safety
// constraints. Zero constraints are ignored so callers can still use it as a
// final syntax-and-bounds guard when no resolved policy is available.
func ValidateCropFilter(filter string, sourceWidth, sourceHeight, minWidth, minHeight int, minRetainedAreaPercent float64, requiredAlignment int) (CropSpec, float64, error) {
	crop, ok := ParseCropFilter(filter)
	if !ok || crop.Width <= 0 || crop.Height <= 0 {
		return CropSpec{}, 0, fmt.Errorf("candidate %q is not a valid positive crop rectangle", filter)
	}
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return crop, 0, errors.New("source video dimensions are unavailable")
	}
	retainedAreaPercent := float64(crop.Width) * float64(crop.Height) * 100 /
		(float64(sourceWidth) * float64(sourceHeight))

	var reasons []string
	if crop.Width > sourceWidth || crop.Height > sourceHeight ||
		crop.X > sourceWidth-crop.Width || crop.Y > sourceHeight-crop.Height {
		reasons = append(reasons, fmt.Sprintf("crop rectangle exceeds source dimensions %dx%d", sourceWidth, sourceHeight))
	}
	widthTooSmall := minWidth > 0 && crop.Width < minWidth
	heightTooSmall := minHeight > 0 && crop.Height < minHeight
	switch {
	case widthTooSmall && heightTooSmall:
		reasons = append(reasons, fmt.Sprintf("output %dx%d is smaller than minimum %dx%d", crop.Width, crop.Height, minWidth, minHeight))
	case widthTooSmall:
		reasons = append(reasons, fmt.Sprintf("output width %d is smaller than minimum %d", crop.Width, minWidth))
	case heightTooSmall:
		reasons = append(reasons, fmt.Sprintf("output height %d is smaller than minimum %d", crop.Height, minHeight))
	}
	if math.IsNaN(minRetainedAreaPercent) || math.IsInf(minRetainedAreaPercent, 0) {
		reasons = append(reasons, "minimum retained area must be finite")
	} else if minRetainedAreaPercent > 0 && retainedAreaPercent < minRetainedAreaPercent {
		reasons = append(reasons, fmt.Sprintf("retained area %.2f%% is below minimum %.2f%%", retainedAreaPercent, minRetainedAreaPercent))
	}
	if requiredAlignment > 1 && (crop.Width%requiredAlignment != 0 ||
		crop.Height%requiredAlignment != 0 ||
		crop.X%requiredAlignment != 0 ||
		crop.Y%requiredAlignment != 0) {
		reasons = append(reasons, fmt.Sprintf("crop geometry is not aligned to %d pixels", requiredAlignment))
	}
	if len(reasons) > 0 {
		return crop, retainedAreaPercent, errors.New(strings.Join(reasons, "; "))
	}
	return crop, retainedAreaPercent, nil
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

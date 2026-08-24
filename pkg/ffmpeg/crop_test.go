package ffmpeg

import (
	"slices"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestArgsOmitsUnsafeSoftwareCropAtProcessBoundary(t *testing.T) {
	args := Args(unsafeCropPlan())
	if slices.Contains(args, "-vf") {
		t.Fatalf("Args applied unsafe crop: %q", args)
	}
}

func TestArgsOmitsUnsafeQSVCropAtProcessBoundary(t *testing.T) {
	plan := unsafeCropPlan()
	plan.Accelerator = "qsv"
	plan.VideoCodec = "av1_qsv"
	plan.InputVideoCodec = "hevc"
	args := Args(plan)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "cw=176") || strings.Contains(joined, plan.CropFilter) {
		t.Fatalf("Args applied unsafe QSV crop: %q", args)
	}
}

func unsafeCropPlan() domain.EncodePlan {
	return domain.EncodePlan{
		InputPath:   "movie.mkv",
		OutputPath:  "movie.anvil-part",
		InputWidth:  1920,
		InputHeight: 1080,
		VideoCodec:  "libsvtav1",
		CropFilter:  "crop=176:64:996:64",
		CropPolicy: domain.CropPolicy{
			MinRetainedAreaPercent: 70,
			MinWidth:               128,
			MinHeight:              128,
			RequiredAlignment:      2,
		},
		Container: "mkv",
	}
}

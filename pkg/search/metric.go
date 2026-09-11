package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/ffmpeg"
	"github.com/zekurio/anvil/pkg/process"
	"github.com/zekurio/anvil/pkg/video"
)

var planarFormat = regexp.MustCompile(`^(yuv(?:420|422|444)p)(?:(9|10|12|14|16)(?:le|be))?$`)

// Preserve source precision and chroma when comparing to a lower-depth or
// subsampled output. Downconverting the reference would hide that loss.
func metricPixelFormat(plan domain.EncodePlan) string {
	match := planarFormat.FindStringSubmatch(plan.InputPixelFormat)
	if match == nil {
		return "yuv444p16le"
	}
	depth := video.NormalizeBitDepth(plan.BitDepth)
	switch match[2] {
	case "9", "10":
		depth = max(depth, 10)
	case "12":
		depth = max(depth, 12)
	case "14", "16":
		depth = 16
	}
	if depth == 8 {
		return match[1]
	}
	return match[1] + strconv.Itoa(depth) + "le"
}

func metricArgs(plan domain.EncodePlan, reference, encoded, logName string) []string {
	rate := "25"
	if plan.Metric == domain.QualityMetricXPSNR {
		rate = "60"
	}
	threads := strconv.Itoa(max(plan.Threads, 1))
	// Regenerate timestamps from frame order on both inputs. Frame counts are
	// checked separately, so shortest/repeatlast cannot hide a truncated encode.
	normalize := "format=" + metricPixelFormat(plan) + ",settb=AVTB,setpts=N/(" + rate + "*TB)"
	refFilter := ffmpeg.ReferenceFilter(plan)
	if refFilter != "" {
		refFilter += ","
	}
	metric := "libvmaf=log_fmt=json:log_path=" + logName + ":n_threads=" + threads
	if plan.Metric == domain.QualityMetricXPSNR {
		metric = "xpsnr=stats_file=" + logName
	}
	graph := "[0:v]" + normalize + "[dist];[1:v]" + refFilter + normalize + "[ref];[dist][ref]" + metric + ":shortest=1:repeatlast=0"
	return []string{"-hide_banner", "-nostdin", "-xerror", "-threads", "1", "-r", rate, "-i", encoded,
		"-threads", "1", "-r", rate, "-i", reference, "-filter_complex_threads", threads,
		"-filter_complex", graph, "-an", "-sn", "-dn", "-f", "null", "-"}
}

func measureQuality(ctx context.Context, runner process.Runner, plan domain.EncodePlan, ref sample, encoded, dir string) (float64, error) {
	logName := "metric.json"
	if plan.Metric == domain.QualityMetricXPSNR {
		logName = "metric.log"
	}
	path := filepath.Join(dir, logName)
	// A successful process without a new metric file must not reuse old scores.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("clear metric log: %w", err)
	}
	_, err := runner.Run(ctx, process.Command{Name: "ffmpeg", Args: metricArgs(plan, ref.path, encoded, logName), Dir: dir})
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat metric log: %w", err)
	}
	if info.Size() > process.FullCaptureLimit {
		return 0, errors.New("metric log exceeds capture limit; reduce sample_duration")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read metric log: %w", err)
	}
	if plan.Metric == domain.QualityMetricXPSNR {
		return parseXPSNR(data, ref.frames)
	}
	return parseVMAF(data, ref.frames)
}

func parseVMAF(data []byte, frames int) (float64, error) {
	var result struct {
		Frames []json.RawMessage `json:"frames"`
		Pooled struct {
			VMAF struct {
				Mean *float64 `json:"mean"`
			} `json:"vmaf"`
		} `json:"pooled_metrics"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return 0, fmt.Errorf("parse VMAF log: %w", err)
	}
	score := result.Pooled.VMAF.Mean
	if frames <= 0 || len(result.Frames) != frames || score == nil || math.IsNaN(*score) || math.IsInf(*score, 0) || *score < 0 || *score > 100 {
		return 0, fmt.Errorf("invalid VMAF log: expected %d scored frames and a finite mean in [0,100]", frames)
	}
	return *score, nil
}

var xpsnrSummary = regexp.MustCompile(`(?m)^XPSNR average,\s*(\d+) frames[^\r\n]*`)
var xpsnrPlane = regexp.MustCompile(`\b[yuv]:\s*([^\s)]+)`)

func parseXPSNR(data []byte, frames int) (float64, error) {
	summary := xpsnrSummary.FindSubmatch(data)
	if len(summary) != 2 {
		return 0, errors.New("XPSNR log has no summary")
	}
	count, err := strconv.Atoi(string(summary[1]))
	if err != nil || count != frames || frames <= 0 {
		return 0, fmt.Errorf("XPSNR log frame count does not match %d reference frames", frames)
	}
	planes := xpsnrPlane.FindAllSubmatch(summary[0], -1)
	if len(planes) != 3 {
		return 0, errors.New("XPSNR log must contain all three YUV plane averages")
	}
	score := math.Inf(1)
	for _, plane := range planes {
		value, err := strconv.ParseFloat(strings.ToLower(string(plane[1])), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, -1) {
			return 0, fmt.Errorf("invalid XPSNR plane score %q", plane[1])
		}
		score = min(score, value)
	}
	// Identical images have infinite XPSNR. All supported targets are <=100;
	// store 100 for that case so checkpoints and operator JSON remain valid.
	if math.IsInf(score, 1) {
		score = 100
	}
	return score, nil
}

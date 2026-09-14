package search

import (
	"slices"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestVMAFAnalysisMatchesABAV1AutoScale(t *testing.T) {
	for _, tc := range []struct {
		name      string
		width     int
		height    int
		crop      string
		wantScale string
		wantModel string
	}{
		{name: "720p upscales to 1080p", width: 1280, height: 720, wantScale: "scale=1920:-1:flags=bicubic"},
		{name: "1080p native", width: 1920, height: 1080},
		{name: "portrait upscales vertically", width: 1000, height: 900, wantScale: "scale=-1:1080:flags=bicubic"},
		{name: "1440p uses default model", width: 2560, height: 1440},
		{name: "3k upscales to 4k with 4k model", width: 3008, height: 1692, wantScale: "scale=3840:-1:flags=bicubic", wantModel: "vmaf_4k_v0.6.1"},
		{name: "4k uses 4k model", width: 3840, height: 2160, wantModel: "vmaf_4k_v0.6.1"},
		{name: "crop drives the analysis size", width: 1920, height: 1080, crop: "crop=1280:720:0:0", wantScale: "scale=1920:-1:flags=bicubic"},
		{name: "missing dimensions", width: 0, height: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := domain.EncodePlan{InputWidth: tc.width, InputHeight: tc.height, CropFilter: tc.crop}
			scale, model := vmafAnalysis(plan)
			if scale != tc.wantScale || model != tc.wantModel {
				t.Fatalf("vmafAnalysis = %q, %q; want %q, %q", scale, model, tc.wantScale, tc.wantModel)
			}
		})
	}
}

func TestMetricArgsScaleModelAndRate(t *testing.T) {
	vmaf := metricArgs(domain.EncodePlan{InputWidth: 1280, InputHeight: 720, Metric: domain.QualityMetricVMAF, Threads: 4}, "ref.mkv", "dist.mkv", "metric.json")
	graph := vmaf[slices.Index(vmaf, "-filter_complex")+1]
	if !strings.Contains(graph, "scale=1920:-1:flags=bicubic") || !strings.Contains(graph, "libvmaf=") {
		t.Fatalf("vmaf graph = %q", graph)
	}
	if !containsPair(vmaf, "-threads", "4") {
		t.Fatalf("decoder threads = %q", vmaf)
	}
	if !containsPair(vmaf, "-r", "25") {
		t.Fatalf("vmaf rate = %q", vmaf)
	}

	uhd := metricArgs(domain.EncodePlan{InputWidth: 3840, InputHeight: 2160, Metric: domain.QualityMetricVMAF, Threads: 4}, "ref.mkv", "dist.mkv", "metric.json")
	graph = uhd[slices.Index(uhd, "-filter_complex")+1]
	if !strings.Contains(graph, "model=version=vmaf_4k_v0.6.1") || strings.Contains(graph, "scale=") {
		t.Fatalf("4k vmaf graph = %q", graph)
	}

	xpsnr := metricArgs(domain.EncodePlan{InputWidth: 1280, InputHeight: 720, Metric: domain.QualityMetricXPSNR, Threads: 4}, "ref.mkv", "dist.mkv", "metric.log")
	graph = xpsnr[slices.Index(xpsnr, "-filter_complex")+1]
	if strings.Contains(graph, "scale=") || !strings.Contains(graph, "xpsnr=stats_file=metric.log") {
		t.Fatalf("xpsnr graph = %q", graph)
	}
	if !containsPair(xpsnr, "-r", "60") {
		t.Fatalf("xpsnr rate = %q", xpsnr)
	}
}

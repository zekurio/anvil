package search

import (
	"slices"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestSearchArgsSamples(t *testing.T) {
	args := SearchArgs(domain.EncodePlan{
		InputPath: "movie.mkv", CRFMin: 14, CRFMax: 38, SearchSamples: 12,
	})
	want := []string{"crf-search", "-i", "movie.mkv", "--min-crf", "14", "--max-crf", "38", "--samples", "12"}
	if !slices.Equal(args, want) {
		t.Fatalf("SearchArgs = %q, want %q", args, want)
	}
}

func TestSearchArgsOmitsUnsafeCropAtProcessBoundary(t *testing.T) {
	args := SearchArgs(domain.EncodePlan{
		InputPath:   "movie.mkv",
		InputWidth:  1920,
		InputHeight: 1080,
		CropFilter:  "crop=176:64:996:64",
		CropPolicy: domain.CropPolicy{
			MinRetainedAreaPercent: 70,
			MinWidth:               128,
			MinHeight:              128,
			RequiredAlignment:      2,
		},
	})
	if slices.Contains(args, "--vfilter") {
		t.Fatalf("SearchArgs applied unsafe crop: %q", args)
	}
}

func TestParseNoFitResultChoosesBestCandidateWithinSizeLimit(t *testing.T) {
	output := `
[2026-08-28T23:10:46Z INFO  ab_av1::command::sample_encode] sample 1/10 crf 18 VMAF 94.45 (70%)
[2026-08-28T23:10:47Z INFO  ab_av1::vmaf] vmaf episode.sample123+480f.hevc_qsv.crf8.mkv vs reference episode.sample123+480f.mkv
[2026-08-28T23:13:35Z INFO  ab_av1::command::crf_search] crf 18 VMAF 94.27 (50%)
[2026-08-28T23:15:46Z INFO  ab_av1::command::crf_search] crf 8 VMAF 96.95 (254%)
[2026-08-28T23:18:05Z INFO  ab_av1::command::crf_search] crf 12 VMAF 96.52 (180%)
[2026-08-28T23:21:28Z INFO  ab_av1::command::crf_search] crf 13 VMAF 96.22 (147%)
[2026-08-28T23:24:55Z INFO  ab_av1::command::crf_search] crf 14 VMAF 95.90 (120%)
Error: Failed to find a suitable crf`

	result, ok := ParseNoFitResult(output, domain.EncodePlan{
		CRFMin:             8,
		Metric:             domain.QualityMetricVMAF,
		MinSavingsPercent:  10,
		ForceEncodeOnNoFit: true,
	})
	if !ok || result.CRF != 18 || result.VMAF != 94.27 {
		t.Fatalf("ParseNoFitResult = (%+v, %t), want CRF 18 at VMAF 94.27", result, ok)
	}
}

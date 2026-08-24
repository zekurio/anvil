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

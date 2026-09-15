package search

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestSearchQualityAndSizePolicy(t *testing.T) {
	for _, tc := range []struct {
		name            string
		target, savings float64
		force           bool
		crf             int
		skip, forced    bool
	}{
		{name: "highest passing CRF", target: 95, savings: 20, crf: 5},
		{name: "zero target", target: 0, savings: 20, crf: 10},
		{name: "quality impossible", target: 100, savings: 20, skip: true},
		{name: "size impossible", target: 95, savings: 90, skip: true},
		{name: "forced size boundary", target: 100, savings: 50, force: true, crf: 7, forced: true},
		{name: "forced quality when no size fits", target: 100, savings: 99, force: true, crf: 2, forced: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := make(map[int]int)
			plan := domain.EncodePlan{CRFMin: 2, CRFMax: 10, Metric: domain.QualityMetricVMAF, Target: tc.target, MinSavingsPercent: tc.savings, ForceEncodeOnNoFit: tc.force}
			result, err := searchCRF(context.Background(), plan, func(_ context.Context, crf int) (domain.SearchCandidate, error) {
				calls[crf]++
				return domain.SearchCandidate{Score: 100 - float64(crf), EncodedPercent: 120 - 10*float64(crf)}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.CRF != tc.crf || result.SkipVideoEncode != tc.skip || (result.ForcedVideoEncodeReason != "") != tc.forced {
				t.Fatalf("result = %+v", result)
			}
			for crf, n := range calls {
				if n != 1 {
					t.Fatalf("CRF %d encoded %d times", crf, n)
				}
			}
			if len(calls) != len(result.Candidates) || result.RawOutput == "" {
				t.Fatal("candidate diagnostics missing")
			}
		})
	}
}

func TestSearchSingleCRFAndFailure(t *testing.T) {
	plan := domain.EncodePlan{CRFMin: 0, CRFMax: 0, Target: 95, Metric: domain.QualityMetricXPSNR, ForceEncodeOnNoFit: true}
	result, err := searchCRF(context.Background(), plan, func(context.Context, int) (domain.SearchCandidate, error) {
		return domain.SearchCandidate{Score: 96, EncodedPercent: 30}, nil
	})
	if err != nil || result.CRF != 0 || result.XPSNR != 96 || result.SkipVideoEncode {
		t.Fatalf("result = %+v, %v", result, err)
	}
	failure := errors.New("encoder failed")
	_, err = searchCRF(context.Background(), plan, func(context.Context, int) (domain.SearchCandidate, error) { return domain.SearchCandidate{}, failure })
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = searchCRF(ctx, plan, func(context.Context, int) (domain.SearchCandidate, error) {
		t.Fatal("measured after cancellation")
		return domain.SearchCandidate{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestSearchInterpolation(t *testing.T) {
	// Measured VMAF scores from the live-action comparison clip.
	scores := map[int]float64{40: 93.1287, 28: 94.5419, 24: 95.0986, 25: 94.9367}
	plan := domain.EncodePlan{CRFMin: 18, CRFMax: 40, Target: 95, Metric: domain.QualityMetricVMAF}
	result, err := searchCRF(context.Background(), plan, func(_ context.Context, crf int) (domain.SearchCandidate, error) {
		score, ok := scores[crf]
		if !ok {
			t.Fatalf("unexpected extra trial at CRF %d", crf)
		}
		return domain.SearchCandidate{Score: score, EncodedPercent: 40}, nil
	})
	if err != nil || result.CRF != 24 || len(result.Candidates) != 4 {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

func TestSearchInterpolationMatchesSweep(t *testing.T) {
	for name, score := range map[string]func(int) float64{
		"linear":  func(crf int) float64 { return 100 - float64(crf) },
		"curved":  func(crf int) float64 { return 100 - math.Pow(float64(crf)/8, 2) },
		"plateau": func(crf int) float64 { return 100 - float64(crf/8)*8 },
		"cliff": func(crf int) float64 {
			if crf < 40 {
				return 99 - float64(crf)/100
			}
			return 50 - float64(crf)/100
		},
	} {
		t.Run(name, func(t *testing.T) {
			for target := 0.0; target <= 100; target += 0.5 {
				want := -1
				for crf := 0; crf <= 63; crf++ {
					if score(crf) >= target {
						want = crf
					}
				}
				plan := domain.EncodePlan{CRFMin: 0, CRFMax: 63, Target: target, Metric: domain.QualityMetricVMAF}
				result, err := searchCRF(context.Background(), plan, func(_ context.Context, crf int) (domain.SearchCandidate, error) {
					return domain.SearchCandidate{Score: score(crf), EncodedPercent: 40}, nil
				})
				if err != nil || result.SkipVideoEncode != (want < 0) || (want >= 0 && result.CRF != want) || len(result.Candidates) > 15 {
					t.Fatalf("target %v: want CRF %d, got %+v, %v", target, want, result, err)
				}
			}
		})
	}
}

func TestSampleWindows(t *testing.T) {
	windows, err := sampleWindows(3600, 0, 20*time.Second)
	if err != nil || len(windows) != 5 || windows[0].offset != 350 || windows[4].offset != 3230 {
		t.Fatalf("windows = %+v, %v", windows, err)
	}
	windows, err = sampleWindows(10, 20, 20*time.Second)
	if err != nil || len(windows) != 1 || windows[0].offset != 0 || windows[0].duration != 10 {
		t.Fatalf("short windows = %+v, %v", windows, err)
	}
	for _, duration := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := sampleWindows(duration, 0, 0); err == nil {
			t.Fatalf("accepted duration %v", duration)
		}
	}
}

func TestMetricLogsRequireCompleteMeasurements(t *testing.T) {
	score, err := parseVMAF([]byte(`{"frames":[{},{}],"pooled_metrics":{"vmaf":{"mean":95.5}}}`), 2)
	if err != nil || score != 95.5 {
		t.Fatalf("VMAF = %v, %v", score, err)
	}
	for _, data := range []string{`{}`, `{"frames":[{}],"pooled_metrics":{"vmaf":{"mean":95}}}`, `{"frames":[{},{}],"pooled_metrics":{"vmaf":{"mean":null}}}`} {
		if _, err := parseVMAF([]byte(data), 2); err == nil {
			t.Fatalf("accepted invalid VMAF log %s", data)
		}
	}
	score, err = parseXPSNR([]byte("n: 1 XPSNR y: 90\nXPSNR average, 2 frames  y: 42.1  u: 43.2  v: 39.9  (minimum: 39.9)\n"), 2)
	if err != nil || score != 39.9 {
		t.Fatalf("XPSNR = %v, %v", score, err)
	}
	score, err = parseXPSNR([]byte("XPSNR average, 2 frames  y: inf  u: inf  v: inf\n"), 2)
	if err != nil || score != 100 {
		t.Fatalf("identical XPSNR = %v, %v", score, err)
	}
	for _, data := range []string{"", "XPSNR average, 1 frames y: 50 u: 50 v: 50", "XPSNR average, 2 frames y: nan u: 50 v: 50", "XPSNR average, 2 frames y: 50"} {
		if _, err := parseXPSNR([]byte(data), 2); err == nil {
			t.Fatalf("accepted invalid XPSNR log %s", data)
		}
	}
}

func TestMetricPreservesReferencePrecision(t *testing.T) {
	for _, tc := range []struct {
		source string
		depth  int
		want   string
	}{
		{"yuv420p", 8, "yuv420p"}, {"yuv420p", 10, "yuv420p10le"}, {"yuv420p10le", 8, "yuv420p10le"},
		{"yuv422p12le", 10, "yuv422p12le"}, {"gbrp16le", 8, "yuv444p16le"},
	} {
		if got := metricPixelFormat(domain.EncodePlan{InputPixelFormat: tc.source, BitDepth: tc.depth}); got != tc.want {
			t.Fatalf("%s/%d: %s, want %s", tc.source, tc.depth, got, tc.want)
		}
	}
}

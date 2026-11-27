package search

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

type countingSearcher struct {
	calls  int
	result domain.SearchResult
}

func (s *countingSearcher) Search(context.Context, domain.EncodePlan, string) (domain.SearchResult, error) {
	s.calls++
	return s.result, nil
}

func fixedJob(video domain.VideoProfile) *pipeline.JobContext {
	return &pipeline.JobContext{
		InputPath:  "movie.mkv",
		OutputPath: "movie.anvil-part",
		Profile:    domain.Profile{Container: "mkv", Video: video},
		Probe:      &domain.ProbeResult{DurationSeconds: 60, Streams: []domain.MediaStream{{Index: 0, Type: "video", Codec: "hevc", Width: 1920, Height: 1080}}},
	}
}

func TestBlockQualityModes(t *testing.T) {
	searched := domain.SearchResult{CRF: 31, Metric: domain.QualityMetricVMAF, VMAF: 95.4, Candidates: make([]domain.SearchCandidate, 4)}
	tests := []struct {
		name        string
		video       domain.VideoProfile
		wantCalls   int
		wantCRF     int
		wantMode    domain.VideoQualityMode
		wantSkip    bool
		wantMessage string
		wantErr     string
	}{
		{
			name: "fixed CRF never samples",
			// A target nothing could meet and a savings floor nothing could
			// reach must not matter in fixed mode.
			video:       domain.VideoProfile{Codec: "av1", QualityMode: domain.VideoQualityFixed, CRF: 24, CRFMin: 18, CRFMax: 40, Target: 100, MinSavingsPercent: 99},
			wantCRF:     24,
			wantMode:    domain.VideoQualityFixed,
			wantMessage: "fixed CRF 24, search skipped",
		},
		{
			name:        "search mode searches",
			video:       domain.VideoProfile{Codec: "av1", QualityMode: domain.VideoQualitySearch, CRF: 24, CRFMin: 18, CRFMax: 40},
			wantCalls:   1,
			wantCRF:     31,
			wantMessage: "selected CRF 31 (vmaf 95.40, 4 trials)",
		},
		{
			name:        "the zero mode searches",
			video:       domain.VideoProfile{Codec: "av1", CRFMin: 18, CRFMax: 40},
			wantCalls:   1,
			wantCRF:     31,
			wantMessage: "selected CRF 31 (vmaf 95.40, 4 trials)",
		},
		{
			name:        "skip encode wins over a fixed CRF",
			video:       domain.VideoProfile{Codec: "av1", QualityMode: domain.VideoQualityFixed, CRF: 24, SkipEncode: true},
			wantSkip:    true,
			wantMessage: "video encode skipped: video encoding disabled by profile",
		},
		{
			name:    "fixed CRF outside the encoder range",
			video:   domain.VideoProfile{Codec: "av1", QualityMode: domain.VideoQualityFixed, CRF: 64},
			wantErr: "fixed CRF 64 is outside the 1-63 range of encoder libsvtav1",
		},
		{
			name:    "libsvtav1 reads CRF 0 as unset",
			video:   domain.VideoProfile{Codec: "av1", QualityMode: domain.VideoQualityFixed},
			wantErr: "fixed CRF 0 is outside the 1-63 range of encoder libsvtav1",
		},
		{
			name:    "hardware encoders use their own scale",
			video:   domain.VideoProfile{Codec: "hevc", Accelerator: "qsv", QualityMode: domain.VideoQualityFixed, CRF: 52},
			wantErr: "fixed CRF 52 is outside the 1-51 range of encoder hevc_qsv",
		},
		{
			name:    "unknown mode",
			video:   domain.VideoProfile{Codec: "av1", QualityMode: "bitrate", CRF: 24},
			wantErr: `unsupported video quality mode "bitrate"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			searcher := &countingSearcher{result: searched}
			block := Block{Searcher: searcher}
			job := fixedJob(tt.video)
			err := block.Run(context.Background(), job)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				if searcher.calls != 0 || job.Search != nil {
					t.Fatalf("calls = %d, search = %+v", searcher.calls, job.Search)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if searcher.calls != tt.wantCalls {
				t.Fatalf("searcher ran %d times, want %d", searcher.calls, tt.wantCalls)
			}
			if job.Search.CRF != tt.wantCRF || job.Search.QualityMode != tt.wantMode || job.Search.SkipVideoEncode != tt.wantSkip {
				t.Fatalf("search = %+v", job.Search)
			}
			if tt.wantMode == domain.VideoQualityFixed && (len(job.Search.Candidates) != 0 || job.Search.ForcedVideoEncodeReason != "") {
				t.Fatalf("fixed result carries search output: %+v", job.Search)
			}
			report, ok := block.Artifact(job)
			if !ok || report.Name != SelectionArtifact || report.Message != tt.wantMessage {
				t.Fatalf("report = %+v, %v; want message %q", report, ok, tt.wantMessage)
			}
		})
	}
}

func TestBlockPropagatesSearchFailureWithoutArtifact(t *testing.T) {
	failure := errors.New("sample encode failed")
	block := Block{Searcher: searcherFunc(func(context.Context, domain.EncodePlan, string) (domain.SearchResult, error) {
		return domain.SearchResult{}, failure
	})}
	job := fixedJob(domain.VideoProfile{Codec: "av1", CRFMin: 18, CRFMax: 40})
	if err := block.Run(context.Background(), job); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	if _, ok := block.Artifact(job); ok {
		t.Fatal("failed search reported a CRF selection")
	}
}

type searcherFunc func(context.Context, domain.EncodePlan, string) (domain.SearchResult, error)

func (f searcherFunc) Search(ctx context.Context, plan domain.EncodePlan, scratchDir string) (domain.SearchResult, error) {
	return f(ctx, plan, scratchDir)
}

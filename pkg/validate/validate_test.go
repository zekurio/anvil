package validate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/marker"
)

func TestValidatorAcceptsEncodedOutput(t *testing.T) {
	outputPath := writeSizedFile(t, "out.mkv", 800)
	plan := encodePlan(false)

	result, err := Validator{Prober: fakeProber{result: outputProbe(plan)}}.Validate(context.Background(), Request{
		SourceProbe: sourceProbe(),
		OutputPath:  outputPath,
		Profile:     testProfile(),
		EncodePlan:  &plan,
	})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !result.OK {
		t.Fatalf("result.OK = false, errors = %v", result.Errors)
	}
	if got, want := result.ExpectedVideoCodec, "av1"; got != want {
		t.Fatalf("expected video codec = %q, want %q", got, want)
	}
	if got, want := result.SizeSavingsBytes, int64(200); got != want {
		t.Fatalf("size savings bytes = %d, want %d", got, want)
	}
	if got, want := result.SizeSavingsPercent, 20.0; got != want {
		t.Fatalf("size savings percent = %f, want %f", got, want)
	}
}

func TestValidatorRejectsDurationMismatch(t *testing.T) {
	outputPath := writeSizedFile(t, "out.mkv", 800)
	plan := encodePlan(false)
	probed := outputProbe(plan)
	probed.DurationSeconds = 90

	result, err := Validator{Prober: fakeProber{result: probed}}.Validate(context.Background(), Request{
		SourceProbe: sourceProbe(),
		OutputPath:  outputPath,
		Profile:     testProfile(),
		EncodePlan:  &plan,
	})
	if err == nil {
		t.Fatal("Validate() error = nil, want duration mismatch")
	}
	if result.OK {
		t.Fatal("result.OK = true, want false")
	}
	assertErrorContains(t, result, "duration")
}

func TestValidatorRejectsWrongVideoCodec(t *testing.T) {
	outputPath := writeSizedFile(t, "out.mkv", 800)
	plan := encodePlan(false)
	probed := outputProbe(plan)
	probed.Streams[0].Codec = "hevc"

	result, err := Validator{Prober: fakeProber{result: probed}}.Validate(context.Background(), Request{
		SourceProbe: sourceProbe(),
		OutputPath:  outputPath,
		Profile:     testProfile(),
		EncodePlan:  &plan,
	})
	if err == nil {
		t.Fatal("Validate() error = nil, want codec mismatch")
	}
	assertErrorContains(t, result, "video codec")
}

func TestValidatorRejectsMissingEncodedMarker(t *testing.T) {
	outputPath := writeSizedFile(t, "out.mkv", 800)
	plan := encodePlan(false)
	probed := outputProbe(plan)
	probed.Streams[0].Tags = nil

	result, err := Validator{Prober: fakeProber{result: probed}}.Validate(context.Background(), Request{
		SourceProbe: sourceProbe(),
		OutputPath:  outputPath,
		Profile:     testProfile(),
		EncodePlan:  &plan,
	})
	if err == nil {
		t.Fatal("Validate() error = nil, want missing marker")
	}
	assertErrorContains(t, result, "marker")
}

func TestValidatorAcceptsVideoCopyWithProcessedMarkerAndLargerOutput(t *testing.T) {
	outputPath := writeSizedFile(t, "out.mkv", 1200)
	plan := encodePlan(true)
	probed := domain.ProbeResult{
		DurationSeconds: 100,
		Streams: []domain.MediaStream{
			{Index: 0, Type: "video", Codec: "hevc", PixelFormat: "yuv420p", Tags: marker.OutputTags(plan)},
			{Index: 1, Type: "audio", Codec: "aac"},
			{Index: 2, Type: "audio", Codec: "aac"},
			{Index: 3, Type: "subtitle", Codec: "subrip"},
		},
	}

	result, err := Validator{Prober: fakeProber{result: probed}}.Validate(context.Background(), Request{
		SourceProbe: sourceProbe(),
		OutputPath:  outputPath,
		Profile:     testProfile(),
		EncodePlan:  &plan,
	})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !result.OK {
		t.Fatalf("result.OK = false, errors = %v", result.Errors)
	}
	if got, want := result.ExpectedVideoCodec, "hevc"; got != want {
		t.Fatalf("expected video codec = %q, want %q", got, want)
	}
	if result.ExpectedVideoPixelFormat != "" {
		t.Fatalf("expected pixel format = %q, want empty for video copy", result.ExpectedVideoPixelFormat)
	}
	if got, want := result.SizeSavingsBytes, int64(-200); got != want {
		t.Fatalf("size savings bytes = %d, want %d", got, want)
	}
	if !result.AnvilProcessedMarkerPresent || !result.AnvilMarkerCompatible {
		t.Fatalf("processed/compatible markers = %t/%t, want true/true", result.AnvilProcessedMarkerPresent, result.AnvilMarkerCompatible)
	}
}

func TestValidatorRejectsVideoCopyMissingProcessedMarker(t *testing.T) {
	outputPath := writeSizedFile(t, "out.mkv", 800)
	plan := encodePlan(true)
	probed := domain.ProbeResult{
		DurationSeconds: 100,
		Streams: []domain.MediaStream{
			{Index: 0, Type: "video", Codec: "hevc", PixelFormat: "yuv420p"},
			{Index: 1, Type: "audio", Codec: "aac"},
			{Index: 2, Type: "audio", Codec: "aac"},
			{Index: 3, Type: "subtitle", Codec: "subrip"},
		},
	}

	result, err := Validator{Prober: fakeProber{result: probed}}.Validate(context.Background(), Request{
		SourceProbe: sourceProbe(),
		OutputPath:  outputPath,
		Profile:     testProfile(),
		EncodePlan:  &plan,
	})
	if err == nil {
		t.Fatal("Validate() error = nil, want missing processed marker")
	}
	assertErrorContains(t, result, "processed marker")
}

func TestValidatorRejectsSelectedAudioCountMismatch(t *testing.T) {
	outputPath := writeSizedFile(t, "out.mkv", 800)
	plan := encodePlan(false)
	plan.AudioSelectionApplied = true
	plan.AudioStreamIndexes = []int{1}
	probed := outputProbe(plan)
	probed.Streams = append(probed.Streams, domain.MediaStream{Index: 4, Type: "audio", Codec: "aac"})

	result, err := Validator{Prober: fakeProber{result: probed}}.Validate(context.Background(), Request{
		SourceProbe: sourceProbe(),
		OutputPath:  outputPath,
		Profile:     testProfile(),
		EncodePlan:  &plan,
	})
	if err == nil {
		t.Fatal("Validate() error = nil, want audio count mismatch")
	}
	assertErrorContains(t, result, "audio stream count")
}

func TestValidatorRejectsSubtitleCountMismatch(t *testing.T) {
	outputPath := writeSizedFile(t, "out.mkv", 800)
	plan := encodePlan(false)
	probed := outputProbe(plan)
	probed.Streams = probed.Streams[:3]

	result, err := Validator{Prober: fakeProber{result: probed}}.Validate(context.Background(), Request{
		SourceProbe: sourceProbe(),
		OutputPath:  outputPath,
		Profile:     testProfile(),
		EncodePlan:  &plan,
	})
	if err == nil {
		t.Fatal("Validate() error = nil, want subtitle count mismatch")
	}
	assertErrorContains(t, result, "subtitle stream count")
}

func TestValidatorAcceptsPreservedHDRAndDolbyVisionMetadata(t *testing.T) {
	outputPath := writeSizedFile(t, "out.mkv", 800)
	plan := encodePlan(false)
	plan.VideoCodec = "hevc_qsv"
	plan.PixelFormat = "p010le"
	source := sourceProbe()
	source.Streams[0].ColorTransfer = "smpte2084"
	source.Streams[0].ColorPrimaries = "bt2020"
	source.Streams[0].ColorSpace = "bt2020nc"
	source.Streams[0].DolbyVision = &domain.DolbyVisionMetadata{Profile: 8, ConfigurationRecordFound: true}
	output := domain.ProbeResult{
		DurationSeconds: 100,
		Streams: []domain.MediaStream{
			{
				Index:          0,
				Type:           "video",
				Codec:          "hevc",
				PixelFormat:    "p010le",
				ColorTransfer:  "smpte2084",
				ColorPrimaries: "bt2020",
				ColorSpace:     "bt2020nc",
				DolbyVision:    &domain.DolbyVisionMetadata{Profile: 8, ConfigurationRecordFound: true},
				Tags:           marker.OutputTags(plan),
			},
			{Index: 1, Type: "audio", Codec: "aac"},
			{Index: 2, Type: "audio", Codec: "aac"},
			{Index: 3, Type: "subtitle", Codec: "subrip"},
		},
	}
	profile := testProfile()
	profile.Video.DolbyVision = domain.DolbyVisionProfile{Mode: domain.DolbyVisionModeAuto, Codec: "hevc_qsv"}

	result, err := Validator{Prober: fakeProber{result: output}}.Validate(context.Background(), Request{
		SourceProbe: source,
		OutputPath:  outputPath,
		Profile:     profile,
		EncodePlan:  &plan,
	})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !result.SourceDolbyVisionPresent || !result.OutputDolbyVisionPresent {
		t.Fatalf("Dolby Vision result flags = %t/%t, want true/true", result.SourceDolbyVisionPresent, result.OutputDolbyVisionPresent)
	}
}

func TestValidatorRejectsLostHDRMetadata(t *testing.T) {
	outputPath := writeSizedFile(t, "out.mkv", 800)
	plan := encodePlan(false)
	source := sourceProbe()
	source.Streams[0].ColorTransfer = "smpte2084"
	source.Streams[0].ColorPrimaries = "bt2020"
	output := outputProbe(plan)

	result, err := Validator{Prober: fakeProber{result: output}}.Validate(context.Background(), Request{
		SourceProbe: source,
		OutputPath:  outputPath,
		Profile:     testProfile(),
		EncodePlan:  &plan,
	})
	if err == nil {
		t.Fatal("Validate() error = nil, want HDR metadata mismatch")
	}
	assertErrorContains(t, result, "HDR color transfer")
}

func TestValidatorRejectsLostDolbyVisionMetadata(t *testing.T) {
	outputPath := writeSizedFile(t, "out.mkv", 800)
	plan := encodePlan(false)
	source := sourceProbe()
	source.Streams[0].DolbyVision = &domain.DolbyVisionMetadata{Profile: 8, ConfigurationRecordFound: true}
	output := outputProbe(plan)
	profile := testProfile()
	profile.Video.DolbyVision = domain.DolbyVisionProfile{Mode: domain.DolbyVisionModeAuto, Codec: "hevc_qsv"}

	result, err := Validator{Prober: fakeProber{result: output}}.Validate(context.Background(), Request{
		SourceProbe: source,
		OutputPath:  outputPath,
		Profile:     profile,
		EncodePlan:  &plan,
	})
	if err == nil {
		t.Fatal("Validate() error = nil, want missing Dolby Vision metadata")
	}
	assertErrorContains(t, result, "Dolby Vision")
}

func sourceProbe() *domain.ProbeResult {
	return &domain.ProbeResult{
		DurationSeconds: 100,
		SizeBytes:       1000,
		Streams: []domain.MediaStream{
			{Index: 0, Type: "video", Codec: "hevc", PixelFormat: "yuv420p"},
			{Index: 1, Type: "audio", Codec: "aac"},
			{Index: 2, Type: "audio", Codec: "aac"},
			{Index: 3, Type: "subtitle", Codec: "subrip"},
		},
	}
}

func outputProbe(plan domain.EncodePlan) domain.ProbeResult {
	return domain.ProbeResult{
		DurationSeconds: 100,
		Streams: []domain.MediaStream{
			{Index: 0, Type: "video", Codec: "av1", PixelFormat: "yuv420p10le", Tags: marker.OutputTags(plan)},
			{Index: 1, Type: "audio", Codec: "aac"},
			{Index: 2, Type: "audio", Codec: "aac"},
			{Index: 3, Type: "subtitle", Codec: "subrip"},
		},
	}
}

func encodePlan(videoCopy bool) domain.EncodePlan {
	return domain.EncodePlan{
		ProfileName: domain.ProfileName("default-av1"),
		VideoCodec:  "libsvtav1",
		VideoCopy:   videoCopy,
		PixelFormat: "yuv420p10le",
		CRF:         29,
	}
}

func testProfile() domain.Profile {
	return domain.Profile{
		Name: domain.ProfileName("default-av1"),
		Video: domain.VideoProfile{
			Codec:       "libsvtav1",
			PixelFormat: "yuv420p10le",
		},
		Subtitles: domain.SubtitleProfile{Mode: domain.StreamPolicyPreserve},
	}
}

func writeSizedFile(t *testing.T, name string, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatalf("write output: %v", err)
	}
	return path
}

func assertErrorContains(t *testing.T, result domain.ValidationResult, want string) {
	t.Helper()
	for _, err := range result.Errors {
		if strings.Contains(err, want) {
			return
		}
	}
	t.Fatalf("errors = %v, want containing %q", result.Errors, want)
}

type fakeProber struct {
	result domain.ProbeResult
	err    error
}

func (f fakeProber) Probe(_ context.Context, path string) (domain.ProbeResult, error) {
	if f.err != nil {
		return domain.ProbeResult{}, f.err
	}
	result := f.result
	if result.Path == "" {
		result.Path = path
	}
	return result, nil
}

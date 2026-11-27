package worker

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/ffmpeg"
	"github.com/zekurio/anvil/pkg/probe"
	"github.com/zekurio/anvil/pkg/process"
)

// Run with ANVIL_MEDIA_TEST=1 inside nix develop. Uses synthetic media only.
// A real encode, run with the arguments the plan produces, has to end at the
// full duration: ffmpeg's last progress block carries the final timestamp.
func TestProgressFromRealEncode(t *testing.T) {
	if os.Getenv("ANVIL_MEDIA_TEST") != "1" {
		t.Skip("set ANVIL_MEDIA_TEST=1 to run real FFmpeg encodes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	input := filepath.Join(root, "source.mkv")
	if _, err := (process.OSRunner{}).Run(ctx, process.Command{Name: "ffmpeg", Args: []string{
		"-hide_banner", "-nostdin", "-n", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=12",
		"-c:v", "ffv1", "-threads", "2", input,
	}}); err != nil {
		t.Fatal(err)
	}
	source, err := (probe.FFProbe{}).Probe(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ffmpeg.BuildPlanFromRequest(ffmpeg.BuildPlanRequest{
		InputPath: input, OutputPath: filepath.Join(root, "out.anvil-part"), Probe: &source,
		Resources: domain.ResourceAllocation{Threads: 2},
		Profile: domain.Profile{Container: "mkv", Video: domain.VideoProfile{
			Codec: "av1", Accelerator: "software", Preset: "10", BitDepth: 8, QualityMode: domain.VideoQualityFixed, CRF: 40,
			FFmpegArgs: []string{"-svtav1-params", "lp=2"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tracker := &ProgressTracker{}
	recorder := &processLogRecorder{root: filepath.Join(root, "logs"), jobID: 9, attempt: 1, tracker: tracker}
	duration := time.Duration(source.DurationSeconds * float64(time.Second))
	encodeCtx := process.WithLogger(process.WithMediaDuration(process.WithStep(ctx, "encode"), duration), recorder)
	if _, err := (ffmpeg.Encoder{}).Encode(encodeCtx, plan); err != nil {
		t.Fatal(err)
	}
	progress, ok := tracker.Snapshot(9)
	t.Logf("duration %s, final progress %+v", duration, progress)
	// A clip this short finishes inside ffmpeg's first stats period, where it
	// still reports fps=0.00, so only position and speed are asserted.
	if !ok || progress.Step != "encode" || progress.Percent < 99 || progress.Percent > 100 || progress.Speed <= 0 {
		t.Fatalf("final progress = %+v, %v", progress, ok)
	}
	if progress.ETASeconds < 0 || progress.ETASeconds > 1 {
		t.Fatalf("ETA at the end = %v", progress.ETASeconds)
	}
	raw, err := os.ReadFile(filepath.Join(root, "logs", "job-9", "attempt-1", "encode-ffmpeg.stdout.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ffmpeg wrote %d progress blocks; the last one:\n%s", strings.Count(string(raw), "progress="), lastProgressBlock(string(raw)))
}

func lastProgressBlock(output string) string {
	blocks := strings.SplitAfter(strings.TrimSpace(output), "progress=continue\n")
	return blocks[len(blocks)-1]
}

func TestParseMediaPosition(t *testing.T) {
	tests := []struct {
		name  string
		parse func(string) (time.Duration, bool)
		value string
		want  time.Duration
		ok    bool
	}{
		{"microseconds", parseMediaMicroseconds, "5005000", 5005 * time.Millisecond, true},
		{"microseconds with padding", parseMediaMicroseconds, " 1000000 ", time.Second, true},
		{"negative microseconds", parseMediaMicroseconds, "-21000", -21 * time.Millisecond, true},
		{"microseconds before the first packet", parseMediaMicroseconds, "N/A", 0, false},
		{"missing microseconds", parseMediaMicroseconds, "", 0, false},
		{"microseconds beyond a duration", parseMediaMicroseconds, "9223372036854775807", 0, false},
		{"clock", parseMediaClock, "00:01:02.500000", 62500 * time.Millisecond, true},
		{"clock from a stats line", parseMediaClock, "01:00:05.00", time.Hour + 5*time.Second, true},
		{"clock past 99 hours", parseMediaClock, "100:00:00.000000", 100 * time.Hour, true},
		{"negative clock", parseMediaClock, "-00:00:00.021000", -21 * time.Millisecond, true},
		{"clock before the first packet", parseMediaClock, "N/A", 0, false},
		{"missing clock", parseMediaClock, "", 0, false},
		{"clock with too few fields", parseMediaClock, "12:30", 0, false},
		{"clock with a bad field", parseMediaClock, "00:xx:01.0", 0, false},
		{"clock with nonsense seconds", parseMediaClock, "00:00:NaN", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.parse(tt.value)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("parse(%q) = %v, %v; want %v, %v", tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestProgressEstimate(t *testing.T) {
	tests := []struct {
		name        string
		position    time.Duration
		duration    time.Duration
		speed       float64
		wantPercent float64
		wantETA     float64
	}{
		{"halfway at double speed", 30 * time.Minute, time.Hour, 2, 50, 900},
		{"slower than realtime", 15 * time.Minute, time.Hour, 0.5, 25, 5400},
		{"unknown speed keeps the percentage", 30 * time.Minute, time.Hour, 0, 50, -1},
		{"negative start offset clamps to zero", -21 * time.Millisecond, time.Hour, 1, 0, 3600},
		{"overshooting the probed duration clamps to done", 61 * time.Minute, time.Hour, 3, 100, 0},
		{"unknown duration", 30 * time.Minute, 0, 2, -1, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			percent, eta := progressEstimate(tt.position, tt.duration, tt.speed)
			if math.Abs(percent-tt.wantPercent) > 1e-9 || math.Abs(eta-tt.wantETA) > 1e-9 {
				t.Fatalf("estimate = %v%%, %vs; want %v%%, %vs", percent, eta, tt.wantPercent, tt.wantETA)
			}
		})
	}
}

// A subtitle track whose last line is early in the file holds ffmpeg's output
// timestamp there while the video goes on; the progress must follow the frames.
func TestProgressFollowsFramesPastAStalledTimestamp(t *testing.T) {
	tracker := &ProgressTracker{}
	recorder := &processLogRecorder{jobID: 3, attempt: 1, tracker: tracker}
	ctx := process.WithMediaFrames(process.WithMediaDuration(process.WithStep(context.Background(), "encode"), 150*time.Second), 3600)
	report := func(frame, fps, outTimeUS, speed string) domain.JobProgress {
		t.Helper()
		recorder.logFFmpegProgress(ctx, map[string]string{"frame": frame, "fps": fps, "out_time_us": outTimeUS, "speed": speed})
		progress, ok := tracker.Snapshot(3)
		if !ok {
			t.Fatal("no progress recorded")
		}
		return progress
	}
	near := func(got, want float64) bool { return math.Abs(got-want) < 0.01 }

	// The timestamp is ahead of the frame count: it stays the authority.
	if got := report("100", "12", "5000000", "0.5x"); !near(got.Percent, 100*5.0/150) || !near(got.Speed, 0.5) || !near(got.ETASeconds, 145/0.5) {
		t.Fatalf("timestamp ahead: %+v", got)
	}
	// 1800 of 3600 frames at 12 fps against a 24 fps source: half done at half speed.
	if got := report("1800", "12", "8023000", "0.0802x"); !near(got.Percent, 50) || !near(got.Speed, 0.5) || !near(got.ETASeconds, 150) {
		t.Fatalf("stalled timestamp: %+v", got)
	}
	// Before the first packet there is no timestamp at all.
	if got := report("900", "12", "N/A", "N/A"); !near(got.Percent, 25) || !near(got.Speed, 0.5) {
		t.Fatalf("no timestamp: %+v", got)
	}
	// More frames than estimated is the end, not past it.
	if got := report("3700", "12", "8023000", "0.04x"); !near(got.Percent, 100) || !near(got.ETASeconds, 0) {
		t.Fatalf("past the estimate: %+v", got)
	}
}

func TestFramePosition(t *testing.T) {
	for name, tt := range map[string]struct {
		frames, total int64
		duration      time.Duration
		want          time.Duration
		ok            bool
	}{
		"halfway":              {1800, 3600, 150 * time.Second, 75 * time.Second, true},
		"past the estimate":    {4000, 3600, 150 * time.Second, 150 * time.Second, true},
		"unknown frame count":  {1800, 0, 150 * time.Second, 0, false},
		"unknown duration":     {1800, 3600, 0, 0, false},
		"nonsense frame count": {-1, 3600, 150 * time.Second, 0, false},
	} {
		if got, ok := framePosition(tt.frames, tt.total, tt.duration); got != tt.want || ok != tt.ok {
			t.Errorf("%s: framePosition = %v, %v; want %v, %v", name, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParseProgressNumber(t *testing.T) {
	for value, want := range map[string]float64{"23.5": 23.5, " 1.02": 1.02, "N/A": 0, "": 0, "inf": 0, "NaN": 0, "-3": 0} {
		if got := parseProgressNumber(value); got != want {
			t.Fatalf("parseProgressNumber(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestRecorderTracksLiveProgress(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	command := process.Command{Name: "ffmpeg"}
	block := func(frame, fps, us, clock, speed, state string) string {
		return "frame=" + frame + "\nfps=" + fps + "\nstream_0_0_q=28.0\nbitrate= 512.0kbits/s\ntotal_size=1024\nout_time_us=" + us +
			"\nout_time_ms=" + us + "\nout_time=" + clock + "\ndup_frames=0\ndrop_frames=0\nspeed=" + speed + "\nprogress=" + state + "\n"
	}
	tests := []struct {
		name     string
		step     string
		duration time.Duration
		output   string
		want     domain.JobProgress
	}{
		{
			name: "encode reports percent and ETA", step: "encode", duration: 100 * time.Second,
			output: block("600", "48.0", "25000000", "00:00:25.000000", "   2x", "continue"),
			want:   domain.JobProgress{Step: "encode", Percent: 25, FPS: 48, Speed: 2, ETASeconds: 37.5},
		},
		{
			name: "the latest report wins", step: "encode", duration: 100 * time.Second,
			output: block("600", "48.0", "25000000", "00:00:25.000000", "2x", "continue") + block("2400", "50.0", "100000000", "00:01:40.000000", "2.5x", "end"),
			want:   domain.JobProgress{Step: "encode", Percent: 100, FPS: 50, Speed: 2.5, ETASeconds: 0},
		},
		{
			name: "nothing written yet", step: "encode", duration: 100 * time.Second,
			output: block("0", "0.00", "N/A", "N/A", "N/A", "continue"),
			want:   domain.JobProgress{Step: "encode", Percent: -1, ETASeconds: -1},
		},
		{
			name: "the clock stands in for a missing microsecond count", step: "encode", duration: 100 * time.Second,
			output: "frame=10\nfps=5\nout_time=00:00:50.000000\nspeed=N/A\nprogress=continue\n",
			want:   domain.JobProgress{Step: "encode", Percent: 50, FPS: 5, ETASeconds: -1},
		},
		{
			name: "a step without a declared duration has no percentage", step: "crf-search",
			output: block("120", "30.0", "5000000", "00:00:05.000000", "1.25x", "continue"),
			want:   domain.JobProgress{Step: "crf-search", Percent: -1, FPS: 30, Speed: 1.25, ETASeconds: -1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := &ProgressTracker{}
			recorder := &processLogRecorder{root: t.TempDir(), jobID: 7, attempt: 1, tracker: tracker, now: func() time.Time { return now }}
			ctx := process.WithMediaDuration(process.WithStep(context.Background(), tt.step), tt.duration)
			logger, err := recorder.StartProcess(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			started, ok := tracker.Snapshot(7)
			wantStart := float64(-1)
			if tt.duration > 0 {
				wantStart = 0
			}
			if !ok || started.Step != tt.step || started.Percent != wantStart || started.ETASeconds != -1 {
				t.Fatalf("progress at process start = %+v, %v", started, ok)
			}
			session := logger.(*processLogSession)
			// Split mid-line: progress arrives in arbitrary chunks.
			half := len(tt.output) / 2
			for _, chunk := range []string{tt.output[:half], tt.output[half:]} {
				if err := session.LogProcessOutput(ctx, command, "stdout", []byte(chunk)); err != nil {
					t.Fatal(err)
				}
			}
			got, ok := tracker.Snapshot(7)
			tt.want.JobID, tt.want.UpdatedAt = 7, now
			if !ok || got.Step != tt.want.Step || got.FPS != tt.want.FPS || got.Speed != tt.want.Speed || !got.UpdatedAt.Equal(now) ||
				math.Abs(got.Percent-tt.want.Percent) > 1e-9 || math.Abs(got.ETASeconds-tt.want.ETASeconds) > 1e-9 {
				t.Fatalf("progress = %+v\nwant       %+v", got, tt.want)
			}
			if err := session.LogProcess(ctx, command, process.Result{}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecorderMovesProgressToTheNextStep(t *testing.T) {
	tracker := &ProgressTracker{}
	recorder := &processLogRecorder{jobID: 7, tracker: tracker}
	tracker.Update(domain.JobProgress{JobID: 7, Step: "encode", Percent: 100, FPS: 40, Speed: 2})
	// ffprobe gets no log files, but it still marks the step that runs it.
	if logger, err := recorder.StartProcess(process.WithStep(context.Background(), "validate"), process.Command{Name: "ffprobe"}); err != nil || logger != nil {
		t.Fatalf("logger = %v, %v", logger, err)
	}
	got, _ := tracker.Snapshot(7)
	if got.Step != "validate" || got.Percent != -1 || got.FPS != 0 || got.ETASeconds != -1 {
		t.Fatalf("progress = %+v", got)
	}
	tracker.Update(domain.JobProgress{JobID: 7, Step: "validate", Percent: -1, FPS: 12})
	if _, err := recorder.StartProcess(process.WithStep(context.Background(), "validate"), process.Command{Name: "ffprobe"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := tracker.Snapshot(7); got.FPS != 12 {
		t.Fatalf("a second process in the same step reset its progress: %+v", got)
	}
}

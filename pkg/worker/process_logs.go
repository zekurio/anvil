package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/process"
)

type processLogStore interface {
	RecordAttemptEvent(context.Context, domain.AttemptEvent) (domain.AttemptEvent, error)
}

type processLogRecorder struct {
	root      string
	jobID     domain.JobID
	jobSlug   string
	attemptID domain.AttemptID
	attempt   int
	events    processLogStore
	tracker   *ProgressTracker
	now       func() time.Time
	mu        sync.Mutex
	names     map[string]int
}

type processProgress struct {
	stdout string
	stderr string
	values map[string]string
}

var ffmpegStatsPattern = regexp.MustCompile(`frame=\s*([0-9]+).*fps=\s*([^ ]+).*time=\s*([^ ]+).*speed=\s*([^ ]+)`)

type processLogArtifact struct {
	Step           string   `json:"step,omitempty"`
	Command        []string `json:"command"`
	ExitCode       int      `json:"exit_code"`
	DurationMillis int64    `json:"duration_ms"`
	StdoutPath     string   `json:"stdout_path"`
	StderrPath     string   `json:"stderr_path"`
	StdoutBytes    int64    `json:"stdout_bytes"`
	StderrBytes    int64    `json:"stderr_bytes"`
	Error          string   `json:"error,omitempty"`
}

type processLogSession struct {
	recorder *processLogRecorder
	stdout   *os.File
	stderr   *os.File
	progress processProgress
	mu       sync.Mutex
}

func (r *processLogRecorder) StartProcess(ctx context.Context, command process.Command) (process.Logger, error) {
	r.enterStep(ctx)
	switch filepath.Base(command.Name) {
	case "ffmpeg":
	default:
		return nil, nil
	}
	root := strings.TrimSpace(r.root)
	if root == "" {
		return nil, nil
	}
	step := process.Step(ctx)
	if step == "" {
		step = "process"
	}
	dir := filepath.Join(root, r.jobLabel(), fmt.Sprintf("attempt-%d", r.attempt))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create process log dir: %w", err)
	}
	baseName := r.uniqueLogBaseName(sanitizeLogName(step) + "-" + sanitizeLogName(filepath.Base(command.Name)))
	stdout, err := os.OpenFile(filepath.Join(dir, baseName+".stdout.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open process stdout log: %w", err)
	}
	stderr, err := os.OpenFile(filepath.Join(dir, baseName+".stderr.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open process stderr log: %w", err), stdout.Close())
	}
	return &processLogSession{recorder: r, stdout: stdout, stderr: stderr, progress: processProgress{values: make(map[string]string)}}, nil
}

// enterStep moves the job's live progress to the step that is starting a
// process, so the last figures of a finished step are not shown for the next
// one until its first progress report arrives.
func (r *processLogRecorder) enterStep(ctx context.Context) {
	step := process.Step(ctx)
	if step == "" {
		return
	}
	if current, ok := r.tracker.Snapshot(r.jobID); ok && current.Step == step {
		return
	}
	percent := float64(-1)
	if process.MediaDuration(ctx) > 0 {
		percent = 0
	}
	r.tracker.Update(domain.JobProgress{JobID: r.jobID, Step: step, Percent: percent, ETASeconds: -1, UpdatedAt: r.timestamp()})
}

// LogProcess keeps the context logger contract. OSRunner uses a per-run session.
func (r *processLogRecorder) LogProcess(context.Context, process.Command, process.Result, error) error {
	return nil
}

func (s *processLogSession) LogProcess(ctx context.Context, command process.Command, result process.Result, runErr error) error {
	closeErr := errors.Join(s.stdout.Close(), s.stderr.Close())
	r := s.recorder
	step := process.Step(ctx)
	if step == "" {
		step = "process"
	}
	commandName := command.Name
	if runErr != nil {
		slog.Error("external process failed", "job", r.jobLabel(), "attempt", r.attempt, "step", step,
			"command", filepath.Base(commandName), "exit_code", result.ExitCode, "duration", result.Duration,
			"diagnostic", lastProcessDiagnostic(result.Stderr))
	}
	artifactErr := s.recordArtifact(ctx, command, result, errors.Join(runErr, closeErr), step, commandName)
	return errors.Join(closeErr, artifactErr)
}

func (s *processLogSession) recordArtifact(ctx context.Context, command process.Command, result process.Result, runErr error, step, commandName string) error {
	r := s.recorder
	artifact := processLogArtifact{
		Step:           step,
		Command:        result.Command,
		ExitCode:       result.ExitCode,
		DurationMillis: result.Duration.Milliseconds(),
		StdoutPath:     s.stdout.Name(),
		StderrPath:     s.stderr.Name(),
		StdoutBytes:    result.StdoutBytes,
		StderrBytes:    result.StderrBytes,
	}
	if runErr != nil {
		artifact.Error = runErr.Error()
	}
	if len(artifact.Command) == 0 {
		artifact.Command = command.ArgsWithName()
	}
	if r.events == nil || r.attemptID == 0 {
		return nil
	}
	payload, err := json.Marshal(artifact)
	if err != nil {
		return fmt.Errorf("encode process log artifact: %w", err)
	}
	_, err = r.events.RecordAttemptEvent(context.WithoutCancel(ctx), domain.AttemptEvent{
		AttemptID: r.attemptID,
		Type:      domain.AttemptEventArtifact,
		Name:      "process-output",
		Message:   fmt.Sprintf("captured process output for %s", filepath.Base(commandName)),
		Payload:   payload,
		CreatedAt: r.timestamp(),
	})
	if err != nil {
		return fmt.Errorf("record process log artifact: %w", err)
	}
	return nil
}

func lastProcessDiagnostic(output []byte) string {
	lines := strings.FieldsFunc(string(output), func(r rune) bool { return r == '\r' || r == '\n' })
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line != "" && !ffmpegStatsPattern.MatchString(line) {
			if len(line) > maxProgressLine {
				line = line[len(line)-maxProgressLine:]
			}
			return line
		}
	}
	return ""
}

const maxProgressLine = 4096

func (s *processLogSession) LogProcessOutput(ctx context.Context, command process.Command, stream string, output []byte) error {
	file := s.stderr
	if stream == "stdout" {
		file = s.stdout
	}
	if _, err := file.Write(output); err != nil {
		return fmt.Errorf("write process %s log: %w", stream, err)
	}
	if filepath.Base(command.Name) != "ffmpeg" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	buffered := &s.progress.stderr
	if stream == "stdout" {
		buffered = &s.progress.stdout
	}
	// Bound partial lines even when a tool emits no delimiters.
	for len(output) > 0 {
		n := min(len(output), maxProgressLine)
		*buffered = s.recorder.consumeProgress(ctx, &s.progress, *buffered+string(output[:n]))
		if len(*buffered) > maxProgressLine {
			*buffered = (*buffered)[len(*buffered)-maxProgressLine:]
		}
		output = output[n:]
	}
	return nil
}

func (r *processLogRecorder) consumeProgress(ctx context.Context, progress *processProgress, buffered string) string {
	for {
		index := strings.IndexAny(buffered, "\r\n")
		if index < 0 {
			return buffered
		}
		line := strings.TrimSpace(buffered[:index])
		buffered = strings.TrimLeft(buffered[index+1:], "\r\n")
		if line == "" {
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok && !strings.Contains(key, " ") {
			switch key {
			case "frame", "fps", "out_time", "out_time_us", "out_time_ms", "speed":
				progress.values[key] = value
			}
			if key == "progress" {
				r.logFFmpegProgress(ctx, progress.values)
			}
			continue
		}
		matches := ffmpegStatsPattern.FindStringSubmatch(line)
		if len(matches) == 5 {
			position, known := parseMediaClock(matches[3])
			r.logProgress(ctx, matches[1], matches[2], matches[3], matches[4], position, known)
		}
	}
}

func (r *processLogRecorder) logFFmpegProgress(ctx context.Context, values map[string]string) {
	outTime := values["out_time"]
	if outTime == "" {
		outTime = values["out_time_us"]
	}
	// out_time_us is exact; out_time_ms is the same microsecond count under
	// an older, misleading name; the clock is the fallback.
	position, known := parseMediaMicroseconds(values["out_time_us"])
	if !known {
		position, known = parseMediaMicroseconds(values["out_time_ms"])
	}
	if !known {
		position, known = parseMediaClock(values["out_time"])
	}
	r.logProgress(ctx, values["frame"], values["fps"], outTime, values["speed"], position, known)
}

// logProgress logs one ffmpeg progress report and stores it as the job's
// latest progress. position is how much media has been written; it is unknown
// until ffmpeg has written a first packet.
func (r *processLogRecorder) logProgress(ctx context.Context, frame, fps, outTime, speed string, position time.Duration, positionKnown bool) {
	slog.Info("ffmpeg progress",
		"job", r.jobLabel(),
		"attempt", r.attempt,
		"step", process.Step(ctx),
		"frame", frame,
		"fps", fps,
		"media_time", outTime,
		"speed", speed,
	)
	progress := domain.JobProgress{
		JobID:      r.jobID,
		Step:       process.Step(ctx),
		Percent:    -1,
		FPS:        parseProgressNumber(fps),
		Speed:      parseProgressNumber(strings.TrimSuffix(strings.TrimSpace(speed), "x")),
		ETASeconds: -1,
		UpdatedAt:  r.timestamp(),
	}
	// Only a step that declared the media it works through gets a percentage:
	// a search sample's own position says nothing about the whole search.
	if positionKnown {
		progress.Percent, progress.ETASeconds = progressEstimate(position, process.MediaDuration(ctx), progress.Speed)
	}
	r.tracker.Update(progress)
}

// progressEstimate turns the media written so far into a percentage of
// duration and the seconds left at speed times realtime. Either value is -1
// when it cannot be known.
func progressEstimate(position, duration time.Duration, speed float64) (float64, float64) {
	if duration <= 0 {
		return -1, -1
	}
	// ffmpeg reports a negative position while a start offset is pending and
	// can run past a probed duration that was a little short.
	position = min(max(position, 0), duration)
	percent := 100 * float64(position) / float64(duration)
	if speed <= 0 {
		return percent, -1
	}
	return percent, (duration - position).Seconds() / speed
}

// parseMediaMicroseconds reads ffmpeg's out_time_us; "N/A" is not known.
func parseMediaMicroseconds(value string) (time.Duration, bool) {
	microseconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || microseconds > math.MaxInt64/1000 || microseconds < math.MinInt64/1000 {
		return 0, false
	}
	return time.Duration(microseconds) * time.Microsecond, true
}

// parseMediaClock reads ffmpeg's [-]HH:MM:SS.ffffff timestamps, whose hours
// field grows past two digits; "N/A" is not known.
func parseMediaClock(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	negative := strings.HasPrefix(value, "-")
	parts := strings.Split(strings.TrimPrefix(value, "-"), ":")
	if len(parts) != 3 {
		return 0, false
	}
	// The bit sizes keep the sum far inside a time.Duration.
	hours, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil {
		return 0, false
	}
	minutes, err := strconv.ParseUint(parts[1], 10, 8)
	if err != nil {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(parts[2], 64)
	if err != nil || math.IsNaN(seconds) || seconds < 0 || seconds >= 61 {
		return 0, false
	}
	position := time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds*float64(time.Second))
	if negative {
		position = -position
	}
	return position, true
}

// parseProgressNumber reads an ffmpeg progress figure; "N/A" and other
// values that are not a finite, non-negative number report 0.
func parseProgressNumber(value string) float64 {
	number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return 0
	}
	return number
}

func (r *processLogRecorder) jobLabel() string {
	if r.jobSlug != "" {
		return r.jobSlug
	}
	return fmt.Sprintf("job-%d", r.jobID)
}

func (r *processLogRecorder) uniqueLogBaseName(baseName string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names == nil {
		r.names = make(map[string]int)
	}
	r.names[baseName]++
	if r.names[baseName] == 1 {
		return baseName
	}
	return fmt.Sprintf("%s-%d", baseName, r.names[baseName])
}

func (r *processLogRecorder) timestamp() time.Time {
	if r.now != nil {
		return r.now().UTC()
	}
	return time.Now().UTC()
}

func sanitizeLogName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "process"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	cleaned := strings.Trim(b.String(), "-")
	if cleaned == "" {
		return "process"
	}
	return cleaned
}

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
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
	now       func() time.Time
	mu        sync.Mutex
	names     map[string]int
	progress  map[string]*processProgress
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
	StdoutBytes    int      `json:"stdout_bytes"`
	StderrBytes    int      `json:"stderr_bytes"`
	Error          string   `json:"error,omitempty"`
}

func (r *processLogRecorder) LogProcess(ctx context.Context, command process.Command, result process.Result, runErr error) error {
	if !shouldCaptureProcess(command, result, runErr) {
		return nil
	}
	root := strings.TrimSpace(r.root)
	if root == "" {
		return nil
	}
	step := process.Step(ctx)
	if strings.TrimSpace(step) == "" {
		step = "process"
	}
	commandName := command.Name
	if commandName == "" && len(result.Command) > 0 {
		commandName = result.Command[0]
	}
	if runErr != nil {
		slog.Error("external process failed",
			"job", r.jobLabel(),
			"attempt", r.attempt,
			"step", step,
			"command", filepath.Base(commandName),
			"exit_code", result.ExitCode,
			"duration", result.Duration,
			"diagnostic", lastProcessDiagnostic(result.Stderr),
		)
	}
	dir := filepath.Join(root, r.jobLabel(), fmt.Sprintf("attempt-%d", r.attempt))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create process log dir: %w", err)
	}
	baseName := r.uniqueLogBaseName(sanitizeLogName(step) + "-" + sanitizeLogName(filepath.Base(commandName)))
	stdoutPath := filepath.Join(dir, baseName+".stdout.log")
	stderrPath := filepath.Join(dir, baseName+".stderr.log")
	if err := os.WriteFile(stdoutPath, result.Stdout, 0o640); err != nil {
		return fmt.Errorf("write process stdout log: %w", err)
	}
	if err := os.WriteFile(stderrPath, result.Stderr, 0o640); err != nil {
		return fmt.Errorf("write process stderr log: %w", err)
	}
	artifact := processLogArtifact{
		Step:           step,
		Command:        result.Command,
		ExitCode:       result.ExitCode,
		DurationMillis: result.Duration.Milliseconds(),
		StdoutPath:     stdoutPath,
		StderrPath:     stderrPath,
		StdoutBytes:    len(result.Stdout),
		StderrBytes:    len(result.Stderr),
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
			return line
		}
	}
	return ""
}

func (r *processLogRecorder) LogProcessOutput(ctx context.Context, command process.Command, stream string, output []byte) {
	key := process.Step(ctx) + "\x00" + strings.Join(command.ArgsWithName(), "\x00")
	r.mu.Lock()
	if r.progress == nil {
		r.progress = make(map[string]*processProgress)
	}
	progress := r.progress[key]
	if progress == nil {
		progress = &processProgress{values: make(map[string]string)}
		r.progress[key] = progress
	}
	if stream == "stdout" {
		progress.stdout += string(output)
		if filepath.Base(command.Name) == "ffmpeg" {
			progress.stdout = r.consumeProgress(ctx, progress, progress.stdout)
		} else {
			progress.stdout = r.consumeToolOutput(ctx, command, stream, progress.stdout)
		}
	} else {
		progress.stderr += string(output)
		if filepath.Base(command.Name) == "ffmpeg" {
			progress.stderr = r.consumeProgress(ctx, progress, progress.stderr)
		} else {
			progress.stderr = r.consumeToolOutput(ctx, command, stream, progress.stderr)
		}
	}
	r.mu.Unlock()
}

func (r *processLogRecorder) consumeToolOutput(ctx context.Context, command process.Command, stream, buffered string) string {
	if !shouldLogLiveOutput(command.Name) {
		return ""
	}
	for {
		index := strings.IndexAny(buffered, "\r\n")
		if index < 0 {
			return buffered
		}
		line := strings.TrimSpace(buffered[:index])
		buffered = strings.TrimLeft(buffered[index+1:], "\r\n")
		if line != "" {
			slog.Info("external process output",
				"job", r.jobLabel(),
				"attempt", r.attempt,
				"step", process.Step(ctx),
				"command", filepath.Base(command.Name),
				"stream", stream,
				"message", line,
			)
		}
	}
}

func shouldLogLiveOutput(command string) bool {
	switch filepath.Base(command) {
	case "ab-av1", "dovi_tool", "mkvextract", "mkvmerge", "mkvpropedit":
		return true
	default:
		return false
	}
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
			progress.values[key] = value
			if key == "progress" {
				r.logFFmpegProgress(ctx, progress.values)
			}
			continue
		}
		matches := ffmpegStatsPattern.FindStringSubmatch(line)
		if len(matches) == 5 {
			r.logProgress(ctx, matches[1], matches[2], matches[3], matches[4])
		}
	}
}

func (r *processLogRecorder) logFFmpegProgress(ctx context.Context, values map[string]string) {
	outTime := values["out_time"]
	if outTime == "" {
		outTime = values["out_time_us"]
	}
	r.logProgress(ctx, values["frame"], values["fps"], outTime, values["speed"])
}

func (r *processLogRecorder) logProgress(ctx context.Context, frame, fps, outTime, speed string) {
	slog.Info("ffmpeg progress",
		"job", r.jobLabel(),
		"attempt", r.attempt,
		"step", process.Step(ctx),
		"frame", frame,
		"fps", fps,
		"media_time", outTime,
		"speed", speed,
	)
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

func shouldCaptureProcess(command process.Command, result process.Result, runErr error) bool {
	name := filepath.Base(command.Name)
	if name == "" && len(result.Command) > 0 {
		name = filepath.Base(result.Command[0])
	}
	switch name {
	case "ffmpeg", "ab-av1", "dovi_tool", "mkvextract", "mkvmerge", "mkvinfo", "mkvpropedit":
	default:
		return false
	}
	return runErr != nil || len(result.Stdout) > 0 || len(result.Stderr) > 0
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

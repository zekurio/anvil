package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	attemptID domain.AttemptID
	events    processLogStore
	now       func() time.Time
}

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

func (r processLogRecorder) LogProcess(ctx context.Context, command process.Command, result process.Result, runErr error) error {
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
	dir := filepath.Join(root, fmt.Sprintf("job-%d-attempt-%d", r.jobID, r.attemptID))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create process log dir: %w", err)
	}
	baseName := sanitizeLogName(step) + "-" + sanitizeLogName(filepath.Base(commandName))
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

func shouldCaptureProcess(command process.Command, result process.Result, runErr error) bool {
	name := filepath.Base(command.Name)
	if name == "" && len(result.Command) > 0 {
		name = filepath.Base(result.Command[0])
	}
	if name != "ffmpeg" && name != "ab-av1" {
		return false
	}
	return runErr != nil || len(result.Stdout) > 0 || len(result.Stderr) > 0
}

func (r processLogRecorder) timestamp() time.Time {
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

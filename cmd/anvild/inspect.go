package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/store"
)

const processOutputArtifactName = "process-output"

type inspectReport struct {
	Job      inspectJob       `json:"job"`
	Attempts []inspectAttempt `json:"attempts"`
}

type inspectJob struct {
	ID           int64      `json:"id"`
	State        string     `json:"state"`
	Library      string     `json:"library"`
	AttemptCount int        `json:"attempt_count"`
	UpdatedAt    time.Time  `json:"updated_at"`
	CreatedAt    time.Time  `json:"created_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	SourceKind   string     `json:"source_kind"`
	SourcePath   string     `json:"source_path"`
	AssetPath    string     `json:"asset_path"`
	AssetRole    string     `json:"asset_role"`
	Path         string     `json:"path"`
	LastError    string     `json:"last_error"`
}

type inspectAttempt struct {
	ID         int64          `json:"id"`
	Number     int            `json:"number"`
	State      string         `json:"state"`
	WorkerID   string         `json:"worker_id"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	Error      string         `json:"error"`
	Events     []inspectEvent `json:"events"`
}

type inspectEvent struct {
	ID            int64                 `json:"id"`
	AttemptID     int64                 `json:"attempt_id"`
	CreatedAt     time.Time             `json:"created_at"`
	Type          string                `json:"type"`
	Name          string                `json:"name"`
	Message       string                `json:"message"`
	Payload       *inspectPayload       `json:"payload,omitempty"`
	ProcessOutput *inspectProcessOutput `json:"process_output,omitempty"`
	PayloadError  string                `json:"payload_error,omitempty"`
}

type inspectPayload struct {
	Kind        string          `json:"kind"`
	SizeBytes   int             `json:"size_bytes"`
	JSON        json.RawMessage `json:"json,omitempty"`
	Text        string          `json:"text,omitempty"`
	BytesBase64 string          `json:"bytes_base64,omitempty"`
}

type inspectProcessOutput struct {
	Step           string   `json:"step"`
	Command        []string `json:"command"`
	ExitCode       int      `json:"exit_code"`
	DurationMillis int64    `json:"duration_ms"`
	StdoutPath     string   `json:"stdout_path"`
	StderrPath     string   `json:"stderr_path"`
	StdoutBytes    int      `json:"stdout_bytes"`
	StderrBytes    int      `json:"stderr_bytes"`
	Error          string   `json:"error"`
}

func runInspectCommand(ctx context.Context, cfg config.Config, opts options) error {
	if len(opts.jobIDs) != 1 {
		return fmt.Errorf("inspect requires exactly one job ID")
	}
	state, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)

	report, err := buildInspectReport(ctx, state, opts.jobIDs[0])
	if err != nil {
		return err
	}
	if opts.jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	return writeInspectReport(os.Stdout, report)
}

func buildInspectReport(ctx context.Context, state *store.SQLiteStore, jobID domain.JobID) (inspectReport, error) {
	summary, err := state.GetJobSummary(ctx, jobID)
	if err != nil {
		return inspectReport{}, err
	}
	attempts, err := state.ListAttemptsForJob(ctx, jobID)
	if err != nil {
		return inspectReport{}, err
	}

	report := inspectReport{
		Job:      inspectJobFromSummary(summary),
		Attempts: make([]inspectAttempt, 0, len(attempts)),
	}
	for _, attempt := range attempts {
		events, err := state.ListAttemptEvents(ctx, attempt.ID)
		if err != nil {
			return inspectReport{}, err
		}
		report.Attempts = append(report.Attempts, inspectAttemptFromDomain(attempt, events))
	}
	return report, nil
}

func inspectJobFromSummary(summary store.JobSummary) inspectJob {
	return inspectJob{
		ID:           int64(summary.Job.ID),
		State:        string(summary.Job.State),
		Library:      string(summary.Job.LibraryName),
		AttemptCount: summary.Job.AttemptCount,
		UpdatedAt:    summary.Job.UpdatedAt,
		CreatedAt:    summary.Job.CreatedAt,
		CompletedAt:  summary.Job.CompletedAt,
		SourceKind:   string(summary.SourceKind),
		SourcePath:   summary.SourcePath,
		AssetPath:    summary.AssetPath,
		AssetRole:    string(summary.AssetRole),
		Path:         jobPath(summary),
		LastError:    summary.Job.LastError,
	}
}

func inspectAttemptFromDomain(attempt domain.Attempt, events []domain.AttemptEvent) inspectAttempt {
	result := inspectAttempt{
		ID:         int64(attempt.ID),
		Number:     attempt.Number,
		State:      string(attempt.State),
		WorkerID:   attempt.WorkerID,
		StartedAt:  attempt.StartedAt,
		FinishedAt: attempt.FinishedAt,
		Error:      attempt.Error,
		Events:     make([]inspectEvent, 0, len(events)),
	}
	for _, event := range events {
		result.Events = append(result.Events, inspectEventFromDomain(event))
	}
	return result
}

func inspectEventFromDomain(event domain.AttemptEvent) inspectEvent {
	result := inspectEvent{
		ID:        int64(event.ID),
		AttemptID: int64(event.AttemptID),
		CreatedAt: event.CreatedAt,
		Type:      string(event.Type),
		Name:      event.Name,
		Message:   event.Message,
	}
	if isProcessOutputEvent(event) {
		output, err := decodeProcessOutput(event.Payload)
		if err != nil {
			result.Payload = decodeInspectPayload(event.Payload)
			result.PayloadError = err.Error()
			return result
		}
		result.ProcessOutput = output
		return result
	}
	result.Payload = decodeInspectPayload(event.Payload)
	return result
}

func isProcessOutputEvent(event domain.AttemptEvent) bool {
	return event.Type == domain.AttemptEventArtifact && event.Name == processOutputArtifactName
}

func decodeProcessOutput(payload []byte) (*inspectProcessOutput, error) {
	var output inspectProcessOutput
	if err := json.Unmarshal(payload, &output); err != nil {
		return nil, err
	}
	if output.Command == nil {
		output.Command = []string{}
	}
	return &output, nil
}

func decodeInspectPayload(payload []byte) *inspectPayload {
	if len(payload) == 0 {
		return nil
	}
	result := &inspectPayload{SizeBytes: len(payload)}

	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err == nil {
		result.Kind = "json"
		result.JSON = append(json.RawMessage(nil), compact.Bytes()...)
		return result
	}

	if utf8.Valid(payload) {
		result.Kind = "text"
		result.Text = string(payload)
		return result
	}

	result.Kind = "bytes"
	result.BytesBase64 = base64.StdEncoding.EncodeToString(payload)
	return result
}

type inspectWriter struct {
	w   io.Writer
	err error
}

func (w *inspectWriter) printf(format string, args ...any) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintf(w.w, format, args...)
}

func writeInspectReport(out io.Writer, report inspectReport) error {
	w := &inspectWriter{w: out}
	job := report.Job
	w.printf("Job %d\n", job.ID)
	w.printf("  State: %s\n", job.State)
	w.printf("  Library: %s\n", job.Library)
	w.printf("  Attempts: %d\n", job.AttemptCount)
	w.printf("  Updated: %s\n", formatInspectTime(job.UpdatedAt))
	w.printf("  Source path: %s\n", displayOrNone(job.SourcePath))
	w.printf("  Asset path: %s\n", displayOrNone(job.AssetPath))
	w.printf("  Path: %s\n", displayOrNone(job.Path))
	w.printf("  Last error: %s\n", displayOrNone(job.LastError))

	if len(report.Attempts) == 0 {
		w.printf("\nAttempts: none\n")
		return w.err
	}

	w.printf("\nAttempts:\n")
	for _, attempt := range report.Attempts {
		w.printf("\n  Attempt %d (id=%d)\n", attempt.Number, attempt.ID)
		w.printf("    State: %s\n", attempt.State)
		w.printf("    Worker: %s\n", displayOrNone(attempt.WorkerID))
		w.printf("    Started: %s\n", formatInspectTime(attempt.StartedAt))
		w.printf("    Finished: %s\n", formatInspectTimePtr(attempt.FinishedAt))
		w.printf("    Error: %s\n", displayOrNone(attempt.Error))

		if len(attempt.Events) == 0 {
			w.printf("    Events: none\n")
			continue
		}
		w.printf("    Events:\n")
		for _, event := range attempt.Events {
			w.printf("      [%d] %s type=%s name=%s message=%q\n",
				event.ID,
				formatInspectTime(event.CreatedAt),
				event.Type,
				event.Name,
				event.Message,
			)
			if event.PayloadError != "" {
				w.printf("        payload_error: %s\n", event.PayloadError)
			}
			if event.ProcessOutput != nil {
				writeProcessOutput(w, "        ", *event.ProcessOutput)
				continue
			}
			if event.Payload != nil {
				w.printf("        payload: %s\n", inspectPayloadDisplay(event.Payload))
			}
		}
	}
	return w.err
}

func writeProcessOutput(w *inspectWriter, indent string, output inspectProcessOutput) {
	w.printf("%sprocess output:\n", indent)
	w.printf("%s  step: %s\n", indent, displayOrNone(output.Step))
	w.printf("%s  command: %s\n", indent, formatCommand(output.Command))
	w.printf("%s  exit_code: %d\n", indent, output.ExitCode)
	w.printf("%s  duration: %s\n", indent, formatDurationMillis(output.DurationMillis))
	w.printf("%s  stdout: %s\n", indent, formatLogPath(output.StdoutPath, output.StdoutBytes))
	w.printf("%s  stderr: %s\n", indent, formatLogPath(output.StderrPath, output.StderrBytes))
	if output.Error != "" {
		w.printf("%s  error: %s\n", indent, output.Error)
	}
}

func inspectPayloadDisplay(payload *inspectPayload) string {
	if payload == nil {
		return "<none>"
	}
	switch payload.Kind {
	case "json":
		return string(payload.JSON)
	case "text":
		return strconv.QuoteToASCII(payload.Text)
	case "bytes":
		return "base64:" + payload.BytesBase64
	default:
		return fmt.Sprintf("<%s payload: %d bytes>", payload.Kind, payload.SizeBytes)
	}
}

func formatCommand(command []string) string {
	if len(command) == 0 {
		return "[]"
	}
	data, err := json.Marshal(command)
	if err != nil {
		return fmt.Sprintf("%q", command)
	}
	return string(data)
}

func formatDurationMillis(ms int64) string {
	duration := time.Duration(ms) * time.Millisecond
	if ms == 0 {
		return "0ms"
	}
	return fmt.Sprintf("%s (%dms)", duration, ms)
}

func formatLogPath(path string, byteCount int) string {
	return fmt.Sprintf("%s (%d bytes)", displayOrNone(path), byteCount)
}

func formatInspectTime(t time.Time) string {
	if t.IsZero() {
		return "<none>"
	}
	return t.Format(time.RFC3339Nano)
}

func formatInspectTimePtr(t *time.Time) string {
	if t == nil {
		return "<none>"
	}
	return formatInspectTime(*t)
}

func displayOrNone(value string) string {
	if value == "" {
		return "<none>"
	}
	return value
}

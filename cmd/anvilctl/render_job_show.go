package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/zekurio/anvil/internal/textout"
	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/domain"
)

// writeJobShow renders the full recorded history of one job. It is the
// post-mortem view, so it prints unreadable records as unreadable instead of
// dropping them: an omitted decision reads as "nothing was dropped", which is a
// different and much worse answer.
func writeJobShow(out io.Writer, report control.JobShowResponse) error {
	return textout.Write(out, func(w *textout.Writer) {
		job := report.Job
		w.Printf("Job %s (id=%d)\n", job.Slug, job.ID)
		w.Printf("  State: %s\n", job.State)
		w.Printf("  Library: %s\n", job.Library)
		w.Printf("  Attempts: %d\n", job.AttemptCount)
		w.Printf("  Updated: %s\n", formatTime(job.UpdatedAt))
		w.Printf("  Source path: %s\n", textout.OrNone(job.SourcePath))
		w.Printf("  Asset path: %s\n", textout.OrNone(job.AssetPath))
		w.Printf("  Path: %s\n", textout.OrNone(job.Path))
		w.Printf("  Last error: %s\n", textout.OrNone(job.LastError))

		if report.PipelineContext != nil {
			writePipelineContext(w, *report.PipelineContext)
		}
		if report.PublishOperation != nil {
			operation := report.PublishOperation
			w.Printf("\nPublish operation:\n")
			w.Printf("  Kind: %s\n", operation.Kind)
			w.Printf("  Mode: %s\n", operation.Mode)
			w.Printf("  Stage: %s\n", operation.Stage)
			w.Printf("  Artifact: %s\n", operation.ArtifactPath)
			w.Printf("  Destination: %s\n", operation.DestinationPath)
			w.Printf("  Cleanup source: %s\n", textout.OrNone(operation.CleanupSourcePath))
			w.Printf("  Backup: %s\n", textout.OrNone(operation.BackupPath))
			w.Printf("  Artifact size: %d bytes\n", operation.ArtifactSizeBytes)
			w.Printf("  Digest: %s\n", textout.OrNone(strings.TrimSpace(operation.DigestAlgorithm)))
			w.Printf("  Conflict: %s\n", textout.OrNone(operation.ConflictDescription))
			w.Printf("  Updated: %s\n", formatTime(operation.UpdatedAt))
		}

		if len(report.StreamSelection) > 0 {
			w.Printf("\nStream selection (attempt %d):\n", report.StreamSelection[0].AttemptNumber)
			for _, selection := range report.StreamSelection {
				if selection.Decision == nil {
					w.Printf("  unreadable: %s\n", selection.DecisionError)
					continue
				}
				writeStreamSelection(w, "  ", *selection.Decision)
			}
		}

		if len(report.Attempts) == 0 {
			w.Printf("\nAttempts: none\n")
			return
		}
		w.Printf("\nAttempts:\n")
		for _, attempt := range report.Attempts {
			w.Printf("\n  Attempt %d\n", attempt.Number)
			w.Printf("    State: %s\n", attempt.State)
			w.Printf("    Worker: %s\n", textout.OrNone(attempt.WorkerID))
			w.Printf("    Started: %s\n", formatTime(attempt.StartedAt))
			w.Printf("    Finished: %s\n", formatTimePtr(attempt.FinishedAt))
			w.Printf("    Error: %s\n", textout.OrNone(attempt.Error))

			if len(attempt.Events) == 0 {
				w.Printf("    Events: none\n")
				continue
			}
			w.Printf("    Events:\n")
			for _, event := range attempt.Events {
				w.Printf("      [%d] %s type=%s name=%s message=%q\n",
					event.ID, formatTime(event.CreatedAt), event.Type, event.Name, event.Message)
				if event.PayloadError != "" {
					w.Printf("        payload_error: %s\n", event.PayloadError)
				}
				switch {
				case event.ProcessOutput != nil:
					writeProcessOutput(w, "        ", *event.ProcessOutput)
				case event.StreamSelection != nil:
					writeStreamSelection(w, "        ", *event.StreamSelection)
				case event.Payload != nil:
					w.Printf("        payload: %s\n", payloadDisplay(event.Payload))
				}
			}
		}
	})
}

func writePipelineContext(w *textout.Writer, context control.PipelineContextDetail) {
	w.Printf("\nSaved context:\n")
	w.Printf("  Version: %d\n", context.Version)
	w.Printf("  Steps: %s\n", formatPipelineSteps(context.Steps))
	if context.CropFilter != "" {
		w.Printf("  Crop: %s\n", context.CropFilter)
	}
	if context.SearchCRF > 0 || context.SearchSkipReason != "" {
		if context.SearchSkipReason != "" {
			w.Printf("  Search: skipped video encode (%s)\n", context.SearchSkipReason)
		} else {
			w.Printf("  Search: CRF %d", context.SearchCRF)
			if context.SearchVMAF > 0 {
				w.Printf(" VMAF %.2f", context.SearchVMAF)
			}
			w.Printf("\n")
		}
	}
	if context.EncodeVideoCodec != "" {
		w.Printf("  Encode plan: codec=%s crf=%d\n", context.EncodeVideoCodec, context.EncodeCRF)
	}
	if context.ValidationOK != nil {
		w.Printf("  Validation: %t", *context.ValidationOK)
		if len(context.ValidationErrors) > 0 {
			w.Printf(" (%s)", strings.Join(context.ValidationErrors, "; "))
		}
		w.Printf("\n")
	}
}

func formatPipelineSteps(steps []control.PipelineStepDetail) string {
	if len(steps) == 0 {
		return "<none>"
	}
	values := make([]string, 0, len(steps))
	for _, step := range steps {
		value := step.Name
		if step.Resumable {
			value += "*"
		}
		values = append(values, value)
	}
	return strings.Join(values, ", ")
}

func writeStreamSelection(w *textout.Writer, indent string, decision domain.StreamSelectionDecision) {
	w.Printf("%s%s streams: rule=%s\n", indent, textout.OrNone(string(decision.Kind)), textout.OrNone(string(decision.Rule)))
	w.Printf("%s  original language: %s\n", indent, textout.OrNone(decision.OriginalLanguage))
	w.Printf("%s  requested: %s\n", indent, formatLanguages(decision.RequestedLanguages))
	w.Printf("%s  resolved: %s\n", indent, formatLanguages(decision.ResolvedLanguages))
	if len(decision.MissingLanguages) > 0 {
		w.Printf("%s  missing from source: %s\n", indent, formatLanguages(decision.MissingLanguages))
	}
	if decision.CleanupDisabledReason != "" {
		w.Printf("%s  cleanup disabled: %s\n", indent, decision.CleanupDisabledReason)
	}
	w.Printf("%s  kept: %s\n", indent, formatIndexes(decision.KeptIndexes()))
	w.Printf("%s  dropped: %s\n", indent, formatIndexes(decision.DroppedIndexes()))
	for _, stream := range decision.Streams {
		state := "dropped"
		if stream.Kept {
			state = "kept"
		}
		w.Printf("%s  [%d] %s %s %s %s (%s)\n",
			indent, stream.Index, textout.OrNone(stream.Language), textout.OrNone(stream.Codec),
			strconv.Quote(stream.Title), state, stream.Reason)
	}
}

func formatLanguages(values []string) string {
	if len(values) == 0 {
		return "<none>"
	}
	return strings.Join(values, ", ")
}

func formatIndexes(values []int) string {
	if len(values) == 0 {
		return "<none>"
	}
	formatted := make([]string, 0, len(values))
	for _, value := range values {
		formatted = append(formatted, strconv.Itoa(value))
	}
	return strings.Join(formatted, ", ")
}

func writeProcessOutput(w *textout.Writer, indent string, output control.ProcessOutputDetail) {
	w.Printf("%sprocess output:\n", indent)
	w.Printf("%s  step: %s\n", indent, textout.OrNone(output.Step))
	w.Printf("%s  command: %s\n", indent, formatCommand(output.Command))
	w.Printf("%s  exit_code: %d\n", indent, output.ExitCode)
	w.Printf("%s  duration: %s\n", indent, formatDurationMillis(output.DurationMillis))
	w.Printf("%s  stdout: %s\n", indent, formatLogPath(output.StdoutPath, output.StdoutBytes))
	w.Printf("%s  stderr: %s\n", indent, formatLogPath(output.StderrPath, output.StderrBytes))
	if output.Error != "" {
		w.Printf("%s  error: %s\n", indent, output.Error)
	}
}

func payloadDisplay(payload *control.EventPayload) string {
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
	if ms == 0 {
		return "0ms"
	}
	return fmt.Sprintf("%s (%dms)", time.Duration(ms)*time.Millisecond, ms)
}

func formatLogPath(path string, byteCount int) string {
	return fmt.Sprintf("%s (%d bytes)", textout.OrNone(path), byteCount)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "<none>"
	}
	return t.Format(time.RFC3339Nano)
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return "<none>"
	}
	return formatTime(*t)
}

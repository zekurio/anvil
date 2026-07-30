// Package trackstats refreshes Matroska track statistics tags (BPS, DURATION,
// NUMBER_OF_FRAMES, NUMBER_OF_BYTES) on the staged output. Players rely on the
// BPS tag to display per-track bitrates for codecs whose bitstream does not
// declare one (DTS-HD, TrueHD, AAC, FLAC, Opus), and recomputing replaces
// source statistics made stale by the video re-encode.
package trackstats

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
)

type Block struct {
	Runner process.Runner
	Binary string
}

func (Block) Name() string {
	return "track-stats"
}

// Run recomputes statistics tags for every track in the staged output. A
// failed in-place edit must stop publication because mkvpropedit may have
// partially modified the staged artifact.
func (b Block) Run(ctx context.Context, job *pipeline.JobContext) error {
	output := strings.TrimSpace(job.OutputPath)
	if output == "" {
		return errors.New("refresh track statistics: staged output path is required")
	}
	// mkvpropedit only understands Matroska.
	if !strings.EqualFold(filepath.Ext(output), ".mkv") {
		return nil
	}
	if _, err := os.Stat(output); err != nil {
		return fmt.Errorf("stat staged output for track statistics: %w", err)
	}
	runner := b.Runner
	if runner == nil {
		runner = process.OSRunner{}
	}
	binary := b.Binary
	if binary == "" {
		binary = "mkvpropedit"
	}
	result, err := runner.Run(ctx, process.Command{
		Name: binary,
		Args: []string{output, "--add-track-statistics-tags"},
	})
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if detail := outputHint(result); detail != "" {
		return fmt.Errorf("refresh track statistics for %q: %w: %s", output, err, detail)
	}
	return fmt.Errorf("refresh track statistics for %q: %w", output, err)
}

func outputHint(result process.Result) string {
	stdout := strings.TrimSpace(string(result.Stdout))
	stderr := strings.TrimSpace(string(result.Stderr))
	text := stdout
	if stderr != "" {
		if text != "" {
			text += "\n"
		}
		text += stderr
	}
	text = strings.ReplaceAll(text, "\n", " ")
	if len(text) > 300 {
		text = text[:300] + "..."
	}
	return text
}

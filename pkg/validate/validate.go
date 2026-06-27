package validate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/probe"
)

const defaultDurationToleranceSeconds = 2

type Prober interface {
	Probe(ctx context.Context, path string) (domain.ProbeResult, error)
}

type Validator struct {
	Prober                   Prober
	DurationToleranceSeconds float64
}

func (v Validator) Validate(ctx context.Context, source *domain.ProbeResult, outputPath string) (domain.ValidationResult, error) {
	result := domain.ValidationResult{OK: true}
	info, err := os.Stat(outputPath)
	if err != nil {
		result.OK = false
		result.Errors = append(result.Errors, fmt.Sprintf("output stat failed: %v", err))
		return result, fmt.Errorf("validate output: %w", err)
	}
	result.OutputSizeBytes = info.Size()
	if info.Size() <= 0 {
		result.OK = false
		result.Errors = append(result.Errors, "output file is empty")
	}

	prober := v.Prober
	if prober == nil {
		prober = probe.FFProbe{}
	}
	outputProbe, err := prober.Probe(ctx, outputPath)
	if err != nil {
		result.OK = false
		result.Errors = append(result.Errors, fmt.Sprintf("output probe failed: %v", err))
		return result, fmt.Errorf("validate output probe: %w", err)
	}
	result.OutputDurationSeconds = outputProbe.DurationSeconds
	if source != nil {
		result.SourceDurationSeconds = source.DurationSeconds
	}
	tolerance := v.DurationToleranceSeconds
	if tolerance <= 0 {
		tolerance = defaultDurationToleranceSeconds
	}
	if source != nil && source.DurationSeconds > 0 && outputProbe.DurationSeconds > 0 {
		if math.Abs(source.DurationSeconds-outputProbe.DurationSeconds) > tolerance {
			result.OK = false
			result.Errors = append(result.Errors, "output duration differs from source")
		}
	}
	if !result.OK {
		return result, errors.New("validation failed")
	}
	return result, nil
}

type Block struct {
	Validator Validator
}

func (Block) Name() string {
	return "validate"
}

func (b Block) Run(ctx context.Context, job *pipeline.JobContext) error {
	result, err := b.Validator.Validate(ctx, job.Probe, job.OutputPath)
	job.Validation = &result
	return err
}

package validate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestValidatorAcceptsReadableMatchingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.mkv")
	if err := os.WriteFile(path, []byte("encoded"), 0o600); err != nil {
		t.Fatalf("write output: %v", err)
	}
	result, err := Validator{Prober: fakeProber{duration: 100}}.Validate(context.Background(), &domain.ProbeResult{DurationSeconds: 101}, path)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !result.OK {
		t.Fatalf("result.OK = false, errors = %v", result.Errors)
	}
}

func TestValidatorRejectsDurationMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.mkv")
	if err := os.WriteFile(path, []byte("encoded"), 0o600); err != nil {
		t.Fatalf("write output: %v", err)
	}
	result, err := Validator{Prober: fakeProber{duration: 90}}.Validate(context.Background(), &domain.ProbeResult{DurationSeconds: 100}, path)
	if err == nil {
		t.Fatal("Validate() error = nil, want mismatch")
	}
	if result.OK {
		t.Fatal("result.OK = true, want false")
	}
}

type fakeProber struct {
	duration float64
}

func (f fakeProber) Probe(_ context.Context, path string) (domain.ProbeResult, error) {
	return domain.ProbeResult{Path: path, DurationSeconds: f.duration, SizeBytes: 1}, nil
}

package config

import (
	"strings"
	"testing"
)

func TestRejectNegativeThreadCap(t *testing.T) {
	cfg := Default()
	cfg.Daemon.MaxThreadsPerJob = -1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "max_threads_per_job") {
		t.Fatalf("Validate error = %v", err)
	}
}

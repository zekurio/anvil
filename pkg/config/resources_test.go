package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestLoadThreadCap(t *testing.T) {
	for _, cap := range []int{0, 3, -1} {
		t.Run(fmt.Sprint(cap), func(t *testing.T) {
			cfg, err := load(t, fmt.Sprintf("[daemon]\nmax_threads_per_job = %d\n", cap))
			if cap < 0 {
				if err == nil || !strings.Contains(err.Error(), "max_threads_per_job") {
					t.Fatalf("Load error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Daemon.MaxThreadsPerJob != cap {
				t.Fatalf("MaxThreadsPerJob = %d, want %d", cfg.Daemon.MaxThreadsPerJob, cap)
			}
		})
	}
	cfg, err := load(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Daemon.MaxThreadsPerJob != 0 {
		t.Fatalf("default MaxThreadsPerJob = %d, want 0", cfg.Daemon.MaxThreadsPerJob)
	}
}

func TestRejectNegativeThreadCap(t *testing.T) {
	cfg := Default()
	cfg.Daemon.MaxThreadsPerJob = -1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "max_threads_per_job") {
		t.Fatalf("Validate error = %v", err)
	}
}

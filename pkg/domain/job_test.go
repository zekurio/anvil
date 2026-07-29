package domain

import "testing"

func TestCanTransitionJobToCanceled(t *testing.T) {
	tests := []struct {
		name string
		from JobState
		want bool
	}{
		{name: "pending", from: JobStatePending, want: true},
		{name: "leased", from: JobStateLeased, want: true},
		{name: "running", from: JobStateRunning, want: true},
		{name: "validating", from: JobStateValidating, want: true},
		{name: "replacing", from: JobStateReplacing, want: true},
		{name: "retrying", from: JobStateRetrying, want: true},
		{name: "complete", from: JobStateComplete, want: false},
		{name: "failed", from: JobStateFailed, want: false},
		{name: "skipped", from: JobStateSkipped, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanTransitionJob(tt.from, JobStateCanceled); got != tt.want {
				t.Fatalf("CanTransitionJob(%q, canceled) = %t, want %t", tt.from, got, tt.want)
			}
		})
	}
}

func TestCanceledIsTerminalAndIdempotent(t *testing.T) {
	if !JobStateCanceled.Terminal() {
		t.Fatal("JobStateCanceled.Terminal() = false, want true")
	}
	if !CanTransitionJob(JobStateCanceled, JobStateCanceled) {
		t.Fatal("canceled -> canceled must stay allowed so repeated cancels are no-ops")
	}
	for _, state := range JobStates() {
		if state == JobStateCanceled {
			continue
		}
		if CanTransitionJob(JobStateCanceled, state) {
			t.Fatalf("CanTransitionJob(canceled, %q) = true, want false", state)
		}
	}
}

func TestCancelableExcludesTerminalAndUnknownStates(t *testing.T) {
	tests := []struct {
		name  string
		state JobState
		want  bool
	}{
		{name: "pending", state: JobStatePending, want: true},
		{name: "running", state: JobStateRunning, want: true},
		{name: "replacing", state: JobStateReplacing, want: true},
		{name: "canceled", state: JobStateCanceled, want: false},
		{name: "skipped", state: JobStateSkipped, want: false},
		{name: "complete", state: JobStateComplete, want: false},
		{name: "unknown", state: JobState("bogus"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.state.Cancelable(); got != tt.want {
				t.Fatalf("%q.Cancelable() = %t, want %t", tt.state, got, tt.want)
			}
		})
	}
}

func TestCanceledIsDistinguishableFromSkipped(t *testing.T) {
	if JobStateCanceled == JobStateSkipped {
		t.Fatal("canceled and skipped must remain distinct states")
	}
	if !ValidJobState(JobStateCanceled) {
		t.Fatal("ValidJobState(canceled) = false, want true")
	}
}

package controlapi

import (
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

const Version = "v1"

var BuildVersion = "dev"

type ErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type StatusResponse struct {
	APIVersion string           `json:"api_version"`
	ServerTime time.Time        `json:"server_time"`
	Daemon     DaemonStatus     `json:"daemon"`
	Workers    WorkerStatus     `json:"workers"`
	Queue      map[string]int64 `json:"queue"`
}

type DaemonStatus struct {
	State     string    `json:"state"`
	StartedAt time.Time `json:"started_at"`
	Version   string    `json:"version"`
}

type WorkerStatus struct {
	Configured int `json:"configured"`
	Active     int `json:"active"`
}

type JobListResponse struct {
	APIVersion string        `json:"api_version"`
	ServerTime time.Time     `json:"server_time"`
	Matched    int           `json:"matched"`
	Truncated  bool          `json:"truncated"`
	Jobs       []JobResponse `json:"jobs"`
	// PathOutsideLibraries reports that the absolute_path selector resolved
	// under no configured library root or handoff destination. Without this,
	// zero results are indistinguishable from a question Anvil was structurally
	// unable to answer, and a caller reports absence as fact.
	//
	// It describes the path against the current configuration, not the whole
	// job history: a job journaled against a library that has since been
	// reconfigured can still own a path reported as outside.
	PathOutsideLibraries bool `json:"path_outside_libraries,omitempty"`
}

type JobResponse struct {
	ID              int64               `json:"id"`
	Slug            string              `json:"slug"`
	Library         string              `json:"library"`
	State           string              `json:"state"`
	Source          OccurrenceResponse  `json:"source"`
	Asset           *OccurrenceResponse `json:"asset,omitempty"`
	AttemptCount    int                 `json:"attempt_count"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
	CompletedAt     *time.Time          `json:"completed_at,omitempty"`
	LeaseOwner      string              `json:"lease_owner,omitempty"`
	LeaseDeadline   *time.Time          `json:"lease_deadline,omitempty"`
	HeartbeatAt     *time.Time          `json:"heartbeat_at,omitempty"`
	LastError       string              `json:"last_error,omitempty"`
	DestinationPath string              `json:"destination_path,omitempty"`
	PublishStage    string              `json:"publish_stage,omitempty"`
	// MatchedOn reports every path of this job the absolute_path selector
	// matched. It is empty when the query did not select by absolute path, so
	// it never implies a match that was not asked for.
	//
	// It is a list because one path is legitimately several sides at once: an
	// in-place replacement writes the converted file back over its own source,
	// so reporting a single side would claim the output is not a destination.
	MatchedOn []PathMatchSide `json:"matched_on,omitempty"`
	// StreamSelection carries the audio and subtitle decisions of the most
	// recent attempt that recorded any. It is only populated when the query
	// asks for it, and stays absent for a job that never recorded a decision.
	StreamSelection []StreamSelectionResponse `json:"stream_selection,omitempty"`
}

// PathMatchSide names which of a job's paths an absolute_path selector matched.
// A destination match is the interesting one: it answers "which job produced
// this file", which a source-indexed lookup cannot.
type PathMatchSide string

const (
	PathMatchSource               PathMatchSide = "source"
	PathMatchAsset                PathMatchSide = "asset"
	PathMatchDestination          PathMatchSide = "destination"
	PathMatchDestinationDirectory PathMatchSide = "destination_directory"
)

// StreamSelectionResponse is one recorded stream selection decision together
// with the attempt that produced it.
type StreamSelectionResponse struct {
	AttemptID  int64     `json:"attempt_id"`
	RecordedAt time.Time `json:"recorded_at"`
	// Decision is absent when the record could not be decoded, so a reader can
	// never mistake an unreadable decision for one that dropped nothing.
	Decision      *domain.StreamSelectionDecision `json:"decision,omitempty"`
	DecisionError string                          `json:"decision_error,omitempty"`
}

type OccurrenceResponse struct {
	Path         string `json:"path"`
	AbsolutePath string `json:"absolute_path,omitempty"`
	Generation   int    `json:"generation"`
	Current      bool   `json:"current"`
	Status       string `json:"status"`
}

type JobQuery struct {
	Library      string
	Path         string
	AbsolutePath string
	States       []string
	CurrentOnly  bool
	Limit        int
	// WithSelection populates StreamSelection on each returned job. It is
	// opt-in because the decisions are far larger than the rest of a listing.
	WithSelection bool
}

// JobCancelRequest reuses the JobQuery selector vocabulary so a cancel can
// never target a broader set than the equivalent job list. IDs narrow the
// selection further; they never widen it.
type JobCancelRequest struct {
	Library      string   `json:"library,omitempty"`
	Path         string   `json:"path,omitempty"`
	AbsolutePath string   `json:"absolute_path,omitempty"`
	States       []string `json:"state,omitempty"`
	CurrentOnly  bool     `json:"current_only,omitempty"`
	IDs          []int64  `json:"ids,omitempty"`
	Reason       string   `json:"reason,omitempty"`
}

func (r JobCancelRequest) query() JobQuery {
	return JobQuery{
		Library: r.Library, Path: r.Path, AbsolutePath: r.AbsolutePath,
		States: r.States, CurrentOnly: r.CurrentOnly,
	}
}

// hasSelector reports whether the request narrows the queue. CurrentOnly is
// deliberately excluded: it only refines another selector, and on its own it
// matches every job in every library and state.
func (r JobCancelRequest) hasSelector() bool {
	if len(r.IDs) > 0 {
		return true
	}
	if strings.TrimSpace(r.Library) != "" || strings.TrimSpace(r.Path) != "" || strings.TrimSpace(r.AbsolutePath) != "" {
		return true
	}
	for _, state := range r.States {
		if strings.TrimSpace(state) != "" {
			return true
		}
	}
	return false
}

type JobCancelResponse struct {
	APIVersion string            `json:"api_version"`
	ServerTime time.Time         `json:"server_time"`
	Matched    int               `json:"matched"`
	Canceled   int               `json:"canceled"`
	Jobs       []JobCancelResult `json:"jobs"`
}

type JobCancelResult struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Library       string `json:"library"`
	PreviousState string `json:"previous_state"`
	State         string `json:"state"`
	Canceled      bool   `json:"canceled"`
	// SkipReason is a stable machine-readable explanation of why a matched job
	// was not canceled, such as publish_in_progress.
	SkipReason     string `json:"skip_reason,omitempty"`
	WorkerSignaled bool   `json:"worker_signaled"`
}

package controlapi

import (
	"strings"
	"time"
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

func (r JobCancelRequest) hasSelector() bool {
	if len(r.IDs) > 0 || r.CurrentOnly {
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
	ID             int64  `json:"id"`
	Slug           string `json:"slug"`
	Library        string `json:"library"`
	PreviousState  string `json:"previous_state"`
	State          string `json:"state"`
	Canceled       bool   `json:"canceled"`
	WorkerSignaled bool   `json:"worker_signaled"`
}

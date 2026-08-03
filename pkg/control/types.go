package control

import (
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

// Version names the control surface contract: the anvilctl command syntax,
// its --json shapes, and the error codes. It moves only when that contract
// changes, independently of ProtocolVersion, which is about wire compatibility
// between two binaries.
const Version = "v1"

// BuildVersion is stamped at link time. Both binaries report it, so an operator
// can tell which client is talking to which daemon.
var BuildVersion = "dev"

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
	Library      string   `json:"library,omitempty"`
	Path         string   `json:"path,omitempty"`
	AbsolutePath string   `json:"absolute_path,omitempty"`
	States       []string `json:"state,omitempty"`
	CurrentOnly  bool     `json:"current_only,omitempty"`
	// Limit bounds how many jobs are returned, not how many are matched. It is
	// a display bound: a selector-driven operation such as cancel must never
	// carry it, or it would silently act on a subset of what it selected.
	Limit int `json:"limit,omitempty"`
	// WithSelection populates StreamSelection on each returned job. It is
	// opt-in because the decisions are far larger than the rest of a listing.
	WithSelection bool `json:"with_selection,omitempty"`
}

// NormalizedJobQuery is a validated job selector. Both sides normalize through
// the same code, so the client refuses a mistake with the daemon's wording and
// the daemon never has to trust the client's normalization.
type NormalizedJobQuery struct {
	Library string
	// RelativePath is library-relative and slash-separated, empty when the
	// query did not select by relative path.
	RelativePath string
	// AbsolutePath is cleaned, empty when the query did not select by it.
	AbsolutePath string
	States       []domain.JobState
	CurrentOnly  bool
	Limit        int
}

// Normalize validates the selector and returns its canonical form.
func (q JobQuery) Normalize() (NormalizedJobQuery, error) {
	normalized := NormalizedJobQuery{
		Library:     strings.TrimSpace(q.Library),
		CurrentOnly: q.CurrentOnly,
		Limit:       q.Limit,
	}
	if q.Limit < 0 {
		return NormalizedJobQuery{}, NewError(CodeInvalidArgument, "limit must be non-negative")
	}
	if strings.TrimSpace(q.Path) != "" && strings.TrimSpace(q.AbsolutePath) != "" {
		return NormalizedJobQuery{}, NewError(CodeInvalidArgument, "path and absolute_path are mutually exclusive")
	}
	if strings.TrimSpace(q.Path) != "" {
		if normalized.Library == "" {
			return NormalizedJobQuery{}, NewError(CodeInvalidArgument, "library is required with path")
		}
		cleaned, err := CleanRelativePath(q.Path)
		if err != nil {
			return NormalizedJobQuery{}, err
		}
		normalized.RelativePath = cleaned
	}
	if strings.TrimSpace(q.AbsolutePath) != "" {
		if strings.ContainsRune(q.AbsolutePath, '\x00') || !filepath.IsAbs(q.AbsolutePath) {
			return NormalizedJobQuery{}, NewError(CodeInvalidArgument, "absolute_path must be absolute")
		}
		normalized.AbsolutePath = filepath.Clean(q.AbsolutePath)
	}
	states, err := ParseJobStates(q.States)
	if err != nil {
		return NormalizedJobQuery{}, err
	}
	normalized.States = states
	return normalized, nil
}

// validate rejects a query the daemon would reject anyway, so an operator
// mistake is reported without a round trip and with the same wording.
func (q JobQuery) validate() error {
	_, err := q.Normalize()
	return err
}

// ParseJobStates accepts repeated and comma-separated state selectors so the
// same wording works in a flag, a request field, and a config value.
func ParseJobStates(values []string) ([]domain.JobState, error) {
	states := make([]domain.JobState, 0, len(values))
	seen := make(map[domain.JobState]struct{}, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			state := domain.JobState(strings.TrimSpace(part))
			if state == "" {
				continue
			}
			if !domain.ValidJobState(state) {
				return nil, NewError(CodeInvalidArgument, "unknown job state %q", state)
			}
			if _, ok := seen[state]; ok {
				continue
			}
			seen[state] = struct{}{}
			states = append(states, state)
		}
	}
	return states, nil
}

// CleanRelativePath canonicalizes a library-relative path and refuses anything
// that could escape the library root.
func CleanRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, '\x00') || filepath.IsAbs(value) {
		return "", NewError(CodeInvalidArgument, "path must be relative to the library root")
	}
	cleaned := path.Clean(filepath.ToSlash(value))
	if cleaned == "." || cleaned == ".." || path.IsAbs(cleaned) || strings.HasPrefix(cleaned, "../") {
		return "", NewError(CodeInvalidArgument, "path must stay within the library root")
	}
	return cleaned, nil
}

// JobCancelRequest reuses the JobQuery selector vocabulary so a cancel can
// never target a broader set than the equivalent jobs listing. IDs narrow the
// selection further; they never widen it.
type JobCancelRequest struct {
	Library      string   `json:"library,omitempty"`
	Path         string   `json:"path,omitempty"`
	AbsolutePath string   `json:"absolute_path,omitempty"`
	States       []string `json:"state,omitempty"`
	CurrentOnly  bool     `json:"current_only,omitempty"`
	// References narrow the selection to specific jobs by id or slug. Like
	// IDs, they never widen it.
	References []string `json:"references,omitempty"`
	IDs        []int64  `json:"ids,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// Query is the listing this cancel acts on. It deliberately carries no Limit:
// a cancel must act on everything its selector matched, never on the first
// page of it.
func (r JobCancelRequest) Query() JobQuery {
	return JobQuery{
		Library: r.Library, Path: r.Path, AbsolutePath: r.AbsolutePath,
		States: r.States, CurrentOnly: r.CurrentOnly,
	}
}

// HasSelector reports whether the request narrows the queue. CurrentOnly is
// deliberately excluded: it only refines another selector, and on its own it
// matches every job in every library and state.
func (r JobCancelRequest) HasSelector() bool {
	if len(r.IDs) > 0 || len(r.References) > 0 {
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

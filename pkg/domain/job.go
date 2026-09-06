package domain

import (
	"fmt"
	"slices"
	"time"
)

type JobState string

const (
	JobStatePending    JobState = "pending"
	JobStateLeased     JobState = "leased"
	JobStateRunning    JobState = "running"
	JobStateValidating JobState = "validating"
	JobStateReplacing  JobState = "replacing"
	JobStateComplete   JobState = "complete"
	JobStateFailed     JobState = "failed"
	JobStateRetrying   JobState = "retrying"
	JobStateSkipped    JobState = "skipped"
	JobStateCanceled   JobState = "canceled"
)

// jobStates lists every persisted job state in lifecycle order.
var jobStates = []JobState{
	JobStatePending, JobStateLeased, JobStateRunning, JobStateValidating,
	JobStateReplacing, JobStateComplete, JobStateFailed, JobStateRetrying,
	JobStateSkipped, JobStateCanceled,
}

// JobStates lists every persisted job state in lifecycle order.
func JobStates() []JobState {
	return slices.Clone(jobStates)
}

func ValidJobState(state JobState) bool {
	return slices.Contains(jobStates, state)
}

type Job struct {
	ID              JobID
	Slug            string
	SourceID        MediaSourceID
	AssetID         MediaAssetID
	LibraryName     LibraryName
	Priority        int
	State           JobState
	LeaseOwner      string
	LeaseDeadline   *time.Time
	HeartbeatAt     *time.Time
	AttemptCount    int
	LastError       string
	InputSizeBytes  int64
	OutputSizeBytes int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
}

func (j Job) Label() string {
	if j.Slug != "" {
		return j.Slug
	}
	return fmt.Sprintf("job-%d", j.ID)
}

const JobPipelineContextVersion = 4

type JobPipelineContext struct {
	Version             int             `json:"version"`
	InputPath           string          `json:"input_path"`
	SourceFingerprint   FileFingerprint `json:"source_fingerprint"`
	AssetFingerprint    FileFingerprint `json:"asset_fingerprint"`
	InitialMetadata     JobMetadata     `json:"initial_metadata"`
	ResolvedLibraryJSON string          `json:"resolved_library_json"`
	ResolvedProfileJSON string          `json:"resolved_profile_json"`
	Metadata            JobMetadata     `json:"metadata"`
	Probe               *ProbeResult    `json:"probe,omitempty"`
	Audio               *AudioSelection `json:"audio,omitempty"`
	Crop                *CropResult     `json:"crop,omitempty"`
	Search              *SearchResult   `json:"search,omitempty"`
}

func (s JobState) Terminal() bool {
	switch s {
	case JobStateComplete, JobStateFailed, JobStateSkipped, JobStateCanceled:
		return true
	default:
		return false
	}
}

// Cancelable reports whether an operator cancel request still has work to do.
// Terminal states are not cancelable so that cancellation stays idempotent.
func (s JobState) Cancelable() bool {
	return ValidJobState(s) && !s.Terminal()
}

// jobTransitions lists every state reachable from a given state. States absent
// from the map are terminal: nothing but re-asserting themselves is allowed.
var jobTransitions = map[JobState][]JobState{
	JobStatePending:    {JobStateLeased, JobStateSkipped, JobStateCanceled},
	JobStateLeased:     {JobStateRunning, JobStateFailed, JobStateRetrying, JobStateSkipped, JobStateCanceled},
	JobStateRunning:    {JobStateValidating, JobStateFailed, JobStateRetrying, JobStateSkipped, JobStateCanceled},
	JobStateValidating: {JobStateReplacing, JobStateComplete, JobStateFailed, JobStateRetrying, JobStateSkipped, JobStateCanceled},
	JobStateReplacing:  {JobStateComplete, JobStateFailed, JobStateRetrying, JobStateSkipped, JobStateCanceled},
	JobStateFailed:     {JobStateRetrying},
	JobStateRetrying:   {JobStatePending, JobStateFailed, JobStateSkipped, JobStateCanceled},
}

func CanTransitionJob(from, to JobState) bool {
	if from == to {
		return true
	}
	return slices.Contains(jobTransitions[from], to)
}

type AttemptState string

const (
	AttemptStateRunning   AttemptState = "running"
	AttemptStateSucceeded AttemptState = "succeeded"
	AttemptStateFailed    AttemptState = "failed"
	AttemptStateCanceled  AttemptState = "canceled"
)

type Attempt struct {
	ID              AttemptID
	JobID           JobID
	Number          int
	WorkerID        string
	State           AttemptState
	ResolvedLibrary []byte
	ResolvedFlow    []byte
	ResolvedProfile []byte
	StartedAt       time.Time
	FinishedAt      *time.Time
	Error           string
}

type ResourceAllocation struct {
	WorkerID string
	Threads  int
}

type AttemptEventType string

const (
	AttemptEventBlockStarted  AttemptEventType = "block_started"
	AttemptEventBlockFinished AttemptEventType = "block_finished"
	AttemptEventBlockFailed   AttemptEventType = "block_failed"
	AttemptEventArtifact      AttemptEventType = "artifact"
)

type AttemptEvent struct {
	ID        AttemptEventID
	AttemptID AttemptID
	Type      AttemptEventType
	Name      string
	Message   string
	Payload   []byte
	CreatedAt time.Time
}

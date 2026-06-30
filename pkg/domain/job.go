package domain

import "time"

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
)

type Job struct {
	ID              JobID
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

func (s JobState) Terminal() bool {
	switch s {
	case JobStateComplete, JobStateFailed, JobStateSkipped:
		return true
	default:
		return false
	}
}

func CanTransitionJob(from, to JobState) bool {
	if from == to {
		return true
	}

	switch from {
	case JobStatePending:
		return to == JobStateLeased || to == JobStateSkipped
	case JobStateLeased:
		return to == JobStateRunning || to == JobStateFailed || to == JobStateRetrying
	case JobStateRunning:
		return to == JobStateValidating || to == JobStateFailed || to == JobStateRetrying
	case JobStateValidating:
		return to == JobStateReplacing || to == JobStateComplete || to == JobStateFailed || to == JobStateRetrying
	case JobStateReplacing:
		return to == JobStateComplete || to == JobStateFailed || to == JobStateRetrying
	case JobStateFailed:
		return to == JobStateRetrying
	case JobStateRetrying:
		return to == JobStatePending || to == JobStateFailed
	default:
		return false
	}
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

type ExecutionPlan struct {
	JobID           JobID
	AttemptID       AttemptID
	SourceID        MediaSourceID
	AssetID         MediaAssetID
	InputPath       string
	OutputPath      string
	ResolvedLibrary Library
	ResolvedFlow    Flow
	ResolvedProfile Profile
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

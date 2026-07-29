package control

import (
	"encoding/json"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

// JobShowRequest names one job by id or slug. It is deliberately a single
// reference: the detail report is the "what exactly happened to this file"
// view, and a multi-job variant of it is a listing, which job.list already is.
type JobShowRequest struct {
	Reference string `json:"reference"`
}

// JobShowResponse is the full recorded history of a job. Its JSON shape is the
// contract `anvilctl job show --json` publishes.
type JobShowResponse struct {
	APIVersion       string                   `json:"api_version"`
	ServerTime       time.Time                `json:"server_time"`
	Job              JobDetail                `json:"job"`
	PipelineContext  *PipelineContextDetail   `json:"pipeline_context,omitempty"`
	PublishOperation *PublishOperationDetail  `json:"publish_operation,omitempty"`
	StreamSelection  []AttemptStreamSelection `json:"stream_selection,omitempty"`
	Attempts         []AttemptDetail          `json:"attempts"`
}

type JobDetail struct {
	ID           int64      `json:"id"`
	Slug         string     `json:"slug"`
	State        string     `json:"state"`
	Library      string     `json:"library"`
	AttemptCount int        `json:"attempt_count"`
	UpdatedAt    time.Time  `json:"updated_at"`
	CreatedAt    time.Time  `json:"created_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	SourceKind   string     `json:"source_kind"`
	SourcePath   string     `json:"source_path"`
	AssetPath    string     `json:"asset_path"`
	AssetRole    string     `json:"asset_role"`
	Path         string     `json:"path"`
	LastError    string     `json:"last_error"`
}

// AttemptStreamSelection reports one stream-selection decision together with
// the attempt that produced it. It is the record that answers "did Anvil drop
// that track" after the source file is gone.
type AttemptStreamSelection struct {
	AttemptID     int64     `json:"attempt_id"`
	AttemptNumber int       `json:"attempt_number"`
	RecordedAt    time.Time `json:"recorded_at"`
	// Decision is absent when the record could not be decoded, so a reader can
	// never mistake an unreadable decision for one that dropped nothing.
	Decision      *domain.StreamSelectionDecision `json:"decision,omitempty"`
	DecisionError string                          `json:"decision_error,omitempty"`
}

type PublishOperationDetail struct {
	Kind                string    `json:"kind"`
	Mode                string    `json:"mode"`
	Stage               string    `json:"stage"`
	ArtifactPath        string    `json:"artifact_path"`
	DestinationPath     string    `json:"destination_path"`
	CleanupSourcePath   string    `json:"cleanup_source_path,omitempty"`
	BackupPath          string    `json:"backup_path,omitempty"`
	ArtifactSizeBytes   int64     `json:"artifact_size_bytes"`
	DigestAlgorithm     string    `json:"digest_algorithm,omitempty"`
	ConflictDescription string    `json:"conflict_description,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type AttemptDetail struct {
	ID         int64                `json:"id"`
	Number     int                  `json:"number"`
	State      string               `json:"state"`
	WorkerID   string               `json:"worker_id"`
	StartedAt  time.Time            `json:"started_at"`
	FinishedAt *time.Time           `json:"finished_at,omitempty"`
	Error      string               `json:"error"`
	Events     []AttemptEventDetail `json:"events"`
}

type AttemptEventDetail struct {
	ID              int64                           `json:"id"`
	AttemptID       int64                           `json:"attempt_id"`
	CreatedAt       time.Time                       `json:"created_at"`
	Type            string                          `json:"type"`
	Name            string                          `json:"name"`
	Message         string                          `json:"message"`
	Payload         *EventPayload                   `json:"payload,omitempty"`
	ProcessOutput   *ProcessOutputDetail            `json:"process_output,omitempty"`
	StreamSelection *domain.StreamSelectionDecision `json:"stream_selection,omitempty"`
	PayloadError    string                          `json:"payload_error,omitempty"`
}

type EventPayload struct {
	Kind        string          `json:"kind"`
	SizeBytes   int             `json:"size_bytes"`
	JSON        json.RawMessage `json:"json,omitempty"`
	Text        string          `json:"text,omitempty"`
	BytesBase64 string          `json:"bytes_base64,omitempty"`
}

type ProcessOutputDetail struct {
	Step           string   `json:"step"`
	Command        []string `json:"command"`
	ExitCode       int      `json:"exit_code"`
	DurationMillis int64    `json:"duration_ms"`
	StdoutPath     string   `json:"stdout_path"`
	StderrPath     string   `json:"stderr_path"`
	StdoutBytes    int      `json:"stdout_bytes"`
	StderrBytes    int      `json:"stderr_bytes"`
	Error          string   `json:"error"`
}

type PipelineContextDetail struct {
	Version          int                  `json:"version"`
	Steps            []PipelineStepDetail `json:"steps"`
	CropFilter       string               `json:"crop_filter,omitempty"`
	SearchCRF        int                  `json:"search_crf,omitempty"`
	SearchVMAF       float64              `json:"search_vmaf,omitempty"`
	SearchSkipReason string               `json:"search_skip_reason,omitempty"`
	EncodeVideoCodec string               `json:"encode_video_codec,omitempty"`
	EncodeCRF        int                  `json:"encode_crf,omitempty"`
	ValidationOK     *bool                `json:"validation_ok,omitempty"`
	ValidationErrors []string             `json:"validation_errors,omitempty"`
}

type PipelineStepDetail struct {
	Name       string    `json:"name"`
	AttemptID  int64     `json:"attempt_id"`
	FinishedAt time.Time `json:"finished_at"`
	Resumable  bool      `json:"resumable"`
}

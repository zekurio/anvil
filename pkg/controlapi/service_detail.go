package controlapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
	"github.com/zekurio/anvil/pkg/store"
)

// processOutputArtifactName is the artifact name pkg/worker records external
// process output under.
const processOutputArtifactName = "process-output"

// ShowJob reports everything Anvil recorded about one job: its occurrence
// paths, the resumable pipeline context, the publish journal, the stream
// selection decisions, and every attempt event. It is the post-mortem view, so
// it never hides a record it cannot decode.
func (s Service) ShowJob(ctx context.Context, request control.JobShowRequest) (control.JobShowResponse, error) {
	if err := s.requireStore(); err != nil {
		return control.JobShowResponse{}, err
	}
	reference := strings.TrimSpace(request.Reference)
	if reference == "" {
		return control.JobShowResponse{}, invalidArgumentf("a job id or slug is required")
	}
	job, err := s.Store.ResolveJobReference(ctx, reference)
	if errors.Is(err, store.ErrNotFound) {
		return control.JobShowResponse{}, notFoundf("no job matches reference %q", reference)
	}
	if err != nil {
		return control.JobShowResponse{}, err
	}

	summary, err := s.Store.GetJobSummary(ctx, job.ID)
	if err != nil {
		return control.JobShowResponse{}, err
	}
	attempts, err := s.Store.ListAttemptsForJob(ctx, job.ID)
	if err != nil {
		return control.JobShowResponse{}, err
	}
	snapshot, hasSnapshot, err := s.Store.GetJobPipelineContext(ctx, job.ID)
	if err != nil {
		return control.JobShowResponse{}, err
	}
	response := control.JobShowResponse{
		APIVersion: control.Version,
		ServerTime: s.now(),
		Job:        jobDetailFromSummary(summary),
		Attempts:   make([]control.AttemptDetail, 0, len(attempts)),
	}
	if hasSnapshot {
		context := pipelineContextDetail(snapshot)
		response.PipelineContext = &context
	}
	operation, hasOperation, err := s.Store.GetPublishOperation(ctx, job.ID)
	if err != nil {
		return control.JobShowResponse{}, err
	}
	if hasOperation {
		detail := publishOperationDetail(operation)
		response.PublishOperation = &detail
	}
	for _, attempt := range attempts {
		events, err := s.Store.ListAttemptEvents(ctx, attempt.ID)
		if err != nil {
			return control.JobShowResponse{}, err
		}
		response.Attempts = append(response.Attempts, attemptDetail(attempt, events))
	}
	response.StreamSelection = latestStreamSelection(response.Attempts)
	return response, nil
}

// latestStreamSelection returns the stream-selection decisions of the most
// recent attempt that recorded any, because that is the state of the file on
// disk right now.
func latestStreamSelection(attempts []control.AttemptDetail) []control.AttemptStreamSelection {
	for i := len(attempts) - 1; i >= 0; i-- {
		if selections := streamSelections(attempts[i]); len(selections) > 0 {
			return selections
		}
	}
	return nil
}

// streamSelections reports the decisions this attempt recorded, including ones
// that failed to decode. An unreadable record still counts as recorded: hiding
// it would let latestStreamSelection fall back to an older attempt and present
// a stale decision as the current one.
func streamSelections(a control.AttemptDetail) []control.AttemptStreamSelection {
	var result []control.AttemptStreamSelection
	for _, event := range a.Events {
		// A payload error on some other artifact is not a stream selection, and
		// treating it as one would both invent a decision and stop the scan from
		// reaching the real record.
		if event.StreamSelection == nil && (event.PayloadError == "" || event.Name != pipeline.StreamSelectionArtifact) {
			continue
		}
		result = append(result, control.AttemptStreamSelection{
			AttemptID:     a.ID,
			AttemptNumber: a.Number,
			RecordedAt:    event.CreatedAt,
			Decision:      event.StreamSelection,
			DecisionError: event.PayloadError,
		})
	}
	return result
}

func publishOperationDetail(operation replacepkg.PublishOperation) control.PublishOperationDetail {
	detail := control.PublishOperationDetail{
		Kind:                operation.Kind,
		Mode:                operation.Mode,
		Stage:               string(operation.Stage),
		ArtifactPath:        operation.ArtifactPath,
		DestinationPath:     operation.DestinationPath,
		CleanupSourcePath:   operation.CleanupSourcePath,
		BackupPath:          operation.BackupPath,
		ArtifactSizeBytes:   operation.ArtifactIdentity.SizeBytes,
		DigestAlgorithm:     operation.DigestAlgorithm,
		ConflictDescription: operation.ConflictDescription,
		UpdatedAt:           operation.UpdatedAt,
	}
	if len(operation.CleanupEntries) > 0 {
		detail.CleanupEntries = make([]control.CleanupEntryDetail, 0, len(operation.CleanupEntries))
		for _, entry := range operation.CleanupEntries {
			detail.CleanupEntries = append(detail.CleanupEntries, control.CleanupEntryDetail{
				Path:      entry.Path,
				SizeBytes: entry.Identity.SizeBytes,
			})
		}
	}
	if len(operation.CleanupDirectories) > 0 {
		detail.CleanupDirectories = make([]string, 0, len(operation.CleanupDirectories))
		for _, directory := range operation.CleanupDirectories {
			detail.CleanupDirectories = append(detail.CleanupDirectories, directory.Path)
		}
	}
	return detail
}

func jobDetailFromSummary(summary store.JobSummary) control.JobDetail {
	return control.JobDetail{
		ID:           int64(summary.Job.ID),
		Slug:         summary.Job.Label(),
		State:        string(summary.Job.State),
		Library:      string(summary.Job.LibraryName),
		AttemptCount: summary.Job.AttemptCount,
		UpdatedAt:    summary.Job.UpdatedAt,
		CreatedAt:    summary.Job.CreatedAt,
		CompletedAt:  summary.Job.CompletedAt,
		SourceKind:   string(summary.SourceKind),
		SourcePath:   summary.SourcePath,
		AssetPath:    summary.AssetPath,
		AssetRole:    string(summary.AssetRole),
		Path:         jobPath(summary),
		LastError:    summary.Job.LastError,
	}
}

// jobPath joins a job's source and asset paths into the single path an operator
// recognizes. A package job's asset lives under its source, so reporting only
// one of them would name a directory or a file with no context.
func jobPath(summary store.JobSummary) string {
	if summary.AssetPath == "" || summary.AssetPath == summary.SourcePath {
		return summary.SourcePath
	}
	source := strings.Trim(summary.SourcePath, "/")
	asset := strings.Trim(summary.AssetPath, "/")
	if source == "" {
		return asset
	}
	if asset == "" {
		return source
	}
	return source + "/" + asset
}

func attemptDetail(attempt domain.Attempt, events []domain.AttemptEvent) control.AttemptDetail {
	result := control.AttemptDetail{
		ID:         int64(attempt.ID),
		Number:     attempt.Number,
		State:      string(attempt.State),
		WorkerID:   attempt.WorkerID,
		StartedAt:  attempt.StartedAt,
		FinishedAt: attempt.FinishedAt,
		Error:      attempt.Error,
		Events:     make([]control.AttemptEventDetail, 0, len(events)),
	}
	for _, event := range events {
		result.Events = append(result.Events, attemptEventDetail(event))
	}
	return result
}

func attemptEventDetail(event domain.AttemptEvent) control.AttemptEventDetail {
	result := control.AttemptEventDetail{
		ID:        int64(event.ID),
		AttemptID: int64(event.AttemptID),
		CreatedAt: event.CreatedAt,
		Type:      string(event.Type),
		Name:      event.Name,
		Message:   event.Message,
	}
	if isProcessOutputEvent(event) {
		output, err := decodeProcessOutput(event.Payload)
		if err != nil {
			result.Payload = decodeEventPayload(event.Payload)
			result.PayloadError = err.Error()
			return result
		}
		result.ProcessOutput = output
		return result
	}
	if pipeline.IsStreamSelectionEvent(event) {
		decision, err := pipeline.DecodeStreamSelection(event.Payload)
		if err != nil {
			result.Payload = decodeEventPayload(event.Payload)
			result.PayloadError = err.Error()
			return result
		}
		result.StreamSelection = &decision
		return result
	}
	result.Payload = decodeEventPayload(event.Payload)
	return result
}

func isProcessOutputEvent(event domain.AttemptEvent) bool {
	return event.Type == domain.AttemptEventArtifact && event.Name == processOutputArtifactName
}

func decodeProcessOutput(payload []byte) (*control.ProcessOutputDetail, error) {
	var output control.ProcessOutputDetail
	if err := json.Unmarshal(payload, &output); err != nil {
		return nil, err
	}
	if output.Command == nil {
		output.Command = []string{}
	}
	return &output, nil
}

func decodeEventPayload(payload []byte) *control.EventPayload {
	if len(payload) == 0 {
		return nil
	}
	result := &control.EventPayload{SizeBytes: len(payload)}

	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err == nil {
		result.Kind = "json"
		result.JSON = append(json.RawMessage(nil), compact.Bytes()...)
		return result
	}
	if utf8.Valid(payload) {
		result.Kind = "text"
		result.Text = string(payload)
		return result
	}
	result.Kind = "bytes"
	result.BytesBase64 = base64.StdEncoding.EncodeToString(payload)
	return result
}

func pipelineContextDetail(snapshot domain.JobPipelineContext) control.PipelineContextDetail {
	result := control.PipelineContextDetail{
		Version: snapshot.Version,
		Steps:   pipelineStepDetails(snapshot.Steps),
	}
	if snapshot.Crop != nil {
		result.CropFilter = snapshot.Crop.Filter
	}
	if snapshot.Search != nil {
		result.SearchCRF = snapshot.Search.CRF
		result.SearchVMAF = snapshot.Search.VMAF
		result.SearchSkipReason = snapshot.Search.VideoEncodeSkipReason
	}
	if snapshot.EncodePlan != nil {
		result.EncodeVideoCodec = snapshot.EncodePlan.VideoCodec
		result.EncodeCRF = snapshot.EncodePlan.CRF
	}
	if snapshot.Validation != nil {
		ok := snapshot.Validation.OK
		result.ValidationOK = &ok
		result.ValidationErrors = append([]string(nil), snapshot.Validation.Errors...)
	}
	return result
}

func pipelineStepDetails(steps map[string]domain.JobPipelineStep) []control.PipelineStepDetail {
	names := make([]string, 0, len(steps))
	for name := range steps {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]control.PipelineStepDetail, 0, len(names))
	for _, name := range names {
		step := steps[name]
		result = append(result, control.PipelineStepDetail{
			Name:       name,
			AttemptID:  int64(step.AttemptID),
			FinishedAt: step.FinishedAt,
			Resumable:  step.Resumable,
		})
	}
	return result
}

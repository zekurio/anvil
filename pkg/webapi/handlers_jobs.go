package webapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/controlapi"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/store"
)

// maxActionJobs bounds how many jobs one cancel or retry request may name.
const maxActionJobs = 1000

// cancelReason is recorded on jobs canceled from the web UI.
const cancelReason = "canceled from the web UI"

func (s *Server) listJobs(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	query := r.URL.Query()
	states, err := control.ParseJobStates(query["state"])
	if err != nil {
		return fail(invalidf("state", "%s", sentence(err.Error())))
	}
	// status selects by the groups the UI shows. A job is listed when it
	// matches either selector, so both narrow the same thing: the state.
	for _, value := range query["status"] {
		for _, part := range strings.Split(value, ",") {
			status := store.FileStatus(strings.TrimSpace(part))
			if status == "" {
				continue
			}
			grouped := store.JobStatesOf(status)
			if len(grouped) == 0 {
				return fail(invalidf("status", "status must be one of queued, running, done, failed, or skipped, not %q.", status))
			}
			for _, state := range grouped {
				if !slices.Contains(states, state) {
					states = append(states, state)
				}
			}
		}
	}
	limit, offset, err := pageParams(r)
	if err != nil {
		return fail(err)
	}
	items, total, err := s.Jobs.JobPage(r.Context(), controlapi.JobPageQuery{
		Library: strings.TrimSpace(query.Get("library")),
		States:  states,
		Search:  strings.TrimSpace(query.Get("q")),
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		return fail(err)
	}
	ids := make([]domain.JobID, 0, len(items))
	for _, item := range items {
		ids = append(ids, domain.JobID(item.Job.ID))
	}
	attempts, err := s.Store.LatestAttempts(r.Context(), ids)
	if err != nil {
		return fail(err)
	}
	list := JobList{Total: total, Jobs: make([]Job, 0, len(items))}
	for _, item := range items {
		job := Job{
			JobResponse:     item.Job,
			Status:          string(store.FileStatusOf(domain.JobState(item.Job.State))),
			Path:            item.Path,
			InputSizeBytes:  item.InputSizeBytes,
			OutputSizeBytes: item.OutputSizeBytes,
		}
		if attempt, started := attempts[domain.JobID(item.Job.ID)]; started {
			job.StartedAt, job.FinishedAt = &attempt.StartedAt, attempt.FinishedAt
		}
		if job.Status == FileStatusRunning {
			progress := s.progressOf(item.Job.ID, item.Job.Library, item.Path, attempts[domain.JobID(item.Job.ID)].FlowName, item.Job.UpdatedAt)
			job.Progress = &progress
		}
		list.Jobs = append(list.Jobs, job)
	}
	return ok(list)
}

func (s *Server) getJob(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return fail(notFoundf("There is no job %q.", r.PathValue("id")))
	}
	shown, err := s.Jobs.ShowJob(r.Context(), control.JobShowRequest{Reference: strconv.FormatInt(id, 10)})
	if err != nil {
		return fail(err)
	}
	// The row the queue lists carries what the detail report leaves out:
	// the sizes and where the result goes.
	item, err := s.Jobs.JobItem(r.Context(), domain.JobID(id))
	if err != nil {
		return fail(err)
	}
	detail := JobDetail{
		JobShowResponse: shown,
		Status:          string(store.FileStatusOf(domain.JobState(shown.Job.State))),
		InputPath:       item.Job.Source.AbsolutePath,
		InputSizeBytes:  item.InputSizeBytes,
		OutputSizeBytes: item.OutputSizeBytes,
		DestinationPath: item.Job.DestinationPath,
	}
	if item.Job.Asset != nil {
		detail.InputPath = item.Job.Asset.AbsolutePath
	}
	if detail.Status == FileStatusRunning {
		attempts, err := s.Store.LatestAttempts(r.Context(), []domain.JobID{domain.JobID(id)})
		if err != nil {
			return fail(err)
		}
		progress := s.progressOf(id, item.Job.Library, item.Path, attempts[domain.JobID(id)].FlowName, item.Job.UpdatedAt)
		detail.Progress = &progress
	}
	if detail.Flow, err = s.jobFlow(r, id, shown.Flow); err != nil {
		return fail(err)
	}
	checkpoint, found, err := s.Store.GetJobPipelineContext(r.Context(), domain.JobID(id))
	if err != nil {
		return fail(err)
	}
	if found && checkpoint.Probe != nil {
		facts := fileFacts(*checkpoint.Probe)
		detail.File = &facts
	}
	return ok(detail)
}

// jobFlow reports how the flow resolved on the latest attempt that evaluated
// it: the path and decisions that attempt recorded, on the graph it actually
// ran, which is the snapshot the attempt took and not the flow as it is now.
func (s *Server) jobFlow(r *http.Request, id int64, resolved *control.JobFlowDetail) (*JobFlow, error) {
	if resolved == nil {
		return nil, nil
	}
	result := &JobFlow{
		FlowID:    resolved.FlowID,
		FlowName:  resolved.FlowName,
		Revision:  resolved.Revision,
		Graph:     normalizeGraph(flow.Graph{}),
		Outcome:   flow.Outcome(resolved.Outcome),
		Path:      nonNil(resolved.Path),
		Decisions: nonNil(resolved.Decisions),
		Plan:      nonNil(resolved.Plan),
	}
	attempts, err := s.Store.ListAttemptsForJob(r.Context(), domain.JobID(id))
	if err != nil {
		return nil, err
	}
	for _, attempt := range attempts {
		if int64(attempt.ID) != resolved.AttemptID || len(attempt.ResolvedFlow) == 0 {
			continue
		}
		// A snapshot that cannot be read leaves the graph empty: the path and
		// the decisions are still worth showing without it.
		var snapshot domain.FlowSnapshot
		var graph flow.Graph
		if json.Unmarshal(attempt.ResolvedFlow, &snapshot) == nil && json.Unmarshal(snapshot.Graph, &graph) == nil {
			result.Graph = normalizeGraph(graph)
		}
	}
	return result, nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// actionRequest reads a cancel or retry request and checks its selector.
func actionRequest(r *http.Request) (JobActionRequest, []domain.JobState, error) {
	var request JobActionRequest
	if err := decodeBody(r, &request); err != nil {
		return JobActionRequest{}, nil, err
	}
	request.Library = strings.TrimSpace(request.Library)
	states, err := control.ParseJobStates(request.States)
	if err != nil {
		return JobActionRequest{}, nil, invalidf("states", "%s", sentence(err.Error()))
	}
	switch {
	case len(request.IDs) > maxActionJobs:
		return JobActionRequest{}, nil, invalidf("ids", "At most %d jobs can be named in one request.", maxActionJobs)
	case len(request.IDs) == 0 && request.Library == "" && len(states) == 0:
		return JobActionRequest{}, nil, invalidf("", "Name the jobs by ids, or select them by library, states, or both.")
	}
	for _, id := range request.IDs {
		if id <= 0 {
			return JobActionRequest{}, nil, invalidf("ids", "%d is not a job ID.", id)
		}
	}
	return request, states, nil
}

// cancelSkip reports a job a cancel left alone in the contract's vocabulary.
func cancelSkip(id int64, reason, state string) JobActionSkip {
	skip := JobActionSkip{ID: id}
	switch store.CancelSkipReason(reason) {
	case store.CancelSkipAlreadyTerminal:
		skip.Reason, skip.Detail = SkipFinished, fmt.Sprintf("The job is already %s.", state)
	case store.CancelSkipPublishInFlight:
		skip.Reason, skip.Detail = SkipPublishing, "The job is publishing its result. Canceling now could leave the file half replaced, so it finishes first."
	case store.CancelSkipStateChanged:
		skip.Reason, skip.Detail = SkipStateChanged, fmt.Sprintf("The job moved to %s in the meantime.", state)
	case store.CancelSkipMissing:
		skip.Reason, skip.Detail = SkipNotFound, "The job no longer exists."
	default:
		skip.Reason, skip.Detail = SkipError, sentence(strings.ReplaceAll(reason, "_", " "))
	}
	return skip
}

func (s *Server) cancelJobs(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	request, _, err := actionRequest(r)
	if err != nil {
		return fail(err)
	}
	result := JobActionResult{Skipped: []JobActionSkip{}}
	cancel := control.JobCancelRequest{Reason: cancelReason}
	if len(request.IDs) > 0 {
		// The job service refuses the whole request when one named job is
		// gone. A selection made in a browser goes stale all the time, so the
		// jobs that still exist are canceled and the others reported.
		for _, id := range request.IDs {
			if _, err := s.Store.GetJob(r.Context(), domain.JobID(id)); errors.Is(err, store.ErrNotFound) {
				result.Matched++
				result.Skipped = append(result.Skipped, cancelSkip(id, string(store.CancelSkipMissing), ""))
				continue
			} else if err != nil {
				return fail(err)
			}
			cancel.IDs = append(cancel.IDs, id)
		}
		if len(cancel.IDs) == 0 {
			return ok(result)
		}
	} else {
		cancel.Library, cancel.States = request.Library, request.States
	}
	canceled, err := s.Jobs.CancelJobs(r.Context(), cancel)
	if err != nil {
		return fail(err)
	}
	result.Matched += canceled.Matched
	result.Changed = canceled.Canceled
	for _, job := range canceled.Jobs {
		if !job.Canceled {
			result.Skipped = append(result.Skipped, cancelSkip(job.ID, job.SkipReason, job.State))
		}
	}
	return ok(result)
}

// retryableStates are the states a job can be queued again from.
var retryableStates = []domain.JobState{domain.JobStateFailed, domain.JobStateCanceled, domain.JobStateSkipped}

// retryJobs queues jobs again. Named jobs are retried one by one, so a job
// that cannot be retried is reported and does not hold up the others. Without
// IDs the selector picks the jobs; states default to failed, and only failed,
// canceled, and skipped jobs can be selected.
func (s *Server) retryJobs(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	request, states, err := actionRequest(r)
	if err != nil {
		return fail(err)
	}
	ids := request.IDs
	if len(ids) == 0 {
		if len(states) == 0 {
			states = []domain.JobState{domain.JobStateFailed}
		}
		for _, state := range states {
			if !slices.Contains(retryableStates, state) {
				return fail(invalidf("states", "Jobs that are %s cannot be retried. Only failed, canceled, and skipped jobs can.", state))
			}
		}
		if request.Library != "" {
			if _, err := s.storedLibrary(r.Context(), request.Library); err != nil {
				return fail(err)
			}
		}
		selected, err := s.Store.ListJobs(r.Context(), store.JobListFilter{LibraryName: domain.LibraryName(request.Library), States: states})
		if err != nil {
			return fail(err)
		}
		for _, job := range selected {
			ids = append(ids, int64(job.Job.ID))
		}
	}

	result := JobActionResult{Matched: len(ids), Skipped: []JobActionSkip{}}
	for _, id := range ids {
		_, err := s.Jobs.RetryJobs(r.Context(), control.JobRetryRequest{References: []string{strconv.FormatInt(id, 10)}})
		switch {
		case err == nil:
			result.Changed++
		case r.Context().Err() != nil:
			return fail(r.Context().Err())
		default:
			result.Skipped = append(result.Skipped, retrySkip(r, id, err))
		}
	}
	return ok(result)
}

// retrySkip reports a job a retry left alone. The store's refusals ("cannot
// retry job 12 from state ...") are written for the operator already.
func retrySkip(r *http.Request, id int64, err error) JobActionSkip {
	failure := classify(r.Context(), err)
	skip := JobActionSkip{ID: id, Reason: SkipError, Detail: failure.message}
	var refusal *store.RetryRefusedError
	switch {
	case errors.As(err, &refusal) && refusal.InputChanged:
		skip.Reason = SkipInputChanged
	case errors.As(err, &refusal) && refusal.Denied:
		skip.Reason = SkipDenied
	case errors.As(err, &refusal):
		skip.Reason = SkipNotRetryable
	case failure.status == http.StatusNotFound:
		skip.Reason, skip.Detail = SkipNotFound, "The job no longer exists."
	}
	return skip
}

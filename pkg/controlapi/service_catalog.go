package controlapi

import (
	"context"

	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/flowstep"
)

// ListLibraries reports the libraries the daemon manages, straight from the
// store, with the flow each one runs and how its files and jobs stand.
func (s Service) ListLibraries(ctx context.Context) (control.LibraryListResponse, error) {
	if err := s.requireStore(); err != nil {
		return control.LibraryListResponse{}, err
	}
	libraries, err := s.Store.ListLibraries(ctx)
	if err != nil {
		return control.LibraryListResponse{}, err
	}
	overviews, err := s.Store.LibraryOverviews(ctx)
	if err != nil {
		return control.LibraryListResponse{}, err
	}
	flows, err := s.Store.FlowNames(ctx)
	if err != nil {
		return control.LibraryListResponse{}, err
	}
	response := control.LibraryListResponse{
		APIVersion: control.Version, ServerTime: s.now(),
		Libraries: make([]control.LibraryEntry, 0, len(libraries)),
	}
	for _, library := range libraries {
		overview := overviews[library.Name]
		entry := control.LibraryEntry{
			Name:             library.Name,
			Reprocess:        library.Reprocess,
			Path:             library.Path,
			FlowID:           library.FlowID,
			FlowName:         flows[library.FlowID],
			Paused:           library.Paused,
			Priority:         library.Priority,
			ConcurrencyLimit: library.ConcurrencyLimit,
			Arr:              library.Arr,
			Files:            overview.Files,
			TotalBytes:       overview.TotalBytes,
			Jobs:             make(map[string]int64, len(overview.Jobs)),
			SavedBytes:       overview.SavedBytes,
			LastScanAt:       overview.LastScanAt,
		}
		for state, count := range overview.Jobs {
			entry.Jobs[string(state)] = count
		}
		response.Libraries = append(response.Libraries, entry)
	}
	return response, nil
}

// ListFlows reports the stored flows, each with the libraries it is assigned
// to and how many validation issues it has.
func (s Service) ListFlows(ctx context.Context) (control.FlowListResponse, error) {
	if err := s.requireStore(); err != nil {
		return control.FlowListResponse{}, err
	}
	flows, err := s.Store.ListFlows(ctx)
	if err != nil {
		return control.FlowListResponse{}, err
	}
	usage, err := s.Store.FlowLibraries(ctx)
	if err != nil {
		return control.FlowListResponse{}, err
	}
	response := control.FlowListResponse{
		APIVersion: control.Version, ServerTime: s.now(),
		Flows: make([]control.FlowEntry, 0, len(flows)),
	}
	for _, stored := range flows {
		entry := control.FlowEntry{
			ID:          stored.ID,
			Name:        stored.Name,
			Description: stored.Description,
			Revision:    stored.Revision,
			NodeCount:   len(stored.Graph.Nodes),
			Libraries:   usage[stored.ID],
			CreatedAt:   stored.CreatedAt,
			UpdatedAt:   stored.UpdatedAt,
		}
		if entry.Libraries == nil {
			entry.Libraries = []string{}
		}
		for _, issue := range flow.Validate(stored.Graph) {
			if issue.Severity == flow.SeverityError {
				entry.Errors++
			} else {
				entry.Warnings++
			}
		}
		response.Flows = append(response.Flows, entry)
	}
	return response, nil
}

// latestFlowResolution returns how the flow resolved on the most recent
// attempt that recorded it. An attempt that failed before the flow step, such
// as one whose probe failed, records none, and the one before it still says
// what the flow made of the file.
func latestFlowResolution(attempts []domain.Attempt, events map[domain.AttemptID][]domain.AttemptEvent) *control.JobFlowDetail {
	for index := len(attempts) - 1; index >= 0; index-- {
		attempt := attempts[index]
		for _, event := range events[attempt.ID] {
			if event.Type != domain.AttemptEventArtifact || event.Name != flowstep.ArtifactName {
				continue
			}
			resolution, err := flowstep.DecodeResolution(event.Payload)
			if err != nil {
				// The event itself is still listed under its attempt with its
				// raw payload; there is just nothing to summarize from it.
				continue
			}
			return &control.JobFlowDetail{
				AttemptID:     int64(attempt.ID),
				AttemptNumber: attempt.Number,
				FlowID:        resolution.FlowID,
				FlowName:      resolution.FlowName,
				Revision:      resolution.Revision,
				Outcome:       resolution.Outcome,
				Path:          orEmpty(resolution.Path),
				Decisions:     orEmpty(resolution.Decisions),
				Plan:          orEmpty(resolution.Plan),
			}
		}
	}
	return nil
}

func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

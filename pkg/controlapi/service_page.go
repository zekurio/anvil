package controlapi

import (
	"context"
	"strings"

	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/store"
)

// JobPageQuery selects one page of the queue for a front end that pages,
// which the web UI does and the control listing does not.
type JobPageQuery struct {
	Library string
	States  []domain.JobState
	// Search keeps jobs whose source or asset path contains it.
	Search string
	// Limit must be positive; Offset skips that many jobs.
	Limit  int
	Offset int
}

// JobPageItem is one listed job together with the sizes the control listing
// leaves out.
type JobPageItem struct {
	Job             control.JobResponse
	InputSizeBytes  int64
	OutputSizeBytes int64
}

// JobPage lists one page of jobs, newest activity first, and counts every job
// the query matches. It builds each row the way ListJobs does, so both front
// doors describe a job identically, but only for the rows of the page.
func (s Service) JobPage(ctx context.Context, query JobPageQuery) ([]JobPageItem, int64, error) {
	if err := s.requireStore(); err != nil {
		return nil, 0, err
	}
	if query.Limit <= 0 {
		return nil, 0, invalidArgumentf("limit must be positive")
	}
	if query.Offset < 0 {
		return nil, 0, invalidArgumentf("offset must be non-negative")
	}
	cfg := s.runtimeConfig()
	library := strings.TrimSpace(query.Library)
	if err := requireConfiguredLibrary(cfg, library); err != nil {
		return nil, 0, err
	}
	filter := store.JobListFilter{
		LibraryName:         domain.LibraryName(library),
		States:              query.States,
		Query:               query.Search,
		NewestActivityFirst: true,
		Limit:               query.Limit,
		Offset:              query.Offset,
	}
	total, err := s.Store.CountJobs(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	summaries, err := s.Store.ListJobs(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	items := make([]JobPageItem, 0, len(summaries))
	for _, summary := range summaries {
		response, _, _, err := s.jobResponse(ctx, cfg, summary)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, JobPageItem{
			Job:             response,
			InputSizeBytes:  summary.Job.InputSizeBytes,
			OutputSizeBytes: summary.Job.OutputSizeBytes,
		})
	}
	return items, total, nil
}

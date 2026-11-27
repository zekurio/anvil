package controlapi

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/zekurio/anvil/pkg/config"

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
	Job control.JobResponse
	// Path is the media file relative to the library root, slash-separated.
	// A package job's asset lives below its source directory; Path joins the
	// two, which neither Job.Source nor Job.Asset does on its own.
	Path            string
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
		item, err := s.jobPageItem(ctx, cfg, summary)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, nil
}

// JobItem describes one job the way JobPage lists it.
func (s Service) JobItem(ctx context.Context, id domain.JobID) (JobPageItem, error) {
	if err := s.requireStore(); err != nil {
		return JobPageItem{}, err
	}
	summary, err := s.Store.GetJobSummary(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return JobPageItem{}, notFoundf("no job has the id %d", id)
	}
	if err != nil {
		return JobPageItem{}, err
	}
	return s.jobPageItem(ctx, s.runtimeConfig(), summary)
}

func (s Service) jobPageItem(ctx context.Context, cfg config.Config, summary store.JobSummary) (JobPageItem, error) {
	response, _, _, err := s.jobResponse(ctx, cfg, summary)
	if err != nil {
		return JobPageItem{}, err
	}
	return JobPageItem{
		Job:             response,
		Path:            filepath.ToSlash(jobPath(summary)),
		InputSizeBytes:  summary.Job.InputSizeBytes,
		OutputSizeBytes: summary.Job.OutputSizeBytes,
	}, nil
}

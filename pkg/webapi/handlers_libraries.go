package webapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/store"
)

// libraryName is what a new library may be called. The name is part of
// URLs, job slugs, and log lines, so it stays plain.
var libraryName = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// maxRequeuePaths bounds one requeue request.
const maxRequeuePaths = 1000

func (s *Server) listLibraries(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	stored, err := s.Store.ListLibraries(r.Context())
	if err != nil {
		return fail(err)
	}
	overviews, err := s.Store.LibraryOverviews(r.Context())
	if err != nil {
		return fail(err)
	}
	flows, err := s.Store.FlowNames(r.Context())
	if err != nil {
		return fail(err)
	}
	list := LibraryList{Libraries: make([]Library, 0, len(stored))}
	for _, library := range stored {
		list.Libraries = append(list.Libraries, libraryOf(library, flows[library.FlowID], overviews[library.Name]))
	}
	return ok(list)
}

func libraryOf(stored store.Library, flowName string, overview store.LibraryOverview) Library {
	return Library{
		LibraryWrite: libraryWrite(stored.LibraryConfig),
		FlowName:     flowName,
		Stats:        libraryStats(overview),
		LastScanAt:   overview.LastScanAt,
		CreatedAt:    stored.CreatedAt,
		UpdatedAt:    stored.UpdatedAt,
	}
}

// libraryResponse describes one stored library with its flow name and stats.
func (s *Server) libraryResponse(ctx context.Context, stored store.Library) (Library, error) {
	overviews, err := s.Store.LibraryOverviews(ctx)
	if err != nil {
		return Library{}, err
	}
	flows, err := s.Store.FlowNames(ctx)
	if err != nil {
		return Library{}, err
	}
	return libraryOf(stored, flows[stored.FlowID], overviews[stored.Name]), nil
}

func (s *Server) storedLibrary(ctx context.Context, name string) (store.Library, error) {
	stored, err := s.Store.GetLibrary(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Library{}, notFoundf("There is no library %q.", name)
	}
	return stored, err
}

func (s *Server) getLibrary(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	stored, err := s.storedLibrary(r.Context(), r.PathValue("name"))
	if err != nil {
		return fail(err)
	}
	response, err := s.libraryResponse(r.Context(), stored)
	if err != nil {
		return fail(err)
	}
	return ok(response)
}

// fieldProblem turns the first problem of a list into the request error.
func fieldProblem(problems []config.FieldProblem) error {
	if len(problems) == 0 {
		return nil
	}
	return invalidf(problems[0].Field, "%s", sentence(problems[0].Message))
}

// checkLibrary applies every rule a library has to meet before it is stored,
// in the order an operator would fix them: its own settings, its place among
// the other libraries, and what it refers to. current is the stored library on
// an update and nil on a create.
func (s *Server) checkLibrary(ctx context.Context, library config.LibraryConfig, current *store.Library) error {
	if err := fieldProblem(library.Problems()); err != nil {
		return err
	}
	// The directories are compared against the stored libraries, not the
	// runtime snapshot: the store is what this write is about to change. An
	// update that leaves both directories alone is not checked again, so a
	// library can always be paused or given another flow.
	moved := current == nil || current.Path != library.Path || current.Download.HandoffPath != library.Download.HandoffPath
	if moved {
		stored, err := s.Store.ListLibraries(ctx)
		if err != nil {
			return err
		}
		others := make(map[string]config.LibraryConfig, len(stored))
		for _, other := range stored {
			others[other.Name] = other.LibraryConfig
		}
		if err := fieldProblem(config.LibraryPathConflicts(others, library)); err != nil {
			return err
		}
	}
	assigned, err := s.Store.GetFlow(ctx, library.FlowID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return invalidf("flow_id", "There is no flow %d. Choose one of the existing flows.", library.FlowID)
	case err != nil:
		return err
	}
	// A flow with errors can only come from the one-time import of a legacy
	// profile. Every job of a library using it would fail, so it has to be
	// fixed before it is assigned anywhere new.
	if problems := flowIssues(assigned.Graph); problems != "" && (current == nil || current.FlowID != library.FlowID) {
		return invalidf("flow_id", "Flow %q has problems and cannot be assigned until they are fixed: %s", assigned.Name, problems)
	}
	if library.Arr != "" {
		if _, err := s.Store.GetArr(ctx, library.Arr); errors.Is(err, store.ErrNotFound) {
			return invalidf("arr", "There is no Arr connection %q.", library.Arr)
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) createLibrary(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	var write LibraryWrite
	if err := decodeBody(r, &write); err != nil {
		return fail(err)
	}
	if !libraryName.MatchString(write.Name) {
		return fail(invalidf("name", "A library name has 1 to 64 characters out of a-z, 0-9, \"-\", and \"_\"."))
	}
	library, err := libraryConfig(write)
	if err != nil {
		return fail(err)
	}
	s.mutations.Lock()
	defer s.mutations.Unlock()
	if _, err := s.Store.GetLibrary(r.Context(), library.Name); err == nil {
		return fail(conflictf("A library named %q already exists.", library.Name))
	} else if !errors.Is(err, store.ErrNotFound) {
		return fail(err)
	}
	if err := s.checkLibrary(r.Context(), library, nil); err != nil {
		return fail(err)
	}
	stored, err := s.Store.CreateLibrary(r.Context(), library, s.now())
	if err != nil {
		return fail(err)
	}
	if err := s.refresh(r.Context()); err != nil {
		return fail(err)
	}
	response, err := s.libraryResponse(r.Context(), stored)
	if err != nil {
		return fail(err)
	}
	return created(response)
}

func (s *Server) updateLibrary(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	name := r.PathValue("name")
	var write LibraryWrite
	if err := decodeBody(r, &write); err != nil {
		return fail(err)
	}
	if write.Name != name {
		return fail(invalidf("name", "The name in the request (%q) must be the library the URL names (%q). A library cannot be renamed.", write.Name, name))
	}
	library, err := libraryConfig(write)
	if err != nil {
		return fail(err)
	}
	s.mutations.Lock()
	defer s.mutations.Unlock()
	current, err := s.storedLibrary(r.Context(), name)
	if err != nil {
		return fail(err)
	}
	if library.Kind != current.Kind {
		return fail(invalidf("kind", "Library %q is a %s library, and the kind cannot change. Create a new library instead.", name, current.Kind))
	}
	if err := s.checkLibrary(r.Context(), library, &current); err != nil {
		return fail(err)
	}
	stored, err := s.Store.UpdateLibrary(r.Context(), library, s.now())
	if err != nil {
		return fail(err)
	}
	if err := s.refresh(r.Context()); err != nil {
		return fail(err)
	}
	response, err := s.libraryResponse(r.Context(), stored)
	if err != nil {
		return fail(err)
	}
	return ok(response)
}

func (s *Server) deleteLibrary(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	name := r.PathValue("name")
	s.mutations.Lock()
	defer s.mutations.Unlock()
	if _, err := s.storedLibrary(r.Context(), name); err != nil {
		return fail(err)
	}
	// The store refuses while the library has queued or running jobs or an
	// unfinished publish, and says which; classify turns that into a 409.
	if err := s.Store.DeleteLibrary(r.Context(), name); err != nil {
		return fail(err)
	}
	if err := s.refresh(r.Context()); err != nil {
		return fail(err)
	}
	return noContent()
}

func (s *Server) scanLibrary(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	name := r.PathValue("name")
	if _, err := s.storedLibrary(r.Context(), name); err != nil {
		return fail(err)
	}
	scanned, err := s.Jobs.ScanLibraries(r.Context(), control.LibraryScanRequest{Library: name})
	if err != nil {
		return fail(err)
	}
	return ok(ScanResult{
		Sources:         scanned.Sources,
		Assets:          scanned.Assets,
		EnqueuedJobs:    scanned.EnqueuedJobs,
		ExistingJobs:    scanned.ExistingJobs,
		SkippedIgnored:  scanned.SkippedIgnored,
		SkippedUnstable: scanned.SkippedUnstable,
	})
}

func (s *Server) listFiles(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	library, err := s.storedLibrary(r.Context(), r.PathValue("name"))
	if err != nil {
		return fail(err)
	}
	query := r.URL.Query()
	filter := store.LibraryFileFilter{
		LibraryName: domain.LibraryName(library.Name),
		Status:      store.FileStatus(strings.TrimSpace(query.Get("status"))),
		Query:       strings.TrimSpace(query.Get("q")),
		Sort:        store.LibraryFileSort(strings.TrimSpace(query.Get("sort"))),
	}
	if filter.Status != "" && !store.ValidFileStatus(filter.Status) {
		return fail(invalidf("status", "status must be one of none, queued, running, done, failed, or skipped."))
	}
	switch filter.Sort {
	case "":
		filter.Sort = store.LibraryFileSortPath
	case store.LibraryFileSortPath, store.LibraryFileSortSize, store.LibraryFileSortStatus, store.LibraryFileSortUpdated:
	default:
		return fail(invalidf("sort", "sort must be one of path, size, status, or updated."))
	}
	switch strings.TrimSpace(query.Get("dir")) {
	case "", "asc":
	case "desc":
		filter.Descending = true
	default:
		return fail(invalidf("dir", "dir must be asc or desc."))
	}
	if filter.Limit, filter.Offset, err = pageParams(r); err != nil {
		return fail(err)
	}

	files, total, err := s.Store.ListLibraryFiles(r.Context(), filter)
	if err != nil {
		return fail(err)
	}
	list := FileList{Total: total, Files: make([]File, 0, len(files))}
	for _, file := range files {
		item := File{
			SourceID:        int64(file.SourceID),
			AssetID:         int64(file.AssetID),
			Path:            file.Path,
			AbsolutePath:    filepath.Join(library.Path, filepath.FromSlash(file.Path)),
			SizeBytes:       file.SizeBytes,
			Status:          string(file.Status),
			JobID:           int64(file.JobID),
			JobState:        string(file.JobState),
			LastError:       file.LastError,
			OutputSizeBytes: file.OutputSizeBytes,
			UpdatedAt:       file.UpdatedAt,
		}
		if file.Probe != nil {
			item.Container = containerLabel(*file.Probe)
			item.Resolution = flow.Describe(*file.Probe).Resolution
			item.Audio = audioLabel(*file.Probe)
			if video, found := domain.PrimaryVideoStream(file.Probe.Streams); found {
				item.VideoCodec = video.Codec
			}
		}
		if file.Status == store.FileStatusRunning {
			item.Progress = s.jobProgress(int64(file.JobID))
		}
		list.Files = append(list.Files, item)
	}
	return ok(list)
}

// requeueFiles forces a fresh job for each named file, also for files that
// were already processed. Every path goes through the scan rules again, so a
// file the rules deny, one still being written, and one with a job in flight
// are each left alone and reported.
func (s *Server) requeueFiles(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	name := r.PathValue("name")
	var request RequeueRequest
	if err := decodeBody(r, &request); err != nil {
		return fail(err)
	}
	switch {
	case len(request.Paths) == 0:
		return fail(invalidf("paths", "Name at least one file to requeue."))
	case len(request.Paths) > maxRequeuePaths:
		return fail(invalidf("paths", "At most %d files can be requeued in one request.", maxRequeuePaths))
	}
	if _, err := s.storedLibrary(r.Context(), name); err != nil {
		return fail(err)
	}
	forced, err := s.Jobs.ForceOccurrences(r.Context(), name, request.Paths)
	if err != nil {
		return fail(err)
	}
	result := JobActionResult{Matched: len(forced), Skipped: []JobActionSkip{}}
	for _, occurrence := range forced {
		if occurrence.Err == nil {
			result.Changed++
			continue
		}
		// A file that was not requeued has no new job, so the entry carries no
		// job ID and names the file in its reason.
		result.Skipped = append(result.Skipped, JobActionSkip{
			Reason: fmt.Sprintf("%s: %s", occurrence.Path, classify(r.Context(), occurrence.Err).message),
		})
	}
	return ok(result)
}

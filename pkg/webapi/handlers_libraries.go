package webapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/controlapi"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/staging"
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
	if roots := s.Config().Daemon.MediaRoots; len(roots) > 0 {
		// A folder the update leaves alone is not checked again. The one-time
		// import of anvil.toml may store a library outside the media roots,
		// and it must stay possible to pause it or give it another flow.
		kept := config.LibraryConfig{}
		if current != nil {
			kept = current.LibraryConfig
		}
		for _, candidate := range []struct{ field, path, stored string }{
			{"path", library.Path, kept.Path},
			{"download.handoff_path", library.Download.HandoffPath, kept.Download.HandoffPath},
		} {
			if candidate.path == "" || candidate.path == candidate.stored {
				continue
			}
			allowed, err := pathWithinMediaRoots(candidate.path, roots)
			if err != nil {
				return invalidf(candidate.field, "The path cannot be resolved inside an administrator-declared media root: %v.", err)
			}
			if !allowed {
				return invalidf(candidate.field, "The path must be inside one of the administrator-declared media roots.")
			}
		}
	}
	// The directories are compared against the stored libraries, not the
	// runtime snapshot: the store is what this write is about to change. An
	// update that leaves both directories alone is not checked again, so a
	// library can always be paused or given another flow. Resuming is the
	// exception: the one-time import of anvil.toml pauses a library whose
	// directories conflict, and it must not run again until they are fixed.
	moved := current == nil || current.Path != library.Path || current.Download.HandoffPath != library.Download.HandoffPath
	resumed := current != nil && current.Paused && !library.Paused
	if moved || resumed {
		stored, err := s.Store.ListLibraries(ctx)
		if err != nil {
			return err
		}
		others := make(map[string]config.LibraryConfig, len(stored))
		for _, other := range stored {
			others[other.Name] = other.LibraryConfig
		}
		if err := fieldProblem(config.LibraryPathConflicts(s.Config().Daemon.TempDir, others, library)); err != nil {
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
		return fail(nameTakenf("A library named %q already exists. Choose another name.", library.Name))
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
	// Jobs that only wait go with the library, and so does a job whose
	// publish stopped in conflict. The store refuses while a worker holds one
	// of its jobs or an interrupted publish still has to be resumed, and says
	// which. Whether a worker still runs an attempt is asked inside the
	// delete's transaction, so no attempt can start between the answer and
	// the delete; the same transaction reads where the deleted jobs staged,
	// so a job that stages or fails meanwhile cannot be missed either.
	deleted, err := s.Store.DeleteLibrary(r.Context(), name, func() []domain.JobID {
		return s.Jobs.AttemptsInFlight(name)
	})
	var busy *store.LibraryBusyError
	switch {
	case errors.As(err, &busy):
		return fail(conflictf("%s", libraryBusy(busy)))
	case err != nil:
		return fail(err)
	}
	if err := s.refresh(r.Context()); err != nil {
		return fail(err)
	}
	// What the deleted jobs left on disk goes last and cannot fail the
	// request: the library is gone either way. It also has to happen whether
	// or not the client is still there, because once the rows are deleted
	// nothing else knows which part files were theirs.
	s.Jobs.RemoveParts(context.WithoutCancel(r.Context()), deleted)
	// Scratch under the temp dir is only disk space the staging cleanup also
	// knows how to reclaim.
	scratch := make([]staging.Job, 0, len(deleted))
	for _, job := range deleted {
		scratch = append(scratch, staging.Job{ID: int64(job.ID), Slug: job.Slug})
	}
	if err := staging.RemoveJobScratch(s.Config().Daemon.TempDir, scratch); err != nil {
		slog.Warn("scratch of a deleted library could not be removed", "library", name, "jobs", len(deleted), "error", err)
	}
	return noContent()
}

// libraryBusy says why a library cannot be deleted right now, and what to do.
func libraryBusy(busy *store.LibraryBusyError) string {
	count := func(n int64, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	var reasons []string
	if busy.Running > 0 {
		reasons = append(reasons, count(busy.Running, "job is running. Cancel it or wait for it.", "jobs are running. Cancel them or wait for them."))
	}
	if busy.Publishing > 0 {
		reasons = append(reasons, count(busy.Publishing,
			"job was interrupted while publishing. Retry it if it failed, and resume the library if it is paused.",
			"jobs were interrupted while publishing. Retry the failed ones, and resume the library if it is paused."))
	}
	return fmt.Sprintf("The library %q cannot be deleted yet. %s", busy.Library, strings.Join(reasons, " "))
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
		DeniedJobs:      scanned.DeniedJobs,
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
		return fail(invalidf("sort", "sort must be one of path, size, status, or updated_at."))
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
			InputSizeBytes:  file.InputSizeBytes,
			OutputSizeBytes: file.OutputSizeBytes,
			UpdatedAt:       file.UpdatedAt,
		}
		probe := listedProbe(file.Path, file.Probe)
		item.Container = containerLabel(probe)
		item.Resolution = flow.Describe(probe).Resolution
		item.Audio = audioLabel(probe)
		item.VideoCodec = file.Probe.VideoCodec
		list.Files = append(list.Files, item)
	}
	if err := s.attachProgress(r.Context(), library, list.Files); err != nil {
		return fail(err)
	}
	return ok(list)
}

// attachProgress gives every running file of a page its live progress.
func (s *Server) attachProgress(ctx context.Context, library store.Library, files []File) error {
	var running []domain.JobID
	for _, file := range files {
		if file.Status == FileStatusRunning {
			running = append(running, domain.JobID(file.JobID))
		}
	}
	if len(running) == 0 {
		return nil
	}
	attempts, err := s.Store.LatestAttempts(ctx, running)
	if err != nil {
		return err
	}
	for index, file := range files {
		if file.Status != FileStatusRunning {
			continue
		}
		progress := s.progressOf(file.JobID, library.Name, file.Path, attempts[domain.JobID(file.JobID)].FlowName, file.UpdatedAt)
		files[index].Progress = &progress
	}
	return nil
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
		// A file that was not requeued has no new job, so the entry names the
		// file and not a job.
		result.Skipped = append(result.Skipped, JobActionSkip{
			Path: occurrence.Path, Reason: requeueSkipReason(occurrence.Refusal), Detail: classify(r.Context(), occurrence.Err).message,
		})
	}
	return ok(result)
}

// requeueSkipReason puts the job service's refusal into the contract's
// vocabulary. A failure that is not a refusal is the daemon's own.
func requeueSkipReason(refusal controlapi.ForceRefusal) string {
	switch refusal {
	case controlapi.ForceRefusedNotFound:
		return SkipNotFound
	case controlapi.ForceRefusedDenied:
		return SkipDenied
	case controlapi.ForceRefusedIgnored:
		return SkipIgnored
	case controlapi.ForceRefusedUnstable:
		return SkipUnstable
	case controlapi.ForceRefusedActive:
		return SkipActive
	case controlapi.ForceRefusedInvalid:
		return SkipInvalid
	default:
		return SkipError
	}
}

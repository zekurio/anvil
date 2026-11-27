package webapi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/store"
)

func (s *Server) session(r *http.Request) Session {
	required := s.auth.required()
	return Session{AuthRequired: required, Authenticated: !required || s.auth.authenticated(r, s.now())}
}

func (s *Server) getSession(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	return ok(s.session(r))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) (int, any, error) {
	var request LoginRequest
	if err := decodeBody(r, &request); err != nil {
		return fail(err)
	}
	if !s.auth.required() {
		return ok(Session{Authenticated: true})
	}
	session, wait, valid := s.auth.login(clientKey(r), request.Password, s.now())
	if !valid {
		failure := &apiError{status: http.StatusUnauthorized, code: CodeUnauthenticated, field: "password", message: "That password is not right."}
		if wait > 0 {
			seconds := int(wait.Seconds()) + 1
			failure.status = http.StatusTooManyRequests
			failure.message = fmt.Sprintf("Too many failed sign-ins. Try again in %d seconds.", seconds)
		}
		return fail(failure)
	}
	// The cookie is set through the request's own response writer by the
	// wrapper below; a handler only returns a body.
	http.SetCookie(w, sessionCookieFor(r, session))
	return ok(Session{AuthRequired: true, Authenticated: true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) (int, any, error) {
	s.auth.logout(r)
	http.SetCookie(w, sessionCookieFor(r, ""))
	return noContent()
}

func (s *Server) status(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	status, err := s.Jobs.Status(r.Context())
	if err != nil {
		return fail(err)
	}
	flows, err := s.Store.FlowNames(r.Context())
	if err != nil {
		return fail(err)
	}
	running, err := s.running(r.Context())
	if err != nil {
		return fail(err)
	}
	return ok(Status{
		Version:    status.Daemon.Version,
		ServerTime: status.ServerTime,
		StartedAt:  status.Daemon.StartedAt,
		Workers:    Workers{Configured: status.Workers.Configured, Active: status.Workers.Active},
		Queue:      status.Queue,
		Running:    running,
		Libraries:  len(s.Config().Libraries),
		Flows:      len(flows),
	})
}

// running lists every running job with its live progress, oldest job first.
// The store decides which jobs run: the progress tracker only knows a job
// once it has reported, and keeps one a moment after it stopped.
func (s *Server) running(ctx context.Context) ([]Progress, error) {
	jobs, err := s.Store.ListJobs(ctx, store.JobListFilter{States: store.JobStatesOf(store.FileStatusRunning)})
	if err != nil {
		return nil, err
	}
	ids := make([]domain.JobID, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.Job.ID)
	}
	attempts, err := s.Store.LatestAttempts(ctx, ids)
	if err != nil {
		return nil, err
	}
	running := make([]Progress, 0, len(jobs))
	for _, job := range jobs {
		running = append(running, s.progressOf(int64(job.Job.ID), string(job.Job.LibraryName), jobPath(job), attempts[job.Job.ID].FlowName, job.Job.UpdatedAt))
	}
	slices.SortFunc(running, func(left, right Progress) int { return cmp.Compare(left.JobID, right.JobID) })
	return running, nil
}

// jobPath is the library-relative path of a job's media file, as File.Path:
// a package's asset lives below its source directory.
func jobPath(job store.JobSummary) string {
	if job.SourceKind == domain.SourceKindPackage && job.AssetPath != "" {
		return job.SourcePath + "/" + job.AssetPath
	}
	return job.SourcePath
}

// progressOf describes one running job: which job it is, and its latest
// progress report. Without a report the step is empty and nothing is known
// yet; updatedAt, the job's own, then says since when.
func (s *Server) progressOf(id int64, library, path, flowName string, updatedAt time.Time) Progress {
	progress := Progress{
		JobID: id, Library: library, Path: path, FlowName: flowName,
		Percent: -1, ETASeconds: -1, UpdatedAt: updatedAt.UTC(),
	}
	if s.Progress == nil {
		return progress
	}
	reported, found := s.Progress.Snapshot(domain.JobID(id))
	if !found {
		return progress
	}
	progress.Step = reported.Step
	progress.Percent, progress.FPS, progress.Speed, progress.ETASeconds = reported.Percent, reported.FPS, reported.Speed, reported.ETASeconds
	progress.UpdatedAt = reported.UpdatedAt.UTC()
	return progress
}

func (s *Server) settings(http.ResponseWriter, *http.Request) (int, any, error) {
	cfg := s.Config()
	return ok(Settings{
		Version:    s.Version,
		ConfigPath: s.ConfigPath,
		Daemon: DaemonSettings{
			MediaRoots:        slices.Clone(cfg.Daemon.MediaRoots),
			TempDir:           cfg.Daemon.TempDir,
			StorePath:         cfg.Daemon.StorePath,
			ControlSocket:     cfg.Daemon.ControlSocket,
			WorkerCount:       cfg.Daemon.WorkerCount,
			TotalThreads:      cfg.Daemon.TotalThreads,
			MaxThreadsPerJob:  cfg.Daemon.MaxThreadsPerJob,
			MaxAttempts:       cfg.Daemon.MaxAttempts,
			ScanInterval:      durationText(cfg.Daemon.ScanInterval.Duration),
			LeaseDuration:     durationText(cfg.Daemon.LeaseDuration.Duration),
			ShutdownPolicy:    cfg.Daemon.ShutdownPolicy,
			StagingCleanupAge: durationText(cfg.Daemon.StagingCleanupAge.Duration),
			LogLevel:          cfg.Daemon.LogLevel,
		},
		Web: WebSettings{Listen: cfg.Web.Listen, AuthRequired: s.auth.required()},
	})
}

func (s *Server) nodeTypes(http.ResponseWriter, *http.Request) (int, any, error) {
	return ok(NodeTypes{NodeTypes: flow.Catalog()})
}

// maxDirectoryEntries bounds one directory listing. A folder picker has no
// use for more, and a directory with a million files must not become a
// response of a hundred megabytes.
const maxDirectoryEntries = 2000

// listDirectory lists one directory for the folder picker: its
// subdirectories, and with files=1 also the media files in it. Hidden entries
// are left out.
func (s *Server) listDirectory(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	requested := r.URL.Query().Get("path")
	if strings.TrimSpace(requested) == "" {
		requested = string(filepath.Separator)
	}
	if !filepath.IsAbs(requested) {
		return fail(invalidf("path", "The path must be absolute, such as /srv/media."))
	}
	directory := filepath.Clean(requested)
	mediaRoots := s.Config().Daemon.MediaRoots
	if len(mediaRoots) > 0 {
		allowed, err := pathWithinMediaRoots(directory, mediaRoots)
		if err != nil || !allowed {
			return ok(mediaRootListing(mediaRoots))
		}
	}
	withFiles := false
	if text := r.URL.Query().Get("files"); text != "" {
		parsed, err := strconv.ParseBool(text)
		if err != nil {
			return fail(invalidf("files", "files must be 1 or 0."))
		}
		withFiles = parsed
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		return fail(invalidf("path", "%s", directoryProblem(directory, err)))
	}
	listing := Directory{Path: directory, Entries: []DirEntry{}}
	if parent := filepath.Dir(directory); parent != directory {
		if len(mediaRoots) == 0 {
			listing.Parent = parent
		} else {
			allowed, err := pathWithinMediaRoots(parent, mediaRoots)
			if err == nil && allowed {
				listing.Parent = parent
			}
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(directory, name)
		if len(mediaRoots) > 0 {
			allowed, err := pathWithinMediaRoots(full, mediaRoots)
			if err != nil || !allowed {
				continue
			}
		}
		// Stat follows links: a linked media directory is a directory to the
		// operator. An entry that cannot be read is simply not offered.
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		switch {
		case info.IsDir():
			listing.Entries = append(listing.Entries, DirEntry{Name: name, Path: full, Dir: true})
		case withFiles && info.Mode().IsRegular() && scanner.LikelyMediaFile(name):
			listing.Entries = append(listing.Entries, DirEntry{Name: name, Path: full, SizeBytes: info.Size()})
		}
		if len(listing.Entries) >= maxDirectoryEntries {
			break
		}
	}
	slices.SortFunc(listing.Entries, func(left, right DirEntry) int {
		return cmp.Or(cmp.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name)), cmp.Compare(left.Name, right.Name))
	})
	return ok(listing)
}

func mediaRootListing(roots []string) Directory {
	listing := Directory{Entries: make([]DirEntry, 0, len(roots))}
	for _, root := range roots {
		listing.Entries = append(listing.Entries, DirEntry{Name: filepath.Base(root), Path: root, Dir: true})
	}
	slices.SortFunc(listing.Entries, func(left, right DirEntry) int {
		return cmp.Or(cmp.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name)), cmp.Compare(left.Name, right.Name))
	})
	return listing
}

func pathWithinMediaRoots(path string, roots []string) (bool, error) {
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, err
	}
	for _, root := range roots {
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(resolvedRoot, resolvedPath)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true, nil
		}
	}
	return false, nil
}

// directoryProblem says in plain words why a directory cannot be listed.
func directoryProblem(directory string, err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("%s does not exist.", directory)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Sprintf("The daemon is not allowed to read %s.", directory)
	case errors.Is(err, syscall.ENOTDIR):
		return fmt.Sprintf("%s is a file, not a directory.", directory)
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return sentence(fmt.Sprintf("%s cannot be read: %v", directory, pathErr.Err))
	}
	return sentence(fmt.Sprintf("%s cannot be read: %v", directory, err))
}

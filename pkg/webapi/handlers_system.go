package webapi

import (
	"cmp"
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

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/scanner"
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
	return ok(Status{
		Version:    status.Daemon.Version,
		ServerTime: status.ServerTime,
		StartedAt:  status.Daemon.StartedAt,
		Workers:    Workers{Configured: status.Workers.Configured, Active: status.Workers.Active},
		Queue:      status.Queue,
		Running:    s.running(),
		Libraries:  len(s.Config().Libraries),
		Flows:      len(flows),
	})
}

// running lists the live progress of every running job, by job ID.
func (s *Server) running() []Progress {
	running := []Progress{}
	if s.Progress == nil {
		return running
	}
	for _, progress := range s.Progress.All() {
		running = append(running, progressOf(progress))
	}
	slices.SortFunc(running, func(left, right Progress) int { return cmp.Compare(left.JobID, right.JobID) })
	return running
}

// jobProgress returns the live progress of one job, or nil when it reports
// none.
func (s *Server) jobProgress(id int64) *Progress {
	if s.Progress == nil {
		return nil
	}
	progress, found := s.Progress.Snapshot(domain.JobID(id))
	if !found {
		return nil
	}
	converted := progressOf(progress)
	return &converted
}

func progressOf(progress domain.JobProgress) Progress {
	return Progress{
		JobID:      int64(progress.JobID),
		Step:       progress.Step,
		Percent:    progress.Percent,
		FPS:        progress.FPS,
		Speed:      progress.Speed,
		ETASeconds: progress.ETASeconds,
		UpdatedAt:  progress.UpdatedAt.UTC(),
	}
}

func (s *Server) settings(http.ResponseWriter, *http.Request) (int, any, error) {
	cfg := s.Config()
	return ok(Settings{
		Version:    s.Version,
		ConfigPath: s.ConfigPath,
		Daemon: DaemonSettings{
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
		listing.Parent = parent
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(directory, name)
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

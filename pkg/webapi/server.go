package webapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/controlapi"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/probe"
	"github.com/zekurio/anvil/pkg/store"
)

// Store is what the web API needs from persistence beyond what it asks of the
// job service: flows, libraries, and Arr connections, which only it edits,
// and the read side of files and jobs.
type Store interface {
	ListFlows(context.Context) ([]store.Flow, error)
	GetFlow(context.Context, int64) (store.Flow, error)
	CreateFlow(context.Context, store.FlowInput, time.Time) (store.Flow, error)
	UpdateFlow(context.Context, int64, store.FlowInput, int, time.Time) (store.Flow, error)
	DeleteFlow(context.Context, int64) error
	FlowNames(context.Context) (map[int64]string, error)
	FlowLibraries(context.Context) (map[int64][]string, error)

	ListLibraries(context.Context) ([]store.Library, error)
	GetLibrary(context.Context, string) (store.Library, error)
	CreateLibrary(context.Context, config.LibraryConfig, time.Time) (store.Library, error)
	UpdateLibrary(context.Context, config.LibraryConfig, time.Time) (store.Library, error)
	DeleteLibrary(context.Context, string) ([]store.DeletedJob, error)
	LibraryOverviews(context.Context) (map[string]store.LibraryOverview, error)
	ListLibraryFiles(context.Context, store.LibraryFileFilter) ([]store.LibraryFile, int64, error)

	ListArrs(context.Context) ([]store.Arr, error)
	GetArr(context.Context, string) (store.Arr, error)
	CreateArr(context.Context, config.ArrConfig, time.Time) (store.Arr, error)
	UpdateArr(context.Context, config.ArrConfig, time.Time) (store.Arr, error)
	DeleteArr(context.Context, string) error
	ArrLibraries(context.Context) (map[string][]string, error)

	ListJobs(context.Context, store.JobListFilter) ([]store.JobSummary, error)
	GetJob(context.Context, domain.JobID) (domain.Job, error)
	ListAttemptsForJob(context.Context, domain.JobID) ([]domain.Attempt, error)
	LatestAttempts(context.Context, []domain.JobID) (map[domain.JobID]store.LatestAttempt, error)
	GetJobPipelineContext(context.Context, domain.JobID) (domain.JobPipelineContext, bool, error)
}

// Jobs is the daemon's job service, the same one the control socket serves.
// The web API goes through it for everything that touches a job or scans a
// library, so both front doors apply the same rules.
type Jobs interface {
	Status(context.Context) (control.StatusResponse, error)
	JobPage(context.Context, controlapi.JobPageQuery) ([]controlapi.JobPageItem, int64, error)
	JobItem(context.Context, domain.JobID) (controlapi.JobPageItem, error)
	ShowJob(context.Context, control.JobShowRequest) (control.JobShowResponse, error)
	CancelJobs(context.Context, control.JobCancelRequest) (control.JobCancelResponse, error)
	RetryJobs(context.Context, control.JobRetryRequest) (control.JobRetryResponse, error)
	ScanLibraries(context.Context, control.LibraryScanRequest) (control.LibraryScanResponse, error)
	ForceOccurrences(ctx context.Context, library string, paths []string) ([]controlapi.ForcedOccurrence, error)
}

// ProgressSource is the live progress of running jobs.
type ProgressSource interface {
	Snapshot(domain.JobID) (domain.JobProgress, bool)
	All() []domain.JobProgress
}

// Prober probes one media file. The flow dry run uses it.
type Prober interface {
	Probe(ctx context.Context, path string) (domain.ProbeResult, error)
}

// Server serves the web UI and its JSON API. Fill the dependency fields, set
// the password with SetPassword, and call Serve.
type Server struct {
	Store Store
	Jobs  Jobs
	// Config returns the daemon's runtime snapshot.
	Config func() config.Config
	// Refresh re-reads libraries and Arr connections from the store into the
	// runtime snapshot and wakes the scan monitor. Every change to either is
	// persisted, then refreshed, and only then answered.
	Refresh func(context.Context) error
	// Progress may be nil: no job then reports live progress.
	Progress ProgressSource
	// Prober defaults to ffprobe.
	Prober Prober
	// UI is the built frontend rooted at its index.html; UIBuilt says whether
	// a build is actually there.
	UI      fs.FS
	UIBuilt bool
	// ConfigPath is shown on the settings page.
	ConfigPath string
	Version    string
	Now        func() time.Time
	// ProbeTimeout bounds the probe of a flow dry run; zero uses a minute.
	ProbeTimeout time.Duration

	auth auth
	// mutations serializes changes to flows, libraries, and Arr connections,
	// so validating against the current state and then writing is one step.
	mutations sync.Mutex
}

// SetPassword installs the shared password; an empty one turns sign-in off.
// Changing it signs everyone out.
func (s *Server) SetPassword(password string) {
	s.auth.setPassword(password)
}

// Serve answers requests on listener until ctx is canceled, then shuts down
// gracefully. It returns nil for that orderly shutdown.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Minute,
		// A manual scan of a large library answers when it is done, which takes
		// minutes. The limit only has to end a response nobody reads any more.
		WriteTimeout:   15 * time.Minute,
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: 64 << 10,
		BaseContext:    func(net.Listener) context.Context { return ctx },
		ErrorLog:       slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		// The shutdown outlives the cancellation that asked for it: in-flight
		// answers get a moment to leave, and whatever is left is cut off.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close() //nolint:errcheck // the listener is going away either way
		}
	}()
	err := server.Serve(listener)
	if ctx.Err() != nil {
		<-stopped
		return nil
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Handler returns the HTTP handler: the API under /api/v1 and the UI
// everywhere else.
func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	route := func(pattern string, h handler) {
		api.Handle(pattern, serve(h))
	}
	route("GET /api/v1/session", s.getSession)
	route("POST /api/v1/session", s.login)
	route("DELETE /api/v1/session", s.logout)

	route("GET /api/v1/status", s.status)
	route("GET /api/v1/settings", s.settings)
	route("GET /api/v1/fs", s.listDirectory)

	route("GET /api/v1/node-types", s.nodeTypes)
	route("GET /api/v1/flows", s.listFlows)
	route("POST /api/v1/flows", s.createFlow)
	route("POST /api/v1/flows/check", s.checkFlow)
	route("POST /api/v1/flows/test", s.testFlow)
	route("GET /api/v1/flows/{id}", s.getFlow)
	route("PUT /api/v1/flows/{id}", s.updateFlow)
	route("DELETE /api/v1/flows/{id}", s.deleteFlow)
	route("POST /api/v1/flows/{id}/duplicate", s.duplicateFlow)

	route("GET /api/v1/libraries", s.listLibraries)
	route("POST /api/v1/libraries", s.createLibrary)
	route("GET /api/v1/libraries/{name}", s.getLibrary)
	route("PUT /api/v1/libraries/{name}", s.updateLibrary)
	route("DELETE /api/v1/libraries/{name}", s.deleteLibrary)
	route("POST /api/v1/libraries/{name}/scan", s.scanLibrary)
	route("GET /api/v1/libraries/{name}/files", s.listFiles)
	route("POST /api/v1/libraries/{name}/requeue", s.requeueFiles)

	route("GET /api/v1/jobs", s.listJobs)
	route("GET /api/v1/jobs/{id}", s.getJob)
	route("POST /api/v1/jobs/cancel", s.cancelJobs)
	route("POST /api/v1/jobs/retry", s.retryJobs)

	route("GET /api/v1/arrs", s.listArrs)
	route("POST /api/v1/arrs", s.createArr)
	route("PUT /api/v1/arrs/{name}", s.updateArr)
	route("DELETE /api/v1/arrs/{name}", s.deleteArr)

	// Everything else under /api/ is answered in JSON, never with the UI: a
	// misspelled route must not come back as a page of HTML with status 200.
	// The pattern has no method, so it also catches a known path asked for
	// with a method it does not have.
	missing := serve(func(_ http.ResponseWriter, r *http.Request) (int, any, error) {
		return fail(notFoundf("There is no API route %s %s.", r.Method, r.URL.Path))
	})
	api.Handle("/api/", missing)

	index := s.indexPage()
	ui := s.uiHandler(index)
	guarded, guardedMissing := s.guardAPI(api), s.guardAPI(missing)
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api":
			// The mux would redirect this to /api/; a client of the API has
			// no use for a redirect to a route that does not exist either.
			guardedMissing.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/"):
			guarded.ServeHTTP(w, r)
		default:
			ui.ServeHTTP(w, r)
		}
	})
	return s.recoverPanics(securityHeaders(contentSecurityPolicy(index), s.logRequests(s.guardHost(root))))
}

// recoverPanics turns a panic in a handler into a 500 and one error log. The
// daemon keeps running: a bug in a page must not take the encodes down.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}
			slog.Error("web handler panicked", "method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: Error{
				Code: CodeInternal, Message: "The daemon hit an internal error. Its log has the details.",
			}})
		}()
		next.ServeHTTP(w, r)
	})
}

// inlineScript finds scripts written into the page itself, as opposed to
// loaded from a file.
var inlineScript = regexp.MustCompile(`(?is)<script(\s[^>]*)?>(.*?)</script>`)

// contentSecurityPolicy allows what the UI is built from and nothing else:
// scripts, styles, fonts, and images from this origin, and requests back to
// it. Inline style attributes are allowed because the UI positions things
// with them; inline style elements and inline scripts are not. The one
// exception is a script the served index page itself carries, such as the
// theme switch that has to run before first paint: it is allowed by the hash
// of its exact text, so anything injected next to it still is not.
func contentSecurityPolicy(index []byte) string {
	scripts := "'self'"
	for _, match := range inlineScript.FindAllSubmatch(index, -1) {
		if strings.Contains(strings.ToLower(string(match[1])), "src=") || len(match[2]) == 0 {
			continue
		}
		sum := sha256.Sum256(match[2])
		scripts += " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + scripts,
		"style-src 'self'",
		"style-src-attr 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

func securityHeaders(policy string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Content-Security-Policy", policy)
		next.ServeHTTP(w, r)
	})
}

// statusRecorder notes the status a handler answered with, for the log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !slog.Default().Enabled(r.Context(), slog.LevelDebug) {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		slog.Debug("web request", "method", r.Method, "path", r.URL.Path, "status", recorder.status, "duration", time.Since(started))
	})
}

// guardHost defends a daemon without a password against DNS rebinding. A
// page on another site can make a browser resolve its own host name to this
// machine and then talk to the daemon as "same origin"; the one thing it
// cannot fake is the Host header, which still names the attacker's host. So
// without a password a request has to name the daemon by a loopback name or
// address, by the host it was told to listen on, or by the very IP address
// the connection arrived at. With a password the check is not needed: the
// attacker's origin has no session cookie.
func (s *Server) guardHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.required() && !s.trustedHost(r) {
			writeError(w, r, &apiError{
				status: http.StatusForbidden, code: CodeInvalid,
				message: fmt.Sprintf("This daemon has no password, so it only answers requests addressed to it by a loopback name, its listen host, or its own IP address, not %q. Set web.password_file to reach it under another name.", r.Host),
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) trustedHost(r *http.Request) bool {
	host := hostOnly(r.Host)
	if config.LoopbackHost(host) {
		return true
	}
	if listen := s.Config().Web.ListenHost(); listen != "" && strings.EqualFold(host, listen) {
		return true
	}
	// An IP literal that is the address this connection came in on cannot be
	// a rebinding: rebinding needs a host name the attacker controls.
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return false
	}
	localHost, _, err := net.SplitHostPort(local.String())
	if err != nil {
		return false
	}
	localIP := net.ParseIP(localHost)
	return localIP != nil && localIP.Equal(ip)
}

// hostOnly strips the port and IPv6 brackets from a Host header value.
func hostOnly(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}

// guardAPI is the browser-facing protection of the API, applied before any
// route runs. This API replaces and deletes media files, so a request has to
// show three things: that it comes from this origin (the cross-site request
// forgery defence), that a mutating request was built by a script on purpose
// (Content-Type: application/json, which no HTML form can send), and, with a
// password set, that it carries a session.
func (s *Server) guardAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reason := crossOrigin(r); reason != "" {
			writeError(w, r, &apiError{status: http.StatusForbidden, code: CodeInvalid, message: reason})
			return
		}
		if mutating(r.Method) && !jsonContentType(r) {
			writeError(w, r, &apiError{
				status: http.StatusUnsupportedMediaType, code: CodeInvalid,
				message: fmt.Sprintf("%s requests need the header Content-Type: application/json.", r.Method),
			})
			return
		}
		if r.URL.Path != "/api/v1/session" && !s.auth.authenticated(r, s.now()) {
			writeError(w, r, &apiError{status: http.StatusUnauthorized, code: CodeUnauthenticated, message: "Sign in first."})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mutating(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// crossOrigin says why a request does not come from the daemon's own origin,
// or returns "" when it does. Browsers attach Origin to every request that
// can change something and Sec-Fetch-Site to all of them; a client that sends
// neither is not a browser being steered by another site.
func crossOrigin(r *http.Request) string {
	if site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); site != "" && site != "same-origin" && site != "none" {
		return "Requests from other sites are refused."
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return ""
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return "Requests from other sites are refused."
	}
	// Behind a reverse proxy the browser's origin is the proxy's public name,
	// which the proxy passes on in X-Forwarded-Host when it rewrites Host. A
	// page on another site cannot set that header without a preflight this
	// server never answers.
	for _, own := range []string{r.Host, r.Header.Get("X-Forwarded-Host")} {
		if own = strings.TrimSpace(own); own != "" && strings.EqualFold(parsed.Host, own) {
			return ""
		}
	}
	return fmt.Sprintf("Requests from another origin (%s) are refused.", origin)
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Server) prober() Prober {
	if s.Prober != nil {
		return s.Prober
	}
	return probe.FFProbe{}
}

// refresh brings the runtime snapshot and the scan monitor up to date after
// a change was persisted.
func (s *Server) refresh(ctx context.Context) error {
	if s.Refresh == nil {
		return nil
	}
	if err := s.Refresh(ctx); err != nil {
		return fmt.Errorf("the change was saved, but the running daemon could not load it: %w", err)
	}
	return nil
}

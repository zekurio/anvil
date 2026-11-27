package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/controlapi"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/store"
)

const testHost = "127.0.0.1:8484"

// fixture is a web server over a real store in a temporary directory, the
// real job service with the real scanner, and a runtime snapshot that is
// refreshed from the store the way the daemon's is.
type fixture struct {
	t       *testing.T
	state   *store.SQLiteStore
	server  *Server
	handler http.Handler
	// media is a directory a library can be rooted at.
	media string
	// codec is the video codec the fake probe reports.
	codec string
	// cookie is sent with every request once set.
	cookie *http.Cookie

	mu  sync.Mutex
	cfg config.Config
	now time.Time
}

func newFixture(t *testing.T, password string, ui fs.FS) *fixture {
	t.Helper()
	state, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "anvil.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	media, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, state: state, media: media, codec: "h264", cfg: config.Default(), now: time.Now().UTC()}
	f.cfg.Web.Listen = testHost
	f.cfg.Daemon.TempDir = t.TempDir()
	f.server = &Server{
		Store: state,
		Jobs: controlapi.Service{
			Store: state, Scanner: scanner.Scanner{Store: state}, Config: f.config, StartedAt: f.now, Now: f.clock,
		},
		Config:     f.config,
		Refresh:    f.refresh,
		Prober:     f,
		UI:         ui,
		UIBuilt:    ui != nil,
		ConfigPath: "/etc/anvil/anvil.toml",
		Version:    "test",
		Now:        f.clock,
	}
	f.server.SetPassword(password)
	f.handler = f.server.Handler()
	return f
}

func (f *fixture) config() config.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) advance(by time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(by)
}

func (f *fixture) refresh(ctx context.Context) error {
	libraries, err := f.state.ListLibraries(ctx)
	if err != nil {
		return err
	}
	arrs, err := f.state.ListArrs(ctx)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg.Libraries = make(map[string]config.LibraryConfig, len(libraries))
	for _, library := range libraries {
		f.cfg.Libraries[library.Name] = library.LibraryConfig
	}
	f.cfg.Arrs = make(map[string]config.ArrConfig, len(arrs))
	for _, arr := range arrs {
		f.cfg.Arrs[arr.Name] = arr.ArrConfig
	}
	return nil
}

// Probe stands in for ffprobe: a 1080p file with one stereo English track.
func (f *fixture) Probe(_ context.Context, path string) (domain.ProbeResult, error) {
	return domain.ProbeResult{
		Path: path, FormatName: "matroska,webm", SizeBytes: 6_000_000, DurationSeconds: 60,
		Streams: []domain.MediaStream{
			{Index: 0, Type: "video", Codec: f.codec, Width: 1920, Height: 1080, PixelFormat: "yuv420p"},
			{Index: 1, Type: "audio", Codec: "aac", Channels: 2, Language: "eng", Disposition: map[string]bool{"default": true}},
			{Index: 2, Type: "subtitle", Codec: "subrip", Language: "eng"},
		},
	}, nil
}

// writeMedia creates a file below the media directory that a scan accepts as
// settled.
func (f *fixture) writeMedia(relative string) string {
	f.t.Helper()
	full := filepath.Join(f.media, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, bytes.Repeat([]byte("anvil"), 1000), 0o644); err != nil {
		f.t.Fatal(err)
	}
	settled := time.Now().Add(-time.Hour)
	if err := os.Chtimes(full, settled, settled); err != nil {
		f.t.Fatal(err)
	}
	return full
}

// call describes one request. The zero value of everything but method and
// path is what the UI sends: this origin's Host, and a JSON content type on
// every method that changes something.
type call struct {
	method string
	path   string
	// body is sent as JSON. A string is sent as it is.
	body   any
	host   string
	header map[string]string
	// plain leaves the JSON content type off a mutating request.
	plain bool
}

func (f *fixture) do(c call) *httptest.ResponseRecorder {
	f.t.Helper()
	var body bytes.Buffer
	switch value := c.body.(type) {
	case nil:
	case string:
		body.WriteString(value)
	default:
		if err := json.NewEncoder(&body).Encode(value); err != nil {
			f.t.Fatal(err)
		}
	}
	request := httptest.NewRequest(c.method, c.path, &body)
	request.Host = testHost
	if c.host != "" {
		request.Host = c.host
	}
	if mutating(c.method) && !c.plain {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range c.header {
		request.Header.Set(name, value)
	}
	if f.cookie != nil {
		request.AddCookie(f.cookie)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

func (f *fixture) get(path string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.do(call{method: http.MethodGet, path: path})
}

func (f *fixture) send(method, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.do(call{method: method, path: path, body: body})
}

// want checks the status of a JSON answer and decodes it.
func want[T any](t *testing.T, recorder *httptest.ResponseRecorder, status int) T {
	t.Helper()
	var value T
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", recorder.Code, status, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON; body: %s", contentType, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %T: %v; body: %s", value, err, recorder.Body.String())
	}
	return value
}

// wantError checks that an answer is an ErrorResponse with the given status
// and code, and that it has a message.
func wantError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) Error {
	t.Helper()
	response := want[ErrorResponse](t, recorder, status)
	if response.Error.Code != code {
		t.Fatalf("error code = %q, want %q; body: %s", response.Error.Code, code, recorder.Body.String())
	}
	if strings.TrimSpace(response.Error.Message) == "" {
		t.Fatalf("error has no message; body: %s", recorder.Body.String())
	}
	return response.Error
}

func wantNoContent(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 {
		t.Fatalf("status = %d with %d body bytes, want 204 and none; body: %s", recorder.Code, recorder.Body.Len(), recorder.Body.String())
	}
}

// skipHEVC leaves HEVC files alone and encodes everything else at a fixed
// CRF, keeping the original-language audio.
func skipHEVC() flow.Graph {
	return flow.Graph{
		Nodes: []flow.Node{
			{ID: "in", Type: "input"},
			{ID: "is-hevc", Type: "check.video_codec", Label: "Video is hevc?", Options: map[string]any{"codecs": []string{"hevc"}}},
			{ID: "skip", Type: "output.skip"},
			{ID: "encode", Type: "video.encode", Options: map[string]any{"quality_mode": "fixed", "crf": 30, "preset": "10"}},
			{ID: "audio", Type: "audio.keep", Options: map[string]any{"languages": []string{"orig"}}},
			{ID: "publish", Type: "output.publish"},
		},
		Edges: []flow.Edge{
			{From: "in", To: "is-hevc"},
			{From: "is-hevc", Port: 0, To: "skip"},
			{From: "is-hevc", Port: 1, To: "encode"},
			{From: "encode", To: "audio"},
			{From: "audio", To: "publish"},
		},
	}
}

// defaultFlow returns the flow a fresh store is seeded with.
func (f *fixture) defaultFlow() FlowSummary {
	f.t.Helper()
	flows := want[FlowList](f.t, f.get("/api/v1/flows"), http.StatusOK)
	if len(flows.Flows) != 1 || flows.Flows[0].Name != store.DefaultFlowName {
		f.t.Fatalf("flows = %+v, want only %q", flows.Flows, store.DefaultFlowName)
	}
	return flows.Flows[0]
}

// mediaLibrary creates a media library rooted at the fixture's directory.
func (f *fixture) mediaLibrary(name string, flowID int64) Library {
	f.t.Helper()
	return want[Library](f.t, f.send(http.MethodPost, "/api/v1/libraries", LibraryWrite{
		Name: name, Kind: "media", Path: f.media, FlowID: flowID,
	}), http.StatusCreated)
}

func pointer[T any](value T) *T {
	return &value
}

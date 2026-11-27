package webapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zekurio/anvil/pkg/flow"
)

func TestPasswordGuardsEveryRouteButTheSession(t *testing.T) {
	f := newFixture(t, "open sesame", nil)

	session := want[Session](t, f.get("/api/v1/session"), http.StatusOK)
	if !session.AuthRequired || session.Authenticated {
		t.Fatalf("session before sign-in = %+v", session)
	}
	for _, c := range []call{
		{method: http.MethodGet, path: "/api/v1/status"},
		{method: http.MethodGet, path: "/api/v1/settings"},
		{method: http.MethodGet, path: "/api/v1/fs?path=/"},
		{method: http.MethodGet, path: "/api/v1/flows"},
		{method: http.MethodPost, path: "/api/v1/flows", body: FlowWrite{Name: "x", Graph: skipHEVC()}},
		{method: http.MethodDelete, path: "/api/v1/libraries/movies"},
		{method: http.MethodPost, path: "/api/v1/jobs/cancel", body: JobActionRequest{States: []string{"pending"}}},
		{method: http.MethodGet, path: "/api/v1/arrs"},
		{method: http.MethodGet, path: "/api/v1/unknown"},
	} {
		wantError(t, f.do(c), http.StatusUnauthorized, CodeUnauthenticated)
	}

	wrong := wantError(t, f.send(http.MethodPost, "/api/v1/session", LoginRequest{Password: "open says me"}), http.StatusUnauthorized, CodeUnauthenticated)
	if wrong.Field != "password" {
		t.Fatalf("wrong password = %+v, want the password field named", wrong)
	}

	signedIn := f.do(call{method: http.MethodPost, path: "/api/v1/session", body: LoginRequest{Password: "open sesame"}})
	if session = want[Session](t, signedIn, http.StatusOK); !session.Authenticated || !session.AuthRequired {
		t.Fatalf("session after sign-in = %+v", session)
	}
	cookies := signedIn.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v, want the session cookie", cookies)
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Secure || len(cookie.Value) < 43 {
		t.Fatalf("session cookie = %+v, want HttpOnly, SameSite=Strict, Path=/, and 32 random bytes", cookie)
	}

	f.cookie = cookie
	want[Status](t, f.get("/api/v1/status"), http.StatusOK)
	if session = want[Session](t, f.get("/api/v1/session"), http.StatusOK); !session.Authenticated {
		t.Fatalf("session with the cookie = %+v", session)
	}

	signedOut := f.send(http.MethodDelete, "/api/v1/session", nil)
	wantNoContent(t, signedOut)
	if cleared := signedOut.Result().Cookies(); len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Fatalf("sign-out cookies = %+v, want the session cookie cleared", cleared)
	}
	// The session is gone on the server, not just in the browser.
	wantError(t, f.get("/api/v1/status"), http.StatusUnauthorized, CodeUnauthenticated)
}

func TestSessionCookieIsSecureBehindHTTPS(t *testing.T) {
	f := newFixture(t, "open sesame", nil)
	signedIn := f.do(call{
		method: http.MethodPost, path: "/api/v1/session", body: LoginRequest{Password: "open sesame"},
		header: map[string]string{"X-Forwarded-Proto": "https"},
	})
	want[Session](t, signedIn, http.StatusOK)
	if cookies := signedIn.Result().Cookies(); len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("cookies = %+v, want a Secure session cookie", cookies)
	}
}

func TestSessionsExpireAndEndWithThePassword(t *testing.T) {
	f := newFixture(t, "open sesame", nil)
	signIn := func(password string) *http.Cookie {
		t.Helper()
		f.cookie = nil
		recorder := f.send(http.MethodPost, "/api/v1/session", LoginRequest{Password: password})
		want[Session](t, recorder, http.StatusOK)
		return recorder.Result().Cookies()[0]
	}

	f.cookie = signIn("open sesame")
	want[Status](t, f.get("/api/v1/status"), http.StatusOK)
	f.advance(sessionLifetime + time.Minute)
	wantError(t, f.get("/api/v1/status"), http.StatusUnauthorized, CodeUnauthenticated)

	f.cookie = signIn("open sesame")
	want[Status](t, f.get("/api/v1/status"), http.StatusOK)
	// A reload that keeps the password keeps the sessions; a new password
	// ends them, and the old one no longer signs in.
	f.server.SetPassword("open sesame")
	want[Status](t, f.get("/api/v1/status"), http.StatusOK)
	f.server.SetPassword("new password")
	wantError(t, f.get("/api/v1/status"), http.StatusUnauthorized, CodeUnauthenticated)
	f.cookie = nil
	wantError(t, f.send(http.MethodPost, "/api/v1/session", LoginRequest{Password: "open sesame"}), http.StatusUnauthorized, CodeUnauthenticated)
	f.cookie = signIn("new password")
	want[Status](t, f.get("/api/v1/status"), http.StatusOK)
}

func TestFailedSignInsAreThrottled(t *testing.T) {
	f := newFixture(t, "open sesame", nil)
	for attempt := 1; attempt < freeAttempts; attempt++ {
		wantError(t, f.send(http.MethodPost, "/api/v1/session", LoginRequest{Password: "guess"}), http.StatusUnauthorized, CodeUnauthenticated)
	}
	// The failure that uses up the allowance already answers with the wait.
	wantError(t, f.send(http.MethodPost, "/api/v1/session", LoginRequest{Password: "guess"}), http.StatusTooManyRequests, CodeUnauthenticated)
	// While locked out, the right password is not even looked at.
	wantError(t, f.send(http.MethodPost, "/api/v1/session", LoginRequest{Password: "open sesame"}), http.StatusTooManyRequests, CodeUnauthenticated)
	f.advance(firstLockout + time.Second)
	want[Session](t, f.send(http.MethodPost, "/api/v1/session", LoginRequest{Password: "open sesame"}), http.StatusOK)
}

func TestRequestsFromOtherOriginsAreRefused(t *testing.T) {
	f := newFixture(t, "", nil)
	flowBody := FlowWrite{Name: "x", Graph: skipHEVC()}
	for name, header := range map[string]map[string]string{
		"another origin":             {"Origin": "http://evil.example"},
		"same host, another port":    {"Origin": "http://127.0.0.1:9000"},
		"null origin":                {"Origin": "null"},
		"cross-site fetch":           {"Sec-Fetch-Site": "cross-site"},
		"same-site fetch":            {"Sec-Fetch-Site": "same-site"},
		"own origin, but cross-site": {"Origin": "http://" + testHost, "Sec-Fetch-Site": "cross-site"},
	} {
		refused := wantError(t, f.do(call{method: http.MethodPost, path: "/api/v1/flows", body: flowBody, header: header}), http.StatusForbidden, CodeInvalid)
		if !strings.Contains(refused.Message, "refused") {
			t.Errorf("%s: %+v", name, refused)
		}
		wantError(t, f.do(call{method: http.MethodGet, path: "/api/v1/flows", header: header}), http.StatusForbidden, CodeInvalid)
	}
	if flows := f.defaultFlow(); flows.ID == 0 {
		t.Fatal("a refused request created a flow")
	}

	for name, header := range map[string]map[string]string{
		"own origin":         {"Origin": "http://" + testHost, "Sec-Fetch-Site": "same-origin"},
		"typed into the bar": {"Sec-Fetch-Site": "none"},
		"behind a proxy":     {"Origin": "https://anvil.example", "X-Forwarded-Host": "anvil.example"},
		"not a browser":      {},
	} {
		if recorder := f.do(call{method: http.MethodGet, path: "/api/v1/flows", header: header}); recorder.Code != http.StatusOK {
			t.Errorf("%s: status = %d, body: %s", name, recorder.Code, recorder.Body.String())
		}
	}
}

func TestMutationsNeedAJSONContentType(t *testing.T) {
	f := newFixture(t, "", nil)
	// A form post is what another site can make a browser send without a
	// preflight; none of its content types is accepted.
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		header := map[string]string{}
		if contentType != "" {
			header["Content-Type"] = contentType
		}
		for _, c := range []call{
			{method: http.MethodPost, path: "/api/v1/flows", body: `{"name":"x"}`},
			{method: http.MethodPut, path: "/api/v1/libraries/movies", body: `{}`},
			{method: http.MethodDelete, path: "/api/v1/flows/1"},
			{method: http.MethodPost, path: "/api/v1/libraries/movies/scan"},
			{method: http.MethodPost, path: "/api/v1/session", body: `{"password":"x"}`},
		} {
			c.plain, c.header = true, header
			wantError(t, f.do(c), http.StatusUnsupportedMediaType, CodeInvalid)
		}
	}
	f.defaultFlow()
	if recorder := f.do(call{
		method: http.MethodPost, path: "/api/v1/flows/check", body: FlowCheckRequest{Graph: skipHEVC()},
		plain: true, header: map[string]string{"Content-Type": "application/json; charset=utf-8"},
	}); recorder.Code != http.StatusOK {
		t.Fatalf("a JSON content type with a charset = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestHostHeaderIsCheckedWithoutAPassword(t *testing.T) {
	f := newFixture(t, "", fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>anvil</title>")}})
	for _, host := range []string{"evil.example", "evil.example:8484", "192.168.1.20:8484", "127.0.0.1.evil.example:8484"} {
		refused := wantError(t, f.do(call{method: http.MethodGet, path: "/api/v1/status", host: host}), http.StatusForbidden, CodeInvalid)
		if !strings.Contains(refused.Message, "web.password_file") {
			t.Errorf("host %s: %+v, want the message to say what to set", host, refused)
		}
		// The UI is refused as well: a rebound page must not even load.
		if recorder := f.do(call{method: http.MethodGet, path: "/", host: host}); recorder.Code != http.StatusForbidden {
			t.Errorf("host %s: UI status = %d, want 403", host, recorder.Code)
		}
	}
	for _, host := range []string{"127.0.0.1:8484", "localhost:8484", "LOCALHOST:8484", "[::1]:8484", "localhost", "anvil.localhost:8484"} {
		if recorder := f.do(call{method: http.MethodGet, path: "/api/v1/status", host: host}); recorder.Code != http.StatusOK {
			t.Errorf("host %s: status = %d, body: %s", host, recorder.Code, recorder.Body.String())
		}
	}

	// A daemon told to listen on a name answers to that name.
	f.mu.Lock()
	f.cfg.Web.Listen = "anvil.lan:8484"
	f.mu.Unlock()
	if recorder := f.do(call{method: http.MethodGet, path: "/api/v1/status", host: "anvil.lan:8484"}); recorder.Code != http.StatusOK {
		t.Errorf("listen host: status = %d, body: %s", recorder.Code, recorder.Body.String())
	}

	// With a password the session cookie is the defence, and any name works.
	f = newFixture(t, "open sesame", nil)
	wantError(t, f.do(call{method: http.MethodGet, path: "/api/v1/status", host: "nas.example"}), http.StatusUnauthorized, CodeUnauthenticated)
}

func TestErrorShapes(t *testing.T) {
	f := newFixture(t, "", nil)
	seeded := f.defaultFlow()
	f.writeMedia("film.mkv")
	f.mediaLibrary("movies", seeded.ID)

	// Routes that do not exist answer in JSON, also for a known path asked
	// for with a method it does not have.
	wantError(t, f.get("/api/v1/nope"), http.StatusNotFound, CodeNotFound)
	wantError(t, f.get("/api/nope"), http.StatusNotFound, CodeNotFound)
	wantError(t, f.get("/api"), http.StatusNotFound, CodeNotFound)
	wantError(t, f.send(http.MethodPatch, "/api/v1/flows/1", `{}`), http.StatusNotFound, CodeNotFound)
	wantError(t, f.send(http.MethodPut, "/api/v1/status", `{}`), http.StatusNotFound, CodeNotFound)

	// Missing things.
	wantError(t, f.get("/api/v1/flows/999"), http.StatusNotFound, CodeNotFound)
	wantError(t, f.get("/api/v1/flows/abc"), http.StatusNotFound, CodeNotFound)
	wantError(t, f.get("/api/v1/libraries/ghost"), http.StatusNotFound, CodeNotFound)
	wantError(t, f.get("/api/v1/libraries/ghost/files"), http.StatusNotFound, CodeNotFound)
	wantError(t, f.send(http.MethodPost, "/api/v1/libraries/ghost/scan", nil), http.StatusNotFound, CodeNotFound)
	wantError(t, f.send(http.MethodDelete, "/api/v1/libraries/ghost", nil), http.StatusNotFound, CodeNotFound)
	wantError(t, f.get("/api/v1/jobs/999"), http.StatusNotFound, CodeNotFound)
	wantError(t, f.get("/api/v1/jobs?library=ghost"), http.StatusNotFound, CodeNotFound)
	wantError(t, f.send(http.MethodDelete, "/api/v1/arrs/ghost", nil), http.StatusNotFound, CodeNotFound)
	wantError(t, f.send(http.MethodDelete, "/api/v1/flows/999", nil), http.StatusNotFound, CodeNotFound)

	// Malformed requests.
	wantError(t, f.send(http.MethodPost, "/api/v1/flows", `{"name":"x","graph":{"nodes":[],"edges":[]},"colour":"red"}`), http.StatusBadRequest, CodeInvalid)
	wantError(t, f.send(http.MethodPost, "/api/v1/flows", `{"name":`), http.StatusBadRequest, CodeInvalid)
	wantError(t, f.send(http.MethodPost, "/api/v1/flows", nil), http.StatusBadRequest, CodeInvalid)
	wantError(t, f.send(http.MethodPost, "/api/v1/flows", `{"name":"x"} {"name":"y"}`), http.StatusBadRequest, CodeInvalid)
	wantError(t, f.send(http.MethodPost, "/api/v1/flows", fmt.Sprintf(`{"name":%q}`, strings.Repeat("x", maxBodyBytes))), http.StatusRequestEntityTooLarge, CodeInvalid)
	wantError(t, f.get("/api/v1/jobs?limit=-1"), http.StatusBadRequest, CodeInvalid)
	wantError(t, f.get("/api/v1/jobs?state=sleeping"), http.StatusBadRequest, CodeInvalid)
	wantError(t, f.get("/api/v1/libraries/movies/files?status=odd"), http.StatusBadRequest, CodeInvalid)
	wantError(t, f.get("/api/v1/libraries/movies/files?sort=colour"), http.StatusBadRequest, CodeInvalid)
	wantError(t, f.get("/api/v1/fs?path=relative"), http.StatusBadRequest, CodeInvalid)
	wantError(t, f.get("/api/v1/fs?path="+filepath.Join(f.media, "gone")), http.StatusBadRequest, CodeInvalid)
	wantError(t, f.send(http.MethodPost, "/api/v1/jobs/cancel", JobActionRequest{}), http.StatusBadRequest, CodeInvalid)
	wantError(t, f.send(http.MethodPost, "/api/v1/jobs/retry", JobActionRequest{States: []string{"running"}}), http.StatusBadRequest, CodeInvalid)
	wantError(t, f.send(http.MethodPost, "/api/v1/libraries/movies/requeue", RequeueRequest{}), http.StatusBadRequest, CodeInvalid)

	// A flow with error issues is a 422 that lists them.
	broken := flow.Graph{Nodes: []flow.Node{{ID: "in", Type: "input"}}, Edges: []flow.Edge{}}
	invalid := wantError(t, f.send(http.MethodPost, "/api/v1/flows", FlowWrite{Name: "Broken", Graph: broken}), http.StatusUnprocessableEntity, CodeInvalidFlow)
	if len(invalid.Issues) == 0 {
		t.Fatalf("invalid flow = %+v, want its issues", invalid)
	}
	if named := wantError(t, f.send(http.MethodPost, "/api/v1/flows", FlowWrite{Name: "  ", Graph: skipHEVC()}), http.StatusBadRequest, CodeInvalid); named.Field != "name" {
		t.Fatalf("flow without a name = %+v", named)
	}

	// Conflicts: a taken name, a stale revision.
	wantError(t, f.send(http.MethodPost, "/api/v1/flows", FlowWrite{Name: seeded.Name, Graph: skipHEVC()}), http.StatusConflict, CodeConflict)
	flowPath := fmt.Sprintf("/api/v1/flows/%d", seeded.ID)
	want[Flow](t, f.send(http.MethodPut, flowPath, FlowWrite{Name: seeded.Name, Graph: skipHEVC(), Revision: seeded.Revision}), http.StatusOK)
	stale := wantError(t, f.send(http.MethodPut, flowPath, FlowWrite{Name: seeded.Name, Graph: skipHEVC(), Revision: seeded.Revision}), http.StatusConflict, CodeConflict)
	if !strings.Contains(stale.Message, "Reload") {
		t.Fatalf("stale revision = %+v, want it to say what to do", stale)
	}

	// Library validation names the field at fault.
	library := func(change func(*LibraryWrite)) LibraryWrite {
		write := LibraryWrite{Name: "shows", Kind: "media", Path: filepath.Join(filepath.Dir(f.media), "shows"), FlowID: seeded.ID}
		change(&write)
		return write
	}
	for field, write := range map[string]LibraryWrite{
		"name":              library(func(w *LibraryWrite) { w.Name = "TV Shows" }),
		"kind":              library(func(w *LibraryWrite) { w.Kind = "archive" }),
		"path":              library(func(w *LibraryWrite) { w.Path = "relative/path" }),
		"flow_id":           library(func(w *LibraryWrite) { w.FlowID = 999 }),
		"arr":               library(func(w *LibraryWrite) { w.Arr = "ghost" }),
		"scan_interval":     library(func(w *LibraryWrite) { w.ScanInterval = "soon" }),
		"concurrency_limit": library(func(w *LibraryWrite) { w.ConcurrencyLimit = -1 }),
		"rule_fallback":     library(func(w *LibraryWrite) { w.RuleFallback = "maybe" }),
		"rules[0].value":    library(func(w *LibraryWrite) { w.Rules = []Rule{{Action: "deny", Field: "size", Op: "lt", Value: "200"}} }),
		"rules[1].op": library(func(w *LibraryWrite) {
			w.Rules = []Rule{{Action: "allow", Field: "path", Op: "matches", Value: "**"}, {Action: "deny", Field: "size", Op: "is", Value: "1 GB"}}
		}),
		"media.replacement_mode": library(func(w *LibraryWrite) { w.Media.ReplacementMode = "shred" }),
		"download.handoff_path":  library(func(w *LibraryWrite) { w.Kind = "download" }),
	} {
		refused := wantError(t, f.send(http.MethodPost, "/api/v1/libraries", write), http.StatusBadRequest, CodeInvalid)
		if refused.Field != field {
			t.Errorf("library with a bad %s: field = %q, message %q", field, refused.Field, refused.Message)
		}
	}

	// Library roots must not contain each other, in either direction, and a
	// handoff directory must stay out of every root.
	for field, write := range map[string]LibraryWrite{
		"path": library(func(w *LibraryWrite) { w.Path = filepath.Join(f.media, "nested") }),
		"download.handoff_path": library(func(w *LibraryWrite) {
			w.Kind, w.Download.HandoffPath = "download", filepath.Join(f.media, "import")
		}),
	} {
		refused := wantError(t, f.send(http.MethodPost, "/api/v1/libraries", write), http.StatusBadRequest, CodeInvalid)
		if refused.Field != field || !strings.Contains(refused.Message, "movies") {
			t.Errorf("overlapping %s: %+v, want it to name the other library", field, refused)
		}
	}
	if parent := wantError(t, f.send(http.MethodPost, "/api/v1/libraries", library(func(w *LibraryWrite) { w.Path = filepath.Dir(f.media) })), http.StatusBadRequest, CodeInvalid); parent.Field != "path" {
		t.Errorf("a root above another library: %+v", parent)
	}
	wantError(t, f.send(http.MethodPost, "/api/v1/libraries", LibraryWrite{Name: "movies", Kind: "media", Path: "/srv/other", FlowID: seeded.ID}), http.StatusConflict, CodeConflict)

	// An update cannot rename a library or change its kind.
	current := want[Library](t, f.get("/api/v1/libraries/movies"), http.StatusOK).LibraryWrite
	renamed := current
	renamed.Name = "films"
	if refused := wantError(t, f.send(http.MethodPut, "/api/v1/libraries/movies", renamed), http.StatusBadRequest, CodeInvalid); refused.Field != "name" {
		t.Errorf("rename = %+v", refused)
	}
	rekinded := current
	rekinded.Kind, rekinded.Download.HandoffPath = "download", "/srv/import"
	if refused := wantError(t, f.send(http.MethodPut, "/api/v1/libraries/movies", rekinded), http.StatusBadRequest, CodeInvalid); refused.Field != "kind" {
		t.Errorf("kind change = %+v", refused)
	}

	// Arr connections.
	for field, write := range map[string]ArrWrite{
		"name":     {Name: "my arr", Type: "radarr", BaseURL: "http://localhost:7878", APIKey: pointer("k")},
		"type":     {Name: "arr", Type: "lidarr", BaseURL: "http://localhost:7878", APIKey: pointer("k")},
		"base_url": {Name: "arr", Type: "radarr", BaseURL: "localhost:7878", APIKey: pointer("k")},
		"api_key":  {Name: "arr", Type: "radarr", BaseURL: "http://localhost:7878"},
	} {
		if refused := wantError(t, f.send(http.MethodPost, "/api/v1/arrs", write), http.StatusBadRequest, CodeInvalid); refused.Field != field {
			t.Errorf("arr with a bad %s: field = %q, message %q", field, refused.Field, refused.Message)
		}
	}
	want[Arr](t, f.send(http.MethodPost, "/api/v1/arrs", ArrWrite{Name: "arr", Type: "radarr", BaseURL: "http://localhost:7878", APIKey: pointer("k")}), http.StatusCreated)
	wantError(t, f.send(http.MethodPost, "/api/v1/arrs", ArrWrite{Name: "arr", Type: "sonarr", BaseURL: "http://localhost:8989", APIKey: pointer("k")}), http.StatusConflict, CodeConflict)
}

func TestPanicBecomesAnInternalError(t *testing.T) {
	f := newFixture(t, "", nil)
	// Without a job service the status route dereferences nil.
	f.server.Jobs = nil
	f.handler = f.server.Handler()
	wantError(t, f.get("/api/v1/status"), http.StatusInternalServerError, CodeInternal)
	// The server is still there afterwards.
	want[Settings](t, f.get("/api/v1/settings"), http.StatusOK)
}

func TestSecurityHeadersOnEveryAnswer(t *testing.T) {
	f := newFixture(t, "", testUI())
	for _, path := range []string{"/", "/flows/3", "/assets/app-1a2b.js", "/api/v1/status", "/api/v1/nope"} {
		header := f.get(path).Header()
		for name, value := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "no-referrer",
			"X-Frame-Options":        "DENY",
		} {
			if header.Get(name) != value {
				t.Errorf("GET %s: %s = %q, want %q", path, name, header.Get(name), value)
			}
		}
		if policy := header.Get("Content-Security-Policy"); !strings.Contains(policy, "default-src 'self'") || !strings.Contains(policy, "frame-ancestors 'none'") {
			t.Errorf("GET %s: Content-Security-Policy = %q", path, policy)
		}
	}
	if recorder := f.get("/api/v1/status"); recorder.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("API Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
}

// themeScript is the kind of inline script a built index.html carries.
const themeScript = `document.documentElement.dataset.theme = localStorage.getItem("theme") || "dark";`

const builtIndex = `<!doctype html><html><head><meta property="csp-nonce"><script>` + themeScript + `</script><script type="module" src="/assets/app-1a2b.js"></script></head><body><div id="root"></div></body></html>`

func testUI() fstest.MapFS {
	return fstest.MapFS{
		"index.html":         {Data: []byte(builtIndex)},
		"assets/app-1a2b.js": {Data: []byte(`console.log("anvil")`)},
		"favicon.svg":        {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
	}
}

func TestStaticUI(t *testing.T) {
	f := newFixture(t, "", testUI())

	// The page itself, and every client-side route, is index.html and is
	// revalidated on each load.
	for _, path := range []string{"/", "/flows/3", "/libraries/movies/files", "/does/not/exist"} {
		recorder := f.get(path)
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `<div id="root">`) {
			t.Fatalf("GET %s = %d %s, want index.html", path, recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s: Cache-Control = %q, want no-cache", path, got)
		}
		if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Errorf("GET %s: Content-Type = %q", path, got)
		}
	}

	asset := f.get("/assets/app-1a2b.js")
	if asset.Code != http.StatusOK || asset.Body.String() != `console.log("anvil")` {
		t.Fatalf("asset = %d %s", asset.Code, asset.Body.String())
	}
	if got := asset.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("asset Cache-Control = %q", got)
	}
	if got := asset.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Errorf("asset Content-Type = %q", got)
	}
	// A missing build file is a 404, never the page: a browser would try to
	// run the HTML as a script.
	if missing := f.get("/assets/gone-9z.js"); missing.Code != http.StatusNotFound || strings.Contains(missing.Body.String(), "<div") {
		t.Fatalf("missing asset = %d %s", missing.Code, missing.Body.String())
	}
	if icon := f.get("/favicon.svg"); icon.Code != http.StatusOK || !strings.Contains(icon.Header().Get("Content-Type"), "svg") {
		t.Fatalf("favicon = %d %q", icon.Code, icon.Header().Get("Content-Type"))
	}
	// The API never falls back to the page.
	wantError(t, f.get("/api/v1/flows/3/nope"), http.StatusNotFound, CodeNotFound)
	if recorder := f.send(http.MethodPost, "/flows/3", `{}`); recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST to a page = %d, want 405", recorder.Code)
	}

	// The inline script of the built page is allowed by its hash, and nothing
	// else inline is.
	policy := f.get("/").Header().Get("Content-Security-Policy")
	for _, directive := range strings.Split(policy, "; ") {
		if !strings.HasPrefix(directive, "script-src ") {
			continue
		}
		if !strings.HasPrefix(directive, "script-src 'self' 'sha256-") || strings.Contains(directive, "unsafe") || strings.Count(directive, "'sha256-") != 1 {
			t.Fatalf("script-src = %q, want the one inline script allowed by its hash and nothing else inline", directive)
		}
	}
	if !strings.Contains(policy, "style-src 'self';") || !strings.Contains(policy, "style-src-attr 'unsafe-inline'") {
		t.Fatalf("Content-Security-Policy = %q, want inline style attributes and no inline style elements", policy)
	}

	// The page is served exactly as it was built.
	page := f.get("/")
	if body := page.Body.String(); body != builtIndex {
		t.Fatalf("index.html = %s, want it served as built", body)
	}
	revalidated := f.do(call{method: http.MethodGet, path: "/flows/3", header: map[string]string{"If-None-Match": page.Header().Get("ETag")}})
	if page.Header().Get("ETag") == "" || revalidated.Code != http.StatusNotModified || revalidated.Body.Len() != 0 {
		t.Fatalf("revalidating the page = %d with %d bytes, want 304 and none", revalidated.Code, revalidated.Body.Len())
	}
}

// The policy above blocks the <style> elements the UI kit writes by default.
// The page the frontend is built from has to tell the kit to construct its
// sheets instead; the daemon no longer adds that tag itself.
func TestTheFrontendPageAsksForConstructedStyles(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "web", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`<meta property="csp-nonce"\s*/?>`).Match(source) {
		t.Fatal("web/index.html has no <meta property=\"csp-nonce\"> without a nonce; the UI kit would write <style> elements the Content-Security-Policy blocks")
	}
}

func TestWithoutABuildThePageSaysSo(t *testing.T) {
	f := newFixture(t, "", nil)
	for _, path := range []string{"/", "/flows/3"} {
		recorder := f.get(path)
		if recorder.Code != http.StatusOK || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/plain") ||
			!strings.Contains(recorder.Body.String(), "built without the web UI") {
			t.Fatalf("GET %s = %d %q %s", path, recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
		}
	}
	// The API works all the same.
	want[Status](t, f.get("/api/v1/status"), http.StatusOK)
	if policy := f.get("/").Header().Get("Content-Security-Policy"); !strings.Contains(policy, "script-src 'self';") {
		t.Fatalf("Content-Security-Policy = %q", policy)
	}
}

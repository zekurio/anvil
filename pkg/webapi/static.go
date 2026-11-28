package webapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
)

// notBuiltPage is what the daemon serves in place of the UI when it was
// compiled without one. The API works all the same.
const notBuiltPage = `anvil is running, but this binary was built without the web UI.

Build the UI and the daemon again:

    npm --prefix web ci && npm --prefix web run build
    make build

The JSON API is available under /api/v1, and anvilctl works as usual.
`

// indexPage returns the page every UI route is answered with, or nil when the
// binary carries no build. It is served as built, except that its
// <base href="/"> names BasePath: the build refers to its files relative to
// that element, and the frontend takes its router and API base from it, so
// one build serves under any path. The Content
// Security Policy blocks inline style elements, which the UI kit writes by
// default; web/index.html carries a <meta property="csp-nonce"> without a
// nonce, which makes the kit build its sheets through the CSSOM instead.
func (s *Server) indexPage() []byte {
	if !s.UIBuilt || s.UI == nil {
		return nil
	}
	page, err := fs.ReadFile(s.UI, "index.html")
	if err != nil {
		slog.Error("the embedded web UI has no readable index.html", "error", err)
		return nil
	}
	if s.BasePath == "" {
		return page
	}
	if !bytes.Contains(page, rootBase) {
		slog.Error("the embedded web UI has no <base href=\"/\">, so it cannot be served under web.base_path", "base_path", s.BasePath)
		return page
	}
	// BasePath holds only characters that need no escaping in an attribute.
	return bytes.Replace(page, rootBase, []byte(`<base href="`+s.BasePath+`/"`), 1)
}

// rootBase is how the built page names the root as its base.
var rootBase = []byte(`<base href="/"`)

// uiHandler serves the built frontend. Hashed files under /assets/ never
// change, so browsers may keep them forever; index.html names those files, so
// it is revalidated on every load. Any other path is a client-side route and
// gets index.html, which is what lets a deep link or a reload work.
func (s *Server) uiHandler(index []byte) http.Handler {
	if index == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(notBuiltPage)) //nolint:errcheck // the client went away; nothing is left to tell it
			}
		})
	}
	// The tag lets a browser revalidate the page with one cheap request: it
	// changes exactly when the build does.
	sum := sha256.Sum256(index)
	indexTag := `"` + hex.EncodeToString(sum[:8]) + `"`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		info, err := fs.Stat(s.UI, name)
		switch {
		case name == "" || name == "index.html":
			name = ""
		case err == nil && !info.IsDir():
		case strings.HasPrefix(name, "assets/"):
			// A missing build file must not come back as a page of HTML: the
			// browser would try to run it as a script or a stylesheet.
			http.NotFound(w, r)
			return
		default:
			name = ""
		}
		switch {
		case name == "":
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("ETag", indexTag)
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
		case strings.HasPrefix(name, "assets/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			http.ServeFileFS(w, r, s.UI, name)
		default:
			w.Header().Set("Cache-Control", "no-cache")
			// Go's table of types does not know the manifest, and nosniff
			// leaves the browser no way to guess.
			if path.Ext(name) == ".webmanifest" {
				w.Header().Set("Content-Type", "application/manifest+json")
			}
			http.ServeFileFS(w, r, s.UI, name)
		}
	})
}

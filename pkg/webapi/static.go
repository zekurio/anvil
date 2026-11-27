package webapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// notBuiltPage is what the daemon serves in place of the UI when it was
// compiled without one. The API works all the same.
const notBuiltPage = `anvil is running, but this binary was built without the web UI.

Build the UI and the daemon again:

    make web          (or: npm --prefix web ci && npm --prefix web run build)
    make build

The JSON API is available under /api/v1, and anvilctl works as usual.
`

// uiHandler serves the built frontend. Hashed files under /assets/ never
// change, so browsers may keep them forever; index.html names those files, so
// it is revalidated on every load. Any other path is a client-side route and
// gets index.html, which is what lets a deep link or a reload work.
func (s *Server) uiHandler() http.Handler {
	if !s.UIBuilt || s.UI == nil {
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		info, err := fs.Stat(s.UI, name)
		switch {
		case err == nil && !info.IsDir():
		case strings.HasPrefix(name, "assets/"):
			// A missing build file must not come back as a page of HTML: the
			// browser would try to run it as a script or a stylesheet.
			http.NotFound(w, r)
			return
		default:
			name = "index.html"
		}
		switch {
		case strings.HasPrefix(name, "assets/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFileFS(w, r, s.UI, name)
	})
}

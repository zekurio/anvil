package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type options struct {
	root string
	addr string
	key  string
}

type arrItem struct {
	Path             string            `json:"path"`
	OriginalLanguage map[string]string `json:"originalLanguage"`
	MovieFile        *arrFile          `json:"movieFile,omitempty"`
}

type arrFile struct {
	Path string `json:"path"`
}

func main() {
	opts := parseOptions()
	root, err := filepath.Abs(opts.root)
	if err != nil {
		log.Fatalf("resolve root: %v", err)
	}

	server := &server{
		root: root,
		key:  opts.key,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", server.handle)

	log.Printf("mock Arr server listening on http://%s for root %s", opts.addr, root)
	if err := http.ListenAndServe(opts.addr, mux); err != nil {
		log.Fatal(err)
	}
}

func parseOptions() options {
	var opts options
	flag.StringVar(&opts.root, "root", getenv("ANVIL_MOCK_ROOT", filepath.Join("tmp", "mock-library")), "mock library root")
	flag.StringVar(&opts.addr, "addr", getenv("ANVIL_MOCK_ARR_ADDR", "127.0.0.1:18080"), "listen address")
	flag.StringVar(&opts.key, "key", getenv("ANVIL_MOCK_ARR_KEY", "mock-secret"), "required API key")
	flag.Parse()
	return opts
}

type server struct {
	root string
	key  string
}

func (s *server) handle(w http.ResponseWriter, r *http.Request) {
	clean := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	if clean == "/healthz" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if got := r.Header.Get("X-Api-Key"); got != s.key {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch clean {
	case "/radarr/api/v3/movie":
		if r.URL.Query().Get("includeMovieFile") != "true" {
			http.Error(w, "includeMovieFile=true is required", http.StatusBadRequest)
			return
		}
		writeJSON(w, s.radarrMovies())
	case "/radarr/api/v3/parse":
		writeJSON(w, s.radarrParse(r.URL.Query().Get("title")))
	case "/sonarr/api/v3/series":
		writeJSON(w, s.sonarrSeries())
	case "/sonarr/api/v3/parse":
		writeJSON(w, s.sonarrParse(r.URL.Query().Get("title"), r.URL.Query().Get("path")))
	default:
		http.NotFound(w, r)
	}
}

func (s *server) radarrMovies() []arrItem {
	movieDir := filepath.Join(s.root, "media", "movies", "Mock Movie (2026)")
	return []arrItem{
		{
			Path:             movieDir,
			OriginalLanguage: map[string]string{"name": "English"},
			MovieFile: &arrFile{
				Path: filepath.Join(movieDir, "Mock Movie (2026).mkv"),
			},
		},
	}
}

func (s *server) sonarrSeries() []arrItem {
	return []arrItem{
		{
			Path:             filepath.Join(s.root, "media", "tv", "Mock Anime"),
			OriginalLanguage: map[string]string{"name": "Japanese"},
		},
	}
}

type parseResponse struct {
	Title  string   `json:"title"`
	Movie  *arrItem `json:"movie,omitempty"`
	Series *arrItem `json:"series,omitempty"`
}

func (s *server) radarrParse(title string) parseResponse {
	return parseResponse{
		Title: title,
		Movie: &arrItem{
			Path:             filepath.Join(s.root, "media", "movies", "Mock Movie (2026)"),
			OriginalLanguage: map[string]string{"name": "English"},
		},
	}
}

func (s *server) sonarrParse(title string, path string) parseResponse {
	return parseResponse{
		Title: title,
		Series: &arrItem{
			Path:             filepath.Join(s.root, "media", "tv", "Mock Anime"),
			OriginalLanguage: map[string]string{"name": "Japanese"},
		},
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, fmt.Sprintf("encode json: %v", err), http.StatusInternalServerError)
	}
}

func getenv(name string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

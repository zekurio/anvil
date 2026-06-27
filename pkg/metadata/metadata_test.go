package metadata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestResolverGetsRadarrOriginalLanguageFromMovieFilePath(t *testing.T) {
	server := metadataServer(t, "/api/v3/movie", `[{
		"path": "/media/movies/Movie (2026)",
		"originalLanguage": {"name": "English"},
		"movieFile": {"path": "/media/movies/Movie (2026)/Movie.mkv"}
	}]`)

	result, err := (Resolver{Client: server.Client()}).ResolveJobMetadata(context.Background(), domain.Library{
		Metadata: domain.MetadataProviderPolicy{
			Provider: domain.MetadataProviderRadarr,
			BaseURL:  server.URL,
			APIKey:   "secret",
		},
	}, domain.MediaSource{}, domain.MediaAsset{}, "/media/movies/Movie (2026)/Movie.mkv")
	if err != nil {
		t.Fatalf("ResolveJobMetadata() error = %v", err)
	}
	if got, want := result.OriginalLanguage, "eng"; got != want {
		t.Fatalf("original language = %q, want %q", got, want)
	}
}

func TestResolverGetsSonarrOriginalLanguageFromSeriesPath(t *testing.T) {
	server := metadataServer(t, "/api/v3/series", `[{
		"path": "/media/tv/Show",
		"originalLanguage": {"name": "Japanese"}
	}]`)

	result, err := (Resolver{Client: server.Client()}).ResolveJobMetadata(context.Background(), domain.Library{
		Metadata: domain.MetadataProviderPolicy{
			Provider: domain.MetadataProviderSonarr,
			BaseURL:  server.URL,
			APIKey:   "secret",
		},
	}, domain.MediaSource{}, domain.MediaAsset{}, "/media/tv/Show/Season 01/Show S01E01.mkv")
	if err != nil {
		t.Fatalf("ResolveJobMetadata() error = %v", err)
	}
	if got, want := result.OriginalLanguage, "jpn"; got != want {
		t.Fatalf("original language = %q, want %q", got, want)
	}
}

func TestResolverReadsAPIKeyFromFile(t *testing.T) {
	server := metadataServer(t, "/api/v3/movie", `[{
		"path": "/media/movies/Movie (2026)",
		"originalLanguage": "eng"
	}]`)
	keyPath := filepath.Join(t.TempDir(), "radarr-api-key")
	if err := os.WriteFile(keyPath, []byte("secret\n"), 0o600); err != nil {
		t.Fatalf("write API key file: %v", err)
	}

	_, err := (Resolver{Client: server.Client()}).ResolveJobMetadata(context.Background(), domain.Library{
		Metadata: domain.MetadataProviderPolicy{
			Provider:   domain.MetadataProviderRadarr,
			BaseURL:    server.URL,
			APIKeyFile: keyPath,
		},
	}, domain.MediaSource{}, domain.MediaAsset{}, "/media/movies/Movie (2026)/Movie.mkv")
	if err != nil {
		t.Fatalf("ResolveJobMetadata() error = %v", err)
	}
}

func TestResolverSkipsHTTPWhenProviderIsUnset(t *testing.T) {
	result, err := (Resolver{}).ResolveJobMetadata(context.Background(), domain.Library{}, domain.MediaSource{}, domain.MediaAsset{}, "/media/movie.mkv")
	if err != nil {
		t.Fatalf("ResolveJobMetadata() error = %v", err)
	}
	if result.OriginalLanguage != "" {
		t.Fatalf("original language = %q, want empty", result.OriginalLanguage)
	}
}

func TestResolverDefaultClientHasTimeout(t *testing.T) {
	client := (Resolver{}).client()
	if client.Timeout != defaultHTTPTimeout {
		t.Fatalf("default client timeout = %v, want %v", client.Timeout, defaultHTTPTimeout)
	}
}

func TestResolverKeepsConfiguredClient(t *testing.T) {
	configured := &http.Client{Timeout: time.Second}
	client := (Resolver{Client: configured}).client()
	if client != configured {
		t.Fatal("client() did not preserve configured HTTP client")
	}
}

func metadataServer(t *testing.T, expectedPath string, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != expectedPath {
			t.Errorf("path = %q, want %q", got, expectedPath)
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("X-Api-Key"); got != "secret" {
			t.Errorf("X-Api-Key = %q, want secret", got)
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/v3/movie" && r.URL.Query().Get("includeMovieFile") != "true" {
			t.Error("includeMovieFile query missing")
			http.Error(w, "missing query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

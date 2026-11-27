package metadata

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

// padded is a JSON document of at least size bytes: value followed by
// whitespace, which a decoder skips.
func padded(value string, size int) io.Reader {
	return io.MultiReader(strings.NewReader(value), bytes.NewReader(bytes.Repeat([]byte(" "), max(size-len(value), 0))))
}

func radarr(url string) domain.Library {
	return domain.Library{Metadata: domain.MetadataProviderPolicy{
		Provider: domain.MetadataProviderRadarr, BaseURL: url, APIKey: "s3cret-key",
	}}
}

func TestListResponseMayExceedTheLookupLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/movie" {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		body := `[{"path":"/media/movies/Film (2020)","originalLanguage":{"name":"French"}}]`
		if _, err := io.Copy(w, padded(body, maxResponseBytes+1<<20)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	metadata, err := Resolver{}.ResolveJobMetadata(context.Background(), radarr(server.URL), domain.MediaSource{}, domain.MediaAsset{}, "/media/movies/Film (2020)/Film.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.OriginalLanguage == "" {
		t.Fatalf("metadata = %+v, want the original language of the matched movie", metadata)
	}
}

func TestResponseLimits(t *testing.T) {
	for _, test := range []struct {
		name  string
		list  int
		parse int
		want  string
	}{
		{name: "list", list: maxListResponseBytes + 1, want: "/api/v3/movie"},
		{name: "parse", list: 0, parse: maxResponseBytes + 1, want: "/api/v3/parse"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				size := test.parse
				body := `{}`
				if r.URL.Path == "/api/v3/movie" {
					size, body = test.list, `[]`
				}
				// The client stops reading at the limit, so a failed write
				// is expected here.
				_, err := io.Copy(w, padded(body, size))
				if err != nil {
					t.Logf("write %s: %v", r.URL.Path, err)
				}
			}))
			defer server.Close()

			_, err := Resolver{}.ResolveJobMetadata(context.Background(), radarr(server.URL), domain.MediaSource{RelativePath: "Film.mkv"}, domain.MediaAsset{}, "/media/movies/Film.mkv")
			if err == nil || !strings.Contains(err.Error(), "too large") || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want the %s response refused as too large", err, test.want)
			}
		})
	}
}

func TestRedirectIsRefused(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		if _, err := fmt.Fprint(w, `[]`); err != nil {
			t.Error(err)
		}
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	defer origin.Close()

	_, err := Resolver{}.ResolveJobMetadata(context.Background(), radarr(origin.URL), domain.MediaSource{}, domain.MediaAsset{}, "/media/movies/Film.mkv")
	if err == nil || !strings.Contains(err.Error(), "refusing metadata redirect") {
		t.Fatalf("err = %v, want the redirect refused", err)
	}
	if reached.Load() {
		t.Fatal("the request followed the redirect")
	}
}

package webapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
)

// The stored key must never follow the connection to a server the write
// names for the first time: whoever can reach the API would receive it with
// the next job's request.
func TestArrKeyStaysWithItsOrigin(t *testing.T) {
	f := newFixture(t, "", nil)
	ctx := context.Background()
	want[Arr](t, f.send(http.MethodPost, "/api/v1/arrs", ArrWrite{
		Name: "radarr", Type: "radarr", BaseURL: "http://radarr.lan:7878", APIKey: pointer("s3cret-key"),
	}), http.StatusCreated)

	for _, moved := range []string{"http://evil.example:7878", "http://radarr.lan:7879", "https://radarr.lan:7878"} {
		refused := wantError(t, f.send(http.MethodPut, "/api/v1/arrs/radarr", ArrWrite{
			Name: "radarr", Type: "radarr", BaseURL: moved,
		}), http.StatusBadRequest, CodeInvalid)
		if refused.Field != "api_key" {
			t.Fatalf("moving to %s without a key: error = %+v, want a problem on api_key", moved, refused)
		}
	}
	if stored, err := f.state.GetArr(ctx, "radarr"); err != nil || stored.BaseURL != "http://radarr.lan:7878" || stored.APIKey != "s3cret-key" {
		t.Fatalf("a refused move changed the connection: %+v, %v", stored, err)
	}

	// The same origin, spelled differently and with another path, keeps it.
	kept := want[Arr](t, f.send(http.MethodPut, "/api/v1/arrs/radarr", ArrWrite{
		Name: "radarr", Type: "radarr", BaseURL: "HTTP://Radarr.LAN:7878/radarr",
	}), http.StatusOK)
	if !kept.APIKeySet {
		t.Fatalf("a path change on the same origin dropped the key: %+v", kept)
	}

	moved := want[Arr](t, f.send(http.MethodPut, "/api/v1/arrs/radarr", ArrWrite{
		Name: "radarr", Type: "radarr", BaseURL: "http://radarr2.lan:7878", APIKey: pointer("other-key"),
	}), http.StatusOK)
	if !moved.APIKeySet || moved.BaseURL != "http://radarr2.lan:7878" {
		t.Fatalf("a move with a key: %+v", moved)
	}
	if stored, err := f.state.GetArr(ctx, "radarr"); err != nil || stored.APIKey != "other-key" {
		t.Fatalf("stored arr = %+v, %v", stored, err)
	}
}

func TestArrDefaultPortIsTheSameOrigin(t *testing.T) {
	for _, pair := range [][2]string{
		{"http://radarr.lan", "http://radarr.lan:80/api"},
		{"https://radarr.lan:443", "https://RADARR.lan"},
	} {
		if !sameOrigin(pair[0], pair[1]) {
			t.Errorf("sameOrigin(%q, %q) = false", pair[0], pair[1])
		}
	}
	if sameOrigin("http://radarr.lan", "https://radarr.lan:80") {
		t.Error("http and https on one port are not the same origin")
	}
}

func TestArrImportedKeyFileStaysWithItsOrigin(t *testing.T) {
	f := newFixture(t, "", nil)
	ctx := context.Background()
	if _, err := f.state.CreateArr(ctx, config.ArrConfig{
		Name: "sonarr", Type: "sonarr", BaseURL: "http://sonarr.lan:8989", APIKeyFile: "/run/secrets/sonarr",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	refused := wantError(t, f.send(http.MethodPut, "/api/v1/arrs/sonarr", ArrWrite{
		Name: "sonarr", Type: "sonarr", BaseURL: "http://evil.example:8989",
	}), http.StatusBadRequest, CodeInvalid)
	if refused.Field != "api_key" {
		t.Fatalf("error = %+v, want a problem on api_key", refused)
	}
	want[Arr](t, f.send(http.MethodPut, "/api/v1/arrs/sonarr", ArrWrite{
		Name: "sonarr", Type: "sonarr", BaseURL: "http://sonarr.lan:8989/sonarr",
	}), http.StatusOK)
	if stored, err := f.state.GetArr(ctx, "sonarr"); err != nil || stored.APIKeyFile != "/run/secrets/sonarr" {
		t.Fatalf("a same-origin update dropped the key file: %+v, %v", stored, err)
	}
}

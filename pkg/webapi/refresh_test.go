package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/store"
)

// disconnectingStore cancels the request the moment the store has committed
// a change, which is what a client going away at the worst time looks like.
type disconnectingStore struct {
	Store
	disconnect context.CancelFunc
}

func (s disconnectingStore) UpdateLibrary(ctx context.Context, library config.LibraryConfig, now time.Time) (store.Library, error) {
	stored, err := s.Store.UpdateLibrary(ctx, library, now)
	s.disconnect()
	return stored, err
}

func (s disconnectingStore) DeleteLibrary(ctx context.Context, name string) ([]store.DeletedJob, error) {
	deleted, err := s.Store.DeleteLibrary(ctx, name)
	s.disconnect()
	return deleted, err
}

// A change that is saved has to reach the running daemon even when the
// request ends before the answer: the scheduler reads the runtime snapshot,
// and a snapshot that missed the change keeps a paused library running.
func TestSavedChangeReachesTheDaemonAfterTheClientDisconnects(t *testing.T) {
	f := newFixture(t, "", nil)
	library := f.mediaLibrary("movies", f.defaultFlow().ID).LibraryWrite
	real := f.server.Store

	send := func(method string, body any) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.server.Store = disconnectingStore{Store: real, disconnect: cancel}
		defer func() { f.server.Store = real }()
		var encoded bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&encoded).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		request := httptest.NewRequest(method, "/api/v1/libraries/movies", &encoded).WithContext(ctx)
		request.Host = testHost
		request.Header.Set("Content-Type", "application/json")
		f.handler.ServeHTTP(httptest.NewRecorder(), request)
		if ctx.Err() == nil {
			t.Fatalf("%s did not reach the store", method)
		}
	}

	library.Paused = true
	send(http.MethodPut, library)
	if stored, err := f.state.GetLibrary(context.Background(), "movies"); err != nil || !stored.Paused {
		t.Fatalf("stored library = %+v, %v; want it paused", stored, err)
	}
	if !f.config().Libraries["movies"].Paused {
		t.Error("the library is paused in the store, but the running daemon still has it running")
	}

	send(http.MethodDelete, nil)
	if _, exists := f.config().Libraries["movies"]; exists {
		t.Error("the library is deleted from the store, but the running daemon still has it")
	}
}

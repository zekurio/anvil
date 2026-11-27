package webapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/store"
)

// arrName is what a new Arr connection may be called. Connections imported
// from a legacy config keep whatever name they had there.
var arrName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func (s *Server) listArrs(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	stored, err := s.Store.ListArrs(r.Context())
	if err != nil {
		return fail(err)
	}
	usage, err := s.Store.ArrLibraries(r.Context())
	if err != nil {
		return fail(err)
	}
	list := ArrList{Arrs: make([]Arr, 0, len(stored))}
	for _, arr := range stored {
		list.Arrs = append(list.Arrs, arrOf(arr, usage[arr.Name]))
	}
	return ok(list)
}

// arrOf describes a stored connection. The key stays in the store: the
// response only says whether there is one.
func arrOf(stored store.Arr, libraries []string) Arr {
	if libraries == nil {
		libraries = []string{}
	}
	return Arr{
		Name:       stored.Name,
		Type:       stored.Type,
		BaseURL:    stored.BaseURL,
		APIKeyFile: stored.APIKeyFile,
		APIKeySet:  stored.APIKey != "",
		Libraries:  libraries,
	}
}

func (s *Server) arrResponse(ctx context.Context, stored store.Arr) (Arr, error) {
	usage, err := s.Store.ArrLibraries(ctx)
	if err != nil {
		return Arr{}, err
	}
	return arrOf(stored, usage[stored.Name]), nil
}

// arrConfig builds the connection a write describes. currentKey is the stored
// key, which a write without api_key keeps.
func arrConfig(write ArrWrite, currentKey string) (config.ArrConfig, error) {
	arr := config.ArrConfig{
		Name:       write.Name,
		Type:       strings.TrimSpace(write.Type),
		BaseURL:    strings.TrimRight(strings.TrimSpace(write.BaseURL), "/"),
		APIKey:     currentKey,
		APIKeyFile: strings.TrimSpace(write.APIKeyFile),
	}
	if write.APIKey != nil {
		arr.APIKey = strings.TrimSpace(*write.APIKey)
	}
	return arr, fieldProblem(arr.Problems())
}

func (s *Server) createArr(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	var write ArrWrite
	if err := decodeBody(r, &write); err != nil {
		return fail(err)
	}
	if !arrName.MatchString(write.Name) {
		return fail(invalidf("name", "An Arr connection name has 1 to 64 characters out of letters, digits, \"-\", \"_\", and \".\"."))
	}
	arr, err := arrConfig(write, "")
	if err != nil {
		return fail(err)
	}
	s.mutations.Lock()
	defer s.mutations.Unlock()
	stored, err := s.Store.CreateArr(r.Context(), arr, s.now())
	if errors.Is(err, store.ErrNameTaken) {
		return fail(conflictf("An Arr connection named %q already exists.", arr.Name))
	}
	if err != nil {
		return fail(err)
	}
	if err := s.refresh(r.Context()); err != nil {
		return fail(err)
	}
	response, err := s.arrResponse(r.Context(), stored)
	if err != nil {
		return fail(err)
	}
	return created(response)
}

func (s *Server) storedArr(ctx context.Context, name string) (store.Arr, error) {
	stored, err := s.Store.GetArr(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Arr{}, notFoundf("There is no Arr connection %q.", name)
	}
	return stored, err
}

func (s *Server) updateArr(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	name := r.PathValue("name")
	var write ArrWrite
	if err := decodeBody(r, &write); err != nil {
		return fail(err)
	}
	if write.Name != name {
		return fail(invalidf("name", "The name in the request (%q) must be the connection the URL names (%q). A connection cannot be renamed.", write.Name, name))
	}
	s.mutations.Lock()
	defer s.mutations.Unlock()
	current, err := s.storedArr(r.Context(), name)
	if err != nil {
		return fail(err)
	}
	arr, err := arrConfig(write, current.APIKey)
	if err != nil {
		return fail(err)
	}
	stored, err := s.Store.UpdateArr(r.Context(), arr, s.now())
	if err != nil {
		return fail(err)
	}
	// Jobs read the connection from the runtime snapshot when they look up
	// the original language, so the next one uses the new address and key.
	if err := s.refresh(r.Context()); err != nil {
		return fail(err)
	}
	response, err := s.arrResponse(r.Context(), stored)
	if err != nil {
		return fail(err)
	}
	return ok(response)
}

func (s *Server) deleteArr(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	name := r.PathValue("name")
	s.mutations.Lock()
	defer s.mutations.Unlock()
	if _, err := s.storedArr(r.Context(), name); err != nil {
		return fail(err)
	}
	err := s.Store.DeleteArr(r.Context(), name)
	var inUse *store.InUseError
	switch {
	case errors.As(err, &inUse):
		return fail(conflictf("Arr connection %q is %s. Remove it from there first.", name, inUse.Error()))
	case err != nil:
		return fail(err)
	}
	if err := s.refresh(r.Context()); err != nil {
		return fail(err)
	}
	return noContent()
}

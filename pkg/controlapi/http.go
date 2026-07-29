package controlapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/store"
)

const maxRequestBytes = 1 << 20

func (s Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", s.handleStatus)
	mux.HandleFunc("/v1/jobs", s.handleJobs)
	mux.HandleFunc("/v1/jobs/cancel", s.handleJobCancel)
	return mux
}

func (s Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
		return
	}
	if r.URL.RawQuery != "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_argument", "status does not accept query parameters")
		return
	}
	response, err := s.Status(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s Service) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is supported")
		return
	}
	query, err := parseJobQuery(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	response, err := s.ListJobs(r.Context(), query)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s Service) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is supported")
		return
	}
	if r.URL.RawQuery != "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_argument", "cancel does not accept query parameters")
		return
	}
	var request JobCancelRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_argument", "decode cancel request: "+err.Error())
		return
	}
	if decoder.More() {
		writeAPIError(w, http.StatusBadRequest, "invalid_argument", "cancel accepts exactly one JSON object")
		return
	}
	response, err := s.CancelJobs(r.Context(), request)
	if err != nil {
		var invalid invalidArgumentError
		switch {
		case errors.As(err, &invalid):
			writeAPIError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		case errors.Is(err, store.ErrNotFound):
			writeAPIError(w, http.StatusNotFound, "not_found", err.Error())
		default:
			writeAPIError(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func parseJobQuery(r *http.Request) (JobQuery, error) {
	values := r.URL.Query()
	allowed := map[string]struct{}{
		"library": {}, "path": {}, "absolute_path": {}, "state": {}, "current_only": {},
		"limit": {}, "with_selection": {},
	}
	for key := range values {
		if _, ok := allowed[key]; !ok {
			return JobQuery{}, errors.New("unknown query parameter " + strconv.Quote(key))
		}
	}
	query := JobQuery{
		Library: values.Get("library"), Path: values.Get("path"),
		AbsolutePath: values.Get("absolute_path"), States: values["state"],
	}
	if value := strings.TrimSpace(values.Get("current_only")); value != "" {
		currentOnly, err := strconv.ParseBool(value)
		if err != nil {
			return JobQuery{}, errors.New("current_only must be true or false")
		}
		query.CurrentOnly = currentOnly
	}
	if value := strings.TrimSpace(values.Get("with_selection")); value != "" {
		withSelection, err := strconv.ParseBool(value)
		if err != nil {
			return JobQuery{}, errors.New("with_selection must be true or false")
		}
		query.WithSelection = withSelection
	}
	if value := strings.TrimSpace(values.Get("limit")); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 0 {
			return JobQuery{}, errors.New("limit must be a non-negative integer")
		}
		query.Limit = limit
	}
	if _, _, _, err := normalizeJobQuery(query); err != nil {
		return JobQuery{}, err
	}
	return query, nil
}

func writeAPIError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, ErrorResponse{Error: APIError{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value) //nolint:errcheck // response headers are already committed
}

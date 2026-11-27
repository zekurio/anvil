package webapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/store"
)

// maxBodyBytes bounds every request body. The largest legitimate one is a
// flow graph, which stays far below it.
const maxBodyBytes = 1 << 20

// apiError is a failure a handler returns instead of writing it, so every
// error leaves through one place and always has the ErrorResponse shape.
type apiError struct {
	status  int
	code    string
	message string
	field   string
	issues  []flow.Issue
}

func (e *apiError) Error() string {
	return e.message
}

func invalidf(field, format string, args ...any) *apiError {
	return &apiError{status: http.StatusBadRequest, code: CodeInvalid, field: field, message: fmt.Sprintf(format, args...)}
}

func notFoundf(format string, args ...any) *apiError {
	return &apiError{status: http.StatusNotFound, code: CodeNotFound, message: fmt.Sprintf(format, args...)}
}

func conflictf(format string, args ...any) *apiError {
	return &apiError{status: http.StatusConflict, code: CodeConflict, message: fmt.Sprintf(format, args...)}
}

// handler is an API route: it returns what to send, or the error to report.
// A nil body with status 204 sends no content.
type handler func(w http.ResponseWriter, r *http.Request) (status int, body any, err error)

func ok(body any) (int, any, error) {
	return http.StatusOK, body, nil
}

func created(body any) (int, any, error) {
	return http.StatusCreated, body, nil
}

func noContent() (int, any, error) {
	return http.StatusNoContent, nil, nil
}

func fail(err error) (int, any, error) {
	return 0, nil, err
}

// serve adapts a handler to net/http.
func serve(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, body, err := h(w, r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if status == http.StatusNoContent {
			w.WriteHeader(status)
			return
		}
		writeJSON(w, status, body)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		slog.Error("web response could not be encoded", "error", err)
		status = http.StatusInternalServerError
		data = []byte(`{"error":{"code":"internal","message":"The response could not be encoded."}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n')) //nolint:errcheck // the client went away; nothing is left to tell it
}

// writeError maps any error a handler can return onto the contract's status
// codes. Errors of the store and of the control service keep their wording:
// both are written for the operator.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	failure := classify(r.Context(), err)
	if failure.status >= http.StatusInternalServerError {
		slog.Error("web request failed", "method", r.Method, "path", r.URL.Path, "status", failure.status, "error", err)
	}
	writeJSON(w, failure.status, ErrorResponse{Error: Error{
		Code: failure.code, Message: failure.message, Field: failure.field, Issues: failure.issues,
	}})
}

func classify(ctx context.Context, err error) *apiError {
	var failure *apiError
	if errors.As(err, &failure) {
		return failure
	}
	var controlErr *control.Error
	if errors.As(err, &controlErr) {
		switch controlErr.Code {
		case control.CodeInvalidArgument:
			return &apiError{status: http.StatusBadRequest, code: CodeInvalid, message: sentence(controlErr.Message)}
		case control.CodeNotFound:
			return &apiError{status: http.StatusNotFound, code: CodeNotFound, message: sentence(controlErr.Message)}
		}
		return &apiError{status: http.StatusInternalServerError, code: CodeInternal, message: sentence(controlErr.Message)}
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return &apiError{status: http.StatusRequestEntityTooLarge, code: CodeInvalid, message: fmt.Sprintf("The request body is larger than %d bytes.", maxBodyBytes)}
	case errors.Is(err, store.ErrNotFound):
		return &apiError{status: http.StatusNotFound, code: CodeNotFound, message: sentence(err.Error())}
	case errors.Is(err, store.ErrRevisionConflict), errors.Is(err, store.ErrNameTaken), errors.Is(err, store.ErrInUse), errors.Is(err, store.ErrActiveWork):
		return &apiError{status: http.StatusConflict, code: CodeConflict, message: sentence(trimSentinel(err.Error()))}
	case ctx.Err() != nil:
		return &apiError{status: http.StatusServiceUnavailable, code: CodeUnavailable, message: "The daemon is shutting down, or the request was abandoned."}
	}
	return &apiError{status: http.StatusInternalServerError, code: CodeInternal, message: sentence(err.Error())}
}

// trimSentinel drops the "store: ..." sentinel text that wraps the part of a
// store error worded for the operator, which follows it after ": ".
func trimSentinel(message string) string {
	if !strings.HasPrefix(message, "store: ") {
		return message
	}
	if _, detail, found := strings.Cut(message, ": "); found {
		if _, rest, found := strings.Cut(detail, ": "); found {
			return rest
		}
	}
	return message
}

// sentence makes an error message read as one: capitalized, with a full stop.
func sentence(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return message
	}
	message = strings.ToUpper(message[:1]) + message[1:]
	if strings.HasSuffix(message, ".") || strings.HasSuffix(message, "?") || strings.HasSuffix(message, "!") {
		return message
	}
	return message + "."
}

// decodeBody reads the one JSON value a request carries. Unknown fields are
// refused: a misspelled field that is silently dropped looks like a setting
// that was saved.
func decodeBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return err
		}
		if errors.Is(err, io.EOF) {
			return invalidf("", "The request needs a JSON body.")
		}
		return invalidf("", "The request body is not valid: %s.", strings.TrimPrefix(err.Error(), "json: "))
	}
	if decoder.More() {
		return invalidf("", "The request body holds more than one JSON value.")
	}
	return nil
}

// jsonContentType reports whether a request declares a JSON body.
func jsonContentType(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

// intParam reads a non-negative integer query parameter.
func intParam(r *http.Request, name string, fallback int) (int, error) {
	text := strings.TrimSpace(r.URL.Query().Get(name))
	if text == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(text)
	if err != nil || value < 0 {
		return 0, invalidf(name, "%s must be a whole number that is not negative.", name)
	}
	return value, nil
}

// pageParams reads limit and offset the way every paged route takes them.
func pageParams(r *http.Request) (limit, offset int, err error) {
	if limit, err = intParam(r, "limit", defaultPageLimit); err != nil {
		return 0, 0, err
	}
	if offset, err = intParam(r, "offset", 0); err != nil {
		return 0, 0, err
	}
	if limit == 0 {
		limit = defaultPageLimit
	}
	return min(limit, maxPageLimit), offset, nil
}

const (
	defaultPageLimit = 100
	maxPageLimit     = 500
)

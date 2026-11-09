package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httplog/v2"
)

const (
	problemContentType = "application/problem+json"
	problemTypeBlank   = "about:blank"

	detailBadRequest   = "The body must be one GitLab event payload: a JSON object of at most 1 MiB."
	detailUnauthorized = "X-Gitlab-Token is missing or wrong."
	detailUnexpected   = "The request could not be completed. Contact your administrator with the request ID."
)

var (
	errUnauthorized = errors.New("hook token missing or wrong")
	errBadPayload   = errors.New("event payload is not a JSON object")
)

// writeProblem writes err as an RFC 9457 problem response. The detail is fixed per status; err goes only to
// the request log line. err must not be nil.
func writeProblem(w http.ResponseWriter, r *http.Request, err error) {
	httplog.LogEntrySetField(r.Context(), "error", slog.StringValue(err.Error()))

	status, detail := http.StatusInternalServerError, detailUnexpected

	var maxBytes *http.MaxBytesError

	switch {
	case errors.Is(err, errUnauthorized):
		status, detail = http.StatusUnauthorized, detailUnauthorized
	case errors.As(err, &maxBytes), errors.Is(err, errBadPayload), isRequestError(err):
		status, detail = http.StatusBadRequest, detailBadRequest
	}

	p := Problem{Type: problemTypeBlank, Title: http.StatusText(status), Status: status, Detail: &detail}

	if id := middleware.GetReqID(r.Context()); id != "" {
		w.Header().Set(middleware.RequestIDHeader, id)

		p.RequestId = &id
	}

	w.Header().Set("Content-Type", problemContentType)
	w.WriteHeader(status)

	// Encode error ignored: headers already sent.
	_ = json.NewEncoder(w).Encode(p)
}

// isRequestError reports errors of the generated request decoding and parameter binding.
func isRequestError(err error) bool {
	var (
		syntax    *json.SyntaxError
		unmarshal *json.UnmarshalTypeError
		required  *RequiredHeaderError
		format    *InvalidParamFormatError
		many      *TooManyValuesForParamError
	)

	return errors.As(err, &syntax) || errors.As(err, &unmarshal) || errors.As(err, &required) ||
		errors.As(err, &format) || errors.As(err, &many) || errors.Is(err, errEOF)
}

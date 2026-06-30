package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// Envelope is the JSON error shape used across the API (docs/design.md §5.1.1).
type Envelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details any    `json:"details,omitempty"`
	} `json:"error"`
}

// Standard error codes (must stay in sync with docs/error-codes.md).
const (
	CodeBadJSON              = "BAD_JSON"
	CodeBadRequest           = "BAD_REQUEST"
	CodeMissingFields        = "MISSING_FIELDS"
	CodeInvalidType          = "INVALID_TYPE"
	CodeInvalidStatus        = "INVALID_STATUS"
	CodeInvalidAsOf          = "INVALID_AS_OF"
	CodeBadQuery             = "BAD_QUERY"
	CodeMissingToken         = "MISSING_TOKEN"
	CodeInvalidToken         = "INVALID_TOKEN"
	CodeForbidden            = "FORBIDDEN"
	CodeNotFound             = "NOT_FOUND"
	CodeMethodNotAllowed     = "METHOD_NOT_ALLOWED"
	CodeAlreadyExists        = "ALREADY_EXISTS"
	CodeVersionMismatch      = "VERSION_MISMATCH"
	CodeBodyTooLarge         = "BODY_TOO_LARGE"
	CodeSecretsDetected      = "SECRETS_DETECTED"
	CodeUnprocessable        = "UNPROCESSABLE"
	CodePreconditionRequired = "PRECONDITION_REQUIRED"
	CodeRateLimited          = "RATE_LIMITED"
	CodeInternal             = "INTERNAL"
	CodeNotImplemented       = "NOT_IMPLEMENTED"
	CodeEnrichmentUnavail    = "ENRICHMENT_UNAVAILABLE"
)

func writeError(w http.ResponseWriter, status int, code, message string, details any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	env := Envelope{}
	env.Error.Code = code
	env.Error.Message = message
	env.Error.Details = details
	_ = json.NewEncoder(w).Encode(env)
}

// writeStoreError translates store sentinels into appropriate HTTP responses.
// Anything unknown becomes 500 INTERNAL.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error(), nil)
	case errors.Is(err, store.ErrAlreadyExists):
		writeError(w, http.StatusConflict, CodeAlreadyExists, err.Error(), nil)
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
	case errors.Is(err, store.ErrVersionMismatch):
		writeError(w, http.StatusConflict, CodeVersionMismatch, err.Error(), nil)
	default:
		writeError(w, http.StatusInternalServerError, CodeInternal, "Internal server error", nil)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

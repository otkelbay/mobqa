package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/mobqa/backend/internal/fault"
)

// maxBodyBytes caps JSON request bodies; uploads use their own endpoints and limits.
const maxBodyBytes = 1 << 20

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

// writeFault is the single place where usecase errors become responses. Only fault kinds and their
// constant details reach the client; anything else is logged and answered with a generic 500.
func writeFault(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	status, message := faultStatus(err)
	if status == http.StatusInternalServerError {
		logger.ErrorContext(r.Context(), "request failed",
			"request_id", requestID(r.Context()), "route", r.Pattern, "error", err)
		writeError(w, status, message)
		return
	}
	if detail, ok := fault.Detail(err); ok {
		message = detail
	}
	writeError(w, status, message)
}

func faultStatus(err error) (int, string) {
	switch {
	case errors.Is(err, fault.ErrInvalidInput):
		return http.StatusBadRequest, "invalid input"
	case errors.Is(err, fault.ErrUnauthorized):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, fault.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, fault.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, fault.ErrConflict):
		return http.StatusConflict, "conflict"
	case errors.Is(err, fault.ErrUnavailable):
		return http.StatusServiceUnavailable, "temporarily unavailable"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// decodeJSON reads exactly one JSON object with no unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.More() {
		return fault.WithDetail(fault.ErrInvalidInput, "request body must be one JSON object with known fields")
	}
	return nil
}

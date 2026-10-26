package response

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"drivethrough/pkg/apperr"
)

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
}

type Envelope struct {
	Success bool      `json:"success"`
	Data    any       `json:"data,omitempty"`
	Error   *APIError `json:"error,omitempty"`
	Meta    any       `json:"meta,omitempty"`
}

func JSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(Envelope{Success: status < 400, Data: data}); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
}

func Error(w http.ResponseWriter, status int, code, message string, detail any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(Envelope{
		Success: false,
		Error:   &APIError{Code: code, Message: message, Detail: detail},
	}); err != nil {
		slog.Error("failed to encode error response", "error", err)
	}
}

func BadRequest(w http.ResponseWriter, message string, detail any) {
	Error(w, http.StatusBadRequest, "BAD_REQUEST", message, detail)
}

func Unauthorized(w http.ResponseWriter, message string) {
	Error(w, http.StatusUnauthorized, "UNAUTHORIZED", message, nil)
}

func Forbidden(w http.ResponseWriter, message string) {
	Error(w, http.StatusForbidden, "FORBIDDEN", message, nil)
}

func NotFound(w http.ResponseWriter, message string) {
	Error(w, http.StatusNotFound, "NOT_FOUND", message, nil)
}

func Internal(w http.ResponseWriter, err error) {
	slog.Error("internal server error", "error", err)
	Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "something went wrong", nil)
}

func Conflict(w http.ResponseWriter, message string) {
	Error(w, http.StatusConflict, "CONFLICT", message, nil)
}

// AppError renders a domain error with the status it actually carries.
//
// The lookup has to go through errors.As: services wrap failures with %w and
// apperr.Wrap, so the *AppError is routinely buried several levels deep. A plain
// type assertion only matches when the error sits at the top of the chain, which
// silently turned every 400/403/404/409 in the codebase into a 500.
func AppError(w http.ResponseWriter, err error) {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		message := ae.Message
		if message == "" {
			message = http.StatusText(ae.Status)
		}
		Error(w, ae.Status, ae.Code, message, ae.Detail)
		return
	}

	// A bare sentinel carries a meaningful status even without the wrapper.
	switch {
	case errors.Is(err, apperr.ErrNotFound):
		Error(w, http.StatusNotFound, "NOT_FOUND", "resource not found", nil)
	case errors.Is(err, apperr.ErrConflict):
		Error(w, http.StatusConflict, "CONFLICT", "request conflicts with current state", nil)
	case errors.Is(err, apperr.ErrInvalidRequest):
		Error(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request", nil)
	case errors.Is(err, apperr.ErrUnauthorized):
		Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", nil)
	case errors.Is(err, apperr.ErrForbidden):
		Error(w, http.StatusForbidden, "FORBIDDEN", "insufficient permissions", nil)
	case errors.Is(err, context.DeadlineExceeded):
		Error(w, http.StatusGatewayTimeout, "TIMEOUT", "request timed out", nil)
	case errors.Is(err, context.Canceled):
		return // client hung up; writing a body is pointless
	default:
		Internal(w, err)
	}
}

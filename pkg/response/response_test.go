package response

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"drivethrough/pkg/apperr"
)

// decode reads the envelope back out of a recorded response.
func decode(t *testing.T, rec *httptest.ResponseRecorder) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decoding response %q: %v", rec.Body.String(), err)
	}
	return env
}

// TestAppErrorUnwrapsThroughWrapping is the regression test for the type
// assertion bug.
//
// AppError used to assert the concrete type directly, so an error wrapped with
// fmt.Errorf("...: %w", err) by any intermediate layer fell through to the
// generic 500 branch. Handlers wrap errors routinely, which turned every
// validation and conflict into an opaque internal error.
func TestAppErrorUnwrapsThroughWrapping(t *testing.T) {
	base := apperr.Conflict("booking already assigned", map[string]string{"driver": "d1"})

	wrapped := fmt.Errorf("accept offer: %w", base)
	doubleWrapped := fmt.Errorf("handler: %w", wrapped)

	for name, err := range map[string]error{
		"bare":          base,
		"wrapped once":  wrapped,
		"wrapped twice": doubleWrapped,
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			AppError(rec, err)

			if rec.Code != http.StatusConflict {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusConflict)
			}
			env := decode(t, rec)
			if env.Success {
				t.Error("success must be false")
			}
			if env.Error == nil {
				t.Fatal("expected an error object")
			}
			if env.Error.Message != "booking already assigned" {
				t.Errorf("message = %q, want the inner message", env.Error.Message)
			}
			if env.Error.Detail == nil {
				t.Error("detail from the inner error must be preserved")
			}
		})
	}
}

// TestAppErrorSentinels covers the bare-sentinel path, where callers return
// errors.New-style sentinels instead of a full AppError.
func TestAppErrorSentinels(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{apperr.ErrNotFound, http.StatusNotFound, "NOT_FOUND"},
		{apperr.ErrConflict, http.StatusConflict, "CONFLICT"},
		{apperr.ErrInvalidRequest, http.StatusBadRequest, "BAD_REQUEST"},
		{apperr.ErrUnauthorized, http.StatusUnauthorized, "UNAUTHORIZED"},
		{apperr.ErrForbidden, http.StatusForbidden, "FORBIDDEN"},
		{context.DeadlineExceeded, http.StatusGatewayTimeout, "TIMEOUT"},
	}

	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			rec := httptest.NewRecorder()
			AppError(rec, tc.err)

			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			env := decode(t, rec)
			if env.Error == nil || env.Error.Code != tc.code {
				t.Errorf("code = %+v, want %q", env.Error, tc.code)
			}
		})
	}
}

// TestAppErrorSentinelsThroughWrapping makes sure the sentinel branch is reached
// even when the sentinel has been wrapped on its way up.
func TestAppErrorSentinelsThroughWrapping(t *testing.T) {
	rec := httptest.NewRecorder()
	AppError(rec, fmt.Errorf("load booking: %w", apperr.ErrNotFound))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestAppErrorUnknownFallsBackToInternal is the important safety property: an
// unrecognised error must never leak its message to the client.
func TestAppErrorUnknownFallsBackToInternal(t *testing.T) {
	rec := httptest.NewRecorder()
	AppError(rec, errors.New("pq: password authentication failed for user drivethrough"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	env := decode(t, rec)
	if env.Error == nil {
		t.Fatal("expected an error object")
	}
	if env.Error.Message == "pq: password authentication failed for user drivethrough" {
		t.Error("the internal error message must not be returned to the client")
	}
}

// TestAppErrorCancelledWritesNoBody covers a client that hung up: attempting to
// write a body is pointless and can corrupt the connection.
func TestAppErrorCancelledWritesNoBody(t *testing.T) {
	rec := httptest.NewRecorder()
	AppError(rec, fmt.Errorf("query: %w", context.Canceled))

	if rec.Body.Len() != 0 {
		t.Errorf("expected an empty body, got %q", rec.Body.String())
	}
}

// TestAppErrorEmptyMessageFallsBackToStatusText ensures a status-only AppError
// still produces a readable body rather than an empty message.
func TestAppErrorEmptyMessageFallsBackToStatusText(t *testing.T) {
	rec := httptest.NewRecorder()
	AppError(rec, &apperr.AppError{Status: http.StatusForbidden, Code: "FORBIDDEN"})

	env := decode(t, rec)
	if env.Error == nil {
		t.Fatal("expected an error object")
	}
	if env.Error.Message == "" {
		t.Error("expected a non-empty fallback message")
	}
}

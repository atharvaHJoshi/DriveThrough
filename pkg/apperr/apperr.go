package apperr

import "errors"

var (
	ErrNotFound          = errors.New("not found")
	ErrConflict          = errors.New("conflict")
	ErrInvalidRequest    = errors.New("invalid request")
	ErrUnauthorized      = errors.New("unauthorized")
	ErrForbidden         = errors.New("forbidden")
	ErrInternal          = errors.New("internal error")
	ErrDriverUnavailable = errors.New("driver unavailable")
	ErrBookingClosed     = errors.New("booking already closed")
)

type AppError struct {
	Status  int
	Code    string
	Message string
	Err     error
	Detail  any
}

// Error renders both the human-facing message and the underlying cause. Only
// returning Err.Error() would hide the message from anyone reading a log line,
// which is usually the only place it shows up.
func (e *AppError) Error() string {
	if e.Err == nil {
		return e.Message
	}
	if e.Message == "" || e.Message == e.Err.Error() {
		return e.Err.Error()
	}
	return e.Message + ": " + e.Err.Error()
}

func (e *AppError) Unwrap() error { return e.Err }

func (e *AppError) StatusCode() int     { return e.Status }
func (e *AppError) ErrorCode() string   { return e.Code }
func (e *AppError) MessageText() string { return e.Message }
func (e *AppError) DetailValue() any    { return e.Detail }

func IsNotFound(err error) bool {
	var ae *AppError
	if errors.As(err, &ae) {
		return ae.Status == 404
	}
	return false
}

// Is reports whether err is, or wraps, an AppError with the given HTTP status.
func Is(err error, status int) bool {
	var ae *AppError
	if errors.As(err, &ae) {
		return ae.Status == status
	}
	return false
}

// IsConflict reports whether err is, or wraps, a 409.
func IsConflict(err error) bool { return Is(err, 409) }

// IsBadRequest reports whether err is, or wraps, a 400.
func IsBadRequest(err error) bool { return Is(err, 400) }

func New(status int, code, message string, detail any) *AppError {
	return &AppError{Status: status, Code: code, Message: message, Detail: detail}
}

func BadRequest(message string, detail any) *AppError {
	return New(400, "BAD_REQUEST", message, detail)
}

func Unauthorized(message string) *AppError {
	return New(401, "UNAUTHORIZED", message, nil)
}

func Forbidden(message string) *AppError {
	return New(403, "FORBIDDEN", message, nil)
}

func NotFound(message string) *AppError {
	return New(404, "NOT_FOUND", message, nil)
}

func Conflict(message string, detail any) *AppError {
	return New(409, "CONFLICT", message, detail)
}

func Wrap(status int, code, message string, err error, detail any) *AppError {
	return &AppError{Status: status, Code: code, Message: message, Err: err, Detail: detail}
}

// IsForbidden reports whether err is a permission error.
func IsForbidden(err error) bool { return Is(err, 403) }

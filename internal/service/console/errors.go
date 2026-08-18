package console

import "fmt"

const (
	// CodeRequestUnavailable identifies an operation that cannot complete temporarily.
	CodeRequestUnavailable = "request_unavailable"
)

// Error is a stable Console application error that is safe to map to HTTP.
// Cause is retained for server-side diagnostics and is never serialized.
type Error struct {
	Code       string
	Message    string
	Param      string
	Status     int
	RetryAfter int
	Cause      error
}

// Error returns the safe public message, falling back to the stable code.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

// Unwrap exposes the internal cause for errors.Is and errors.As.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// RequestUnavailable wraps an internal failure without exposing its source.
func RequestUnavailable(operation string, cause error) *Error {
	return &Error{
		Code:    CodeRequestUnavailable,
		Message: "The request could not be completed. Please try again later.",
		Status:  503,
		Cause:   fmt.Errorf("%s: %w", operation, cause),
	}
}

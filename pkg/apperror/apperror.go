// Package apperror defines typed domain errors that service-layer code
// returns. Handlers map these to HTTP status codes via Code.HTTPStatus(),
// keeping HTTP concerns out of the service layer entirely (Clean
// Architecture boundary: services return domain errors, handlers translate
// them to transport-layer responses).
package apperror

import (
	"errors"
	"fmt"
	"net/http"
)

// Code is a machine-readable error classification, also used verbatim as
// the "code" field in the API error envelope (pkg/apiresponse).
type Code string

const (
	CodeValidation     Code = "VALIDATION_ERROR"
	CodeUnauthorized   Code = "UNAUTHORIZED"
	CodeForbidden      Code = "FORBIDDEN"
	CodeNotFound       Code = "NOT_FOUND"
	CodeConflict       Code = "CONFLICT"
	CodeRateLimited    Code = "RATE_LIMITED"
	CodeInternal       Code = "INTERNAL_ERROR"
	CodeTenantMismatch Code = "TENANT_MISMATCH"
	CodeUnprocessable  Code = "UNPROCESSABLE"
)

// HTTPStatus maps a Code to the HTTP status that should be returned for it.
func (c Code) HTTPStatus() int {
	switch c {
	case CodeValidation, CodeUnprocessable:
		return http.StatusBadRequest
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden, CodeTenantMismatch:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict:
		return http.StatusConflict
	case CodeRateLimited:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// Error is the concrete error type carried through service-layer returns.
type Error struct {
	Code    Code
	Message string
	Err     error // wrapped cause, for logging; never serialized to clients
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// New creates an *Error with no wrapped cause.
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Wrap creates an *Error wrapping an underlying cause (e.g. a database
// error), which is preserved for logging but never sent to the client.
func Wrap(code Code, message string, err error) *Error {
	return &Error{Code: code, Message: message, Err: err}
}

// As extracts an *Error from err via errors.As, returning ok=false if err is
// not (or does not wrap) an *Error.
func As(err error) (*Error, bool) {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr, true
	}
	return nil, false
}

// Package httpx holds the small set of HTTP response helpers shared by
// internal/api's server (auth/CORS middleware) and its handlers package —
// factored out into their own leaf package specifically so neither of
// those two needs to import the other just to format an error response.
package httpx

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5/middleware"
)

// ErrorResponse is the standard error body every management API endpoint
// returns, per spec/10-api.md §4.
type ErrorResponse struct {
	Error     string            `json:"error"`
	Code      string            `json:"code"`
	RequestID string            `json:"request_id"`
	Details   map[string]string `json:"details,omitempty"`
}

// WriteJSON writes v as a JSON response body with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes a standard error response, deriving the machine-readable
// code from status the same way the proxy does (spec/10-api.md §4: "HTTP
// status → code mapping is identical to the proxy error mapping").
func WriteError(w http.ResponseWriter, r *http.Request, status int, message string) {
	WriteJSON(w, status, ErrorResponse{
		Error:     message,
		Code:      CodeForStatus(status),
		RequestID: middleware.GetReqID(r.Context()),
	})
}

// WriteValidationError writes a 400 with per-field validation messages.
func WriteValidationError(w http.ResponseWriter, r *http.Request, details map[string]string) {
	WriteJSON(w, http.StatusBadRequest, ErrorResponse{
		Error:     "validation failed",
		Code:      "VALIDATION_ERROR",
		RequestID: middleware.GetReqID(r.Context()),
		Details:   details,
	})
}

// CodeForStatus maps an HTTP status to Conduit's machine-readable error
// code — the same mapping internal/proxy uses, so a client sees identical
// codes whether the error came from the proxy or the management API.
func CodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "BAD_REQUEST"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusTooManyRequests:
		return "RATE_LIMITED"
	case http.StatusServiceUnavailable:
		return "SERVICE_UNAVAILABLE"
	default:
		return "INTERNAL_ERROR"
	}
}

// DefaultLimit and MaxLimit bound every paginated list endpoint per
// spec/10-api.md §5.
const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// Pagination holds the parsed limit/offset query parameters.
type Pagination struct {
	Limit  int
	Offset int
}

// ParsePagination reads ?limit= and ?offset= from r, applying the default
// and rejecting a limit over MaxLimit (spec/10-api.md §5: "return 400 if
// exceeded").
func ParsePagination(r *http.Request) (Pagination, error) {
	p := Pagination{Limit: DefaultLimit}

	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return p, errInvalidLimit
		}
		if n > MaxLimit {
			return p, errLimitTooLarge
		}
		p.Limit = n
	}

	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return p, errInvalidOffset
		}
		p.Offset = n
	}

	return p, nil
}

var (
	errInvalidLimit  = ValidationErr("limit", "must be a positive integer")
	errLimitTooLarge = ValidationErr("limit", "must not exceed 500")
	errInvalidOffset = ValidationErr("offset", "must be a non-negative integer")
)

// FieldError pairs a field name with why it failed validation, so callers
// can build the "details" map spec/10-api.md §4 documents without
// hand-rolling map[string]string{...} everywhere.
type FieldError struct {
	Field   string
	Message string
}

// ValidationErr wraps a single field error as a Go error, letting
// ParsePagination (and handlers) return it through a normal error return
// value while still carrying the structured field/message pair callers
// need to build a details map.
func ValidationErr(field, message string) error {
	return &FieldError{Field: field, Message: message}
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

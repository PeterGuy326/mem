// Package apiclient is the in-process HTTP client for memd.
//
// Both the `mem` CLI and the `mem-mcp` MCP server depend on this package so
// that a single network/auth/error surface is shared across user-facing
// entrypoints. CLI maps the typed errors here to its exit-code convention
// (SPEC §7.1); the MCP server maps them to MCP error responses.
package apiclient

import (
	"fmt"
	"net/http"
)

// ErrorKind is a coarse category derived from HTTP status — callers translate
// it into their own surface (CLI exit code, MCP error code, etc).
type ErrorKind int

const (
	KindOther    ErrorKind = iota
	KindAuth               // 401 / 403
	KindNotFound           // 404
	KindConflict           // 409
	KindPlan               // 402
	KindQuota              // 429
	KindProvider           // 502 / 503
	KindTimeout            // 504
	KindBadInput           // 400
)

// APIError preserves the {error, hint} pair that memd returns on non-2xx
// responses, together with the underlying HTTP status code.
type APIError struct {
	StatusCode         int
	Code               string // machine-readable, e.g. "not_found", "missing_bearer"
	Message            string
	Hint               string
	Conflicts          []WorkspaceImportConflict
	ConflictTotal      int
	ConflictsTruncated bool
}

func (e *APIError) Error() string {
	if e.Hint != "" {
		return fmt.Sprintf("%s (HTTP %d): %s", e.Message, e.StatusCode, e.Hint)
	}
	return fmt.Sprintf("%s (HTTP %d)", e.Message, e.StatusCode)
}

// Kind returns a typed category for caller-side dispatch.
func (e *APIError) Kind() ErrorKind {
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return KindAuth
	case http.StatusNotFound, http.StatusGone:
		return KindNotFound
	case http.StatusConflict:
		return KindConflict
	case http.StatusPaymentRequired:
		return KindPlan
	case http.StatusTooManyRequests:
		return KindQuota
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return KindProvider
	case http.StatusGatewayTimeout:
		return KindTimeout
	case http.StatusBadRequest:
		return KindBadInput
	}
	return KindOther
}

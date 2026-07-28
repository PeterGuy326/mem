package workspacetransfer

import (
	"errors"
	"fmt"
)

var (
	ErrNotConfigured     = errors.New("workspace transfer is not configured")
	ErrWorkspaceNotFound = errors.New("workspace not found")
	ErrUnsupportedMode   = errors.New("unsupported workspace restore mode")
	ErrConflict          = errors.New("workspace import conflict")
	ErrIntegrity         = errors.New("workspace transfer integrity failure")
	// ErrCommitIndeterminate means the durable ledger could not prove whether
	// PostgreSQL committed. Callers should retry the exact same bundle.
	ErrCommitIndeterminate = errors.New(
		"workspace import commit outcome is indeterminate",
	)
)

type Conflict struct {
	Kind     string `json:"kind"`
	Resource string `json:"resource,omitempty"`
	Value    string `json:"value,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// ConflictError reports a bounded, stable sample of collisions found during
// the read-only preflight. Total is exact when Truncated is false. When
// Truncated is true, Total is the number of distinct conflicts confirmed
// before preflight short-circuited and is therefore a lower bound.
//
// Conflicts remains the backwards-compatible detail field. Callers should not
// assume it contains more than MaxConflictDetails entries.
type ConflictError struct {
	Conflicts []Conflict `json:"conflicts"`
	Total     int        `json:"total,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

func (e *ConflictError) Error() string {
	if e == nil || len(e.Conflicts) == 0 {
		return ErrConflict.Error()
	}
	return fmt.Sprintf("%s: %s", ErrConflict, e.Conflicts[0].Kind)
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

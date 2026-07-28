package workspacebundle

import "errors"

var (
	// ErrInvalidBundle identifies a structurally or semantically invalid v1
	// bundle.
	ErrInvalidBundle = errors.New("invalid workspace bundle")
	// ErrUnsupportedVersion identifies a valid-looking bundle contract that
	// this implementation must not interpret.
	ErrUnsupportedVersion = errors.New("unsupported workspace bundle version")
	// ErrUnsafeArchive identifies archive paths or entry types that could write
	// outside the intended extraction namespace.
	ErrUnsafeArchive = errors.New("unsafe workspace bundle archive")
	// ErrLimitExceeded identifies a bundle rejected before expensive parsing or
	// allocation because it exceeds configured resource limits.
	ErrLimitExceeded = errors.New("workspace bundle limit exceeded")
	// ErrIntegrity identifies checksum, content hash, or immutable payload hash
	// mismatches.
	ErrIntegrity = errors.New("workspace bundle integrity check failed")
	// ErrDependency identifies a missing or inconsistent cross-record edge.
	ErrDependency = errors.New("workspace bundle dependency check failed")
)

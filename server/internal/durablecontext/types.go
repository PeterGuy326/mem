// Package durablecontext owns the scoped durable-context contract (mem#70).
//
// A digital employee may resume context across sessions and channels only
// through an explicit, operator-owned recall allowlist. This package never
// reads another principal's grants, never shares storage with consumers, and
// never calls a model: recall is a deterministic projection over approved,
// active structured memories with stable locators and provenance.
package durablecontext

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/memory"
)

// ContractVersion pins the wire contract. Recall and get refuse any other
// value so incompatible clients fail clearly instead of receiving data under
// semantics they did not negotiate.
const ContractVersion = "durable-context.v1"

// ModeRead is the only grant mode. The contract is read-only by design.
const ModeRead = "read"

// PrincipalPattern mirrors the employee/principal identity shape accepted by
// the counterpart conformance contract: a lowercase alphanumeric lead char
// followed by at most 127 dotted/underscored/dashed characters.
const PrincipalPattern = `^[a-z0-9][a-z0-9._-]{0,127}$`

var (
	// ErrInvalidCommand identifies a validation error in a command or query.
	ErrInvalidCommand = errors.New("invalid durable context command")
	// ErrScopeDenied means the principal holds no unrevoked grant for the
	// requested recall. It is the explicit cross-principal denial.
	ErrScopeDenied = errors.New("durable context scope denied")
	// ErrNotFound covers absent grants and, per F5A.5, out-of-scope or
	// unapproved objects that must behave as nonexistent.
	ErrNotFound = errors.New("durable context grant not found")
	// ErrForgotten means the granted memory payload has been irreversibly
	// redacted; the grant can never produce content again.
	ErrForgotten = errors.New("memory forgotten")
	// ErrStale means the granted memory is archived (superseded): the grant
	// remains auditable but recall must not resume the old state silently.
	ErrStale = errors.New("durable context grant stale")
)

// Grant is one explicit recall approval for (workspace, principal, memory).
// Token identifiers stay server-internal and are excluded from public JSON.
type Grant struct {
	ID               uuid.UUID  `json:"id"`
	WorkspaceID      uuid.UUID  `json:"workspace_id"`
	Principal        string     `json:"principal"`
	MemoryID         uuid.UUID  `json:"memory_id"`
	Mode             string     `json:"mode"`
	GrantedByUserID  *uuid.UUID `json:"granted_by_user_id,omitempty"`
	GrantedByTokenID *uuid.UUID `json:"-"`
	GrantedAt        time.Time  `json:"granted_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevokedByUserID  *uuid.UUID `json:"-"`
	RevokedByTokenID *uuid.UUID `json:"-"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// Active reports whether the grant currently authorizes recall.
func (g Grant) Active() bool {
	return g.RevokedAt == nil
}

// GrantCommand creates or re-activates one explicit read grant. Re-granting a
// revoked triple is an idempotent upsert, never a duplicate row.
type GrantCommand struct {
	WorkspaceID  uuid.UUID
	Principal    string
	MemoryID     uuid.UUID
	ActorUserID  *uuid.UUID
	ActorTokenID *uuid.UUID
}

// RevokeCommand soft-revokes one grant so the audit row survives.
type RevokeCommand struct {
	WorkspaceID  uuid.UUID
	GrantID      uuid.UUID
	ActorUserID  *uuid.UUID
	ActorTokenID *uuid.UUID
}

// ListGrantsQuery lists grants for one workspace, optionally narrowed to a
// single principal. Limit is clamped by the service.
type ListGrantsQuery struct {
	WorkspaceID uuid.UUID
	Principal   string
	Limit       int
}

// RecallQuery is the read-only resume operation. SessionRef is metadata that
// lets one principal resume from any session/channel; allowlists are keyed by
// principal, never by implicit chat capture.
type RecallQuery struct {
	WorkspaceID  uuid.UUID
	Principal    string
	SessionRef   string
	AllowedPaths []string
	Limit        int
}

// GetQuery resolves one granted memory while enforcing principal and path
// scope.
type GetQuery struct {
	WorkspaceID  uuid.UUID
	Principal    string
	MemoryID     uuid.UUID
	AllowedPaths []string
}

// RecallHit is one resumable memory with its version-pinned locator and
// public provenance.
type RecallHit struct {
	Memory       memory.Memory     `json:"memory"`
	Locator      string            `json:"locator"`
	StateVersion int64             `json:"state_version"`
	Provenance   memory.Provenance `json:"provenance"`
}

// RecallResult is the contract envelope returned by recall.
type RecallResult struct {
	Contract  string      `json:"contract"`
	Principal string      `json:"principal"`
	Hits      []RecallHit `json:"hits"`
}

// Locator is the stable, version-pinned URI for one resumable memory state.
func Locator(memoryID uuid.UUID, stateVersion int64) string {
	return fmt.Sprintf("mem://memories/%s@%d", memoryID, stateVersion)
}

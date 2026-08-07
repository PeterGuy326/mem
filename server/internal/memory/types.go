// Package memory owns durable, model-independent Agent memory records.
//
// A record is an occurrence, not a canonicalized fact: equal content written
// under two idempotency keys intentionally produces two records.  The service
// provides deterministic lexical recall and never calls an embedding or chat
// model.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrInvalidCommand identifies a validation error in a command or query.
	ErrInvalidCommand = errors.New("invalid memory command")
	// ErrIdempotencyConflict means a key was previously committed with a
	// different normalized payload.
	ErrIdempotencyConflict = errors.New("memory idempotency conflict")
	// ErrNotFound deliberately covers both absent and out-of-scope records.
	ErrNotFound = errors.New("memory not found")
	// ErrForgotten means the caller was authorized for a memory tombstone, but
	// the user payload has been irreversibly redacted from the live database.
	ErrForgotten = errors.New("memory forgotten")
	// ErrVersionConflict prevents two control-plane writers from silently
	// overwriting one another.
	ErrVersionConflict = errors.New("memory state version conflict")
	// ErrInvalidTransition identifies an action that is not valid from the
	// memory's current lifecycle/control state.
	ErrInvalidTransition = errors.New("invalid memory state transition")
	// ErrInvalidCursor identifies a malformed, stale, or filter-mismatched list
	// cursor. Cursors are opaque to adapters and bound to authorization filters.
	ErrInvalidCursor = errors.New("invalid memory list cursor")
)

const (
	KindObservation = "observation"
	KindDecision    = "decision"
	KindPreference  = "preference"
	KindTaskState   = "task_state"
	KindFact        = "fact"
	KindNote        = "note"
	KindArtifact    = "artifact"
	// KindForgotten is a storage-only discriminator for redacted tombstones.
	// Remember never accepts it as caller input.
	KindForgotten = "forgotten"

	StatusActive    = "active"
	StatusArchived  = "archived"
	StatusForgotten = "forgotten"

	FeedbackPin       = "pin"
	FeedbackUnpin     = "unpin"
	FeedbackUseful    = "useful"
	FeedbackNotUseful = "not_useful"

	ForgetReasonUserRequest = "user_request"
	ForgetReasonIncorrect   = "incorrect"
	ForgetReasonSensitive   = "sensitive"
	ForgetReasonExpired     = "expired"
	ForgetReasonOther       = "other"
)

// Service persists and recalls structured Agent memory.
type Service struct {
	pool *pgxpool.Pool
}

// New constructs a structured-memory service over pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Command is the canonical write contract for one immutable occurrence.
type Command struct {
	WorkspaceID      uuid.UUID
	CreatedByUserID  *uuid.UUID
	CreatedByTokenID *uuid.UUID
	// AllowedPaths is an authorization boundary, not request payload. Empty or
	// ["/"] means unrestricted. It is intentionally excluded from replay hashes.
	AllowedPaths     []string
	Kind             string
	Content          string
	Attributes       json.RawMessage
	Path             string
	EventAt          *time.Time
	SourceType       string
	SourceRef        string
	SourceFileID     *uuid.UUID
	SourceFileSHA256 string
	SourceLocator    json.RawMessage
	ProducerAgent    string
	ProducerSession  string
	ProducerTask     string
	IdempotencyKey   string
}

// Query resolves one known memory while enforcing workspace and path scope.
// Empty Scope and an empty AllowedPaths list both mean root/unrestricted.
type Query struct {
	WorkspaceID  uuid.UUID
	MemoryID     uuid.UUID
	Scope        string
	AllowedPaths []string
}

// RecallQuery describes deterministic lexical retrieval.
type RecallQuery struct {
	WorkspaceID        uuid.UUID
	Text               string
	Scope              string
	AllowedPaths       []string
	Since              *time.Time
	Until              *time.Time
	Kinds              []string
	LifecycleStatus    string
	IncludeSuperseded  bool
	Limit              int
}

// ListQuery describes stable keyset pagination over memory records. Recursive
// defaults to true when nil. LifecycleStatuses defaults to [active].
type ListQuery struct {
	WorkspaceID       uuid.UUID
	Scope             string
	Recursive         *bool
	AllowedPaths      []string
	Kinds             []string
	LifecycleStatuses []string
	Pinned            *bool
	Limit             int
	Cursor            string
}

// ListResult is the canonical paginated memory response.
type ListResult struct {
	Memories   []MemorySummary `json:"memories"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// FeedbackCommand records one explicit, auditable feedback signal.
type FeedbackCommand struct {
	WorkspaceID     uuid.UUID
	MemoryID        uuid.UUID
	AllowedPaths    []string
	ActorUserID     *uuid.UUID
	ActorTokenID    *uuid.UUID
	Action          string
	IdempotencyKey  string
	ExpectedVersion int64
}

// LifecycleCommand drives reversible archive/restore state transitions.
type LifecycleCommand struct {
	WorkspaceID     uuid.UUID
	MemoryID        uuid.UUID
	AllowedPaths    []string
	ActorUserID     *uuid.UUID
	ActorTokenID    *uuid.UUID
	IdempotencyKey  string
	ExpectedVersion int64
}

// ForgetCommand performs an irreversible live-payload redaction while keeping
// the minimum tombstone needed for retry safety and audit.
type ForgetCommand struct {
	LifecycleCommand
	Reason string
}

// Memory is one durable memory occurrence.
//
// Token and idempotency internals remain available to trusted server code for
// auditing and replay checks, but are intentionally excluded from public JSON.
type Memory struct {
	ID                   uuid.UUID       `json:"id"`
	WorkspaceID          uuid.UUID       `json:"workspace_id"`
	CreatedByUserID      *uuid.UUID      `json:"created_by_user_id,omitempty"`
	CreatedByTokenID     *uuid.UUID      `json:"-"`
	Kind                 string          `json:"kind"`
	Content              string          `json:"content"`
	Attributes           json.RawMessage `json:"attributes"`
	Path                 string          `json:"path"`
	EventAt              *time.Time      `json:"event_at,omitempty"`
	SourceType           string          `json:"source_type"`
	SourceRef            string          `json:"source_ref,omitempty"`
	SourceFileID         *uuid.UUID      `json:"source_file_id,omitempty"`
	SourceFileSHA256     string          `json:"source_file_sha256,omitempty"`
	SourceLocator        json.RawMessage `json:"source_locator"`
	ProducerAgent        string          `json:"producer_agent,omitempty"`
	ProducerSession      string          `json:"producer_session,omitempty"`
	ProducerTask         string          `json:"producer_task,omitempty"`
	IdempotencyKeySHA256 string          `json:"-"`
	RequestSHA256        string          `json:"-"`
	ContentSHA256        string          `json:"content_sha256"`
	LifecycleStatus      string          `json:"lifecycle_status"`
	StateVersion         int64           `json:"state_version"`
	Pinned               bool            `json:"pinned"`
	PinnedAt             *time.Time      `json:"pinned_at,omitempty"`
	UsefulCount          int             `json:"useful_count"`
	NotUsefulCount       int             `json:"not_useful_count"`
	FeedbackScore        int             `json:"feedback_score"`
	FeedbackCount        int             `json:"feedback_count"`
	FeedbackAt           *time.Time      `json:"feedback_at,omitempty"`
	ForgottenAt          *time.Time      `json:"forgotten_at,omitempty"`
	ForgottenByUserID    *uuid.UUID      `json:"-"`
	ForgottenByTokenID   *uuid.UUID      `json:"-"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

// MemorySummary is the bounded list projection. It deliberately omits full
// content, attributes, and source locators so one page cannot amplify up to
// 100 * 64 KiB records. Excerpt is capped at 500 Unicode code points by SQL.
type MemorySummary struct {
	ID               uuid.UUID  `json:"id"`
	WorkspaceID      uuid.UUID  `json:"workspace_id"`
	Kind             string     `json:"kind"`
	Excerpt          string     `json:"excerpt"`
	ContentLength    int        `json:"content_length"`
	Path             string     `json:"path"`
	EventAt          *time.Time `json:"event_at,omitempty"`
	SourceType       string     `json:"source_type"`
	SourceRef        string     `json:"source_ref,omitempty"`
	SourceFileID     *uuid.UUID `json:"source_file_id,omitempty"`
	SourceFileSHA256 string     `json:"source_file_sha256,omitempty"`
	ProducerAgent    string     `json:"producer_agent,omitempty"`
	ProducerSession  string     `json:"producer_session,omitempty"`
	ProducerTask     string     `json:"producer_task,omitempty"`
	ContentSHA256    string     `json:"content_sha256"`
	LifecycleStatus  string     `json:"lifecycle_status"`
	StateVersion     int64      `json:"state_version"`
	Pinned           bool       `json:"pinned"`
	PinnedAt         *time.Time `json:"pinned_at,omitempty"`
	UsefulCount      int        `json:"useful_count"`
	NotUsefulCount   int        `json:"not_useful_count"`
	FeedbackScore    int        `json:"feedback_score"`
	FeedbackCount    int        `json:"feedback_count"`
	FeedbackAt       *time.Time `json:"feedback_at,omitempty"`
	Citation         string     `json:"citation"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// Citation is the stable URI used by Context Packs and external Agents.
func (m Memory) Citation() string {
	return fmt.Sprintf("mem://memories/%s", m.ID)
}

// Provenance is the public, source-verifiable origin of a recalled record.
// Authentication token identifiers and idempotency hashes are intentionally
// excluded.
type Provenance struct {
	WorkspaceID      uuid.UUID       `json:"workspace_id"`
	CreatedByUserID  *uuid.UUID      `json:"created_by_user_id,omitempty"`
	EventAt          *time.Time      `json:"event_at,omitempty"`
	SourceType       string          `json:"source_type"`
	SourceRef        string          `json:"source_ref,omitempty"`
	SourceFileID     *uuid.UUID      `json:"source_file_id,omitempty"`
	SourceFileSHA256 string          `json:"source_file_sha256,omitempty"`
	SourceLocator    json.RawMessage `json:"source_locator"`
	ProducerAgent    string          `json:"producer_agent,omitempty"`
	ProducerSession  string          `json:"producer_session,omitempty"`
	ProducerTask     string          `json:"producer_task,omitempty"`
}

// Provenance returns the public provenance projection of m.
func (m Memory) Provenance() Provenance {
	return Provenance{
		WorkspaceID:      m.WorkspaceID,
		CreatedByUserID:  m.CreatedByUserID,
		EventAt:          m.EventAt,
		SourceType:       m.SourceType,
		SourceRef:        m.SourceRef,
		SourceFileID:     m.SourceFileID,
		SourceFileSHA256: m.SourceFileSHA256,
		SourceLocator:    cloneJSON(m.SourceLocator),
		ProducerAgent:    m.ProducerAgent,
		ProducerSession:  m.ProducerSession,
		ProducerTask:     m.ProducerTask,
	}
}

// RememberResult distinguishes a new commit from an idempotent replay.
type RememberResult struct {
	Memory   Memory `json:"memory"`
	Replayed bool   `json:"replayed"`
}

// MemoryEvent is an append-only audit record for control-plane mutations.
// Forget privacy-redacts actor columns on the target's existing events while
// preserving their action/version history. Token IDs, idempotency keys,
// request hashes, and replay receipts remain server-internal.
type MemoryEvent struct {
	ID                    uuid.UUID  `json:"id"`
	WorkspaceID           uuid.UUID  `json:"workspace_id"`
	MemoryID              uuid.UUID  `json:"memory_id"`
	Action                string     `json:"action"`
	ActorUserID           *uuid.UUID `json:"actor_user_id,omitempty"`
	ActorTokenID          *uuid.UUID `json:"-"`
	IdempotencyKeySHA256  string     `json:"-"`
	RequestSHA256         string     `json:"-"`
	ReplayPrincipalSHA256 string     `json:"-"`
	ExpectedVersion       int64      `json:"expected_version"`
	ResultingVersion      int64      `json:"resulting_version"`
	Reason                string     `json:"reason,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
}

// MutationResult distinguishes a new control mutation from an idempotent
// replay. Memory reflects the current row at transaction time.
type MutationResult struct {
	Memory   Memory      `json:"memory"`
	Event    MemoryEvent `json:"event"`
	Replayed bool        `json:"replayed"`
}

// Tombstone is the only public projection returned by Forget. It contains no
// remembered content, attributes, source locator, or producer identifiers.
type Tombstone struct {
	ID              uuid.UUID  `json:"id"`
	LifecycleStatus string     `json:"lifecycle_status"`
	StateVersion    int64      `json:"state_version"`
	ForgottenAt     *time.Time `json:"forgotten_at,omitempty"`
}

// ForgetResult is retry-safe while never returning the redacted payload.
type ForgetResult struct {
	Tombstone Tombstone   `json:"tombstone"`
	Event     MemoryEvent `json:"event"`
	Replayed  bool        `json:"replayed"`
}

// RecallHit contains evidence plus an explicit deterministic retrieval reason.
type RecallHit struct {
	Memory     Memory     `json:"memory"`
	Citation   string     `json:"citation"`
	Reason     string     `json:"reason"`
	Score      float64    `json:"score"`
	Provenance Provenance `json:"provenance"`
}

// Recorder is the narrow write port useful to adapters and tests.
type Recorder interface {
	Remember(context.Context, Command) (*RememberResult, error)
}

func cloneJSON(in json.RawMessage) json.RawMessage {
	if in == nil {
		return nil
	}
	return append(json.RawMessage(nil), in...)
}

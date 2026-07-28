// Package handoff owns the versioned, model-independent task checkpoint
// contract used to resume work across Agent hosts.
package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	ContractName       = "mem.handoff"
	ResumeContractName = "mem.resume"
	SchemaVersionV1    = 1

	CheckpointKindCheckpoint = "checkpoint"
	CheckpointKindHandoff    = "handoff"

	TaskStatusInProgress = "in_progress"
	TaskStatusReady      = "ready"
	TaskStatusBlocked    = "blocked"
	TaskStatusComplete   = "complete"
)

var (
	ErrInvalidCommand      = errors.New("invalid handoff command")
	ErrUnsupportedVersion  = errors.New("unsupported handoff schema version")
	ErrIdempotencyConflict = errors.New("handoff idempotency conflict")
	ErrBaseRequired        = errors.New("base checkpoint is required")
	ErrHeadConflict        = errors.New("checkpoint head conflict")
	// ErrNotFound deliberately covers absent, cross-workspace, and out-of-path
	// records so callers cannot use this service as an authorization oracle.
	ErrNotFound = errors.New("handoff not found")
)

// HandoffV1 mirrors docs/schemas/handoff.v1.schema.json. Keep this type strict:
// a future incompatible contract gets a new Go type and validator rather than
// weakening v1 with an unstructured extensions map.
type HandoffV1 struct {
	Contract         string     `json:"contract"`
	SchemaVersion    int        `json:"schema_version"`
	CheckpointKind   string     `json:"checkpoint_kind"`
	TaskKey          string     `json:"task_key"`
	BaseCheckpointID *uuid.UUID `json:"base_checkpoint_id,omitempty"`
	ScopePath        string     `json:"scope_path"`
	State            StateV1    `json:"state"`
	Producer         ProducerV1 `json:"producer"`
}

type StateV1 struct {
	Status         string          `json:"status"`
	Goal           string          `json:"goal"`
	Progress       ProgressV1      `json:"progress"`
	Decisions      []DecisionV1    `json:"decisions"`
	NextSteps      []NextStepV1    `json:"next_steps"`
	Blockers       []BlockerV1     `json:"blockers"`
	OpenQuestions  []string        `json:"open_questions"`
	Artifacts      []ArtifactV1    `json:"artifacts"`
	WorkspaceState *WorkspaceState `json:"workspace_state,omitempty"`
}

type ProgressV1 struct {
	Summary   string   `json:"summary"`
	Completed []string `json:"completed"`
}

type DecisionV1 struct {
	Summary    string   `json:"summary"`
	Rationale  string   `json:"rationale,omitempty"`
	References []string `json:"references"`
}

type NextStepV1 struct {
	Summary    string   `json:"summary"`
	References []string `json:"references"`
}

type BlockerV1 struct {
	Summary    string   `json:"summary"`
	Needs      string   `json:"needs,omitempty"`
	References []string `json:"references"`
}

// Required is a pointer so strict decoding can distinguish an omitted required
// JSON property from an explicitly false value.
type ArtifactV1 struct {
	URI      string `json:"uri"`
	Role     string `json:"role,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Required *bool  `json:"required"`
}

type WorkspaceState struct {
	WorkingDirectory string    `json:"working_directory,omitempty"`
	VCS              *VCSState `json:"vcs,omitempty"`
}

type VCSState struct {
	Revision      string `json:"revision,omitempty"`
	Branch        string `json:"branch,omitempty"`
	Dirty         bool   `json:"dirty,omitempty"`
	StatusSummary string `json:"status_summary,omitempty"`
}

type ProducerV1 struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id,omitempty"`
}

// CheckpointCommand combines authenticated server provenance with an
// untrusted, versioned handoff payload. TaskKey must equal Handoff.TaskKey.
type CheckpointCommand struct {
	WorkspaceID      uuid.UUID
	CreatedByUserID  *uuid.UUID
	CreatedByTokenID *uuid.UUID
	TaskKey          string
	Handoff          HandoffV1
	IdempotencyKey   string
}

type Task struct {
	ID               uuid.UUID  `json:"id"`
	WorkspaceID      uuid.UUID  `json:"workspace_id"`
	TaskKey          string     `json:"task_key"`
	ScopePath        string     `json:"scope_path"`
	HeadCheckpointID *uuid.UUID `json:"head_checkpoint_id,omitempty"`
	HeadSequence     int64      `json:"head_sequence"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type Reference struct {
	CheckpointID   uuid.UUID       `json:"checkpoint_id"`
	Ordinal        int             `json:"ordinal"`
	Relation       string          `json:"relation"`
	URI            string          `json:"uri"`
	ExpectedSHA256 string          `json:"expected_sha256,omitempty"`
	Required       bool            `json:"required"`
	Metadata       json.RawMessage `json:"metadata"`
}

type CheckpointRecord struct {
	ID               uuid.UUID   `json:"id"`
	WorkspaceID      uuid.UUID   `json:"workspace_id"`
	TaskID           uuid.UUID   `json:"task_id"`
	TaskKey          string      `json:"task_key"`
	Sequence         int64       `json:"sequence"`
	CheckpointKind   string      `json:"checkpoint_kind"`
	Contract         string      `json:"contract"`
	SchemaVersion    int         `json:"schema_version"`
	BaseCheckpointID *uuid.UUID  `json:"base_checkpoint_id,omitempty"`
	ScopePath        string      `json:"scope_path"`
	Handoff          HandoffV1   `json:"handoff"`
	PayloadSHA256    string      `json:"payload_sha256"`
	CreatedByUserID  *uuid.UUID  `json:"created_by_user_id,omitempty"`
	CreatedByTokenID *uuid.UUID  `json:"-"`
	ProducerAgent    string      `json:"producer_agent"`
	ProducerSession  string      `json:"producer_session,omitempty"`
	CreatedAt        time.Time   `json:"created_at"`
	References       []Reference `json:"references"`

	// Server-only replay evidence is intentionally excluded from public JSON.
	IdempotencyKey string `json:"-"`
	RequestSHA256  string `json:"-"`
}

// CheckpointSummary is the bounded list projection for immutable checkpoint
// history. The full handoff payload and evidence references are available only
// through GetCheckpoint or Resume, preventing a list page from amplifying up
// to 200 large Agent payloads into one response.
type CheckpointSummary struct {
	ID               uuid.UUID  `json:"id"`
	WorkspaceID      uuid.UUID  `json:"workspace_id"`
	TaskID           uuid.UUID  `json:"task_id"`
	TaskKey          string     `json:"task_key"`
	Sequence         int64      `json:"sequence"`
	CheckpointKind   string     `json:"checkpoint_kind"`
	Contract         string     `json:"contract"`
	SchemaVersion    int        `json:"schema_version"`
	BaseCheckpointID *uuid.UUID `json:"base_checkpoint_id,omitempty"`
	ScopePath        string     `json:"scope_path"`
	Status           string     `json:"status"`
	ProgressExcerpt  string     `json:"progress_excerpt"`
	ProgressLength   int        `json:"progress_length"`
	CompletedCount   int        `json:"completed_count"`
	ReferenceCount   int        `json:"reference_count"`
	PayloadSHA256    string     `json:"payload_sha256"`
	ProducerAgent    string     `json:"producer_agent"`
	ProducerSession  string     `json:"producer_session,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

type CheckpointResult struct {
	Checkpoint CheckpointRecord `json:"checkpoint"`
	Replayed   bool             `json:"replayed"`
}

type ResumeQuery struct {
	WorkspaceID  uuid.UUID
	TaskKey      string
	CheckpointID *uuid.UUID
	Scope        string
	AllowedPaths []string

	// Focus and budgets are accepted as surface hints for forward-compatible
	// adapters. The v1 persistence service performs deterministic snapshot
	// resolution and does not use them for search or model calls.
	Focus    string
	Limit    int
	MaxChars int
}

type ResumeSnapshot struct {
	Contract      string           `json:"contract"`
	SchemaVersion int              `json:"schema_version"`
	Task          Task             `json:"task"`
	Checkpoint    CheckpointRecord `json:"checkpoint"`
	References    []Reference      `json:"references"`
	RetrievedAt   time.Time        `json:"retrieved_at"`
}

type GetCheckpointQuery struct {
	WorkspaceID  uuid.UUID
	CheckpointID uuid.UUID
	TaskKey      string
	Scope        string
	AllowedPaths []string
}

type ListTasksQuery struct {
	WorkspaceID  uuid.UUID
	Scope        string
	AllowedPaths []string
	Limit        int
	After        *uuid.UUID
}

type ListCheckpointsQuery struct {
	WorkspaceID  uuid.UUID
	TaskKey      string
	Scope        string
	AllowedPaths []string
	Limit        int
	Before       *int64
}

// ServicePort is the narrow domain surface used by HTTP adapters and tests.
type ServicePort interface {
	Checkpoint(context.Context, CheckpointCommand) (*CheckpointResult, error)
	Resume(context.Context, ResumeQuery) (*ResumeSnapshot, error)
	GetCheckpoint(context.Context, GetCheckpointQuery) (*CheckpointRecord, error)
	ListTasks(context.Context, ListTasksQuery) ([]Task, error)
	ListCheckpoints(context.Context, ListCheckpointsQuery) ([]CheckpointSummary, error)
}

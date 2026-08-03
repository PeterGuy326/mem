// Package indexgeneration owns the durable lifecycle for rebuilding derived
// vector indexes without replacing the currently searchable corpus.
//
// This package is deliberately model-independent. It persists pinned routing
// identities, bounded progress and audit events; a separate Worker adapter
// supplies vectors and a separate search change opts into the active tables.
package indexgeneration

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	StateBuilding  = "building"
	StateCancelled = "cancelled"
	StateFailed    = "failed"
	StateReady     = "ready"
	StateActive    = "active"
	StateInactive  = "inactive"
	StateDiscarded = "discarded"

	TargetPending    = "pending"
	TargetProcessing = "processing"
	TargetSucceeded  = "succeeded"
	TargetSkipped    = "skipped"
	TargetFailed     = "failed"

	RouteText   = "text"
	RouteVisual = "visual"
)

var (
	ErrUnavailable        = errors.New("index generation store unavailable")
	ErrWorkspaceRequired  = errors.New("index generation workspace is required")
	ErrActorRequired      = errors.New("index generation actor is required")
	ErrNotFound           = errors.New("index generation not found")
	ErrProfileUnavailable = errors.New("index generation profile is unavailable")
	ErrInvalidTransition  = errors.New("invalid index generation state transition")
	ErrQualityGate        = errors.New("index generation quality gate is not satisfied")
	ErrDimensionMismatch  = errors.New("index generation vector dimension mismatch")
	ErrTargetUnavailable  = errors.New("index generation target is unavailable")
)

// Build is one profile migration. Its route generations activate together so
// text and visual spaces cannot be switched independently by accident.
type Build struct {
	ID               uuid.UUID       `json:"id"`
	WorkspaceID      uuid.UUID       `json:"workspace_id"`
	ProfileID        string          `json:"profile_id"`
	ProfileRevision  string          `json:"profile_revision"`
	PipelineRevision string          `json:"pipeline_revision"`
	AllowedMIMETypes []string        `json:"allowed_mime_types"`
	State            string          `json:"state"`
	ProfileSnapshot  ProfileSnapshot `json:"profile_snapshot"`
	QualityGate      map[string]any  `json:"quality_gate"`
	CorpusCapturedAt time.Time       `json:"corpus_captured_at"`
	CorpusFileCount  int             `json:"corpus_file_count"`
	RequiredTargets  int             `json:"required_targets"`
	SucceededTargets int             `json:"succeeded_targets"`
	SkippedTargets   int             `json:"skipped_targets"`
	FailedTargets    int             `json:"failed_targets"`
	CreatedByUserID  *uuid.UUID      `json:"-"`
	CreatorPresent   bool            `json:"creator_present"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	ReadyAt          *time.Time      `json:"ready_at,omitempty"`
	ActivatedAt      *time.Time      `json:"activated_at,omitempty"`
	CancelledAt      *time.Time      `json:"cancelled_at,omitempty"`
	FailedAt         *time.Time      `json:"failed_at,omitempty"`
	FailureCode      *string         `json:"failure_code,omitempty"`
	RetentionUntil   *time.Time      `json:"retention_until,omitempty"`
	Generations      []Generation    `json:"generations"`
}

// ProfileSnapshot is the immutable, credential-free execution contract saved
// when a build is created. Historical activation never reinterprets it through
// a newer compiled catalog revision; the operator allowlist is checked
// independently at activation time.
type ProfileSnapshot struct {
	ID               string        `json:"id"`
	Revision         string        `json:"revision"`
	PipelineRevision string        `json:"pipeline_revision"`
	DataEgress       string        `json:"data_egress"`
	AllowedMIMETypes []string      `json:"allowed_mime_types"`
	Embedding        StageSnapshot `json:"embedding"`
	VisualEmbedding  StageSnapshot `json:"visual_embedding"`
	LLM              StageSnapshot `json:"llm"`
	VLM              StageSnapshot `json:"vlm"`
	ASR              StageSnapshot `json:"asr"`
	Rerank           StageSnapshot `json:"rerank"`
}

type StageSnapshot struct {
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
}

// Generation identifies exactly one comparable vector space.
type Generation struct {
	ID               uuid.UUID `json:"id"`
	BuildID          uuid.UUID `json:"build_id"`
	WorkspaceID      uuid.UUID `json:"workspace_id"`
	RouteKind        string    `json:"route_kind"`
	Provider         string    `json:"provider"`
	ModelRevision    string    `json:"model_revision"`
	OutputDimension  int       `json:"output_dimension"`
	PipelineRevision string    `json:"pipeline_revision"`
	ProfileID        string    `json:"profile_id"`
	ProfileRevision  string    `json:"profile_revision"`
	State            string    `json:"state"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type Target struct {
	GenerationID     uuid.UUID `json:"generation_id"`
	WorkspaceID      uuid.UUID `json:"workspace_id"`
	FileID           uuid.UUID `json:"file_id"`
	ContentSHA256    string    `json:"content_sha256"`
	Stage            string    `json:"stage"`
	State            string    `json:"state"`
	Attempts         int       `json:"attempts"`
	AttemptToken     uuid.UUID `json:"-"`
	LeaseExpiresAt   time.Time `json:"-"`
	SourcePresent    bool      `json:"source_present"`
	Provider         string    `json:"provider"`
	ModelRevision    string    `json:"model_revision"`
	OutputDimension  int       `json:"output_dimension"`
	ProfileID        string    `json:"profile_id"`
	ProfileRevision  string    `json:"profile_revision"`
	PipelineRevision string    `json:"pipeline_revision"`
}

type Vector struct {
	Ordinal      int
	EvidenceText string
	Values       []float32
}

type Event struct {
	ID           int64          `json:"id"`
	BuildID      uuid.UUID      `json:"build_id"`
	WorkspaceID  uuid.UUID      `json:"workspace_id"`
	ActorPresent bool           `json:"actor_present"`
	EventType    string         `json:"event_type"`
	FromState    *string        `json:"from_state,omitempty"`
	ToState      *string        `json:"to_state,omitempty"`
	Details      map[string]any `json:"details"`
	CreatedAt    time.Time      `json:"created_at"`
}

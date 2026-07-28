package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// HandoffContract and HandoffSchemaVersion identify the portable handoff
	// contract. They mirror docs/schemas/handoff.v1.schema.json.
	HandoffContract      = "mem.handoff"
	HandoffSchemaVersion = 1
)

// HandoffV1 is the vendor-neutral payload persisted by a checkpoint.
//
// TaskKey is deliberately present in the payload as well as the canonical API
// path. Client.Checkpoint rejects mismatches before any request is sent.
type HandoffV1 struct {
	Contract         string          `json:"contract"`
	SchemaVersion    int             `json:"schema_version"`
	CheckpointKind   string          `json:"checkpoint_kind"`
	TaskKey          string          `json:"task_key"`
	BaseCheckpointID *string         `json:"base_checkpoint_id,omitempty"`
	ScopePath        string          `json:"scope_path"`
	State            HandoffState    `json:"state"`
	Producer         HandoffProducer `json:"producer"`
}

type HandoffState struct {
	Status         string                 `json:"status"`
	Goal           string                 `json:"goal"`
	Progress       HandoffProgress        `json:"progress"`
	Decisions      []HandoffDecision      `json:"decisions"`
	NextSteps      []HandoffNextStep      `json:"next_steps"`
	Blockers       []HandoffBlocker       `json:"blockers"`
	OpenQuestions  []string               `json:"open_questions"`
	Artifacts      []HandoffArtifact      `json:"artifacts"`
	WorkspaceState *HandoffWorkspaceState `json:"workspace_state,omitempty"`
}

type HandoffProgress struct {
	Summary   string   `json:"summary"`
	Completed []string `json:"completed"`
}

type HandoffDecision struct {
	Summary    string   `json:"summary"`
	Rationale  string   `json:"rationale,omitempty"`
	References []string `json:"references"`
}

type HandoffNextStep struct {
	Summary    string   `json:"summary"`
	References []string `json:"references"`
}

type HandoffBlocker struct {
	Summary    string   `json:"summary"`
	Needs      string   `json:"needs,omitempty"`
	References []string `json:"references"`
}

type HandoffArtifact struct {
	URI      string `json:"uri"`
	Role     string `json:"role,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Required *bool  `json:"required"`
}

type HandoffWorkspaceState struct {
	WorkingDirectory string      `json:"working_directory,omitempty"`
	VCS              *HandoffVCS `json:"vcs,omitempty"`
}

type HandoffVCS struct {
	Revision      string `json:"revision,omitempty"`
	Branch        string `json:"branch,omitempty"`
	Dirty         *bool  `json:"dirty,omitempty"`
	StatusSummary string `json:"status_summary,omitempty"`
}

type HandoffProducer struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id,omitempty"`
}

// ResumeRequest selects an explicit checkpoint or the current task head and
// optionally narrows/augments the evidence returned with it. TaskKey belongs
// to the URL and is therefore a separate argument to Client.Resume.
type ResumeRequest struct {
	CheckpointID string `json:"checkpoint_id,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Focus        string `json:"focus,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	MaxChars     int    `json:"max_chars,omitempty"`
}

// Task is the bounded task projection returned by GET /v1/tasks.
type Task struct {
	ID               string    `json:"id"`
	WorkspaceID      string    `json:"workspace_id"`
	TaskKey          string    `json:"task_key"`
	ScopePath        string    `json:"scope_path"`
	HeadCheckpointID *string   `json:"head_checkpoint_id,omitempty"`
	HeadSequence     int64     `json:"head_sequence"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// CheckpointReference is one immutable evidence reference attached to a task
// checkpoint. Metadata is intentionally kept as raw JSON so this thin client
// does not invent a second reference contract.
type CheckpointReference struct {
	CheckpointID   string          `json:"checkpoint_id"`
	Ordinal        int             `json:"ordinal"`
	Relation       string          `json:"relation"`
	URI            string          `json:"uri"`
	ExpectedSHA256 string          `json:"expected_sha256,omitempty"`
	Required       bool            `json:"required"`
	Metadata       json.RawMessage `json:"metadata"`
}

// CheckpointRecord mirrors the canonical immutable checkpoint detail returned
// by get/resume endpoints. Server-only replay and token evidence is absent.
type CheckpointRecord struct {
	ID               string                `json:"id"`
	WorkspaceID      string                `json:"workspace_id"`
	TaskID           string                `json:"task_id"`
	TaskKey          string                `json:"task_key"`
	Sequence         int64                 `json:"sequence"`
	CheckpointKind   string                `json:"checkpoint_kind"`
	Contract         string                `json:"contract"`
	SchemaVersion    int                   `json:"schema_version"`
	BaseCheckpointID *string               `json:"base_checkpoint_id,omitempty"`
	ScopePath        string                `json:"scope_path"`
	Handoff          HandoffV1             `json:"handoff"`
	PayloadSHA256    string                `json:"payload_sha256"`
	CreatedByUserID  *string               `json:"created_by_user_id,omitempty"`
	ProducerAgent    string                `json:"producer_agent"`
	ProducerSession  string                `json:"producer_session,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
	References       []CheckpointReference `json:"references"`
}

// CheckpointSummary is the bounded projection returned by checkpoint list
// endpoints. Fetch a selected checkpoint to receive its complete handoff and
// evidence references.
type CheckpointSummary struct {
	ID               string    `json:"id"`
	WorkspaceID      string    `json:"workspace_id"`
	TaskID           string    `json:"task_id"`
	TaskKey          string    `json:"task_key"`
	Sequence         int64     `json:"sequence"`
	CheckpointKind   string    `json:"checkpoint_kind"`
	Contract         string    `json:"contract"`
	SchemaVersion    int       `json:"schema_version"`
	BaseCheckpointID *string   `json:"base_checkpoint_id,omitempty"`
	ScopePath        string    `json:"scope_path"`
	Status           string    `json:"status"`
	ProgressExcerpt  string    `json:"progress_excerpt"`
	ProgressLength   int       `json:"progress_length"`
	CompletedCount   int       `json:"completed_count"`
	ReferenceCount   int       `json:"reference_count"`
	PayloadSHA256    string    `json:"payload_sha256"`
	ProducerAgent    string    `json:"producer_agent"`
	ProducerSession  string    `json:"producer_session,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type TaskListOptions struct {
	Scope string
	Limit int
	After string
}

type TaskListResponse struct {
	Tasks []Task `json:"tasks"`
}

type CheckpointListOptions struct {
	Scope  string
	Limit  int
	Before int64
}

type CheckpointListResponse struct {
	Checkpoints []CheckpointSummary `json:"checkpoints"`
}

type CheckpointGetOptions struct {
	Scope string
}

// Checkpoint idempotently persists one immutable handoff revision. The raw
// response is returned unchanged so adapters do not create a second response
// contract while the canonical API envelope evolves.
func (c *Client) Checkpoint(
	ctx context.Context,
	taskKey string,
	handoff HandoffV1,
	idempotencyKey string,
) (json.RawMessage, error) {
	if c == nil {
		return nil, fmt.Errorf("apiclient: nil client")
	}
	if err := validateTaskKey(taskKey); err != nil {
		return nil, err
	}
	if taskKey != handoff.TaskKey {
		return nil, fmt.Errorf(
			"apiclient: URL task_key %q does not match handoff task_key %q",
			taskKey,
			handoff.TaskKey,
		)
	}
	if err := handoff.Validate(); err != nil {
		return nil, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return nil, fmt.Errorf("apiclient: idempotency key is required")
	}

	var out json.RawMessage
	err := c.DoJSONWithHeaders(
		ctx,
		http.MethodPost,
		taskEndpoint(taskKey, "checkpoints"),
		handoff,
		&out,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Resume returns the canonical API response without reshaping it.
func (c *Client) Resume(
	ctx context.Context,
	taskKey string,
	req ResumeRequest,
) (json.RawMessage, error) {
	if c == nil {
		return nil, fmt.Errorf("apiclient: nil client")
	}
	if err := validateTaskKey(taskKey); err != nil {
		return nil, err
	}
	if req.Scope != "" && !strings.HasPrefix(req.Scope, "/") {
		return nil, fmt.Errorf("apiclient: resume scope must be an absolute virtual path")
	}
	if req.Limit < 0 {
		return nil, fmt.Errorf("apiclient: resume limit must not be negative")
	}
	if req.MaxChars < 0 {
		return nil, fmt.Errorf("apiclient: resume max_chars must not be negative")
	}

	var out json.RawMessage
	if err := c.DoJSON(
		ctx,
		http.MethodPost,
		taskEndpoint(taskKey, "resume"),
		req,
		&out,
	); err != nil {
		return nil, err
	}
	return out, nil
}

// ListTasks returns a bounded page of tasks visible inside the authenticated
// workspace and optional virtual-path scope.
func (c *Client) ListTasks(
	ctx context.Context,
	options TaskListOptions,
) (*TaskListResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("apiclient: nil client")
	}
	query, err := handoffReadQuery(options.Scope, options.Limit)
	if err != nil {
		return nil, err
	}
	if after := strings.TrimSpace(options.After); after != "" {
		id, err := uuid.Parse(after)
		if err != nil || id == uuid.Nil {
			return nil, fmt.Errorf("apiclient: task after cursor must be a UUID")
		}
		query.Set("after", id.String())
	}
	path := "/v1/tasks"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var response TaskListResponse
	if err := c.DoJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	if response.Tasks == nil {
		response.Tasks = []Task{}
	}
	return &response, nil
}

// ListCheckpoints returns newest-first immutable revisions for one task.
func (c *Client) ListCheckpoints(
	ctx context.Context,
	taskKey string,
	options CheckpointListOptions,
) (*CheckpointListResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("apiclient: nil client")
	}
	if err := validateTaskKey(taskKey); err != nil {
		return nil, err
	}
	query, err := handoffReadQuery(options.Scope, options.Limit)
	if err != nil {
		return nil, err
	}
	if options.Before < 0 {
		return nil, fmt.Errorf("apiclient: checkpoint before sequence must not be negative")
	}
	if options.Before > 0 {
		query.Set("before", strconv.FormatInt(options.Before, 10))
	}
	path := taskEndpoint(taskKey, "checkpoints")
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var response CheckpointListResponse
	if err := c.DoJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	if response.Checkpoints == nil {
		response.Checkpoints = []CheckpointSummary{}
	}
	return &response, nil
}

// GetCheckpoint resolves one immutable checkpoint without revealing whether a
// missing record belongs to another workspace or lies outside the token path.
func (c *Client) GetCheckpoint(
	ctx context.Context,
	taskKey string,
	checkpointID string,
	options CheckpointGetOptions,
) (*CheckpointRecord, error) {
	if c == nil {
		return nil, fmt.Errorf("apiclient: nil client")
	}
	if err := validateTaskKey(taskKey); err != nil {
		return nil, err
	}
	id, err := uuid.Parse(strings.TrimSpace(checkpointID))
	if err != nil || id == uuid.Nil {
		return nil, fmt.Errorf("apiclient: checkpoint id must be a UUID")
	}
	query, err := handoffReadQuery(options.Scope, 0)
	if err != nil {
		return nil, err
	}
	path := taskEndpoint(
		taskKey,
		"checkpoints/"+url.PathEscape(id.String()),
	)
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var checkpoint CheckpointRecord
	if err := c.DoJSON(ctx, http.MethodGet, path, nil, &checkpoint); err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

// Validate checks the contract invariants adapters need in order to produce a
// portable handoff. The server remains authoritative for full size, UUID,
// checksum, and path validation.
func (h HandoffV1) Validate() error {
	if h.Contract != HandoffContract {
		return fmt.Errorf("apiclient: handoff contract must be %q", HandoffContract)
	}
	if h.SchemaVersion != HandoffSchemaVersion {
		return fmt.Errorf("apiclient: handoff schema_version must be %d", HandoffSchemaVersion)
	}
	switch h.CheckpointKind {
	case "checkpoint", "handoff":
	default:
		return fmt.Errorf("apiclient: checkpoint_kind must be checkpoint or handoff")
	}
	if err := validateTaskKey(h.TaskKey); err != nil {
		return err
	}
	if h.BaseCheckpointID != nil && strings.TrimSpace(*h.BaseCheckpointID) == "" {
		return fmt.Errorf("apiclient: base_checkpoint_id must not be empty when present")
	}
	if !strings.HasPrefix(h.ScopePath, "/") {
		return fmt.Errorf("apiclient: scope_path must be an absolute virtual path")
	}
	switch h.State.Status {
	case "in_progress", "ready", "blocked", "complete":
	default:
		return fmt.Errorf("apiclient: handoff state.status is invalid")
	}
	if strings.TrimSpace(h.State.Goal) == "" {
		return fmt.Errorf("apiclient: handoff state.goal is required")
	}
	if strings.TrimSpace(h.State.Progress.Summary) == "" {
		return fmt.Errorf("apiclient: handoff state.progress.summary is required")
	}
	if h.State.Progress.Completed == nil ||
		h.State.Decisions == nil ||
		h.State.NextSteps == nil ||
		h.State.Blockers == nil ||
		h.State.OpenQuestions == nil ||
		h.State.Artifacts == nil {
		return fmt.Errorf("apiclient: required handoff arrays must be present")
	}
	for i, decision := range h.State.Decisions {
		if strings.TrimSpace(decision.Summary) == "" || decision.References == nil {
			return fmt.Errorf("apiclient: handoff decision %d requires summary and references", i)
		}
	}
	for i, step := range h.State.NextSteps {
		if strings.TrimSpace(step.Summary) == "" || step.References == nil {
			return fmt.Errorf("apiclient: handoff next_step %d requires summary and references", i)
		}
	}
	for i, blocker := range h.State.Blockers {
		if strings.TrimSpace(blocker.Summary) == "" || blocker.References == nil {
			return fmt.Errorf("apiclient: handoff blocker %d requires summary and references", i)
		}
	}
	for i, artifact := range h.State.Artifacts {
		if strings.TrimSpace(artifact.URI) == "" {
			return fmt.Errorf("apiclient: handoff artifact %d requires uri", i)
		}
		if artifact.Required == nil {
			return fmt.Errorf("apiclient: handoff artifact %d requires required", i)
		}
	}
	if strings.TrimSpace(h.Producer.AgentID) == "" {
		return fmt.Errorf("apiclient: handoff producer.agent_id is required")
	}
	return nil
}

func validateTaskKey(taskKey string) error {
	if strings.TrimSpace(taskKey) == "" {
		return fmt.Errorf("apiclient: task_key is required")
	}
	if !utf8.ValidString(taskKey) {
		return fmt.Errorf("apiclient: task_key must be valid UTF-8")
	}
	if utf8.RuneCountInString(taskKey) > 200 {
		return fmt.Errorf("apiclient: task_key exceeds 200 characters")
	}
	return nil
}

func handoffReadQuery(scope string, limit int) (url.Values, error) {
	query := url.Values{}
	scope = strings.TrimSpace(scope)
	if scope != "" {
		if !strings.HasPrefix(scope, "/") {
			return nil, fmt.Errorf("apiclient: handoff scope must be an absolute virtual path")
		}
		query.Set("scope", scope)
	}
	if limit < 0 || limit > 200 {
		return nil, fmt.Errorf("apiclient: handoff list limit must be between 0 and 200")
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	return query, nil
}

func taskEndpoint(taskKey, action string) string {
	return "/v1/tasks/" + url.PathEscape(taskKey) + "/" + action
}

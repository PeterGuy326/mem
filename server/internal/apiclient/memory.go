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

	"github.com/google/uuid"
)

// Memory is the typed public projection returned by the structured-memory
// read APIs. Authentication token IDs and idempotency hashes are intentionally
// absent from the HTTP contract.
type Memory struct {
	ID               string          `json:"id"`
	WorkspaceID      string          `json:"workspace_id"`
	CreatedByUserID  *string         `json:"created_by_user_id,omitempty"`
	Kind             string          `json:"kind"`
	Content          string          `json:"content"`
	Attributes       json.RawMessage `json:"attributes"`
	Path             string          `json:"path"`
	EventAt          *time.Time      `json:"event_at,omitempty"`
	SourceType       string          `json:"source_type"`
	SourceRef        string          `json:"source_ref,omitempty"`
	SourceFileID     *string         `json:"source_file_id,omitempty"`
	SourceFileSHA256 string          `json:"source_file_sha256,omitempty"`
	SourceLocator    json.RawMessage `json:"source_locator"`
	ProducerAgent    string          `json:"producer_agent,omitempty"`
	ProducerSession  string          `json:"producer_session,omitempty"`
	ProducerTask     string          `json:"producer_task,omitempty"`
	ContentSHA256    string          `json:"content_sha256"`
	LifecycleStatus  string          `json:"lifecycle_status"`
	StateVersion     int64           `json:"state_version"`
	Pinned           bool            `json:"pinned"`
	PinnedAt         *time.Time      `json:"pinned_at,omitempty"`
	FeedbackAt       *time.Time      `json:"feedback_at,omitempty"`
	UsefulCount      int             `json:"useful_count"`
	NotUsefulCount   int             `json:"not_useful_count"`
	FeedbackScore    int             `json:"feedback_score"`
	FeedbackCount    int             `json:"feedback_count"`
	ForgottenAt      *time.Time      `json:"forgotten_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// MemoryProvenance is the canonical public origin projection returned by the
// memory detail endpoint. Authentication token IDs and idempotency material
// are intentionally absent.
type MemoryProvenance struct {
	WorkspaceID      string          `json:"workspace_id"`
	CreatedByUserID  *string         `json:"created_by_user_id,omitempty"`
	EventAt          *time.Time      `json:"event_at,omitempty"`
	SourceType       string          `json:"source_type"`
	SourceRef        string          `json:"source_ref,omitempty"`
	SourceFileID     *string         `json:"source_file_id,omitempty"`
	SourceFileSHA256 string          `json:"source_file_sha256,omitempty"`
	SourceLocator    json.RawMessage `json:"source_locator"`
	ProducerAgent    string          `json:"producer_agent,omitempty"`
	ProducerSession  string          `json:"producer_session,omitempty"`
	ProducerTask     string          `json:"producer_task,omitempty"`
}

// MemoryDetail mirrors GET /v1/memories/{id}: the full public memory
// projection plus its stable citation and explicit provenance envelope.
type MemoryDetail struct {
	Memory
	Citation   string           `json:"citation"`
	Provenance MemoryProvenance `json:"provenance"`
}

// MemoryListOptions filters a stable newest-first keyset page. Cursor is
// opaque and should be passed back unchanged from MemoryListPage.NextCursor.
type MemoryListOptions struct {
	Scope     string
	Recursive *bool
	Kinds     []string
	Lifecycle string
	Pinned    *bool
	Limit     int
	Cursor    string
}

// MemorySummary is the bounded list projection. Full content is available
// only through GetMemory so one list page cannot accidentally become an
// unbounded Agent context.
type MemorySummary struct {
	ID               string     `json:"id"`
	WorkspaceID      string     `json:"workspace_id"`
	Kind             string     `json:"kind"`
	Excerpt          string     `json:"excerpt"`
	ContentLength    int        `json:"content_length"`
	Path             string     `json:"path"`
	EventAt          *time.Time `json:"event_at,omitempty"`
	SourceType       string     `json:"source_type"`
	SourceRef        string     `json:"source_ref,omitempty"`
	SourceFileID     *string    `json:"source_file_id,omitempty"`
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
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	Citation         string     `json:"citation"`
}

// MemoryListResponse is the typed response from GET /v1/memories.
type MemoryListResponse struct {
	Memories   []MemorySummary `json:"memories"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// MemoryListPage is retained as a source-compatible alias.
type MemoryListPage = MemoryListResponse

type MemoryFeedbackRequest struct {
	Action          string `json:"action"`
	ExpectedVersion int64  `json:"expected_version"`
}

type MemoryVersionRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

type MemoryForgetRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
}

type MemoryMutationResponse struct {
	Memory   MemoryControlState `json:"memory"`
	Event    json.RawMessage    `json:"event"`
	Replayed bool               `json:"replayed"`
}

// MemoryControlState is the bounded projection returned by feedback and
// lifecycle mutations. Fetch the detail endpoint explicitly when content or
// provenance is needed.
type MemoryControlState struct {
	ID              string     `json:"id"`
	LifecycleStatus string     `json:"lifecycle_status"`
	StateVersion    int64      `json:"state_version"`
	Pinned          bool       `json:"pinned"`
	PinnedAt        *time.Time `json:"pinned_at,omitempty"`
	UsefulCount     int        `json:"useful_count"`
	NotUsefulCount  int        `json:"not_useful_count"`
	FeedbackScore   int        `json:"feedback_score"`
	FeedbackCount   int        `json:"feedback_count"`
	FeedbackAt      *time.Time `json:"feedback_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type MemoryTombstone struct {
	ID           string     `json:"id"`
	StateVersion int64      `json:"state_version"`
	ForgottenAt  *time.Time `json:"forgotten_at,omitempty"`
}

type MemoryForgetResponse struct {
	// Tombstone is the preferred domain envelope. The flat fields remain
	// compatible with servers that project the same safe tombstone directly.
	Tombstone    *MemoryTombstone `json:"tombstone,omitempty"`
	MemoryID     string           `json:"memory_id,omitempty"`
	StateVersion int64            `json:"state_version,omitempty"`
	ForgottenAt  *time.Time       `json:"forgotten_at,omitempty"`
	Event        json.RawMessage  `json:"event"`
	Replayed     bool             `json:"replayed"`
}

// MemoryGetOptions optionally narrows a known memory citation to a virtual
// path subtree. The authenticated token path remains the authoritative upper
// bound.
type MemoryGetOptions struct {
	Scope string
}

// ListMemories returns a stable, newest-first page suitable for drive-style
// memory browsing.
func (c *Client) ListMemories(
	ctx context.Context,
	options MemoryListOptions,
) (*MemoryListPage, error) {
	if c == nil {
		return nil, fmt.Errorf("apiclient: nil client")
	}
	query := url.Values{}
	scope := strings.TrimSpace(options.Scope)
	if scope != "" {
		if !strings.HasPrefix(scope, "/") {
			return nil, fmt.Errorf("apiclient: memory scope must be an absolute virtual path")
		}
		query.Set("scope", scope)
	}
	if options.Recursive != nil {
		query.Set("recursive", strconv.FormatBool(*options.Recursive))
	}
	if options.Limit < 0 || options.Limit > 100 {
		return nil, fmt.Errorf("apiclient: memory limit must be between 0 and 100")
	}
	if options.Limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", options.Limit))
	}
	status := strings.ToLower(strings.TrimSpace(options.Lifecycle))
	switch status {
	case "", "active", "archived", "all":
	default:
		return nil, fmt.Errorf("apiclient: memory lifecycle must be active, archived, or all")
	}
	if status != "" {
		query.Set("lifecycle", status)
	}
	for _, rawKind := range options.Kinds {
		kind := strings.ToLower(strings.TrimSpace(rawKind))
		if !validMemoryKind(kind) {
			return nil, fmt.Errorf("apiclient: invalid memory kind %q", rawKind)
		}
		query.Add("kind", kind)
	}
	if options.Pinned != nil {
		query.Set("pinned", strconv.FormatBool(*options.Pinned))
	}
	if cursor := strings.TrimSpace(options.Cursor); cursor != "" {
		query.Set("cursor", cursor)
	}

	path := "/v1/memories"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var page MemoryListPage
	if err := c.DoJSON(ctx, http.MethodGet, path, nil, &page); err != nil {
		return nil, err
	}
	if page.Memories == nil {
		page.Memories = []MemorySummary{}
	}
	return &page, nil
}

func (c *Client) FeedbackMemory(
	ctx context.Context,
	memoryID string,
	idempotencyKey string,
	request MemoryFeedbackRequest,
) (*MemoryMutationResponse, error) {
	switch request.Action {
	case "useful", "not_useful", "pin", "unpin":
	default:
		return nil, fmt.Errorf("apiclient: invalid memory feedback action %q", request.Action)
	}
	if request.ExpectedVersion <= 0 {
		return nil, fmt.Errorf("apiclient: expected_version must be greater than zero")
	}
	var response MemoryMutationResponse
	if err := c.postMemoryMutation(
		ctx, memoryID, "feedback", idempotencyKey, request, &response,
	); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ArchiveMemory(
	ctx context.Context,
	memoryID string,
	idempotencyKey string,
	request MemoryVersionRequest,
) (*MemoryMutationResponse, error) {
	if request.ExpectedVersion <= 0 {
		return nil, fmt.Errorf("apiclient: expected_version must be greater than zero")
	}
	var response MemoryMutationResponse
	if err := c.postMemoryMutation(
		ctx, memoryID, "archive", idempotencyKey, request, &response,
	); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) RestoreMemory(
	ctx context.Context,
	memoryID string,
	idempotencyKey string,
	request MemoryVersionRequest,
) (*MemoryMutationResponse, error) {
	if request.ExpectedVersion <= 0 {
		return nil, fmt.Errorf("apiclient: expected_version must be greater than zero")
	}
	var response MemoryMutationResponse
	if err := c.postMemoryMutation(
		ctx, memoryID, "restore", idempotencyKey, request, &response,
	); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ForgetMemory(
	ctx context.Context,
	memoryID string,
	idempotencyKey string,
	request MemoryForgetRequest,
) (*MemoryForgetResponse, error) {
	if request.ExpectedVersion <= 0 {
		return nil, fmt.Errorf("apiclient: expected_version must be greater than zero")
	}
	switch request.Reason {
	case "user_request", "incorrect", "sensitive", "expired", "other":
	default:
		return nil, fmt.Errorf("apiclient: invalid memory forget reason %q", request.Reason)
	}
	var response MemoryForgetResponse
	if err := c.postMemoryMutation(
		ctx, memoryID, "forget", idempotencyKey, request, &response,
	); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) postMemoryMutation(
	ctx context.Context,
	memoryID string,
	action string,
	idempotencyKey string,
	request any,
	response any,
) error {
	if c == nil {
		return fmt.Errorf("apiclient: nil client")
	}
	memoryID = strings.TrimSpace(memoryID)
	id, err := uuid.Parse(memoryID)
	if err != nil || id == uuid.Nil {
		return fmt.Errorf("apiclient: memory id must be a UUID")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return fmt.Errorf("apiclient: idempotency key is required")
	}
	path := "/v1/memories/" + url.PathEscape(id.String()) + "/" + action
	return c.DoJSONWithHeaders(
		ctx,
		http.MethodPost,
		path,
		request,
		response,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
}

// GetMemory resolves one known memory ID. Missing, cross-workspace, and
// out-of-token-path records share the server's not_found response.
func (c *Client) GetMemory(
	ctx context.Context,
	memoryID string,
	options MemoryGetOptions,
) (*MemoryDetail, error) {
	if c == nil {
		return nil, fmt.Errorf("apiclient: nil client")
	}
	memoryID = strings.TrimSpace(memoryID)
	id, err := uuid.Parse(memoryID)
	if err != nil || id == uuid.Nil {
		return nil, fmt.Errorf("apiclient: memory id must be a UUID")
	}
	query := url.Values{}
	scope := strings.TrimSpace(options.Scope)
	if scope != "" {
		if !strings.HasPrefix(scope, "/") {
			return nil, fmt.Errorf("apiclient: memory scope must be an absolute virtual path")
		}
		query.Set("scope", scope)
	}
	path := "/v1/memories/" + url.PathEscape(id.String())
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var record MemoryDetail
	if err := c.DoJSON(ctx, http.MethodGet, path, nil, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func validMemoryKind(kind string) bool {
	switch kind {
	case "observation", "decision", "preference", "task_state", "fact", "note", "artifact":
		return true
	default:
		return false
	}
}

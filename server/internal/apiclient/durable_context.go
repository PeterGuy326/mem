package apiclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DurableContextContract is the pinned wire contract for scoped durable
// context. The client refuses to send any other version so incompatible
// servers fail clearly (contract_unsupported) instead of silently.
const DurableContextContract = "durable-context.v1"

// DurableContextRecallOptions resumes approved context for one principal.
type DurableContextRecallOptions struct {
	Principal  string
	SessionRef string
	Limit      int
}

// DurableContextHit is one resumable memory with its version-pinned locator.
type DurableContextHit struct {
	Memory       Memory           `json:"memory"`
	Locator      string           `json:"locator"`
	StateVersion int64            `json:"state_version"`
	Provenance   MemoryProvenance `json:"provenance"`
}

// DurableContextRecallResult is the contract envelope returned by recall.
type DurableContextRecallResult struct {
	Contract  string              `json:"contract"`
	Principal string              `json:"principal"`
	Hits      []DurableContextHit `json:"hits"`
}

// DurableContextGrant is one explicit recall approval. Token identifiers are
// server-internal and never returned.
type DurableContextGrant struct {
	ID              string     `json:"id"`
	WorkspaceID     string     `json:"workspace_id"`
	Principal       string     `json:"principal"`
	MemoryID        string     `json:"memory_id"`
	Mode            string     `json:"mode"`
	GrantedByUserID *string    `json:"granted_by_user_id,omitempty"`
	GrantedAt       time.Time  `json:"granted_at"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// DurableContextRecall resumes the approved, active context for one
// principal through the pinned contract.
func (c *Client) DurableContextRecall(
	ctx context.Context,
	options DurableContextRecallOptions,
) (*DurableContextRecallResult, error) {
	if c == nil {
		return nil, fmt.Errorf("apiclient: nil client")
	}
	principal := strings.TrimSpace(options.Principal)
	if principal == "" {
		return nil, fmt.Errorf("apiclient: durable context principal is required")
	}
	if options.Limit < 0 || options.Limit > 100 {
		return nil, fmt.Errorf("apiclient: durable context limit must be between 0 and 100")
	}
	body := map[string]any{
		"contract":  DurableContextContract,
		"principal": principal,
	}
	if sessionRef := strings.TrimSpace(options.SessionRef); sessionRef != "" {
		body["session_ref"] = sessionRef
	}
	if options.Limit > 0 {
		body["limit"] = options.Limit
	}
	var result DurableContextRecallResult
	if err := c.DoJSON(ctx, http.MethodPost, "/v1/durable-context/recall", body, &result); err != nil {
		return nil, err
	}
	if result.Hits == nil {
		result.Hits = []DurableContextHit{}
	}
	return &result, nil
}

// DurableContextGetMemory resolves one granted memory for one principal.
func (c *Client) DurableContextGetMemory(
	ctx context.Context,
	principal string,
	memoryID string,
) (*DurableContextHit, error) {
	if c == nil {
		return nil, fmt.Errorf("apiclient: nil client")
	}
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return nil, fmt.Errorf("apiclient: durable context principal is required")
	}
	id, err := uuid.Parse(strings.TrimSpace(memoryID))
	if err != nil || id == uuid.Nil {
		return nil, fmt.Errorf("apiclient: durable context memory id must be a UUID")
	}
	query := url.Values{}
	query.Set("contract", DurableContextContract)
	query.Set("principal", principal)
	path := "/v1/durable-context/memories/" + url.PathEscape(id.String()) + "?" + query.Encode()
	var hit DurableContextHit
	if err := c.DoJSON(ctx, http.MethodGet, path, nil, &hit); err != nil {
		return nil, err
	}
	return &hit, nil
}

// CreateDurableContextGrant approves one explicit read grant (admin scope).
func (c *Client) CreateDurableContextGrant(
	ctx context.Context,
	principal string,
	memoryID string,
) (*DurableContextGrant, error) {
	if c == nil {
		return nil, fmt.Errorf("apiclient: nil client")
	}
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return nil, fmt.Errorf("apiclient: durable context principal is required")
	}
	id, err := uuid.Parse(strings.TrimSpace(memoryID))
	if err != nil || id == uuid.Nil {
		return nil, fmt.Errorf("apiclient: durable context memory id must be a UUID")
	}
	body := map[string]any{
		"principal": principal,
		"memory_id": id.String(),
	}
	var grant DurableContextGrant
	if err := c.DoJSON(ctx, http.MethodPost, "/v1/durable-context/grants", body, &grant); err != nil {
		return nil, err
	}
	return &grant, nil
}

// ListDurableContextGrants lists the workspace allowlist, optionally narrowed
// to one principal.
func (c *Client) ListDurableContextGrants(
	ctx context.Context,
	principal string,
	limit int,
) ([]DurableContextGrant, error) {
	if c == nil {
		return nil, fmt.Errorf("apiclient: nil client")
	}
	if limit < 0 || limit > 100 {
		return nil, fmt.Errorf("apiclient: durable context limit must be between 0 and 100")
	}
	query := url.Values{}
	if trimmed := strings.TrimSpace(principal); trimmed != "" {
		query.Set("principal", trimmed)
	}
	if limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", limit))
	}
	path := "/v1/durable-context/grants"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var page struct {
		Grants []DurableContextGrant `json:"grants"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, path, nil, &page); err != nil {
		return nil, err
	}
	if page.Grants == nil {
		page.Grants = []DurableContextGrant{}
	}
	return page.Grants, nil
}

// RevokeDurableContextGrant soft-revokes one grant (admin scope).
func (c *Client) RevokeDurableContextGrant(
	ctx context.Context,
	grantID string,
) (*DurableContextGrant, error) {
	if c == nil {
		return nil, fmt.Errorf("apiclient: nil client")
	}
	id, err := uuid.Parse(strings.TrimSpace(grantID))
	if err != nil || id == uuid.Nil {
		return nil, fmt.Errorf("apiclient: durable context grant id must be a UUID")
	}
	path := "/v1/durable-context/grants/" + url.PathEscape(id.String()) + "/revoke"
	var grant DurableContextGrant
	if err := c.DoJSON(ctx, http.MethodPost, path, nil, &grant); err != nil {
		return nil, err
	}
	return &grant, nil
}

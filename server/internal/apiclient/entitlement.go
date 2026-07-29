package apiclient

import (
	"context"
	"net/http"
	"time"
)

// ManagedEmbeddingSummary is the read-only, workspace-scoped projection
// returned by the SaaS entitlement control plane. It intentionally contains
// no payment-provider data and no request or embedding material.
type ManagedEmbeddingSummary struct {
	WorkspaceID string    `json:"workspace_id"`
	Plan        string    `json:"plan"`
	Status      string    `json:"status"`
	Qualifying  bool      `json:"qualifying"`
	UnitLimit   int64     `json:"managed_embedding_unit_limit"`
	Reserved    int64     `json:"managed_embedding_units_reserved"`
	Consumed    int64     `json:"managed_embedding_units_consumed"`
	Remaining   int64     `json:"managed_embedding_units_remaining"`
	PeriodStart time.Time `json:"period_start"`
	ResetAt     time.Time `json:"reset_at"`
}

// EntitlementSummary is returned for both deployment modes. Private mode has
// no commercial gate and therefore omits ManagedEmbedding.
type EntitlementSummary struct {
	DeploymentMode   string                   `json:"deployment_mode"`
	CommercialGate   bool                     `json:"commercial_gate"`
	UpgradeRequired  bool                     `json:"upgrade_required"`
	Plan             string                   `json:"plan,omitempty"`
	Status           string                   `json:"status,omitempty"`
	ManagedEmbedding *ManagedEmbeddingSummary `json:"managed_embedding,omitempty"`
}

// GetEntitlementSummary reads the current authenticated workspace's
// commercial status without reserving or consuming a managed embedding unit.
func (c *Client) GetEntitlementSummary(ctx context.Context) (*EntitlementSummary, error) {
	var summary EntitlementSummary
	if err := c.DoJSON(
		ctx,
		http.MethodGet,
		"/v1/entitlements/current",
		nil,
		&summary,
	); err != nil {
		return nil, err
	}
	return &summary, nil
}

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/contextpack"
	"github.com/PeterGuy326/mem/server/internal/entitlement"
	"github.com/PeterGuy326/mem/server/internal/search"
)

var (
	errManagedIdempotencyRequired = errors.New("managed embedding idempotency key required")
	errManagedProviderUnavailable = errors.New("managed embedding provider unavailable")
	errManagedProviderTimeout     = errors.New("managed embedding provider timeout")
	errManagedUsageCommit         = errors.New("managed embedding usage commit indeterminate")
)

type managedSearchExecutor struct {
	base         SearchService
	entitlements EntitlementService
	command      entitlement.ReserveCommand
	providerSpec string

	mu         sync.Mutex
	summary    entitlement.Summary
	hasSummary bool
	replayed   bool
}

func (m *managedSearchExecutor) Search(
	ctx context.Context,
	query search.Query,
) ([]search.Hit, error) {
	reservation, err := m.entitlements.Reserve(ctx, m.command)
	if err != nil {
		var decision *entitlement.DecisionError
		if errors.As(err, &decision) {
			m.setResult(decision.Summary, false)
		}
		return nil, &managedSearchError{cause: err}
	}
	m.setResult(reservation.Summary, reservation.Replayed)
	query.EmbeddingProvider = m.providerSpec

	if reservation.Replayed {
		hits, err := m.base.Replay(ctx, query, reservation.References)
		if err != nil {
			// Never fall back to a provider call: the persisted succeeded usage
			// row is the idempotency boundary.
			return nil, &managedSearchError{cause: search.ErrReplayReferenceUnavailable}
		}
		return hits, nil
	}

	hits, err := m.base.Search(ctx, query)
	if err != nil {
		markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		summary, markErr := m.entitlements.MarkIndeterminate(markCtx, reservation.ID)
		cancel()
		if markErr == nil {
			m.setResult(summary, false)
		}
		if errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, &managedSearchError{cause: errManagedProviderTimeout}
		}
		return nil, &managedSearchError{cause: errManagedProviderUnavailable}
	}
	references, err := search.ReplayReferences(hits)
	if err != nil {
		markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, _ = m.entitlements.MarkIndeterminate(markCtx, reservation.ID)
		cancel()
		return nil, &managedSearchError{cause: errManagedUsageCommit}
	}
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	summary, err := m.entitlements.Finalize(finalizeCtx, reservation.ID, references)
	cancel()
	if err != nil {
		// A crash or failed commit leaves `reserved`; the TTL reconciler moves
		// it to indeterminate. Returning results before this commit would allow
		// an unaccounted managed call.
		return nil, &managedSearchError{cause: errManagedUsageCommit}
	}
	m.setResult(summary, false)
	return hits, nil
}

func (m *managedSearchExecutor) setResult(summary entitlement.Summary, replayed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.summary = summary
	m.hasSummary = true
	m.replayed = replayed
}

func (m *managedSearchExecutor) result() (entitlement.Summary, bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.summary, m.hasSummary, m.replayed
}

type managedSearchError struct {
	cause error
}

func (e *managedSearchError) Error() string { return e.cause.Error() }
func (e *managedSearchError) Unwrap() error { return e.cause }

func (e *managedSearchError) ContextWarning() (string, string) {
	switch {
	case errors.Is(e, entitlement.ErrPlanRequired):
		return "managed_embedding_plan_required", "managed file recall requires an active workspace plan"
	case errors.Is(e, entitlement.ErrQuotaExhausted):
		return "managed_embedding_quota_exhausted", "managed file recall quota is exhausted"
	case errors.Is(e, entitlement.ErrRequestIndeterminate):
		return "managed_embedding_indeterminate", "an earlier managed recall has an indeterminate outcome"
	case errors.Is(e, search.ErrReplayReferenceUnavailable):
		return "managed_embedding_replay_unavailable", "the prior managed recall result is no longer available"
	case errors.Is(e, errManagedProviderTimeout):
		return "managed_embedding_timeout", "managed file recall timed out"
	default:
		return "managed_embedding_unavailable", "managed file recall is temporarily unavailable"
	}
}

func (s *Server) managedSearcher(
	r *http.Request,
	operation string,
	payload any,
	query search.Query,
) (contextpack.Searcher, *managedSearchExecutor, error) {
	if s.DeploymentMode != "saas" {
		return s.Search, nil, nil
	}
	if strings.TrimSpace(s.ManagedEmbeddingProvider) == "" ||
		s.Entitlements == nil ||
		s.Search == nil {
		return nil, nil, entitlement.ErrEntitlementUnavailable
	}
	// A visual-only query does not invoke the managed text embedding provider.
	if query.Route == search.RouteVisual {
		return s.Search, nil, nil
	}
	spec, err := s.Search.EmbeddingSpec(r.Context(), query.UserID)
	if err != nil {
		return nil, nil, errManagedProviderUnavailable
	}
	if !entitlement.IsManagedProvider(s.ManagedEmbeddingProvider, spec) {
		return s.Search, nil, nil
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		return nil, nil, errManagedIdempotencyRequired
	}
	if len(idempotencyKey) > 200 {
		return nil, nil, errManagedIdempotencyRequired
	}
	ws := currentWorkspace(r)
	tok := r.Context().Value(ctxToken).(*auth.Token)
	fingerprint, err := managedRequestFingerprint(
		operation,
		spec,
		ws.ID,
		tok,
		payload,
	)
	if err != nil {
		return nil, nil, err
	}
	executor := &managedSearchExecutor{
		base:         s.Search,
		entitlements: s.Entitlements,
		providerSpec: spec,
		command: entitlement.ReserveCommand{
			WorkspaceID:        ws.ID,
			Operation:          operation,
			ProviderSpec:       spec,
			Units:              1,
			IdempotencyKey:     idempotencyKey,
			RequestFingerprint: fingerprint,
		},
	}
	return executor, executor, nil
}

func managedRequestFingerprint(
	operation, providerSpec string,
	workspaceID uuid.UUID,
	token *auth.Token,
	payload any,
) (string, error) {
	type fingerprintEnvelope struct {
		Contract     string    `json:"contract"`
		Operation    string    `json:"operation"`
		ProviderSpec string    `json:"provider_spec"`
		WorkspaceID  uuid.UUID `json:"workspace_id"`
		TokenID      uuid.UUID `json:"token_id"`
		AllowedPaths []string  `json:"allowed_paths"`
		Payload      any       `json:"payload"`
	}
	if token == nil {
		return "", errors.New("authenticated token required")
	}
	encoded, err := json.Marshal(fingerprintEnvelope{
		Contract:     "mem.managed_embedding.request.v1",
		Operation:    operation,
		ProviderSpec: providerSpec,
		WorkspaceID:  workspaceID,
		TokenID:      token.ID,
		AllowedPaths: token.Paths,
		Payload:      payload,
	})
	if err != nil {
		return "", fmt.Errorf("fingerprint managed request: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	if s.DeploymentMode != "saas" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":              true,
			"deployment_mode": "private",
		})
		return
	}
	if strings.TrimSpace(s.ManagedEmbeddingProvider) == "" ||
		s.Entitlements == nil {
		writeError(
			w,
			http.StatusServiceUnavailable,
			"entitlement_unavailable",
			"saas entitlement configuration is unavailable",
		)
		return
	}
	readyCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.Entitlements.Ready(readyCtx); err != nil {
		if s.Log != nil {
			s.Log.Error("readiness.entitlement_failed", "err", err)
		}
		writeError(
			w,
			http.StatusServiceUnavailable,
			"entitlement_unavailable",
			"saas entitlement store is unavailable",
		)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"deployment_mode": "saas",
	})
}

func (s *Server) handleEntitlementSummary(w http.ResponseWriter, r *http.Request) {
	if s.DeploymentMode != "saas" {
		writeJSON(w, http.StatusOK, map[string]any{
			"deployment_mode":  "private",
			"commercial_gate":  false,
			"upgrade_required": false,
			"plan":             "self_hosted",
			"status":           "active",
		})
		return
	}
	if s.Entitlements == nil || strings.TrimSpace(s.ManagedEmbeddingProvider) == "" {
		writeError(w, http.StatusServiceUnavailable, "entitlement_unavailable",
			"saas entitlement store is unavailable")
		return
	}
	summary, err := s.Entitlements.Summary(r.Context(), currentWorkspace(r).ID)
	if err != nil {
		if s.Log != nil {
			s.Log.Error("entitlement.summary_failed", "err", err)
		}
		writeError(w, http.StatusServiceUnavailable, "entitlement_unavailable",
			"saas entitlement store is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deployment_mode":   "saas",
		"commercial_gate":   true,
		"upgrade_required":  !summary.Qualifying,
		"managed_embedding": summary,
	})
}

func setManagedUsageHeaders(
	w http.ResponseWriter,
	summary entitlement.Summary,
	replayed bool,
) {
	w.Header().Set(
		"X-Mem-Managed-Embedding-Remaining",
		strconv.FormatInt(summary.Remaining, 10),
	)
	w.Header().Set("X-Mem-Managed-Embedding-Reset", summary.ResetAt.UTC().Format(time.RFC3339))
	if replayed {
		w.Header().Set("X-Mem-Idempotent-Replay", "true")
	}
}

func setRetryAfter(w http.ResponseWriter, resetAt time.Time) {
	seconds := int64(time.Until(resetAt).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
}

func writeManagedEmbeddingError(w http.ResponseWriter, err error) {
	var decision *entitlement.DecisionError
	if errors.As(err, &decision) {
		setManagedUsageHeaders(w, decision.Summary, false)
	}
	switch {
	case errors.Is(err, errManagedIdempotencyRequired):
		writeError(w, http.StatusBadRequest, "idempotency_key_required",
			"Idempotency-Key is required for managed embedding requests")
	case errors.Is(err, entitlement.ErrPlanRequired):
		writeError(w, http.StatusPaymentRequired, "plan_required",
			"an active workspace plan is required for managed embeddings")
	case errors.Is(err, entitlement.ErrQuotaExhausted):
		if decision != nil {
			setRetryAfter(w, decision.Summary.ResetAt)
		}
		writeError(w, http.StatusTooManyRequests, "quota_exhausted",
			"managed embedding quota is exhausted until reset")
	case errors.Is(err, entitlement.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict",
			"Idempotency-Key was already used with a different request")
	case errors.Is(err, entitlement.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusConflict, "request_in_progress",
			"a request with this Idempotency-Key is still in progress")
	case errors.Is(err, entitlement.ErrReleasedKey):
		writeError(w, http.StatusConflict, "idempotency_key_released",
			"use a new Idempotency-Key after a released reservation")
	case errors.Is(err, entitlement.ErrRequestIndeterminate):
		writeError(w, http.StatusBadGateway, "managed_embedding_indeterminate",
			"the earlier provider outcome is indeterminate; no retry was attempted")
	case errors.Is(err, search.ErrReplayReferenceUnavailable):
		writeError(w, http.StatusGone, "idempotency_result_unavailable",
			"the prior result can no longer be reconstructed; no provider retry was attempted")
	case errors.Is(err, errManagedProviderTimeout):
		writeError(w, http.StatusGatewayTimeout, "managed_embedding_timeout",
			"managed embedding provider timed out")
	case errors.Is(err, errManagedProviderUnavailable):
		writeError(w, http.StatusBadGateway, "managed_embedding_unavailable",
			"managed embedding provider is temporarily unavailable")
	case errors.Is(err, errManagedUsageCommit):
		writeError(w, http.StatusBadGateway, "managed_embedding_usage_indeterminate",
			"managed embedding usage could not be finalized safely")
	case errors.Is(err, entitlement.ErrEntitlementUnavailable):
		writeError(w, http.StatusServiceUnavailable, "entitlement_unavailable",
			"saas entitlement store is unavailable")
	default:
		writeError(w, http.StatusBadGateway, "managed_embedding_unavailable",
			"managed embedding request failed")
	}
}

// Package entitlement owns the payment-provider-neutral commercial policy and
// auditable usage state for platform-managed embedding calls.
//
// Workspace membership is deliberately resolved by the HTTP authorization
// layer before this service is called. This package accepts only a
// server-derived workspace ID and never interprets client-supplied identity.
package entitlement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	StatusReserved      = "reserved"
	StatusSucceeded     = "succeeded"
	StatusReleased      = "released"
	StatusIndeterminate = "indeterminate"

	defaultReservationTTL = 2 * time.Minute
	maxReplayReferences   = 100
)

var (
	ErrPlanRequired           = errors.New("managed embedding plan required")
	ErrQuotaExhausted         = errors.New("managed embedding quota exhausted")
	ErrIdempotencyConflict    = errors.New("managed embedding idempotency conflict")
	ErrRequestInProgress      = errors.New("managed embedding request is in progress")
	ErrRequestIndeterminate   = errors.New("managed embedding request outcome is indeterminate")
	ErrReleasedKey            = errors.New("managed embedding idempotency key was released")
	ErrReplayResultInvalid    = errors.New("managed embedding replay result is invalid")
	ErrReservationNotFound    = errors.New("managed embedding reservation not found")
	ErrInvalidTransition      = errors.New("invalid managed embedding usage transition")
	ErrEntitlementUnavailable = errors.New("managed embedding entitlement store unavailable")
)

// Summary is the read-only commercial state exposed to authenticated clients.
// Reserved units are included in the remaining calculation so concurrent
// requests cannot oversubscribe the plan.
type Summary struct {
	WorkspaceID uuid.UUID `json:"workspace_id"`
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

// ReplayReference is the only persisted success material. It deliberately
// contains stable derived identifiers and a score, never request text, file
// content, vectors, credentials, or an upstream response body.
type ReplayReference struct {
	Source     string    `json:"source"`
	EvidenceID uuid.UUID `json:"evidence_id"`
	FileID     uuid.UUID `json:"file_id"`
	Score      float32   `json:"score"`
}

type ReserveCommand struct {
	WorkspaceID        uuid.UUID
	Operation          string
	ProviderSpec       string
	Units              int64
	IdempotencyKey     string
	RequestFingerprint string
}

type Reservation struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	Operation   string
	Provider    string
	Model       string
	Units       int64
	Status      string
	Replayed    bool
	References  []ReplayReference
	Summary     Summary
}

// DecisionError retains the atomic quota snapshot returned by a failed
// reservation decision.
type DecisionError struct {
	Kind    error
	Summary Summary
}

func (e *DecisionError) Error() string { return e.Kind.Error() }
func (e *DecisionError) Unwrap() error { return e.Kind }

type Service struct {
	pool           *pgxpool.Pool
	reservationTTL time.Duration
	now            func() time.Time
}

func New(pool *pgxpool.Pool, reservationTTL time.Duration) *Service {
	if reservationTTL <= 0 {
		reservationTTL = defaultReservationTTL
	}
	return &Service{
		pool:           pool,
		reservationTTL: reservationTTL,
		now:            func() time.Time { return time.Now().UTC() },
	}
}

// IsManagedProvider performs an exact, server-owned match. It intentionally
// has no fallback: a typo or unavailable managed provider must fail closed
// instead of silently switching billing or privacy boundaries.
func IsManagedProvider(configured, resolved string) bool {
	configured = strings.TrimSpace(configured)
	resolved = strings.TrimSpace(resolved)
	return configured != "" && resolved == configured
}

// Ready checks configuration-independent schema/store availability. It does
// not require any workspace to have a paid plan.
func (s *Service) Ready(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return ErrEntitlementUnavailable
	}
	var ready bool
	err := s.pool.QueryRow(ctx, `
		SELECT to_regclass('public.workspace_entitlements') IS NOT NULL
		   AND to_regclass('public.managed_embedding_usage') IS NOT NULL
		   AND to_regclass('public.managed_embedding_usage_events') IS NOT NULL
		   AND to_regclass('public.managed_embedding_replay_results') IS NOT NULL
	`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEntitlementUnavailable, err)
	}
	if !ready {
		return ErrEntitlementUnavailable
	}
	return nil
}

func (s *Service) Summary(ctx context.Context, workspaceID uuid.UUID) (Summary, error) {
	if s == nil || s.pool == nil || workspaceID == uuid.Nil {
		return Summary{}, ErrEntitlementUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Summary{}, fmt.Errorf("%w: begin summary: %v", ErrEntitlementUnavailable, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out, err := s.prepareEntitlementTx(ctx, tx, workspaceID, s.now())
	if err != nil {
		return Summary{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Summary{}, fmt.Errorf("%w: commit summary: %v", ErrEntitlementUnavailable, err)
	}
	return out, nil
}

// Reserve atomically evaluates plan/quota state and creates one workspace-
// scoped idempotent reservation before any provider invocation.
func (s *Service) Reserve(ctx context.Context, cmd ReserveCommand) (*Reservation, error) {
	if err := validateReserveCommand(cmd); err != nil {
		return nil, err
	}
	if s == nil || s.pool == nil {
		return nil, ErrEntitlementUnavailable
	}
	providerName, model, _ := strings.Cut(cmd.ProviderSpec, ":")
	idempotencyHash := hashDomain(
		"mem/managed-embedding/idempotency/v1/"+cmd.WorkspaceID.String()+"/"+cmd.Operation,
		cmd.IdempotencyKey,
	)
	now := s.now()

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: begin reserve: %v", ErrEntitlementUnavailable, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	summary, err := s.prepareEntitlementTx(ctx, tx, cmd.WorkspaceID, now)
	if err != nil {
		return nil, err
	}

	// The entitlement row serializes all reservations for one workspace.
	// Re-check idempotency after taking that lock so concurrent first attempts
	// cannot both observe an absent usage row.
	existing, found, err := loadReservationByKey(
		ctx,
		tx,
		cmd.WorkspaceID,
		cmd.Operation,
		idempotencyHash,
	)
	if err != nil {
		return nil, err
	}
	if found {
		if existing.RequestFingerprint != cmd.RequestFingerprint {
			return nil, commitDecision(ctx, tx, ErrIdempotencyConflict)
		}
		switch existing.Status {
		case StatusSucceeded:
			references, err := loadReplayReferences(ctx, tx, existing.ID)
			if err != nil {
				return nil, err
			}
			completeSummary(&summary, now)
			if err := tx.Commit(ctx); err != nil {
				return nil, fmt.Errorf("%w: commit replay: %v", ErrEntitlementUnavailable, err)
			}
			return &Reservation{
				ID:          existing.ID,
				WorkspaceID: cmd.WorkspaceID,
				Operation:   existing.Operation,
				Provider:    existing.Provider,
				Model:       existing.Model,
				Units:       existing.Units,
				Status:      existing.Status,
				Replayed:    true,
				References:  references,
				Summary:     summary,
			}, nil
		case StatusReserved:
			return nil, commitDecision(ctx, tx, ErrRequestInProgress)
		case StatusIndeterminate:
			return nil, commitDecision(ctx, tx, ErrRequestIndeterminate)
		case StatusReleased:
			return nil, commitDecision(ctx, tx, ErrReleasedKey)
		default:
			return nil, commitDecision(ctx, tx, ErrInvalidTransition)
		}
	}

	if summary.Status != "active" || summary.Plan == "free" ||
		now.Before(summary.PeriodStart) {
		decision := &DecisionError{Kind: ErrPlanRequired, Summary: summary}
		return nil, commitDecision(ctx, tx, decision)
	}
	if summary.Remaining < cmd.Units {
		decision := &DecisionError{Kind: ErrQuotaExhausted, Summary: summary}
		return nil, commitDecision(ctx, tx, decision)
	}

	usageID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO managed_embedding_usage (
		    id, workspace_id, operation, provider, model, units, status,
		    request_fingerprint_sha256, idempotency_key_sha256,
		    period_start, period_end, created_at, updated_at
		) VALUES (
		    $1, $2, $3, $4, $5, $6, 'reserved',
		    $7, $8, $9, $10, $11, $11
		)
	`, usageID, cmd.WorkspaceID, cmd.Operation, providerName, model, cmd.Units,
		cmd.RequestFingerprint, idempotencyHash,
		summary.PeriodStart, summary.ResetAt, now); err != nil {
		return nil, fmt.Errorf("%w: insert reservation: %v", ErrEntitlementUnavailable, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE workspace_entitlements
		   SET managed_embedding_units_reserved =
		           managed_embedding_units_reserved + $2,
		       updated_at = $3
		 WHERE workspace_id = $1
	`, cmd.WorkspaceID, cmd.Units, now); err != nil {
		return nil, fmt.Errorf("%w: reserve quota: %v", ErrEntitlementUnavailable, err)
	}
	if err := insertEvent(ctx, tx, usageRow{
		ID:                 usageID,
		WorkspaceID:        cmd.WorkspaceID,
		Operation:          cmd.Operation,
		Provider:           providerName,
		Model:              model,
		Units:              cmd.Units,
		Status:             StatusReserved,
		RequestFingerprint: cmd.RequestFingerprint,
		IdempotencyHash:    idempotencyHash,
	}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit reserve: %v", ErrEntitlementUnavailable, err)
	}
	summary.Reserved += cmd.Units
	summary.Remaining -= cmd.Units
	return &Reservation{
		ID:          usageID,
		WorkspaceID: cmd.WorkspaceID,
		Operation:   cmd.Operation,
		Provider:    providerName,
		Model:       model,
		Units:       cmd.Units,
		Status:      StatusReserved,
		Summary:     summary,
	}, nil
}

func commitDecision(ctx context.Context, tx pgx.Tx, decision error) error {
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: commit reservation decision: %v", ErrEntitlementUnavailable, err)
	}
	return decision
}

func (s *Service) Finalize(
	ctx context.Context,
	usageID uuid.UUID,
	references []ReplayReference,
) (Summary, error) {
	if err := validateReplayReferences(references); err != nil {
		return Summary{}, err
	}
	return s.transition(ctx, usageID, StatusSucceeded, references)
}

func (s *Service) Release(ctx context.Context, usageID uuid.UUID) (Summary, error) {
	return s.transition(ctx, usageID, StatusReleased, nil)
}

func (s *Service) MarkIndeterminate(
	ctx context.Context,
	usageID uuid.UUID,
) (Summary, error) {
	return s.transition(ctx, usageID, StatusIndeterminate, nil)
}

func (s *Service) transition(
	ctx context.Context,
	usageID uuid.UUID,
	target string,
	references []ReplayReference,
) (Summary, error) {
	if s == nil || s.pool == nil || usageID == uuid.Nil {
		return Summary{}, ErrEntitlementUnavailable
	}
	now := s.now()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Summary{}, fmt.Errorf("%w: begin transition: %v", ErrEntitlementUnavailable, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, found, err := loadReservationByID(ctx, tx, usageID)
	if err != nil {
		return Summary{}, err
	}
	if !found {
		return Summary{}, ErrReservationNotFound
	}
	if row.Status == target {
		summary, err := summaryTx(ctx, tx, row.WorkspaceID, now)
		if err != nil {
			return Summary{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Summary{}, fmt.Errorf("%w: commit transition replay: %v", ErrEntitlementUnavailable, err)
		}
		return summary, nil
	}
	if row.Status != StatusReserved {
		return Summary{}, ErrInvalidTransition
	}

	switch target {
	case StatusSucceeded:
		for rank, ref := range references {
			if _, err := tx.Exec(ctx, `
				INSERT INTO managed_embedding_replay_results (
				    usage_id, rank, source, evidence_id, file_id, score, created_at
				) VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (usage_id, rank) DO NOTHING
			`, usageID, rank, ref.Source, ref.EvidenceID, ref.FileID, ref.Score, now); err != nil {
				return Summary{}, fmt.Errorf("%w: store replay reference: %v", ErrEntitlementUnavailable, err)
			}
		}
		tag, err := tx.Exec(ctx, `
			UPDATE workspace_entitlements
			   SET managed_embedding_units_reserved =
			           managed_embedding_units_reserved - $2,
			       managed_embedding_units_consumed =
			           managed_embedding_units_consumed + $2,
			       updated_at = $3
			 WHERE workspace_id = $1
			   AND period_start = $4
			   AND managed_embedding_units_reserved >= $2
		`, row.WorkspaceID, row.Units, now, row.PeriodStart)
		if err != nil {
			return Summary{}, fmt.Errorf("%w: finalize quota: %v", ErrEntitlementUnavailable, err)
		}
		if tag.RowsAffected() != 1 {
			return Summary{}, ErrRequestIndeterminate
		}
	case StatusReleased:
		tag, err := tx.Exec(ctx, `
			UPDATE workspace_entitlements
			   SET managed_embedding_units_reserved =
			           managed_embedding_units_reserved - $2,
			       updated_at = $3
			 WHERE workspace_id = $1
			   AND period_start = $4
			   AND managed_embedding_units_reserved >= $2
		`, row.WorkspaceID, row.Units, now, row.PeriodStart)
		if err != nil {
			return Summary{}, fmt.Errorf("%w: release quota: %v", ErrEntitlementUnavailable, err)
		}
		if tag.RowsAffected() != 1 {
			return Summary{}, ErrRequestIndeterminate
		}
	case StatusIndeterminate:
		// Keep the unit reserved. A timeout cannot prove the provider did not
		// execute; retaining the hold prevents a retry from double spending it.
	default:
		return Summary{}, ErrInvalidTransition
	}

	tag, err := tx.Exec(ctx, `
		UPDATE managed_embedding_usage
		   SET status = $2, updated_at = $3
		 WHERE id = $1 AND status = 'reserved'
	`, usageID, target, now)
	if err != nil {
		return Summary{}, fmt.Errorf("%w: update usage: %v", ErrEntitlementUnavailable, err)
	}
	if tag.RowsAffected() != 1 {
		return Summary{}, ErrInvalidTransition
	}
	row.Status = target
	if err := insertEvent(ctx, tx, row, now); err != nil {
		return Summary{}, err
	}
	summary, err := summaryTx(ctx, tx, row.WorkspaceID, now)
	if err != nil {
		return Summary{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Summary{}, fmt.Errorf("%w: commit transition: %v", ErrEntitlementUnavailable, err)
	}
	return summary, nil
}

// ReconcileStale marks crash-orphaned reservations indeterminate. Their units
// stay held for the current period, and same-key retries never invoke the
// provider again. Period rollover clears old-period counters.
func (s *Service) ReconcileStale(ctx context.Context) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, ErrEntitlementUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("%w: begin reconcile: %v", ErrEntitlementUnavailable, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	count, err := s.reconcileStaleTx(ctx, tx, uuid.Nil, s.now())
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("%w: commit reconcile: %v", ErrEntitlementUnavailable, err)
	}
	return count, nil
}

func (s *Service) prepareEntitlementTx(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
	now time.Time,
) (Summary, error) {
	var summary Summary
	err := tx.QueryRow(ctx, `
		SELECT workspace_id, plan_key, status,
		       managed_embedding_unit_limit,
		       managed_embedding_units_reserved,
		       managed_embedding_units_consumed,
		       period_start, period_end
		  FROM workspace_entitlements
		 WHERE workspace_id = $1
		 FOR UPDATE
	`, workspaceID).Scan(
		&summary.WorkspaceID,
		&summary.Plan,
		&summary.Status,
		&summary.UnitLimit,
		&summary.Reserved,
		&summary.Consumed,
		&summary.PeriodStart,
		&summary.ResetAt,
	)
	if err != nil {
		return Summary{}, fmt.Errorf("%w: lock entitlement: %v", ErrEntitlementUnavailable, err)
	}
	if _, err := s.reconcileStaleTx(ctx, tx, workspaceID, now); err != nil {
		return Summary{}, err
	}
	if !now.Before(summary.ResetAt) {
		// Any request crossing a billing-period boundary has an unknowable
		// provider outcome. Freeze it before resetting the old-period counters.
		if _, err := reconcileExpiredPeriodTx(
			ctx,
			tx,
			workspaceID,
			summary.ResetAt,
			now,
		); err != nil {
			return Summary{}, err
		}
		summary = rollPeriod(summary, now)
		if _, err := tx.Exec(ctx, `
			UPDATE workspace_entitlements
			   SET period_start = $2,
			       period_end = $3,
			       managed_embedding_units_reserved = 0,
			       managed_embedding_units_consumed = 0,
			       updated_at = $4
			 WHERE workspace_id = $1
		`, summary.WorkspaceID, summary.PeriodStart, summary.ResetAt, now); err != nil {
			return Summary{}, fmt.Errorf("%w: roll entitlement period: %v", ErrEntitlementUnavailable, err)
		}
	}
	completeSummary(&summary, now)
	return summary, nil
}

func reconcileExpiredPeriodTx(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
	periodEnd, now time.Time,
) (int64, error) {
	rows, err := tx.Query(ctx, `
		UPDATE managed_embedding_usage
		   SET status = 'indeterminate', updated_at = $3
		 WHERE status = 'reserved'
		   AND workspace_id = $1
		   AND period_end <= $2
		RETURNING id, workspace_id, operation, provider, model, units, status,
		          request_fingerprint_sha256, idempotency_key_sha256,
		          period_start, period_end, created_at, updated_at
	`, workspaceID, periodEnd, now)
	if err != nil {
		return 0, fmt.Errorf("%w: reconcile expired-period reservations: %v", ErrEntitlementUnavailable, err)
	}
	defer rows.Close()
	var expired []usageRow
	for rows.Next() {
		var row usageRow
		if err := scanUsage(rows, &row); err != nil {
			return 0, fmt.Errorf("%w: scan expired-period reservation: %v", ErrEntitlementUnavailable, err)
		}
		expired = append(expired, row)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("%w: expired-period rows: %v", ErrEntitlementUnavailable, err)
	}
	rows.Close()
	for _, row := range expired {
		if err := insertEvent(ctx, tx, row, now); err != nil {
			return 0, err
		}
	}
	return int64(len(expired)), nil
}

func (s *Service) reconcileStaleTx(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
	now time.Time,
) (int64, error) {
	query := `
		UPDATE managed_embedding_usage
		   SET status = 'indeterminate', updated_at = $1
		 WHERE status = 'reserved'
		   AND updated_at <= $2
	`
	args := []any{now, now.Add(-s.reservationTTL)}
	if workspaceID != uuid.Nil {
		query += " AND workspace_id = $3"
		args = append(args, workspaceID)
	}
	query += `
		RETURNING id, workspace_id, operation, provider, model, units, status,
		          request_fingerprint_sha256, idempotency_key_sha256,
		          period_start, period_end, created_at, updated_at
	`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("%w: reconcile stale reservations: %v", ErrEntitlementUnavailable, err)
	}
	defer rows.Close()
	var stale []usageRow
	for rows.Next() {
		var row usageRow
		if err := scanUsage(rows, &row); err != nil {
			return 0, fmt.Errorf("%w: scan stale reservation: %v", ErrEntitlementUnavailable, err)
		}
		stale = append(stale, row)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("%w: stale reservation rows: %v", ErrEntitlementUnavailable, err)
	}
	rows.Close()
	for _, row := range stale {
		if err := insertEvent(ctx, tx, row, now); err != nil {
			return 0, err
		}
	}
	return int64(len(stale)), nil
}

type usageRow struct {
	ID                 uuid.UUID
	WorkspaceID        uuid.UUID
	Operation          string
	Provider           string
	Model              string
	Units              int64
	Status             string
	RequestFingerprint string
	IdempotencyHash    string
	PeriodStart        time.Time
	PeriodEnd          time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUsage(scanner rowScanner, row *usageRow) error {
	return scanner.Scan(
		&row.ID,
		&row.WorkspaceID,
		&row.Operation,
		&row.Provider,
		&row.Model,
		&row.Units,
		&row.Status,
		&row.RequestFingerprint,
		&row.IdempotencyHash,
		&row.PeriodStart,
		&row.PeriodEnd,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
}

func loadReservationByKey(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
	operation, idempotencyHash string,
) (usageRow, bool, error) {
	var row usageRow
	err := scanUsage(tx.QueryRow(ctx, `
		SELECT id, workspace_id, operation, provider, model, units, status,
		       request_fingerprint_sha256, idempotency_key_sha256,
		       period_start, period_end, created_at, updated_at
		  FROM managed_embedding_usage
		 WHERE workspace_id = $1
		   AND operation = $2
		   AND idempotency_key_sha256 = $3
		 FOR UPDATE
	`, workspaceID, operation, idempotencyHash), &row)
	if errors.Is(err, pgx.ErrNoRows) {
		return usageRow{}, false, nil
	}
	if err != nil {
		return usageRow{}, false, fmt.Errorf("%w: read idempotency record: %v", ErrEntitlementUnavailable, err)
	}
	return row, true, nil
}

func loadReservationByID(
	ctx context.Context,
	tx pgx.Tx,
	usageID uuid.UUID,
) (usageRow, bool, error) {
	var row usageRow
	err := scanUsage(tx.QueryRow(ctx, `
		SELECT id, workspace_id, operation, provider, model, units, status,
		       request_fingerprint_sha256, idempotency_key_sha256,
		       period_start, period_end, created_at, updated_at
		  FROM managed_embedding_usage
		 WHERE id = $1
		 FOR UPDATE
	`, usageID), &row)
	if errors.Is(err, pgx.ErrNoRows) {
		return usageRow{}, false, nil
	}
	if err != nil {
		return usageRow{}, false, fmt.Errorf("%w: read reservation: %v", ErrEntitlementUnavailable, err)
	}
	return row, true, nil
}

func loadReplayReferences(
	ctx context.Context,
	tx pgx.Tx,
	usageID uuid.UUID,
) ([]ReplayReference, error) {
	rows, err := tx.Query(ctx, `
		SELECT source, evidence_id, file_id, score
		  FROM managed_embedding_replay_results
		 WHERE usage_id = $1
		 ORDER BY rank
	`, usageID)
	if err != nil {
		return nil, fmt.Errorf("%w: replay references unavailable", ErrReplayResultInvalid)
	}
	defer rows.Close()
	refs := make([]ReplayReference, 0, 16)
	for rows.Next() {
		var ref ReplayReference
		if err := rows.Scan(&ref.Source, &ref.EvidenceID, &ref.FileID, &ref.Score); err != nil {
			return nil, fmt.Errorf("%w: replay reference unreadable", ErrReplayResultInvalid)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: replay references unavailable", ErrReplayResultInvalid)
	}
	if err := validateReplayReferences(refs); err != nil {
		return nil, err
	}
	return refs, nil
}

func insertEvent(ctx context.Context, tx pgx.Tx, row usageRow, at time.Time) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO managed_embedding_usage_events (
		    usage_id, workspace_id, operation, provider, model, units, status,
		    request_fingerprint_sha256, idempotency_key_sha256, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, row.ID, row.WorkspaceID, row.Operation, row.Provider, row.Model,
		row.Units, row.Status, row.RequestFingerprint, row.IdempotencyHash, at); err != nil {
		return fmt.Errorf("%w: append usage event: %v", ErrEntitlementUnavailable, err)
	}
	return nil
}

func summaryTx(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
	now time.Time,
) (Summary, error) {
	var out Summary
	err := tx.QueryRow(ctx, `
		SELECT workspace_id, plan_key, status,
		       managed_embedding_unit_limit,
		       managed_embedding_units_reserved,
		       managed_embedding_units_consumed,
		       period_start, period_end
		  FROM workspace_entitlements
		 WHERE workspace_id = $1
	`, workspaceID).Scan(
		&out.WorkspaceID,
		&out.Plan,
		&out.Status,
		&out.UnitLimit,
		&out.Reserved,
		&out.Consumed,
		&out.PeriodStart,
		&out.ResetAt,
	)
	if err != nil {
		return Summary{}, fmt.Errorf("%w: read entitlement summary: %v", ErrEntitlementUnavailable, err)
	}
	completeSummary(&out, now)
	return out, nil
}

func completeSummary(summary *Summary, now time.Time) {
	summary.Qualifying = summary.Status == "active" &&
		summary.Plan != "free" &&
		!now.Before(summary.PeriodStart) &&
		now.Before(summary.ResetAt)
	remaining := summary.UnitLimit - summary.Reserved - summary.Consumed
	if remaining < 0 {
		remaining = 0
	}
	summary.Remaining = remaining
}

func rollPeriod(summary Summary, now time.Time) Summary {
	duration := summary.ResetAt.Sub(summary.PeriodStart)
	if duration <= 0 {
		duration = 30 * 24 * time.Hour
	}
	for !now.Before(summary.ResetAt) {
		summary.PeriodStart = summary.ResetAt
		summary.ResetAt = summary.ResetAt.Add(duration)
	}
	summary.Reserved = 0
	summary.Consumed = 0
	summary.Remaining = summary.UnitLimit
	return summary
}

func validateReserveCommand(cmd ReserveCommand) error {
	if cmd.WorkspaceID == uuid.Nil {
		return errors.New("workspace_id is required")
	}
	if len(cmd.Operation) == 0 || len(cmd.Operation) > 64 {
		return errors.New("operation must be 1-64 characters")
	}
	for i, r := range cmd.Operation {
		if (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9') ||
			(i > 0 && (r == '_' || r == '.' || r == '-')) {
			continue
		}
		return errors.New("operation has invalid characters")
	}
	provider, model, ok := strings.Cut(strings.TrimSpace(cmd.ProviderSpec), ":")
	if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
		return errors.New("provider spec must be '<provider>:<model>'")
	}
	if cmd.Units <= 0 {
		return errors.New("units must be positive")
	}
	if len(cmd.IdempotencyKey) == 0 || len(cmd.IdempotencyKey) > 200 {
		return errors.New("idempotency key must be 1-200 characters")
	}
	if len(cmd.RequestFingerprint) != 64 {
		return errors.New("request fingerprint must be a SHA-256 hex digest")
	}
	if _, err := hex.DecodeString(cmd.RequestFingerprint); err != nil {
		return errors.New("request fingerprint must be a SHA-256 hex digest")
	}
	return nil
}

func validateReplayReferences(refs []ReplayReference) error {
	if len(refs) > maxReplayReferences {
		return ErrReplayResultInvalid
	}
	for _, ref := range refs {
		if ref.Source != "text" && ref.Source != "visual" {
			return ErrReplayResultInvalid
		}
		if ref.FileID == uuid.Nil || ref.EvidenceID == uuid.Nil {
			return ErrReplayResultInvalid
		}
		if math.IsNaN(float64(ref.Score)) ||
			math.IsInf(float64(ref.Score), 0) ||
			ref.Score < -1 ||
			ref.Score > 1 {
			return ErrReplayResultInvalid
		}
	}
	return nil
}

func hashDomain(domain, value string) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

package durablecontext

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/memory"
)

const (
	defaultLimit        = 50
	maxLimit            = 100
	maxSessionRefLength = 512
)

var principalRE = regexp.MustCompile(PrincipalPattern)

// Service persists and enforces the scoped durable-context allowlist.
type Service struct {
	pool     *pgxpool.Pool
	memories *memory.Service
}

// New constructs the durable-context service over pool, delegating per-memory
// reads to the canonical structured-memory service so path scope, forget, and
// lifecycle semantics can never diverge.
func New(pool *pgxpool.Pool, memories *memory.Service) *Service {
	return &Service{pool: pool, memories: memories}
}

func invalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidCommand, reason)
}

func validPrincipal(principal string) (string, error) {
	trimmed := strings.TrimSpace(principal)
	if trimmed == "" {
		return "", invalid("principal is required")
	}
	if !principalRE.MatchString(trimmed) {
		return "", invalid("principal must match " + PrincipalPattern)
	}
	return trimmed, nil
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

// Grant creates or re-activates one explicit read grant. The upsert is
// idempotent: re-granting a revoked triple clears the revocation instead of
// inserting a duplicate approval.
func (s *Service) Grant(ctx context.Context, cmd GrantCommand) (*Grant, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("durable context service is not configured")
	}
	if cmd.WorkspaceID == uuid.Nil {
		return nil, invalid("workspace_id is required")
	}
	if cmd.MemoryID == uuid.Nil {
		return nil, invalid("memory_id is required")
	}
	principal, err := validPrincipal(cmd.Principal)
	if err != nil {
		return nil, err
	}

	var status string
	err = s.pool.QueryRow(ctx, `
		SELECT lifecycle_status
		  FROM memories
		 WHERE workspace_id = $1 AND id = $2`,
		cmd.WorkspaceID, cmd.MemoryID,
	).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		// Out-of-workspace and absent memories are indistinguishable by design.
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve grant target: %w", err)
	}
	if status == memory.StatusForgotten {
		// A redacted payload can never be resumed; approving it would lie.
		return nil, ErrNotFound
	}

	grant, err := scanGrant(s.pool.QueryRow(ctx, `
		INSERT INTO durable_context_grants (
			workspace_id, principal, memory_id, mode,
			granted_by_user_id, granted_by_token_id
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (workspace_id, principal, memory_id) DO UPDATE
		   SET mode = 'read',
		       granted_by_user_id = EXCLUDED.granted_by_user_id,
		       granted_by_token_id = EXCLUDED.granted_by_token_id,
		       granted_at = now(),
		       revoked_at = NULL,
		       revoked_by_user_id = NULL,
		       revoked_by_token_id = NULL,
		       updated_at = now()
		RETURNING `+grantColumns,
		cmd.WorkspaceID, principal, cmd.MemoryID, ModeRead,
		cmd.ActorUserID, cmd.ActorTokenID,
	))
	if err != nil {
		return nil, fmt.Errorf("grant durable context: %w", err)
	}
	return &grant, nil
}

// Revoke soft-revokes one grant. Revoking an already revoked grant replays
// idempotently and returns the current row.
func (s *Service) Revoke(ctx context.Context, cmd RevokeCommand) (*Grant, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("durable context service is not configured")
	}
	if cmd.WorkspaceID == uuid.Nil {
		return nil, invalid("workspace_id is required")
	}
	if cmd.GrantID == uuid.Nil {
		return nil, invalid("grant_id is required")
	}

	grant, err := scanGrant(s.pool.QueryRow(ctx, `
		UPDATE durable_context_grants
		   SET revoked_at = now(),
		       revoked_by_user_id = $3,
		       revoked_by_token_id = $4,
		       updated_at = now()
		 WHERE workspace_id = $1 AND id = $2
		RETURNING `+grantColumns,
		cmd.WorkspaceID, cmd.GrantID, cmd.ActorUserID, cmd.ActorTokenID,
	))
	if err == nil {
		return &grant, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("revoke durable context grant: %w", err)
	}

	// No live grant was updated: replay onto an already revoked row or report
	// a genuine miss.
	grant, err = scanGrant(s.pool.QueryRow(ctx, `
		SELECT `+grantColumns+`
		  FROM durable_context_grants
		 WHERE workspace_id = $1 AND id = $2`,
		cmd.WorkspaceID, cmd.GrantID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("revoke durable context grant: %w", err)
	}
	return &grant, nil
}

// ListGrants returns workspace grants newest-first, optionally narrowed to
// one principal.
func (s *Service) ListGrants(ctx context.Context, q ListGrantsQuery) ([]Grant, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("durable context service is not configured")
	}
	if q.WorkspaceID == uuid.Nil {
		return nil, invalid("workspace_id is required")
	}
	principal := strings.TrimSpace(q.Principal)
	if principal != "" && !principalRE.MatchString(principal) {
		return nil, invalid("principal must match " + PrincipalPattern)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+grantColumns+`
		  FROM durable_context_grants
		 WHERE workspace_id = $1
		   AND ($2 = '' OR principal = $2)
		 ORDER BY granted_at DESC, id DESC
		 LIMIT $3`,
		q.WorkspaceID, principal, clampLimit(q.Limit),
	)
	if err != nil {
		return nil, fmt.Errorf("list durable context grants: %w", err)
	}
	defer rows.Close()

	grants := make([]Grant, 0)
	for rows.Next() {
		grant, err := scanGrant(rows)
		if err != nil {
			return nil, fmt.Errorf("list durable context grants: %w", err)
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

// ListGrantViews returns workspace grants newest-first, annotated with the
// lifecycle of each granted memory, optionally narrowed to one principal.
// The grants table foreign-keys memories with ON DELETE CASCADE, so the join
// cannot drop rows; a grant always sees its memory's current lifecycle.
func (s *Service) ListGrantViews(ctx context.Context, q ListGrantsQuery) ([]GrantView, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("durable context service is not configured")
	}
	if q.WorkspaceID == uuid.Nil {
		return nil, invalid("workspace_id is required")
	}
	principal := strings.TrimSpace(q.Principal)
	if principal != "" && !principalRE.MatchString(principal) {
		return nil, invalid("principal must match " + PrincipalPattern)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+grantColumnsAliased+`, m.lifecycle_status
		  FROM durable_context_grants g
		  JOIN memories m
		    ON m.workspace_id = g.workspace_id AND m.id = g.memory_id
		 WHERE g.workspace_id = $1
		   AND ($2 = '' OR g.principal = $2)
		 ORDER BY g.granted_at DESC, g.id DESC
		 LIMIT $3`,
		q.WorkspaceID, principal, clampLimit(q.Limit),
	)
	if err != nil {
		return nil, fmt.Errorf("list durable context grants: %w", err)
	}
	defer rows.Close()

	views := make([]GrantView, 0)
	for rows.Next() {
		var view GrantView
		if err := scanGrantRow(rows, &view.Grant, &view.MemoryStatus); err != nil {
			return nil, fmt.Errorf("list durable context grants: %w", err)
		}
		view.Status = deriveGrantStatus(view.RevokedAt != nil, view.MemoryStatus)
		views = append(views, view)
	}
	return views, rows.Err()
}

// deriveGrantStatus maps one grant row plus its memory lifecycle to the view
// state surfaced by the allowlist listing. Revocation wins: a revoked grant
// stays revoked even if its memory later changes lifecycle.
func deriveGrantStatus(revoked bool, memoryStatus string) string {
	if revoked {
		return GrantStatusRevoked
	}
	switch memoryStatus {
	case memory.StatusArchived:
		return GrantStatusSuperseded
	case memory.StatusForgotten:
		return GrantStatusForgotten
	default:
		return GrantStatusActive
	}
}

// Recall resumes the approved, active context for one principal. A principal
// with no unrevoked grants is denied explicitly; granted memories that are
// outside the token path boundary, archived, or forgotten are absent.
func (s *Service) Recall(ctx context.Context, q RecallQuery) (*RecallResult, error) {
	if s == nil || s.pool == nil || s.memories == nil {
		return nil, fmt.Errorf("durable context service is not configured")
	}
	if q.WorkspaceID == uuid.Nil {
		return nil, invalid("workspace_id is required")
	}
	principal, err := validPrincipal(q.Principal)
	if err != nil {
		return nil, err
	}
	if len(q.SessionRef) > maxSessionRefLength {
		return nil, invalid("session_ref is too long")
	}

	var hasGrants bool
	err = s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM durable_context_grants
			 WHERE workspace_id = $1
			   AND principal = $2
			   AND revoked_at IS NULL
		)`,
		q.WorkspaceID, principal,
	).Scan(&hasGrants)
	if err != nil {
		return nil, fmt.Errorf("recall durable context: %w", err)
	}
	if !hasGrants {
		return nil, ErrScopeDenied
	}

	// Lifecycle filters inside the query so LIMIT counts only resumable rows;
	// otherwise newer grants pointing at archived or forgotten memories would
	// silently crowd approved active context out of the window.
	rows, err := s.pool.Query(ctx, `
		SELECT g.memory_id
		  FROM durable_context_grants g
		  JOIN memories m
		    ON m.workspace_id = g.workspace_id AND m.id = g.memory_id
		 WHERE g.workspace_id = $1
		   AND g.principal = $2
		   AND g.revoked_at IS NULL
		   AND m.lifecycle_status = $4
		 ORDER BY m.created_at DESC, m.id DESC
		 LIMIT $3`,
		q.WorkspaceID, principal, clampLimit(q.Limit), memory.StatusActive,
	)
	if err != nil {
		return nil, fmt.Errorf("recall durable context: %w", err)
	}
	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("recall durable context: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recall durable context: %w", err)
	}
	rows.Close()

	hits := make([]RecallHit, 0, len(ids))
	for _, id := range ids {
		hit, err := s.resolveHit(ctx, q.WorkspaceID, principal, id, q.AllowedPaths)
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForgotten) ||
			errors.Is(err, ErrStale) {
			// F5A.5: out-of-scope, superseded, and redacted items are absent,
			// never partially recalled.
			continue
		}
		if err != nil {
			return nil, err
		}
		hits = append(hits, *hit)
	}
	return &RecallResult{
		Contract:  ContractVersion,
		Principal: principal,
		Hits:      hits,
	}, nil
}

// Get resolves one granted memory for one principal.
func (s *Service) Get(ctx context.Context, q GetQuery) (*RecallHit, error) {
	if s == nil || s.pool == nil || s.memories == nil {
		return nil, fmt.Errorf("durable context service is not configured")
	}
	if q.WorkspaceID == uuid.Nil {
		return nil, invalid("workspace_id is required")
	}
	if q.MemoryID == uuid.Nil {
		return nil, invalid("memory_id is required")
	}
	principal, err := validPrincipal(q.Principal)
	if err != nil {
		return nil, err
	}

	var approved bool
	err = s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM durable_context_grants
			 WHERE workspace_id = $1
			   AND principal = $2
			   AND memory_id = $3
			   AND revoked_at IS NULL
		)`,
		q.WorkspaceID, principal, q.MemoryID,
	).Scan(&approved)
	if err != nil {
		return nil, fmt.Errorf("check durable context grant: %w", err)
	}
	if !approved {
		// F5A.5: unapproved objects behave as nonexistent.
		return nil, ErrNotFound
	}
	return s.resolveHit(ctx, q.WorkspaceID, principal, q.MemoryID, q.AllowedPaths)
}

// resolveHit reads one memory through the canonical service so path scope and
// forget semantics can never diverge from structured memory.
func (s *Service) resolveHit(
	ctx context.Context,
	workspaceID uuid.UUID,
	principal string,
	memoryID uuid.UUID,
	allowedPaths []string,
) (*RecallHit, error) {
	record, err := s.memories.Get(ctx, memory.Query{
		WorkspaceID:  workspaceID,
		MemoryID:     memoryID,
		AllowedPaths: allowedPaths,
	})
	if errors.Is(err, memory.ErrNotFound) {
		return nil, ErrNotFound
	}
	if errors.Is(err, memory.ErrForgotten) {
		return nil, ErrForgotten
	}
	if err != nil {
		return nil, err
	}
	if record.LifecycleStatus == memory.StatusArchived {
		return nil, ErrStale
	}
	if record.LifecycleStatus != memory.StatusActive {
		return nil, ErrNotFound
	}
	return &RecallHit{
		Memory:       *record,
		Locator:      Locator(record.ID, record.StateVersion),
		StateVersion: record.StateVersion,
		Provenance:   record.Provenance(),
	}, nil
}

const grantColumns = `
	id, workspace_id, principal, memory_id, mode,
	granted_by_user_id, granted_by_token_id, granted_at,
	revoked_at, revoked_by_user_id, revoked_by_token_id, updated_at`

// grantColumnsAliased is the same projection qualified for queries that join
// durable_context_grants against other tables (aliased g).
const grantColumnsAliased = `
	g.id, g.workspace_id, g.principal, g.memory_id, g.mode,
	g.granted_by_user_id, g.granted_by_token_id, g.granted_at,
	g.revoked_at, g.revoked_by_user_id, g.revoked_by_token_id, g.updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanGrant(row rowScanner) (Grant, error) {
	var grant Grant
	err := scanGrantRow(row, &grant)
	return grant, err
}

// scanGrantRow scans the canonical grant column projection into grant,
// followed by any extra joined columns (e.g. the memory lifecycle status).
func scanGrantRow(row rowScanner, grant *Grant, extra ...any) error {
	dest := []any{
		&grant.ID,
		&grant.WorkspaceID,
		&grant.Principal,
		&grant.MemoryID,
		&grant.Mode,
		&grant.GrantedByUserID,
		&grant.GrantedByTokenID,
		&grant.GrantedAt,
		&grant.RevokedAt,
		&grant.RevokedByUserID,
		&grant.RevokedByTokenID,
		&grant.UpdatedAt,
	}
	dest = append(dest, extra...)
	return row.Scan(dest...)
}

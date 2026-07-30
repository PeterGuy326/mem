// Package workspacelock serializes virtual-path rewrites with writes that
// attach durable content to those paths.
//
// Every caller must take the workspace-row lock as the first database action
// in its transaction. Content writers use FOR KEY SHARE; AI profile selection
// and routed index-result commits use FOR NO KEY UPDATE; folder rename, move,
// and delete use FOR UPDATE. PostgreSQL then permits concurrent content writes
// while preventing path/profile mutations from observing or producing partial
// workspace state.
//
// The global AI pipeline lock order is:
//
//  1. workspace row (this package, keyed by workspace/owner)
//  2. workspace_entitlements
//  3. managed_embedding_usage rows in deterministic ID order
//  4. file-derived rows and the settlement outbox
//
// Profile selection may call a paid probe while holding step 1, so every
// routed result must also take step 1 before its existing entitlement→usage
// locks. Callers must never acquire the workspace row after either billing
// table.
package workspacelock

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ForContentWrite locks one workspace before a workspace-scoped content write.
func ForContentWrite(ctx context.Context, tx pgx.Tx, workspaceID uuid.UUID) error {
	var lockedID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id
		  FROM workspaces
		 WHERE id = $1
		 FOR KEY SHARE
	`, workspaceID).Scan(&lockedID); err != nil {
		return fmt.Errorf("lock workspace %s for content write: %w", workspaceID, err)
	}
	return nil
}

// ForContentWriteByOwner is ForContentWrite for the legacy resource-owner
// model used by files and folders. The unique owner column resolves exactly
// one workspace and keeps the lock order identical to workspace-ID writers.
func ForContentWriteByOwner(
	ctx context.Context,
	tx pgx.Tx,
	resourceOwnerUserID uuid.UUID,
) (uuid.UUID, error) {
	return lockByOwner(ctx, tx, resourceOwnerUserID, "FOR KEY SHARE")
}

// ForAIProfileCoordination serializes an active profile mutation with every
// routed result commit for that workspace. FOR NO KEY UPDATE conflicts with
// itself but remains compatible with ordinary FOR KEY SHARE content writes
// and foreign-key checks.
func ForAIProfileCoordination(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
) (uuid.UUID, error) {
	var resourceOwnerUserID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT /* mem.ai-profile-workspace */ resource_owner_user_id
		  FROM workspaces
		 WHERE id = $1
		 FOR NO KEY UPDATE
	`, workspaceID).Scan(&resourceOwnerUserID); err != nil {
		return uuid.Nil, fmt.Errorf(
			"lock workspace %s for AI profile coordination: %w",
			workspaceID,
			err,
		)
	}
	return resourceOwnerUserID, nil
}

// ForPathMutation takes the exclusive workspace mutex used before any folder
// prefix check or rewrite.
func ForPathMutation(
	ctx context.Context,
	tx pgx.Tx,
	resourceOwnerUserID uuid.UUID,
) (uuid.UUID, error) {
	return lockByOwner(ctx, tx, resourceOwnerUserID, "FOR UPDATE")
}

func lockByOwner(
	ctx context.Context,
	tx pgx.Tx,
	resourceOwnerUserID uuid.UUID,
	clause string,
) (uuid.UUID, error) {
	var workspaceID uuid.UUID
	query := `
		SELECT id
		  FROM workspaces
		 WHERE resource_owner_user_id = $1
		` + clause
	if err := tx.QueryRow(ctx, query, resourceOwnerUserID).Scan(&workspaceID); err != nil {
		return uuid.Nil, fmt.Errorf(
			"lock workspace for resource owner %s: %w",
			resourceOwnerUserID,
			err,
		)
	}
	return workspaceID, nil
}

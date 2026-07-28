// Package workspacelock serializes virtual-path rewrites with writes that
// attach durable content to those paths.
//
// Every caller must take the workspace-row lock as the first database action
// in its transaction. Content writers use FOR KEY SHARE; folder rename, move,
// and delete use FOR UPDATE. PostgreSQL then permits concurrent content writes
// while preventing a path rewrite from observing or producing a partial
// workspace state.
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

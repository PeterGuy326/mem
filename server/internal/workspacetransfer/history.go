package workspacetransfer

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// DefaultImportHistoryLimit bounds import history listings when the caller
// does not choose one; MaxImportHistoryLimit caps them. Both mirror the
// bounded-list convention used elsewhere in the API.
const (
	DefaultImportHistoryLimit = 50
	MaxImportHistoryLimit     = 100
)

// ImportHistory lists committed bundle imports for a workspace from the
// workspace_imports idempotency ledger, newest first. It is strictly
// read-only: the ledger only ever contains fully committed imports, so every
// entry is reported as succeeded. Fresh imports commit atomically with zero
// conflicts and zero skipped objects; merge_conservative imports carry their
// recorded conflict and skipped counts. Failed or aborted imports leave no
// ledger row by design.
func (s *Service) ImportHistory(
	ctx context.Context,
	workspaceID uuid.UUID,
	limit int,
) ([]ImportHistoryEntry, error) {
	if s == nil || s.pool == nil || s.store == nil {
		return nil, ErrNotConfigured
	}
	if workspaceID == uuid.Nil {
		return nil, fmt.Errorf("workspace_id is required")
	}
	if limit <= 0 {
		limit = DefaultImportHistoryLimit
	}
	if limit > MaxImportHistoryLimit {
		limit = MaxImportHistoryLimit
	}

	rows, err := s.pool.Query(ctx, `
		SELECT bundle_id,
		       archive_sha256,
		       source_workspace_id,
		       schema_version,
		       restore_mode,
		       imported_at
		  FROM workspace_imports
		 WHERE target_workspace_id = $1
		 ORDER BY imported_at DESC, bundle_id
		 LIMIT $2
	`, workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list workspace import history: %w", err)
	}
	defer rows.Close()

	entries := make([]ImportHistoryEntry, 0, limit)
	for rows.Next() {
		var entry ImportHistoryEntry
		if err := rows.Scan(
			&entry.BundleID,
			&entry.ArchiveSHA256,
			&entry.SourceWorkspaceID,
			&entry.SchemaVersion,
			&entry.RestoreMode,
			&entry.ImportedAt,
		); err != nil {
			return nil, fmt.Errorf("scan workspace import history: %w", err)
		}
		entry.ImportedAt = entry.ImportedAt.UTC()
		entry.ResultStatus = ImportStatusSucceeded
		entry.ConflictCount = 0
		entry.SkippedCount = 0
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace import history: %w", err)
	}
	return entries, nil
}

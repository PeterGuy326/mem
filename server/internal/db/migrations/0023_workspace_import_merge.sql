-- +goose Up
-- merge_conservative restores a validated bundle into an existing, possibly
-- non-empty workspace. The workspace_imports row remains the idempotency
-- boundary for the whole bundle; workspace_import_objects records the durable
-- per-object outcome so a replayed merge can report exactly which objects
-- were inserted, which were skipped, and which diverged from target state
-- without ever overwriting it.

-- +goose StatementBegin
ALTER TABLE workspace_imports
    DROP CONSTRAINT IF EXISTS workspace_imports_restore_mode_check;

ALTER TABLE workspace_imports
    ADD CONSTRAINT workspace_imports_restore_mode_check CHECK (
        restore_mode IN ('fresh', 'merge_conservative')
    );

CREATE TABLE workspace_import_objects (
    target_workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    bundle_id           uuid NOT NULL,
    object_type         text NOT NULL CHECK (
                            object_type IN (
                                'folder',
                                'file',
                                'file_annotation',
                                'memory',
                                'memory_event',
                                'task',
                                'checkpoint',
                                'checkpoint_ref'
                            )
                        ),
    object_id           text NOT NULL,
    outcome             text NOT NULL CHECK (
                            outcome IN ('inserted', 'skipped', 'conflict')
                        ),
    reason              text NOT NULL DEFAULT '',
    recorded_at         timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (target_workspace_id, bundle_id, object_type, object_id),
    CONSTRAINT fk_workspace_import_objects_import
        FOREIGN KEY (target_workspace_id, bundle_id)
        REFERENCES workspace_imports (target_workspace_id, bundle_id)
        ON DELETE CASCADE
);

CREATE INDEX idx_workspace_import_objects_summary
    ON workspace_import_objects (target_workspace_id, bundle_id, outcome);
-- +goose StatementEnd

-- +goose Down
-- Restoring the fresh-only contract is intentionally non-destructive:
-- rollback fails if committed merge_conservative imports exist instead of
-- silently deleting durable import audit state.

-- +goose StatementBegin
DROP TABLE IF EXISTS workspace_import_objects;

ALTER TABLE workspace_imports
    DROP CONSTRAINT IF EXISTS workspace_imports_restore_mode_check;

ALTER TABLE workspace_imports
    ADD CONSTRAINT workspace_imports_restore_mode_check CHECK (
        restore_mode = 'fresh'
    );
-- +goose StatementEnd

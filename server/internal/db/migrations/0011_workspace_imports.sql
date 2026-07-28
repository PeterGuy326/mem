-- +goose Up
-- A committed import is the idempotency boundary for restoring one validated
-- workspace bundle into one target workspace. Only successful imports are
-- recorded: the row is inserted in the same transaction as all portable
-- records, so a failed transaction can never be mistaken for a replay.

-- +goose StatementBegin
CREATE TABLE workspace_imports (
    target_workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    bundle_id            uuid NOT NULL,
    archive_sha256       char(64) NOT NULL CHECK (
                             archive_sha256 ~ '^[0-9a-f]{64}$'
                         ),
    source_workspace_id  uuid NOT NULL,
    schema_version       integer NOT NULL CHECK (schema_version > 0),
    restore_mode         text NOT NULL CHECK (restore_mode = 'fresh'),
    imported_at          timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (target_workspace_id, bundle_id),
    UNIQUE (target_workspace_id, archive_sha256)
);

CREATE INDEX idx_workspace_imports_target_imported
    ON workspace_imports (target_workspace_id, imported_at DESC, bundle_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS workspace_imports;
-- +goose StatementEnd

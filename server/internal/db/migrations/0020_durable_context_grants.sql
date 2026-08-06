-- +goose Up
-- Durable-context grants are an explicit, operator-owned recall allowlist.
-- Authorization is never implicit: a memory is resumable across sessions and
-- channels only while an unrevoked read grant maps the requesting principal
-- to that exact memory in this workspace. Grants cascade with the workspace
-- and with the target memory so forget/delete cannot leave dangling approvals.

-- +goose StatementBegin
CREATE TABLE durable_context_grants (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    principal           text NOT NULL CHECK (
                            principal ~ '^[a-z0-9][a-z0-9._-]{0,127}$'
                        ),
    memory_id           uuid NOT NULL,
    -- Only read grants exist today. The column keeps the contract explicit so
    -- a future write mode can never be smuggled in by a missing check.
    mode                text NOT NULL DEFAULT 'read' CHECK (mode = 'read'),
    granted_by_user_id  uuid REFERENCES users(id) ON DELETE SET NULL,
    -- As with memory_events.actor_token_id, token revocation must not erase
    -- the audit ID.
    granted_by_token_id uuid,
    granted_at          timestamptz NOT NULL DEFAULT now(),
    revoked_at          timestamptz,
    revoked_by_user_id  uuid REFERENCES users(id) ON DELETE SET NULL,
    revoked_by_token_id uuid,
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CHECK (
        (revoked_at IS NULL
            AND revoked_by_user_id IS NULL
            AND revoked_by_token_id IS NULL)
        OR
        (revoked_at IS NOT NULL
            AND (revoked_by_user_id IS NOT NULL
                OR revoked_by_token_id IS NOT NULL))
    ),
    CONSTRAINT durable_context_grants_memory_workspace_fk
        FOREIGN KEY (workspace_id, memory_id)
        REFERENCES memories(workspace_id, id)
        ON DELETE CASCADE,
    UNIQUE (workspace_id, principal, memory_id)
);

CREATE INDEX idx_durable_context_grants_recall
    ON durable_context_grants (workspace_id, principal)
    WHERE revoked_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_durable_context_grants_recall;
DROP TABLE IF EXISTS durable_context_grants;
-- +goose StatementEnd

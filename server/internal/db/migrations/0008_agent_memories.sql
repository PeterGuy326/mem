-- +goose Up
-- Structured Agent memories are immutable occurrences.  They are deliberately
-- independent from the embedding pipeline so remember -> recall remains
-- available when no model or Worker is configured.

-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS pg_trgm;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS memories (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id            uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_by_user_id      uuid REFERENCES users(id) ON DELETE SET NULL,
    -- Tokens are deleted when revoked. Keep the identifier as audit data
    -- instead of a foreign key so revocation cannot erase provenance.
    created_by_token_id     uuid,

    kind                    text NOT NULL CHECK (
                                kind IN (
                                    'observation',
                                    'decision',
                                    'preference',
                                    'task_state',
                                    'fact',
                                    'note',
                                    'artifact'
                                )
                            ),
    content                 text NOT NULL CHECK (
                                octet_length(content) BETWEEN 1 AND 65536
                            ),
    attributes              jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
                                jsonb_typeof(attributes) = 'object'
                            ),
    path                    text NOT NULL CHECK (path <> ''),
    event_at                timestamptz,

    source_type             text NOT NULL CHECK (
                                source_type ~ '^[a-z][a-z0-9_.-]{0,63}$'
                            ),
    source_ref              text NOT NULL DEFAULT '' CHECK (
                                octet_length(source_ref) <= 8192
                            ),
    source_file_id          uuid REFERENCES files(id) ON DELETE SET NULL,
    source_file_sha256      text NOT NULL DEFAULT '' CHECK (
                                source_file_sha256 = ''
                                OR source_file_sha256 ~ '^[0-9a-f]{64}$'
                            ),
    source_locator          jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
                                jsonb_typeof(source_locator) = 'object'
                            ),

    producer_agent          text NOT NULL DEFAULT '' CHECK (
                                char_length(producer_agent) <= 255
                            ),
    producer_session        text NOT NULL DEFAULT '' CHECK (
                                char_length(producer_session) <= 255
                            ),
    producer_task           text NOT NULL DEFAULT '' CHECK (
                                char_length(producer_task) <= 255
                            ),

    idempotency_key         text NOT NULL CHECK (
                                char_length(idempotency_key) BETWEEN 1 AND 200
                            ),
    request_sha256          text NOT NULL CHECK (
                                request_sha256 ~ '^[0-9a-f]{64}$'
                            ),
    content_sha256          text NOT NULL CHECK (
                                content_sha256 ~ '^[0-9a-f]{64}$'
                            ),
    lifecycle_status        text NOT NULL DEFAULT 'active' CHECK (
                                lifecycle_status IN ('active', 'archived')
                            ),

    -- "simple" preserves identifiers and avoids language-specific stemming.
    -- pg_trgm below provides the CJK and typo-tolerant lane.
    search_tsv              tsvector GENERATED ALWAYS AS (
                                to_tsvector('simple', content)
                            ) STORED,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),

    UNIQUE (workspace_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_memories_workspace_path
    ON memories (workspace_id, path text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_memories_workspace_status_time
    ON memories (
        workspace_id,
        lifecycle_status,
        (COALESCE(event_at, created_at)) DESC
    );
CREATE INDEX IF NOT EXISTS idx_memories_kind
    ON memories (workspace_id, kind);
CREATE INDEX IF NOT EXISTS idx_memories_search_tsv
    ON memories USING gin (search_tsv);
CREATE INDEX IF NOT EXISTS idx_memories_content_trgm
    ON memories USING gin (lower(content) gin_trgm_ops);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS memories;
-- pg_trgm is intentionally retained: extensions may be shared by other
-- application objects and dropping one is not a safe migration rollback.
-- +goose StatementEnd

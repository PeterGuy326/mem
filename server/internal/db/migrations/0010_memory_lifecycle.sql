-- +goose Up
-- Memory control-plane state is a mutable projection over immutable,
-- append-only events. Payload erasure keeps a minimal tombstone so retries
-- cannot resurrect a forgotten remember command.

-- +goose StatementBegin
ALTER TABLE memories
    ADD COLUMN state_version bigint NOT NULL DEFAULT 1
        CHECK (state_version > 0),
    ADD COLUMN pinned_at timestamptz,
    ADD COLUMN useful_count integer NOT NULL DEFAULT 0
        CHECK (useful_count >= 0),
    ADD COLUMN not_useful_count integer NOT NULL DEFAULT 0
        CHECK (not_useful_count >= 0),
    ADD COLUMN feedback_at timestamptz,
    ADD COLUMN forgotten_at timestamptz,
    ADD COLUMN forgotten_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    -- As with created_by_token_id, token revocation must not erase the audit ID.
    ADD COLUMN forgotten_by_token_id uuid;

ALTER TABLE memories
    DROP CONSTRAINT IF EXISTS memories_content_check,
    DROP CONSTRAINT IF EXISTS memories_lifecycle_status_check;

ALTER TABLE memories
    ADD CONSTRAINT memories_content_lifecycle_check CHECK (
        (lifecycle_status IN ('active', 'archived')
            AND octet_length(content) BETWEEN 1 AND 65536)
        OR
        (lifecycle_status = 'forgotten' AND content = '')
    ),
    ADD CONSTRAINT memories_lifecycle_status_check CHECK (
        lifecycle_status IN ('active', 'archived', 'forgotten')
    ),
    ADD CONSTRAINT memories_forgotten_timestamp_check CHECK (
        (lifecycle_status = 'forgotten') = (forgotten_at IS NOT NULL)
    ),
    ADD CONSTRAINT memories_forgotten_payload_redacted_check CHECK (
        lifecycle_status <> 'forgotten'
        OR (
            content = ''
            AND attributes = '{}'::jsonb
            AND event_at IS NULL
            AND source_type = 'forgotten'
            AND source_ref = ''
            AND source_file_id IS NULL
            AND source_file_sha256 = ''
            AND source_locator = '{}'::jsonb
            AND producer_agent = ''
            AND producer_session = ''
            AND producer_task = ''
            AND content_sha256 = repeat('0', 64)
            AND pinned_at IS NULL
            AND useful_count = 0
            AND not_useful_count = 0
            AND feedback_at IS NULL
        )
    ),
    -- Enables a composite FK that proves an event and its target share a
    -- workspace instead of relying only on application predicates.
    ADD CONSTRAINT memories_workspace_id_id_key UNIQUE (workspace_id, id);

CREATE TABLE memory_events (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        uuid NOT NULL,
    memory_id           uuid NOT NULL,
    action              text NOT NULL CHECK (
                            action IN (
                                'pin',
                                'unpin',
                                'useful',
                                'not_useful',
                                'archive',
                                'restore',
                                'forget'
                            )
                        ),
    actor_user_id       uuid REFERENCES users(id) ON DELETE SET NULL,
    actor_token_id      uuid,
    -- Never persist the caller's potentially identifying idempotency key.
    idempotency_key_sha256 char(64) NOT NULL CHECK (
                            idempotency_key_sha256 ~ '^[0-9a-f]{64}$'
                        ),
    request_sha256      text NOT NULL CHECK (
                            request_sha256 ~ '^[0-9a-f]{64}$'
                        ),
    expected_version    bigint NOT NULL CHECK (expected_version > 0),
    resulting_version   bigint NOT NULL CHECK (
                            resulting_version = expected_version + 1
                        ),
    reason              text NOT NULL DEFAULT '' CHECK (
                            (
                                action = 'forget'
                                AND reason IN (
                                    'user_request',
                                    'incorrect',
                                    'sensitive',
                                    'expired',
                                    'other'
                                )
                            )
                            OR (action <> 'forget' AND reason = '')
                        ),
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT memory_events_memory_workspace_fk
        FOREIGN KEY (workspace_id, memory_id)
        REFERENCES memories(workspace_id, id)
        ON DELETE CASCADE,
    UNIQUE (workspace_id, idempotency_key_sha256)
);

CREATE INDEX idx_memory_events_memory_created
    ON memory_events (workspace_id, memory_id, created_at DESC, id DESC);
CREATE INDEX idx_memories_workspace_lifecycle_created
    ON memories (workspace_id, lifecycle_status, created_at DESC, id DESC);
CREATE INDEX idx_memories_workspace_pinned_created
    ON memories (workspace_id, pinned_at DESC NULLS LAST, created_at DESC, id DESC)
    WHERE lifecycle_status <> 'forgotten';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS memory_events;
DROP INDEX IF EXISTS idx_memories_workspace_pinned_created;
DROP INDEX IF EXISTS idx_memories_workspace_lifecycle_created;

-- Forgotten rows contain no live payload and cannot satisfy the pre-0010
-- lifecycle/content constraints. Removing tombstones is the only truthful
-- downgrade; this is intentionally explicit rather than fabricating content.
DELETE FROM memories WHERE lifecycle_status = 'forgotten';

ALTER TABLE memories
    DROP CONSTRAINT IF EXISTS memories_workspace_id_id_key,
    DROP CONSTRAINT IF EXISTS memories_forgotten_payload_redacted_check,
    DROP CONSTRAINT IF EXISTS memories_forgotten_timestamp_check,
    DROP CONSTRAINT IF EXISTS memories_content_lifecycle_check,
    DROP CONSTRAINT IF EXISTS memories_lifecycle_status_check;

ALTER TABLE memories
    ADD CONSTRAINT memories_content_check CHECK (
        octet_length(content) BETWEEN 1 AND 65536
    ),
    ADD CONSTRAINT memories_lifecycle_status_check CHECK (
        lifecycle_status IN ('active', 'archived')
    );

ALTER TABLE memories
    DROP COLUMN IF EXISTS forgotten_by_token_id,
    DROP COLUMN IF EXISTS forgotten_by_user_id,
    DROP COLUMN IF EXISTS forgotten_at,
    DROP COLUMN IF EXISTS feedback_at,
    DROP COLUMN IF EXISTS not_useful_count,
    DROP COLUMN IF EXISTS useful_count,
    DROP COLUMN IF EXISTS pinned_at,
    DROP COLUMN IF EXISTS state_version;
-- +goose StatementEnd

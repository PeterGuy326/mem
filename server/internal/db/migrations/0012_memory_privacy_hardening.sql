-- +goose Up
-- Idempotency keys can contain task names, paths, or other caller context.
-- Persist only their SHA-256 digests. Forgotten rows are reduced to a generic
-- tombstone; exact Forget retries are authorized by a one-way principal
-- receipt rather than by retaining the erased path or actor identifiers.

-- +goose StatementBegin
ALTER TABLE memories
    ADD COLUMN idempotency_key_sha256 char(64);

UPDATE memories
   SET idempotency_key_sha256 = encode(
           digest(convert_to(idempotency_key, 'UTF8'), 'sha256'),
           'hex'
       );

ALTER TABLE memories
    ALTER COLUMN idempotency_key_sha256 SET NOT NULL,
    ADD CONSTRAINT memories_idempotency_key_sha256_check CHECK (
        idempotency_key_sha256 ~ '^[0-9a-f]{64}$'
    ),
    DROP CONSTRAINT IF EXISTS memories_workspace_id_idempotency_key_key,
    DROP COLUMN idempotency_key,
    ADD CONSTRAINT memories_workspace_id_idempotency_key_sha256_key
        UNIQUE (workspace_id, idempotency_key_sha256);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE memory_events
    ADD COLUMN replay_principal_sha256 text NOT NULL DEFAULT '';

UPDATE memory_events
   SET replay_principal_sha256 = encode(
           digest(
               convert_to(
                   'mem/forget-replay/v1|'
                   || workspace_id::text || '|'
                   || memory_id::text || '|'
                   || COALESCE(actor_user_id::text, '') || '|'
                   || idempotency_key_sha256::text,
                   'UTF8'
               ),
               'sha256'
           ),
           'hex'
       )
 WHERE action = 'forget';

-- Forget is the privacy boundary. Previous control events retain their action
-- and version audit trail but no longer retain raw actor identifiers.
UPDATE memory_events AS event
   SET actor_user_id = NULL,
       actor_token_id = NULL,
       idempotency_key_sha256 = CASE
           WHEN event.action = 'forget'
               THEN event.idempotency_key_sha256
           ELSE encode(
               digest(
                   convert_to(
                       'mem/redacted-event/v1|' || event.id::text,
                       'UTF8'
                   ),
                   'sha256'
               ),
               'hex'
           )
       END,
       request_sha256 = CASE
           WHEN event.action = 'forget'
               THEN event.request_sha256
           ELSE repeat('0', 64)
       END
  FROM memories AS memory
 WHERE memory.lifecycle_status = 'forgotten'
   AND event.workspace_id = memory.workspace_id
   AND event.memory_id = memory.id;

ALTER TABLE memory_events
    ADD CONSTRAINT memory_events_replay_principal_sha256_check CHECK (
        replay_principal_sha256 = ''
        OR replay_principal_sha256 ~ '^[0-9a-f]{64}$'
    ),
    ADD CONSTRAINT memory_events_forget_receipt_check CHECK (
        (
            action = 'forget'
            AND replay_principal_sha256 ~ '^[0-9a-f]{64}$'
            AND actor_user_id IS NULL
            AND actor_token_id IS NULL
        )
        OR (
            action <> 'forget'
            AND replay_principal_sha256 = ''
        )
    );
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE memories
    DROP CONSTRAINT IF EXISTS memories_kind_check,
    DROP CONSTRAINT IF EXISTS memories_forgotten_payload_redacted_check;

UPDATE memories
   SET created_by_user_id = NULL,
       created_by_token_id = NULL,
       kind = 'forgotten',
       content = '',
       attributes = '{}'::jsonb,
       path = '/',
       event_at = NULL,
       source_type = 'forgotten',
       source_ref = '',
       source_file_id = NULL,
       source_file_sha256 = '',
       source_locator = '{}'::jsonb,
       producer_agent = '',
       producer_session = '',
       producer_task = '',
       request_sha256 = repeat('0', 64),
       content_sha256 = repeat('0', 64),
       pinned_at = NULL,
       useful_count = 0,
       not_useful_count = 0,
       feedback_at = NULL,
       forgotten_by_user_id = NULL,
       forgotten_by_token_id = NULL,
       created_at = forgotten_at,
       updated_at = forgotten_at
 WHERE lifecycle_status = 'forgotten';

ALTER TABLE memories
    ADD CONSTRAINT memories_kind_check CHECK (
        kind IN (
            'observation',
            'decision',
            'preference',
            'task_state',
            'fact',
            'note',
            'artifact',
            'forgotten'
        )
    ),
    ADD CONSTRAINT memories_forgotten_payload_redacted_check CHECK (
        (
            lifecycle_status <> 'forgotten'
            AND kind <> 'forgotten'
        )
        OR (
            lifecycle_status = 'forgotten'
            AND created_by_user_id IS NULL
            AND created_by_token_id IS NULL
            AND kind = 'forgotten'
            AND content = ''
            AND attributes = '{}'::jsonb
            AND path = '/'
            AND event_at IS NULL
            AND source_type = 'forgotten'
            AND source_ref = ''
            AND source_file_id IS NULL
            AND source_file_sha256 = ''
            AND source_locator = '{}'::jsonb
            AND producer_agent = ''
            AND producer_session = ''
            AND producer_task = ''
            AND request_sha256 = repeat('0', 64)
            AND content_sha256 = repeat('0', 64)
            AND pinned_at IS NULL
            AND useful_count = 0
            AND not_useful_count = 0
            AND feedback_at IS NULL
            AND forgotten_by_user_id IS NULL
            AND forgotten_by_token_id IS NULL
            AND created_at = forgotten_at
            AND updated_at = forgotten_at
        )
    );
-- +goose StatementEnd

-- +goose Down
-- This downgrade cannot reconstruct erased plaintext keys or actor metadata.
-- It uses a collision-safe synthetic key derived from the retained digest.

-- +goose StatementBegin
ALTER TABLE memories
    DROP CONSTRAINT IF EXISTS memories_forgotten_payload_redacted_check,
    DROP CONSTRAINT IF EXISTS memories_kind_check;

UPDATE memories
   SET kind = 'note'
 WHERE lifecycle_status = 'forgotten';

ALTER TABLE memories
    ADD CONSTRAINT memories_kind_check CHECK (
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
    );
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE memory_events
    DROP CONSTRAINT IF EXISTS memory_events_forget_receipt_check,
    DROP CONSTRAINT IF EXISTS memory_events_replay_principal_sha256_check,
    DROP COLUMN replay_principal_sha256;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE memories
    ADD COLUMN idempotency_key text;

UPDATE memories
   SET idempotency_key = 'sha256:' || idempotency_key_sha256::text;

ALTER TABLE memories
    ALTER COLUMN idempotency_key SET NOT NULL,
    ADD CONSTRAINT memories_idempotency_key_check CHECK (
        char_length(idempotency_key) BETWEEN 1 AND 200
    ),
    DROP CONSTRAINT IF EXISTS memories_workspace_id_idempotency_key_sha256_key,
    DROP CONSTRAINT IF EXISTS memories_idempotency_key_sha256_check,
    DROP COLUMN idempotency_key_sha256,
    ADD CONSTRAINT memories_workspace_id_idempotency_key_key
        UNIQUE (workspace_id, idempotency_key);
-- +goose StatementEnd

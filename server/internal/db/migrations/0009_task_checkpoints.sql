-- +goose Up
-- Versioned task checkpoints are immutable source records. agent_tasks owns
-- the mutable head pointer used for compare-and-swap writes; references are
-- normalized separately so resume/export can enumerate dependencies without
-- interpreting arbitrary JSON.

-- +goose StatementBegin
CREATE TABLE agent_tasks (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    task_key            text NOT NULL CHECK (
                            char_length(task_key) BETWEEN 1 AND 200
                        ),
    scope_path          text NOT NULL CHECK (
                            char_length(scope_path) BETWEEN 1 AND 2048
                            AND left(scope_path, 1) = '/'
                        ),
    head_checkpoint_id  uuid,
    head_sequence       bigint NOT NULL DEFAULT 0 CHECK (head_sequence >= 0),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    UNIQUE (workspace_id, task_key),
    UNIQUE (workspace_id, id)
);

CREATE INDEX idx_agent_tasks_workspace_path
    ON agent_tasks (workspace_id, scope_path text_pattern_ops);
CREATE INDEX idx_agent_tasks_workspace_updated
    ON agent_tasks (workspace_id, updated_at DESC, id);

CREATE TABLE task_checkpoints (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id            uuid NOT NULL,
    task_id                 uuid NOT NULL,
    sequence                bigint NOT NULL CHECK (sequence > 0),
    checkpoint_kind         text NOT NULL CHECK (
                                checkpoint_kind IN ('checkpoint', 'handoff')
                            ),
    contract_name           text NOT NULL CHECK (contract_name = 'mem.handoff'),
    schema_version          integer NOT NULL CHECK (schema_version > 0),
    base_checkpoint_id      uuid,
    scope_path              text NOT NULL CHECK (
                                char_length(scope_path) BETWEEN 1 AND 2048
                                AND left(scope_path, 1) = '/'
                            ),
    payload                 jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    payload_sha256          text NOT NULL CHECK (
                                payload_sha256 ~ '^[0-9a-f]{64}$'
                            ),
    request_sha256          text NOT NULL CHECK (
                                request_sha256 ~ '^[0-9a-f]{64}$'
                            ),
    idempotency_key         text NOT NULL CHECK (
                                char_length(idempotency_key) BETWEEN 1 AND 200
                            ),
    created_by_user_id      uuid REFERENCES users(id) ON DELETE SET NULL,
    -- Token revocation deletes token rows. Retain this identifier without an
    -- FK so revocation cannot erase checkpoint provenance.
    created_by_token_id     uuid,
    producer_agent          text NOT NULL CHECK (
                                char_length(producer_agent) BETWEEN 1 AND 200
                            ),
    producer_session        text NOT NULL DEFAULT '' CHECK (
                                char_length(producer_session) <= 200
                            ),
    created_at              timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_task_checkpoints_task
        FOREIGN KEY (workspace_id, task_id)
        REFERENCES agent_tasks (workspace_id, id)
        ON DELETE CASCADE,
    CONSTRAINT ck_task_checkpoints_base_sequence
        CHECK (
            (sequence = 1 AND base_checkpoint_id IS NULL)
            OR (sequence > 1 AND base_checkpoint_id IS NOT NULL)
        ),
    UNIQUE (workspace_id, idempotency_key),
    UNIQUE (task_id, sequence),
    UNIQUE (task_id, id),
    UNIQUE (task_id, sequence, id)
);

ALTER TABLE task_checkpoints
    ADD CONSTRAINT fk_task_checkpoints_base
    FOREIGN KEY (task_id, base_checkpoint_id)
    REFERENCES task_checkpoints (task_id, id)
    ON DELETE NO ACTION
    DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE agent_tasks
    ADD CONSTRAINT fk_agent_tasks_head
    FOREIGN KEY (id, head_sequence, head_checkpoint_id)
    REFERENCES task_checkpoints (task_id, sequence, id)
    ON DELETE NO ACTION
    DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX idx_task_checkpoints_task_sequence
    ON task_checkpoints (task_id, sequence DESC);
CREATE INDEX idx_task_checkpoints_workspace_path
    ON task_checkpoints (workspace_id, scope_path text_pattern_ops);
CREATE INDEX idx_task_checkpoints_base
    ON task_checkpoints (base_checkpoint_id);

CREATE TABLE task_checkpoint_refs (
    checkpoint_id      uuid NOT NULL REFERENCES task_checkpoints(id) ON DELETE CASCADE,
    ordinal            integer NOT NULL CHECK (ordinal >= 0),
    relation           text NOT NULL CHECK (
                            relation IN ('decision', 'next_step', 'blocker', 'artifact')
                        ),
    uri                text NOT NULL CHECK (
                            char_length(uri) BETWEEN 1 AND 2048
                        ),
    expected_sha256    text NOT NULL DEFAULT '' CHECK (
                            expected_sha256 = ''
                            OR expected_sha256 ~ '^[0-9a-f]{64}$'
                        ),
    required           boolean NOT NULL DEFAULT false,
    metadata           jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
                            jsonb_typeof(metadata) = 'object'
                        ),

    PRIMARY KEY (checkpoint_id, ordinal)
);

CREATE INDEX idx_task_checkpoint_refs_uri
    ON task_checkpoint_refs (uri);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE task_checkpoint_refs;
ALTER TABLE agent_tasks DROP CONSTRAINT fk_agent_tasks_head;
DROP TABLE task_checkpoints;
DROP TABLE agent_tasks;
-- +goose StatementEnd

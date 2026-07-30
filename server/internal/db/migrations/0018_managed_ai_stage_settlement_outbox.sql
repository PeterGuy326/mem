-- +goose Up
-- Commit file/index output and the exact managed-stage settlement intent in
-- one PostgreSQL transaction. The dispatcher settles these rows idempotently
-- after commit and the periodic reconciler resumes any row left pending by a
-- process crash. Only safe identifiers and a closed outcome vocabulary are
-- stored; source text, vectors, credentials, endpoints, and provider bodies
-- never enter this table.

-- +goose StatementBegin
CREATE TABLE managed_ai_stage_settlement_outbox (
    usage_id                       uuid PRIMARY KEY
        REFERENCES managed_embedding_usage(id) ON DELETE CASCADE,
    -- Preserve a pending accounting intent even when its source file is
    -- deleted. Reconciliation needs only usage_id/outcome; replay matching
    -- needs file_id only while the file itself still exists.
    file_id                        uuid
        REFERENCES files(id) ON DELETE SET NULL,
    -- The content fingerprint is needed only while the source file exists to
    -- match duplicate queue deliveries. File deletion scrubs both identifiers
    -- atomically; detached accounting needs only usage_id/outcome.
    content_sha256                 char(64)
        CHECK (
            content_sha256 IS NULL OR
            content_sha256 ~ '^[0-9a-f]{64}$'
        ),
    profile_id                     text NOT NULL
        CHECK (profile_id ~ '^[a-z][a-z0-9-]{0,63}$'),
    profile_revision               text NOT NULL
        CHECK (octet_length(profile_revision) BETWEEN 1 AND 64),
    pipeline_revision              text NOT NULL
        CHECK (octet_length(pipeline_revision) BETWEEN 1 AND 64),
    stage                          text NOT NULL
        CHECK (stage IN (
            'embedding', 'visual_embedding', 'llm', 'vlm', 'asr', 'rerank'
        )),
    outcome                        text NOT NULL
        CHECK (outcome IN ('succeeded', 'not_invoked', 'indeterminate')),
    -- A committed successful/skipped file result is a durable proof that a
    -- duplicate queue delivery must not call the Worker again. Failed
    -- attempts still need durable accounting, but may retry after settlement.
    replayable                     boolean NOT NULL DEFAULT true,
    created_at                     timestamptz NOT NULL DEFAULT now(),
    settled_at                     timestamptz,

    CHECK (
        (file_id IS NULL AND content_sha256 IS NULL) OR
        (file_id IS NOT NULL AND content_sha256 IS NOT NULL)
    ),

    UNIQUE (
        file_id,
        content_sha256,
        profile_id,
        profile_revision,
        pipeline_revision,
        stage
    )
);

CREATE INDEX idx_managed_ai_stage_settlement_pending
    ON managed_ai_stage_settlement_outbox (created_at, usage_id)
    WHERE settled_at IS NULL;

CREATE INDEX idx_managed_ai_stage_settlement_file_identity
    ON managed_ai_stage_settlement_outbox (
        file_id,
        content_sha256,
        profile_id,
        profile_revision,
        pipeline_revision
    );

CREATE FUNCTION scrub_managed_ai_outbox_file_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    UPDATE managed_ai_stage_settlement_outbox
       SET file_id = NULL,
           content_sha256 = NULL
     WHERE file_id = OLD.id;
    RETURN OLD;
END;
$$;

CREATE TRIGGER trg_scrub_managed_ai_outbox_file_identity
BEFORE DELETE ON files
FOR EACH ROW
EXECUTE FUNCTION scrub_managed_ai_outbox_file_identity();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER trg_scrub_managed_ai_outbox_file_identity ON files;
DROP FUNCTION scrub_managed_ai_outbox_file_identity();
DROP TABLE managed_ai_stage_settlement_outbox;
-- +goose StatementEnd

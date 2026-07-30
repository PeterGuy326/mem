-- +goose Up
-- A workspace AI profile is a server-selected, immutable-at-selection snapshot
-- of harmless pipeline identifiers.  It deliberately stores neither provider
-- credentials/base URLs nor raw provider requests/responses.  Runtime model
-- credentials remain process-local and workspace bundle exports stay
-- target-local.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS workspace_ai_profiles (
    workspace_id                    uuid PRIMARY KEY
        REFERENCES workspaces(id) ON DELETE CASCADE,
    profile_id                      text NOT NULL
        CHECK (profile_id ~ '^[a-z][a-z0-9-]{0,63}$'),
    profile_revision                text NOT NULL
        CHECK (octet_length(profile_revision) BETWEEN 1 AND 64),
    pipeline_revision               text NOT NULL
        CHECK (octet_length(pipeline_revision) BETWEEN 1 AND 64),
    embedding_provider              text NOT NULL
        CHECK (octet_length(embedding_provider) BETWEEN 1 AND 255),
    embedding_dimensions            integer NOT NULL
        CHECK (embedding_dimensions > 0),
    visual_embedding_provider       text,
    visual_embedding_dimensions     integer,
    llm_provider                    text,
    vlm_provider                    text,
    asr_provider                    text,
    rerank_provider                 text,
    data_egress                     text NOT NULL
        CHECK (data_egress IN ('local_only', 'managed_idealab')),
    allowed_mime_types               text[] NOT NULL
        CHECK (cardinality(allowed_mime_types) > 0),
    selected_by_user_id             uuid
        REFERENCES users(id) ON DELETE SET NULL,
    selected_at                     timestamptz NOT NULL DEFAULT now(),
    updated_at                      timestamptz NOT NULL DEFAULT now(),

    CHECK (
        (visual_embedding_provider IS NULL AND visual_embedding_dimensions IS NULL)
        OR
        (visual_embedding_provider IS NOT NULL AND visual_embedding_dimensions > 0)
    ),
    CHECK (visual_embedding_provider IS NULL OR octet_length(visual_embedding_provider) <= 255),
    CHECK (llm_provider IS NULL OR octet_length(llm_provider) <= 255),
    CHECK (vlm_provider IS NULL OR octet_length(vlm_provider) <= 255),
    CHECK (asr_provider IS NULL OR octet_length(asr_provider) <= 255),
    CHECK (rerank_provider IS NULL OR octet_length(rerank_provider) <= 255)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS workspace_ai_profiles;
-- +goose StatementEnd

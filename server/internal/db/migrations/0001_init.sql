-- +goose Up
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "vector";
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS users (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email           text NOT NULL UNIQUE,
    password_hash   text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS tokens (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            text NOT NULL,
    hash            text NOT NULL UNIQUE,
    scopes          text[] NOT NULL DEFAULT '{}',
    quota           jsonb NOT NULL DEFAULT '{}'::jsonb,
    paths           text[] NOT NULL DEFAULT '{}',
    expires_at      timestamptz,
    redact_pii      boolean NOT NULL DEFAULT false,
    last_used_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_tokens_hash    ON tokens (hash);
CREATE INDEX IF NOT EXISTS idx_tokens_user_id ON tokens (user_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS files (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            text NOT NULL,
    path            text NOT NULL DEFAULT '/',
    size            bigint NOT NULL,
    sha256          text NOT NULL,
    mime            text NOT NULL DEFAULT 'application/octet-stream',
    storage_key     text NOT NULL,
    -- AI-Native fields (populated by worker pipeline in W2+)
    summary         text,
    caption         text,
    tags            text[] NOT NULL DEFAULT '{}',
    timeline_at     timestamptz,
    geo             point,
    -- State
    index_status    text NOT NULL DEFAULT 'pending',
    -- Timestamps
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_files_user_timeline ON files (user_id, timeline_at);
CREATE INDEX IF NOT EXISTS idx_files_sha256        ON files (sha256);
CREATE INDEX IF NOT EXISTS idx_files_user_created  ON files (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_files_index_status  ON files (index_status);
CREATE INDEX IF NOT EXISTS idx_files_tags          ON files USING gin (tags);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_files_user_sha ON files (user_id, sha256);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS entities (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type            text NOT NULL,           -- person|place|org|event
    name            text,
    metadata        jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_entities_user_type ON entities (user_id, type);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS file_entities (
    file_id         uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    entity_id       uuid NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    confidence      real NOT NULL DEFAULT 1.0,
    PRIMARY KEY (file_id, entity_id)
);
CREATE INDEX IF NOT EXISTS idx_file_entities_entity ON file_entities (entity_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS file_relations (
    src_id          uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    dst_id          uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    type            text NOT NULL,           -- same_event|same_person|same_topic|sequel
    score           real NOT NULL DEFAULT 0.0,
    computed_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (src_id, dst_id, type)
);
CREATE INDEX IF NOT EXISTS idx_file_relations_src ON file_relations (src_id);
CREATE INDEX IF NOT EXISTS idx_file_relations_dst ON file_relations (dst_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- Text chunk embeddings (long documents are chunked)
CREATE TABLE IF NOT EXISTS embeddings_text (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    file_id         uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    chunk_index     int  NOT NULL,
    chunk_text      text NOT NULL,
    embedding       vector(768)
);
CREATE INDEX IF NOT EXISTS idx_embeddings_text_file ON embeddings_text (file_id);
-- HNSW index will be added by worker once we settle on a model dimension. Kept off here
-- because pgvector requires the table to have data of consistent dim before building.
-- +goose StatementEnd

-- +goose StatementBegin
-- Visual embeddings (one per image file)
CREATE TABLE IF NOT EXISTS embeddings_visual (
    file_id         uuid PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
    embedding       vector(512)
);
-- +goose StatementEnd

-- +goose StatementBegin
-- Face embeddings (multiple faces per image)
CREATE TABLE IF NOT EXISTS embeddings_face (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    file_id         uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    entity_id       uuid REFERENCES entities(id) ON DELETE SET NULL,
    bbox            jsonb NOT NULL DEFAULT '{}'::jsonb,
    embedding       vector(512)
);
CREATE INDEX IF NOT EXISTS idx_embeddings_face_file   ON embeddings_face (file_id);
CREATE INDEX IF NOT EXISTS idx_embeddings_face_entity ON embeddings_face (entity_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS embeddings_face;
DROP TABLE IF EXISTS embeddings_visual;
DROP TABLE IF EXISTS embeddings_text;
DROP TABLE IF EXISTS file_relations;
DROP TABLE IF EXISTS file_entities;
DROP TABLE IF EXISTS entities;
DROP TABLE IF EXISTS files;
DROP TABLE IF EXISTS tokens;
DROP TABLE IF EXISTS users;
-- +goose StatementEnd

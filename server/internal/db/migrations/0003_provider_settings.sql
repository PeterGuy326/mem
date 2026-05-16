-- +goose Up
-- +goose StatementBegin
-- Provider settings: per-user choice of Embedding / LLM / VLM / ASR / OCR
-- vendor + model. SPEC §F8 (Provider 可插拔).
--
-- The `dim` column is populated by `mem provider test` after a successful
-- probe — it is what makes the embeddings_text dim-migration loop deterministic.
-- For non-embedding kinds (llm, vlm) dim is NULL.
CREATE TABLE IF NOT EXISTS provider_settings (
    user_id     uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind        text        NOT NULL CHECK (kind IN ('embedding', 'llm', 'vlm', 'asr', 'ocr')),
    spec        text        NOT NULL,                       -- "<vendor>:<model>", e.g. "ollama:nomic-embed-text"
    dim         int         NULL,                            -- embedding output dimension (probed)
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, kind)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS provider_settings;
-- +goose StatementEnd

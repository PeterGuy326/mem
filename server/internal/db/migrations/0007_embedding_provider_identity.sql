-- +goose Up
-- +goose StatementBegin
ALTER TABLE embeddings_text
    ADD COLUMN IF NOT EXISTS provider text;

ALTER TABLE embeddings_text
    ALTER COLUMN provider SET DEFAULT 'legacy:unknown';

-- Historical rows did not record which model actually produced them. Do not
-- guess from today's provider_settings: PDF/audio overrides were not reliable
-- in older releases, so that would create false provenance. Fail closed until
-- the owner explicitly chooses a provider and rebuilds the corpus.
UPDATE embeddings_text
   SET provider = 'legacy:unknown'
 WHERE provider IS NULL OR provider = '';

ALTER TABLE embeddings_text
    ALTER COLUMN provider SET NOT NULL;

CREATE INDEX IF NOT EXISTS idx_embeddings_text_provider
    ON embeddings_text (provider);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_embeddings_text_provider;
ALTER TABLE embeddings_text DROP COLUMN IF EXISTS provider;
-- +goose StatementEnd

-- +goose Up
-- +goose StatementBegin
ALTER TABLE provider_settings DROP CONSTRAINT IF EXISTS provider_settings_kind_check;
ALTER TABLE provider_settings
    ADD CONSTRAINT provider_settings_kind_check
    CHECK (kind IN ('embedding', 'visual_embedding', 'llm', 'vlm', 'asr', 'ocr'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM provider_settings WHERE kind = 'visual_embedding';
ALTER TABLE provider_settings DROP CONSTRAINT IF EXISTS provider_settings_kind_check;
ALTER TABLE provider_settings
    ADD CONSTRAINT provider_settings_kind_check
    CHECK (kind IN ('embedding', 'llm', 'vlm', 'asr', 'ocr'));
-- +goose StatementEnd

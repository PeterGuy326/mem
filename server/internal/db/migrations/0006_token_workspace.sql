-- +goose Up
-- +goose StatementBegin
ALTER TABLE tokens
    ADD COLUMN IF NOT EXISTS workspace_id uuid REFERENCES workspaces(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_tokens_workspace_id ON tokens (workspace_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_tokens_workspace_id;
ALTER TABLE tokens DROP COLUMN IF EXISTS workspace_id;
-- +goose StatementEnd

-- +goose Up
-- Folders as first-class citizens (SPEC §6.1 / §6.3 / §6bis, decision B locked at v0.3).
--
-- The root directory "/" is NOT stored in this table — it is implicit per user.
-- All folder rows therefore have a non-root `path` such as "/Photos/2012".
-- `parent_id` is NULL when the folder sits directly under root.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS folders (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    parent_id   uuid REFERENCES folders(id) ON DELETE CASCADE,
    path        text NOT NULL,
    name        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, path)
);
CREATE INDEX IF NOT EXISTS idx_folders_user_parent ON folders(user_id, parent_id);
CREATE INDEX IF NOT EXISTS idx_folders_user_path_pattern ON folders(user_id, path text_pattern_ops);
-- +goose StatementEnd

-- +goose StatementBegin
-- Wire files into the folder tree. NULL `folder_id` means "lives in root".
ALTER TABLE files
    ADD COLUMN IF NOT EXISTS folder_id uuid REFERENCES folders(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_files_user_folder        ON files(user_id, folder_id);
CREATE INDEX IF NOT EXISTS idx_files_user_path_pattern  ON files(user_id, path text_pattern_ops);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_files_user_path_pattern;
DROP INDEX IF EXISTS idx_files_user_folder;
ALTER TABLE files DROP COLUMN IF EXISTS folder_id;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_folders_user_path_pattern;
DROP INDEX IF EXISTS idx_folders_user_parent;
DROP TABLE IF EXISTS folders;
-- +goose StatementEnd

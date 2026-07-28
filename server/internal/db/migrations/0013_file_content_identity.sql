-- +goose Up
-- A file row is a user-visible directory entry, while sha256 identifies its
-- bytes. Multiple names and paths may legitimately reference equal content.
-- The object-storage key remains per-file so deleting one entry cannot remove
-- another entry's bytes.

-- +goose StatementBegin
DROP INDEX IF EXISTS uniq_files_user_sha;
-- +goose StatementEnd

-- +goose Down
-- Recreating the old uniqueness invariant is intentionally non-destructive:
-- rollback fails if post-migration data contains legitimate duplicate content
-- instead of silently deleting user files.

-- +goose StatementBegin
CREATE UNIQUE INDEX uniq_files_user_sha ON files (user_id, sha256);
-- +goose StatementEnd

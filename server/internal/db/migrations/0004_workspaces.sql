-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS workspaces (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                    text NOT NULL,
    resource_owner_user_id  uuid NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    created_at              timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workspace_memberships (
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role          text NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_workspace_memberships_user ON workspace_memberships(user_id);

INSERT INTO workspaces (name, resource_owner_user_id, created_at)
SELECT split_part(u.email, '@', 1) || '''s workspace', u.id, u.created_at
FROM users u
ON CONFLICT (resource_owner_user_id) DO NOTHING;

INSERT INTO workspace_memberships (workspace_id, user_id, role, created_at)
SELECT w.id, w.resource_owner_user_id, 'owner', w.created_at
FROM workspaces w
ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = 'owner';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS workspace_memberships;
DROP TABLE IF EXISTS workspaces;
-- +goose StatementEnd

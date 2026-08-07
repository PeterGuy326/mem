-- +goose Up
-- Server-managed sessions for browser (human) authentication.
-- Tokens remain for agent/API/CLI access; sessions add secure cookie-based
-- browser auth with idle/absolute expiry, rotation, and revocation.

CREATE TABLE sessions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash    TEXT NOT NULL UNIQUE, -- SHA-256 of session cookie value
    csrf_token    TEXT NOT NULL,        -- per-session CSRF secret
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL, -- absolute expiry
    rotated_from  UUID REFERENCES sessions(id), -- previous session after rotation
    revoked_at    TIMESTAMPTZ          -- NULL = active; set on logout/revocation
);

CREATE INDEX idx_sessions_user_id ON sessions(user_id);
CREATE INDEX idx_sessions_token_hash ON sessions(token_hash) WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS sessions;

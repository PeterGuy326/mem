-- +goose Up
-- Payment-provider-neutral entitlement state for platform-managed embeddings.
-- Workspace membership remains an authorization concern and is intentionally
-- not reused as commercial plan state.

-- +goose StatementBegin
CREATE TABLE workspace_entitlements (
    workspace_id                       uuid PRIMARY KEY
        REFERENCES workspaces(id) ON DELETE CASCADE,
    plan_key                           text NOT NULL DEFAULT 'free'
        CHECK (plan_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    status                             text NOT NULL DEFAULT 'inactive'
        CHECK (status IN ('active', 'inactive', 'past_due', 'canceled')),
    period_start                       timestamptz NOT NULL,
    period_end                         timestamptz NOT NULL,
    managed_embedding_unit_limit       bigint NOT NULL DEFAULT 0
        CHECK (managed_embedding_unit_limit >= 0),
    managed_embedding_units_reserved  bigint NOT NULL DEFAULT 0
        CHECK (managed_embedding_units_reserved >= 0),
    managed_embedding_units_consumed  bigint NOT NULL DEFAULT 0
        CHECK (managed_embedding_units_consumed >= 0),
    updated_at                         timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT workspace_entitlements_period_check
        CHECK (period_end > period_start),
    CONSTRAINT workspace_entitlements_quota_check
        CHECK (
            managed_embedding_units_reserved
            + managed_embedding_units_consumed
            <= managed_embedding_unit_limit
        )
);

INSERT INTO workspace_entitlements (
    workspace_id,
    period_start,
    period_end
)
SELECT
    id,
    date_trunc('month', now()),
    date_trunc('month', now()) + interval '1 month'
FROM workspaces
ON CONFLICT (workspace_id) DO NOTHING;

CREATE FUNCTION create_default_workspace_entitlement()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO workspace_entitlements (
        workspace_id,
        period_start,
        period_end
    ) VALUES (
        NEW.id,
        date_trunc('month', now()),
        date_trunc('month', now()) + interval '1 month'
    )
    ON CONFLICT (workspace_id) DO NOTHING;
    RETURN NEW;
END
$$;

CREATE TRIGGER workspaces_create_default_entitlement
AFTER INSERT ON workspaces
FOR EACH ROW
EXECUTE FUNCTION create_default_workspace_entitlement();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE managed_embedding_usage (
    id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id                uuid NOT NULL
        REFERENCES workspaces(id) ON DELETE CASCADE,
    operation                   text NOT NULL
        CHECK (operation ~ '^[a-z][a-z0-9_.-]{0,63}$'),
    provider                    text NOT NULL
        CHECK (octet_length(provider) BETWEEN 1 AND 128),
    model                       text NOT NULL
        CHECK (octet_length(model) BETWEEN 1 AND 256),
    units                       bigint NOT NULL CHECK (units > 0),
    status                      text NOT NULL
        CHECK (status IN ('reserved', 'succeeded', 'released', 'indeterminate')),
    request_fingerprint_sha256  char(64) NOT NULL
        CHECK (request_fingerprint_sha256 ~ '^[0-9a-f]{64}$'),
    idempotency_key_sha256      char(64) NOT NULL
        CHECK (idempotency_key_sha256 ~ '^[0-9a-f]{64}$'),
    period_start                timestamptz NOT NULL,
    period_end                  timestamptz NOT NULL,
    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT managed_embedding_usage_period_check
        CHECK (period_end > period_start),
    UNIQUE (workspace_id, operation, idempotency_key_sha256)
);

CREATE INDEX idx_managed_embedding_usage_workspace_created
    ON managed_embedding_usage (workspace_id, created_at DESC, id DESC);
CREATE INDEX idx_managed_embedding_usage_reserved_updated
    ON managed_embedding_usage (updated_at)
    WHERE status = 'reserved';

-- Successful replay stores only normalized derived identifiers and scores.
-- Strict columns make it impossible for a future JSON marshal to add request
-- text, file content, vectors, credentials, or provider bodies.
CREATE TABLE managed_embedding_replay_results (
    usage_id      uuid NOT NULL
        REFERENCES managed_embedding_usage(id) ON DELETE CASCADE,
    rank          integer NOT NULL CHECK (rank BETWEEN 0 AND 99),
    source        text NOT NULL CHECK (source IN ('text', 'visual')),
    evidence_id   uuid NOT NULL,
    file_id       uuid NOT NULL,
    score         real NOT NULL CHECK (
                      score NOT IN (
                          'NaN'::real,
                          'Infinity'::real,
                          '-Infinity'::real
                      )
                      AND score BETWEEN -1.0 AND 1.0
                  ),
    created_at    timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (usage_id, rank),
    UNIQUE (usage_id, source, evidence_id)
);

-- Append-only-by-service audit trail. The projection above enables efficient
-- reservation decisions; every state transition is also retained here.
CREATE TABLE managed_embedding_usage_events (
    id                          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    usage_id                    uuid NOT NULL
        REFERENCES managed_embedding_usage(id) ON DELETE CASCADE,
    workspace_id                uuid NOT NULL
        REFERENCES workspaces(id) ON DELETE CASCADE,
    operation                   text NOT NULL,
    provider                    text NOT NULL,
    model                       text NOT NULL,
    units                       bigint NOT NULL CHECK (units > 0),
    status                      text NOT NULL
        CHECK (status IN ('reserved', 'succeeded', 'released', 'indeterminate')),
    request_fingerprint_sha256  char(64) NOT NULL
        CHECK (request_fingerprint_sha256 ~ '^[0-9a-f]{64}$'),
    idempotency_key_sha256      char(64) NOT NULL
        CHECK (idempotency_key_sha256 ~ '^[0-9a-f]{64}$'),
    created_at                  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_managed_embedding_usage_events_workspace_created
    ON managed_embedding_usage_events (workspace_id, created_at DESC, id DESC);
CREATE INDEX idx_managed_embedding_usage_events_usage
    ON managed_embedding_usage_events (usage_id, id);
-- +goose StatementEnd

-- +goose Down
-- Rollback is schema-only and does not reconstruct consumed quota. Operators
-- must stop saas traffic before downgrade; private deployments are unaffected.

-- +goose StatementBegin
DROP TABLE IF EXISTS managed_embedding_usage_events;
DROP TABLE IF EXISTS managed_embedding_replay_results;
DROP TABLE IF EXISTS managed_embedding_usage;
DROP TRIGGER IF EXISTS workspaces_create_default_entitlement ON workspaces;
DROP FUNCTION IF EXISTS create_default_workspace_entitlement();
DROP TABLE IF EXISTS workspace_entitlements;
-- +goose StatementEnd

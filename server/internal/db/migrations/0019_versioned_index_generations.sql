-- +goose Up
-- Versioned index generations are an additive control-plane and storage
-- substrate. This migration deliberately leaves the released
-- embeddings_text vector(768) and embeddings_visual vector(512) tables
-- untouched: query routing and Worker rebuild execution move to these tables
-- in separately reviewable changes.

-- +goose StatementBegin
CREATE TABLE index_generation_builds (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id            uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    profile_id              text NOT NULL CHECK (profile_id ~ '^[a-z][a-z0-9-]{0,63}$'),
    profile_revision        text NOT NULL CHECK (octet_length(profile_revision) BETWEEN 1 AND 64),
    pipeline_revision       text NOT NULL CHECK (octet_length(pipeline_revision) BETWEEN 1 AND 64),
    allowed_mime_types      text[] NOT NULL CHECK (cardinality(allowed_mime_types) > 0),
    profile_snapshot        jsonb NOT NULL CHECK (
                                jsonb_typeof(profile_snapshot) = 'object' AND
                                octet_length(profile_snapshot::text) <= 8192
                            ),
    corpus_captured_at      timestamptz NOT NULL,
    corpus_file_count       integer NOT NULL CHECK (corpus_file_count >= 0),
    state                   text NOT NULL DEFAULT 'building'
                            CHECK (state IN (
                                'building', 'cancelled', 'failed', 'ready',
                                'active', 'inactive', 'discarded'
                            )),
    quality_gate            jsonb NOT NULL DEFAULT '{"mode":"all_targets"}'::jsonb
                            CHECK (
                                jsonb_typeof(quality_gate) = 'object' AND
                                octet_length(quality_gate::text) <= 1024
                            ),
    required_targets        integer NOT NULL DEFAULT 0 CHECK (required_targets >= 0),
    succeeded_targets       integer NOT NULL DEFAULT 0 CHECK (succeeded_targets >= 0),
    skipped_targets         integer NOT NULL DEFAULT 0 CHECK (skipped_targets >= 0),
    failed_targets          integer NOT NULL DEFAULT 0 CHECK (failed_targets >= 0),
    created_by_user_id      uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    ready_at                timestamptz,
    activated_at            timestamptz,
    cancelled_at            timestamptz,
    failed_at               timestamptz,
    failure_code            text CHECK (
                                failure_code IS NULL OR
                                (octet_length(failure_code) BETWEEN 1 AND 64 AND
                                 failure_code ~ '^[a-z0-9_]+$')
                            ),
    retention_until         timestamptz,

    CHECK (succeeded_targets + skipped_targets + failed_targets <= required_targets),
    CHECK (state <> 'ready' OR (failed_targets = 0 AND succeeded_targets + skipped_targets = required_targets)),
    CHECK (state <> 'active' OR activated_at IS NOT NULL),
    CHECK (state <> 'discarded' OR retention_until IS NOT NULL),
    UNIQUE (id, workspace_id)
);

CREATE INDEX idx_index_generation_builds_workspace_created
    ON index_generation_builds (workspace_id, created_at DESC, id DESC);

CREATE UNIQUE INDEX uniq_index_generation_active_build
    ON index_generation_builds (workspace_id)
    WHERE state = 'active';

CREATE UNIQUE INDEX uniq_index_generation_build_inflight_identity
    ON index_generation_builds (
        workspace_id, profile_id, profile_revision, pipeline_revision
    )
    WHERE state IN ('building', 'cancelled', 'failed', 'ready');

CREATE OR REPLACE FUNCTION mem_keep_index_generation_build_identity_immutable()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.workspace_id IS DISTINCT FROM OLD.workspace_id OR
       NEW.profile_id IS DISTINCT FROM OLD.profile_id OR
       NEW.profile_revision IS DISTINCT FROM OLD.profile_revision OR
       NEW.pipeline_revision IS DISTINCT FROM OLD.pipeline_revision OR
       NEW.allowed_mime_types IS DISTINCT FROM OLD.allowed_mime_types OR
       NEW.profile_snapshot IS DISTINCT FROM OLD.profile_snapshot OR
       NEW.corpus_captured_at IS DISTINCT FROM OLD.corpus_captured_at OR
       NEW.corpus_file_count IS DISTINCT FROM OLD.corpus_file_count OR
       NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'index generation build identity is immutable'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER keep_index_generation_build_identity_immutable
BEFORE UPDATE ON index_generation_builds
FOR EACH ROW EXECUTE FUNCTION mem_keep_index_generation_build_identity_immutable();
-- +goose StatementEnd

-- +goose StatementBegin
-- Account deletion cascades through files and workspaces. PostgreSQL does not
-- guarantee the order of those referential actions, so scrub optional actor
-- references first: a file-delete tombstone may otherwise update a build while
-- its creator FK still points at the user currently being deleted.
CREATE OR REPLACE FUNCTION mem_scrub_index_generation_actors_on_user_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    UPDATE index_generation_builds
       SET created_by_user_id = NULL
     WHERE created_by_user_id = OLD.id;
    RETURN OLD;
END;
$$;

CREATE TRIGGER scrub_index_generation_actors_on_user_delete
BEFORE DELETE ON users
FOR EACH ROW EXECUTE FUNCTION mem_scrub_index_generation_actors_on_user_delete();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE index_generations (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    build_id                uuid NOT NULL,
    workspace_id            uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    route_kind              text NOT NULL CHECK (route_kind IN ('text', 'visual')),
    provider                text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9-]{0,63}$'),
    model_revision          text NOT NULL CHECK (
                                octet_length(model_revision) BETWEEN 1 AND 191 AND
                                model_revision !~ '[[:space:]]'
                            ),
    output_dimension        integer NOT NULL CHECK (output_dimension > 0),
    pipeline_revision       text NOT NULL CHECK (octet_length(pipeline_revision) BETWEEN 1 AND 64),
    profile_id              text NOT NULL CHECK (profile_id ~ '^[a-z][a-z0-9-]{0,63}$'),
    profile_revision        text NOT NULL CHECK (octet_length(profile_revision) BETWEEN 1 AND 64),
    state                   text NOT NULL DEFAULT 'building'
                            CHECK (state IN (
                                'building', 'cancelled', 'failed', 'ready',
                                'active', 'inactive', 'discarded'
                            )),
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),

    UNIQUE (build_id, route_kind),
    UNIQUE (id, workspace_id),
    FOREIGN KEY (build_id, workspace_id)
        REFERENCES index_generation_builds(id, workspace_id) ON DELETE CASCADE
);

CREATE INDEX idx_index_generations_workspace_route_state
    ON index_generations (workspace_id, route_kind, state);

CREATE UNIQUE INDEX uniq_index_generations_active_route
    ON index_generations (workspace_id, route_kind)
    WHERE state = 'active';

CREATE OR REPLACE FUNCTION mem_keep_index_generation_identity_immutable()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.build_id IS DISTINCT FROM OLD.build_id OR
       NEW.workspace_id IS DISTINCT FROM OLD.workspace_id OR
       NEW.route_kind IS DISTINCT FROM OLD.route_kind OR
       NEW.provider IS DISTINCT FROM OLD.provider OR
       NEW.model_revision IS DISTINCT FROM OLD.model_revision OR
       NEW.output_dimension IS DISTINCT FROM OLD.output_dimension OR
       NEW.pipeline_revision IS DISTINCT FROM OLD.pipeline_revision OR
       NEW.profile_id IS DISTINCT FROM OLD.profile_id OR
       NEW.profile_revision IS DISTINCT FROM OLD.profile_revision OR
       NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'index generation identity is immutable'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER keep_index_generation_identity_immutable
BEFORE UPDATE ON index_generations
FOR EACH ROW EXECUTE FUNCTION mem_keep_index_generation_identity_immutable();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE index_generation_targets (
    generation_id           uuid NOT NULL REFERENCES index_generations(id) ON DELETE CASCADE,
    workspace_id            uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    file_id                 uuid NOT NULL,
    content_sha256          text CHECK (content_sha256 IS NULL OR content_sha256 ~ '^[0-9a-f]{64}$'),
    source_present          boolean NOT NULL DEFAULT true,
    stage                   text NOT NULL CHECK (stage IN ('text_embedding', 'visual_embedding')),
    state                   text NOT NULL DEFAULT 'pending'
                            CHECK (state IN ('pending', 'processing', 'succeeded', 'skipped', 'failed')),
    attempts                integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    attempt_token           uuid,
    lease_expires_at        timestamptz,
    error_code              text CHECK (
                                error_code IS NULL OR
                                (octet_length(error_code) BETWEEN 1 AND 64 AND
                                 error_code ~ '^[a-z0-9_]+$')
                            ),
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    started_at              timestamptz,
    completed_at            timestamptz,

    CHECK (
        (source_present AND content_sha256 IS NOT NULL)
        OR (NOT source_present AND content_sha256 IS NULL)
    ),
    CHECK (
        (state = 'processing' AND attempt_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (state <> 'processing' AND attempt_token IS NULL AND lease_expires_at IS NULL)
    ),
    PRIMARY KEY (generation_id, file_id, stage),
    FOREIGN KEY (generation_id, workspace_id)
        REFERENCES index_generations(id, workspace_id) ON DELETE CASCADE
);

CREATE OR REPLACE FUNCTION mem_validate_index_generation_target()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    generation_route text;
    owner_user_id uuid;
BEGIN
    SELECT g.route_kind, w.resource_owner_user_id
      INTO generation_route, owner_user_id
      FROM index_generations g
      JOIN workspaces w ON w.id = g.workspace_id
     WHERE g.id = NEW.generation_id
       AND g.workspace_id = NEW.workspace_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'index generation target workspace mismatch'
            USING ERRCODE = '23514';
    END IF;
    IF (generation_route = 'text' AND NEW.stage <> 'text_embedding') OR
       (generation_route = 'visual' AND NEW.stage <> 'visual_embedding') THEN
        RAISE EXCEPTION 'index generation target route/stage mismatch'
            USING ERRCODE = '23514';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM files f
         WHERE f.id = NEW.file_id
           AND f.user_id = owner_user_id
    ) THEN
        RAISE EXCEPTION 'index generation target file workspace mismatch'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER validate_index_generation_target
BEFORE INSERT OR UPDATE OF generation_id, workspace_id, file_id, stage
ON index_generation_targets
FOR EACH ROW EXECUTE FUNCTION mem_validate_index_generation_target();

CREATE INDEX idx_index_generation_targets_claim
    ON index_generation_targets (generation_id, state, lease_expires_at, updated_at, file_id);
CREATE INDEX idx_index_generation_targets_file_hash
    ON index_generation_targets (file_id, content_sha256, generation_id, stage);

-- +goose StatementEnd

-- +goose StatementBegin
-- `vector` intentionally has no table-wide dimension. Every row is validated
-- against its immutable generation.output_dimension by the canonical service.
-- Future ANN indexes must be route/dimension-specific expression or partition
-- indexes; silently padding or truncating vectors is never allowed.
CREATE TABLE index_generation_vectors (
    generation_id           uuid NOT NULL REFERENCES index_generations(id) ON DELETE CASCADE,
    workspace_id            uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    file_id                 uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    ordinal                 integer NOT NULL CHECK (ordinal >= 0),
    evidence_text           text NOT NULL DEFAULT '',
    embedding               vector NOT NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (generation_id, file_id, ordinal),
    FOREIGN KEY (generation_id, workspace_id)
        REFERENCES index_generations(id, workspace_id) ON DELETE CASCADE
);

CREATE OR REPLACE FUNCTION mem_validate_index_generation_vector()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    expected_dimension integer;
    owner_user_id uuid;
BEGIN
    SELECT g.output_dimension, w.resource_owner_user_id
      INTO expected_dimension, owner_user_id
      FROM index_generations g
      JOIN workspaces w ON w.id = g.workspace_id
     WHERE g.id = NEW.generation_id
       AND g.workspace_id = NEW.workspace_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'index generation vector workspace mismatch'
            USING ERRCODE = '23514';
    END IF;
    IF vector_dims(NEW.embedding) <> expected_dimension THEN
        RAISE EXCEPTION 'index generation vector dimension mismatch'
            USING ERRCODE = '23514';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM files f
         WHERE f.id = NEW.file_id
           AND f.user_id = owner_user_id
    ) THEN
        RAISE EXCEPTION 'index generation vector file workspace mismatch'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER validate_index_generation_vector
BEFORE INSERT OR UPDATE OF generation_id, workspace_id, file_id, embedding
ON index_generation_vectors
FOR EACH ROW EXECUTE FUNCTION mem_validate_index_generation_vector();

CREATE INDEX idx_index_generation_vectors_file
    ON index_generation_vectors (file_id, generation_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE index_generation_events (
    id                      bigserial PRIMARY KEY,
    build_id                uuid NOT NULL,
    workspace_id            uuid NOT NULL,
    actor_user_id           uuid REFERENCES users(id) ON DELETE SET NULL,
    event_type              text NOT NULL CHECK (
                                event_type IN (
                                    'created', 'target_claimed', 'target_succeeded',
                                    'target_skipped', 'target_failed', 'cancelled',
                                    'resumed', 'ready', 'activated', 'rolled_back',
                                    'deactivated', 'failed', 'discarded'
                                )
                            ),
    from_state              text,
    to_state                text,
    details                 jsonb NOT NULL DEFAULT '{}'::jsonb
                            CHECK (
                                jsonb_typeof(details) = 'object' AND
                                octet_length(details::text) <= 4096
                            ),
    created_at              timestamptz NOT NULL DEFAULT now(),

    FOREIGN KEY (build_id, workspace_id)
        REFERENCES index_generation_builds(id, workspace_id) ON DELETE CASCADE
);

CREATE INDEX idx_index_generation_events_build
    ON index_generation_events (build_id, id);
CREATE INDEX idx_index_generation_events_workspace
    ON index_generation_events (workspace_id, id DESC);
-- +goose StatementEnd

-- +goose StatementBegin
-- A target is an immutable member of the build's corpus snapshot. Source
-- deletion therefore leaves a tombstone instead of removing the target and
-- making required_targets permanently disagree with the target set.
CREATE OR REPLACE FUNCTION mem_tombstone_index_generation_targets_on_file_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    affected record;
    old_build_state text;
    pending_count integer;
    failed_count integer;
    succeeded_count integer;
    skipped_count integer;
    next_build_state text;
BEGIN
    -- Every lifecycle path locks build before target. ClaimTarget holds the
    -- build in SHARE mode before taking a target; locking all affected builds
    -- here, in UUID order, prevents file deletion from taking the inverse
    -- target -> build path and forming a deadlock with a concurrent claim.
    PERFORM b.id
      FROM index_generation_builds b
     WHERE b.id IN (
               SELECT DISTINCT g.build_id
                 FROM index_generation_targets t
                 JOIN index_generations g ON g.id = t.generation_id
                WHERE t.file_id = OLD.id
                  AND t.source_present
           )
     ORDER BY b.id
     FOR UPDATE;

    FOR affected IN
        SELECT DISTINCT g.build_id, g.workspace_id
          FROM index_generation_targets t
          JOIN index_generations g ON g.id = t.generation_id
         WHERE t.file_id = OLD.id
           AND t.source_present
    LOOP
        UPDATE index_generation_targets t
           SET source_present = false,
               content_sha256 = NULL,
               state = 'skipped',
               error_code = 'source_deleted',
               attempt_token = NULL,
               lease_expires_at = NULL,
               completed_at = now(),
               updated_at = now()
          FROM index_generations g
         WHERE t.generation_id = g.id
           AND g.build_id = affected.build_id
           AND t.file_id = OLD.id
           AND t.source_present;

        SELECT b.state,
               count(*) FILTER (WHERE t.state IN ('pending', 'processing')),
               count(*) FILTER (WHERE t.state = 'failed'),
               count(*) FILTER (WHERE t.state = 'succeeded'),
               count(*) FILTER (WHERE t.state = 'skipped')
          INTO old_build_state, pending_count, failed_count,
               succeeded_count, skipped_count
          FROM index_generation_builds b
          JOIN index_generations g ON g.build_id = b.id
          JOIN index_generation_targets t ON t.generation_id = g.id
         WHERE b.id = affected.build_id
         GROUP BY b.state;

        next_build_state := old_build_state;
        IF old_build_state = 'building' AND pending_count = 0 THEN
            IF failed_count = 0 THEN
                next_build_state := 'ready';
            ELSE
                next_build_state := 'failed';
            END IF;
        END IF;

        UPDATE index_generation_builds
           SET succeeded_targets = succeeded_count,
               skipped_targets = skipped_count,
               failed_targets = failed_count,
               state = next_build_state,
               ready_at = CASE WHEN next_build_state = 'ready' THEN now() ELSE ready_at END,
               failed_at = CASE WHEN next_build_state = 'failed' THEN now() ELSE failed_at END,
               failure_code = CASE WHEN next_build_state = 'failed' THEN 'target_failures' ELSE failure_code END,
               updated_at = now()
         WHERE id = affected.build_id;

        UPDATE index_generations
           SET state = next_build_state, updated_at = now()
         WHERE build_id = affected.build_id
           AND state = old_build_state
           AND next_build_state <> old_build_state;

        INSERT INTO index_generation_events (
            build_id, workspace_id, event_type, from_state, to_state, details
        ) VALUES (
            affected.build_id, affected.workspace_id, 'target_skipped',
            NULL, 'skipped', '{"reason":"source_deleted"}'::jsonb
        );
        IF next_build_state <> old_build_state THEN
            INSERT INTO index_generation_events (
                build_id, workspace_id, event_type, from_state, to_state, details
            ) VALUES (
                affected.build_id, affected.workspace_id, next_build_state,
                old_build_state, next_build_state,
                '{"quality_gate":"all_targets","reason":"source_deleted"}'::jsonb
            );
        END IF;
    END LOOP;
    RETURN OLD;
END;
$$;

CREATE TRIGGER tombstone_index_generation_targets_on_file_delete
BEFORE DELETE ON files
FOR EACH ROW EXECUTE FUNCTION mem_tombstone_index_generation_targets_on_file_delete();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS tombstone_index_generation_targets_on_file_delete ON files;
DROP FUNCTION IF EXISTS mem_tombstone_index_generation_targets_on_file_delete();
DROP TABLE IF EXISTS index_generation_events;
DROP TABLE IF EXISTS index_generation_vectors;
DROP FUNCTION IF EXISTS mem_validate_index_generation_vector();
DROP TABLE IF EXISTS index_generation_targets;
DROP FUNCTION IF EXISTS mem_validate_index_generation_target();
DROP TABLE IF EXISTS index_generations;
DROP FUNCTION IF EXISTS mem_keep_index_generation_identity_immutable();
DROP TRIGGER IF EXISTS scrub_index_generation_actors_on_user_delete ON users;
DROP FUNCTION IF EXISTS mem_scrub_index_generation_actors_on_user_delete();
DROP TABLE IF EXISTS index_generation_builds;
DROP FUNCTION IF EXISTS mem_keep_index_generation_build_identity_immutable();
-- +goose StatementEnd

-- +goose Up
-- Immutable correction, supersede, and occurrence relations between memories.
-- A relation is append-only: once created it is never mutated or deleted except
-- by Forget (which redacts the graph edges touching a forgotten memory).

CREATE TABLE memory_relations (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id),
    source_id     UUID NOT NULL REFERENCES memories(id),
    target_id     UUID NOT NULL REFERENCES memories(id),
    relation_type TEXT NOT NULL CHECK (relation_type IN ('supersedes', 'corrects', 'occurrence_of')),
    actor_user_id UUID,
    actor_token_id UUID,
    reason        TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A source can relate to a target at most once per type.
    CONSTRAINT uq_memory_relations_triple UNIQUE (workspace_id, source_id, target_id, relation_type),
    -- Self-references are not valid.
    CONSTRAINT chk_memory_relations_no_self CHECK (source_id != target_id)
);

-- Find all relations where a given memory is the target (e.g. "who superseded me?").
CREATE INDEX idx_memory_relations_target ON memory_relations(workspace_id, target_id, relation_type);
-- Find all relations where a given memory is the source (e.g. "what did I supersede?").
CREATE INDEX idx_memory_relations_source ON memory_relations(workspace_id, source_id, relation_type);

-- +goose Down
DROP TABLE IF EXISTS memory_relations;

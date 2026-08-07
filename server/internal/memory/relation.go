package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/PeterGuy326/mem/server/internal/workspacelock"
)

// Relation types for memory-to-memory edges.
const (
	// RelSupersedes means source fully replaces target for active recall.
	RelSupersedes = "supersedes"
	// RelCorrects means source amends target (partial update). The target is
	// still excluded from active recall by default.
	RelCorrects = "corrects"
	// RelOccurrenceOf means source is a duplicate occurrence sharing the same
	// underlying claim as target; both remain active.
	RelOccurrenceOf = "occurrence_of"
)

var (
	// ErrRelationCycle is returned when creating a relation would form a cycle
	// in the supersedes/corrects DAG.
	ErrRelationCycle = errors.New("memory relation would form a cycle")
	// ErrCrossWorkspace is returned when source and target belong to different
	// workspaces.
	ErrCrossWorkspace = errors.New("memory relation crosses workspace boundary")
)

var validRelationTypes = map[string]struct{}{
	RelSupersedes:   {},
	RelCorrects:     {},
	RelOccurrenceOf: {},
}

// Relation is one immutable edge between two memories.
type Relation struct {
	ID           uuid.UUID  `json:"id"`
	WorkspaceID  uuid.UUID  `json:"workspace_id"`
	SourceID     uuid.UUID  `json:"source_id"`
	TargetID     uuid.UUID  `json:"target_id"`
	RelationType string     `json:"relation_type"`
	ActorUserID  *uuid.UUID `json:"actor_user_id,omitempty"`
	ActorTokenID *uuid.UUID `json:"-"`
	Reason       string     `json:"reason,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// CreateRelationCommand is the write contract for one immutable relation.
type CreateRelationCommand struct {
	WorkspaceID  uuid.UUID
	SourceID     uuid.UUID
	TargetID     uuid.UUID
	RelationType string
	ActorUserID  *uuid.UUID
	ActorTokenID *uuid.UUID
	Reason       string
	AllowedPaths []string
}

// CreateRelationResult distinguishes a new insert from a no-op duplicate.
type CreateRelationResult struct {
	Relation Relation `json:"relation"`
	Created  bool     `json:"created"`
}

// ListRelationsQuery retrieves relations for a memory.
type ListRelationsQuery struct {
	WorkspaceID  uuid.UUID
	MemoryID     uuid.UUID
	Direction    string // "source" or "target" — which end MemoryID occupies
	RelationType string // optional filter
	AllowedPaths []string
	Limit        int
}

// validateCreateRelationCommand checks the command fields without touching the database.
func validateCreateRelationCommand(cmd CreateRelationCommand) error {
	if cmd.WorkspaceID == uuid.Nil {
		return invalid("workspace_id is required")
	}
	if cmd.SourceID == uuid.Nil {
		return invalid("source_id is required")
	}
	if cmd.TargetID == uuid.Nil {
		return invalid("target_id is required")
	}
	if cmd.SourceID == cmd.TargetID {
		return invalid("source_id and target_id must differ")
	}
	relType := strings.ToLower(strings.TrimSpace(cmd.RelationType))
	if _, ok := validRelationTypes[relType]; !ok {
		return invalid("relation_type must be supersedes, corrects, or occurrence_of")
	}
	return nil
}

// CreateRelation atomically inserts an immutable relation between two memories
// in the same workspace. It enforces: same workspace, no self-reference,
// no cycles (for supersedes/corrects), and path authorization.
func (s *Service) CreateRelation(ctx context.Context, cmd CreateRelationCommand) (*CreateRelationResult, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("memory service is not configured")
	}

	if err := validateCreateRelationCommand(cmd); err != nil {
		return nil, err
	}
	relType := strings.ToLower(strings.TrimSpace(cmd.RelationType))
	cmd.Reason = strings.TrimSpace(cmd.Reason)

	allowed, err := normalizeAllowedPaths(cmd.AllowedPaths)
	if err != nil {
		return nil, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin create relation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := workspacelock.ForContentWrite(ctx, tx, cmd.WorkspaceID); err != nil {
		return nil, err
	}

	// Verify both memories exist in the same workspace and are path-authorized.
	srcMem, err := s.loadMemoryInTx(ctx, tx, cmd.WorkspaceID, cmd.SourceID, allowed)
	if err != nil {
		return nil, fmt.Errorf("source memory: %w", err)
	}
	tgtMem, err := s.loadMemoryInTx(ctx, tx, cmd.WorkspaceID, cmd.TargetID, allowed)
	if err != nil {
		return nil, fmt.Errorf("target memory: %w", err)
	}

	// Cross-workspace check (redundant given FK + workspace_id but explicit).
	if srcMem.WorkspaceID != tgtMem.WorkspaceID {
		return nil, ErrCrossWorkspace
	}

	// Neither memory may be forgotten.
	if srcMem.LifecycleStatus == StatusForgotten {
		return nil, fmt.Errorf("source: %w", ErrForgotten)
	}
	if tgtMem.LifecycleStatus == StatusForgotten {
		return nil, fmt.Errorf("target: %w", ErrForgotten)
	}

	// Cycle prevention for supersedes and corrects.
	if relType == RelSupersedes || relType == RelCorrects {
		hasCycle, err := s.wouldCycle(ctx, tx, cmd.WorkspaceID, cmd.SourceID, cmd.TargetID)
		if err != nil {
			return nil, fmt.Errorf("cycle check: %w", err)
		}
		if hasCycle {
			return nil, ErrRelationCycle
		}
	}

	// Insert with ON CONFLICT DO NOTHING for idempotency.
	var rel Relation
	err = tx.QueryRow(ctx, `
		INSERT INTO memory_relations (workspace_id, source_id, target_id, relation_type, actor_user_id, actor_token_id, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (workspace_id, source_id, target_id, relation_type) DO NOTHING
		RETURNING id, workspace_id, source_id, target_id, relation_type, actor_user_id, actor_token_id, reason, created_at`,
		cmd.WorkspaceID, cmd.SourceID, cmd.TargetID, relType,
		cmd.ActorUserID, cmd.ActorTokenID, cmd.Reason,
	).Scan(&rel.ID, &rel.WorkspaceID, &rel.SourceID, &rel.TargetID,
		&rel.RelationType, &rel.ActorUserID, &rel.ActorTokenID, &rel.Reason, &rel.CreatedAt)

	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit relation: %w", err)
		}
		return &CreateRelationResult{Relation: rel, Created: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("insert relation: %w", err)
	}

	// Conflict path: load the existing row.
	err = tx.QueryRow(ctx, `
		SELECT id, workspace_id, source_id, target_id, relation_type, actor_user_id, actor_token_id, reason, created_at
		  FROM memory_relations
		 WHERE workspace_id = $1 AND source_id = $2 AND target_id = $3 AND relation_type = $4`,
		cmd.WorkspaceID, cmd.SourceID, cmd.TargetID, relType,
	).Scan(&rel.ID, &rel.WorkspaceID, &rel.SourceID, &rel.TargetID,
		&rel.RelationType, &rel.ActorUserID, &rel.ActorTokenID, &rel.Reason, &rel.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("load existing relation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit relation replay: %w", err)
	}
	return &CreateRelationResult{Relation: rel, Created: false}, nil
}

// validateListRelationsQuery checks the query fields without touching the database.
func validateListRelationsQuery(q ListRelationsQuery) error {
	if q.WorkspaceID == uuid.Nil {
		return invalid("workspace_id is required")
	}
	if q.MemoryID == uuid.Nil {
		return invalid("memory_id is required")
	}
	direction := strings.ToLower(strings.TrimSpace(q.Direction))
	if direction == "" {
		direction = "source"
	}
	if direction != "source" && direction != "target" {
		return invalid("direction must be source or target")
	}
	if q.RelationType != "" {
		relType := strings.ToLower(strings.TrimSpace(q.RelationType))
		if _, ok := validRelationTypes[relType]; !ok {
			return invalid("relation_type must be supersedes, corrects, or occurrence_of")
		}
	}
	return nil
}

// ListRelations returns relations for a given memory. Direction controls
// whether MemoryID is matched as source or target.
func (s *Service) ListRelations(ctx context.Context, q ListRelationsQuery) ([]Relation, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("memory service is not configured")
	}
	if err := validateListRelationsQuery(q); err != nil {
		return nil, err
	}
	direction := strings.ToLower(strings.TrimSpace(q.Direction))
	if direction == "" {
		direction = "source"
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 200 {
		q.Limit = 200
	}

	args := []any{q.WorkspaceID, q.MemoryID}
	var dirColumn string
	if direction == "source" {
		dirColumn = "source_id"
	} else {
		dirColumn = "target_id"
	}
	where := []string{
		"r.workspace_id = $1",
		fmt.Sprintf("r.%s = $2", dirColumn),
	}
	if q.RelationType != "" {
		relType := strings.ToLower(strings.TrimSpace(q.RelationType))
		args = append(args, relType)
		where = append(where, fmt.Sprintf("r.relation_type = $%d", len(args)))
	}
	args = append(args, q.Limit)
	limitIdx := len(args)

	sql := fmt.Sprintf(`
		SELECT r.id, r.workspace_id, r.source_id, r.target_id, r.relation_type,
		       r.actor_user_id, r.actor_token_id, r.reason, r.created_at
		  FROM memory_relations r
		 WHERE %s
		 ORDER BY r.created_at DESC, r.id
		 LIMIT $%d`, strings.Join(where, " AND "), limitIdx)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list relations: %w", err)
	}
	defer rows.Close()

	out := make([]Relation, 0, q.Limit)
	for rows.Next() {
		var rel Relation
		if err := rows.Scan(&rel.ID, &rel.WorkspaceID, &rel.SourceID, &rel.TargetID,
			&rel.RelationType, &rel.ActorUserID, &rel.ActorTokenID, &rel.Reason, &rel.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan relation: %w", err)
		}
		out = append(out, rel)
	}
	return out, rows.Err()
}

// IsSuperseded returns true if the given memory has been superseded or corrected
// by another active memory.
func (s *Service) IsSuperseded(ctx context.Context, workspaceID, memoryID uuid.UUID) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM memory_relations r
			  JOIN memories m ON m.id = r.source_id AND m.workspace_id = r.workspace_id
			 WHERE r.workspace_id = $1
			   AND r.target_id = $2
			   AND r.relation_type IN ('supersedes', 'corrects')
			   AND m.lifecycle_status = 'active'
		)`, workspaceID, memoryID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check superseded: %w", err)
	}
	return exists, nil
}

// SupersededIDs returns all memory IDs in the workspace that have been
// superseded or corrected by an active memory. Used by Recall to filter.
func (s *Service) SupersededIDs(ctx context.Context, workspaceID uuid.UUID, candidateIDs []uuid.UUID) (map[uuid.UUID]struct{}, error) {
	if len(candidateIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT r.target_id
		  FROM memory_relations r
		  JOIN memories m ON m.id = r.source_id AND m.workspace_id = r.workspace_id
		 WHERE r.workspace_id = $1
		   AND r.target_id = ANY($2::uuid[])
		   AND r.relation_type IN ('supersedes', 'corrects')
		   AND m.lifecycle_status = 'active'`,
		workspaceID, candidateIDs)
	if err != nil {
		return nil, fmt.Errorf("load superseded IDs: %w", err)
	}
	defer rows.Close()
	result := make(map[uuid.UUID]struct{})
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = struct{}{}
	}
	return result, rows.Err()
}

// RedactRelationsForMemory removes actor metadata from relations touching a
// forgotten memory (called during Forget).
func (s *Service) RedactRelationsForMemory(ctx context.Context, tx pgx.Tx, workspaceID, memoryID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE memory_relations
		   SET actor_user_id = NULL,
		       actor_token_id = NULL,
		       reason = ''
		 WHERE workspace_id = $1
		   AND (source_id = $2 OR target_id = $2)`,
		workspaceID, memoryID)
	if err != nil {
		return fmt.Errorf("redact memory relations: %w", err)
	}
	return nil
}

// --- internals ---

func (s *Service) loadMemoryInTx(ctx context.Context, tx pgx.Tx, workspaceID, memoryID uuid.UUID, allowed []string) (Memory, error) {
	args := []any{workspaceID, memoryID}
	where := []string{"m.workspace_id = $1", "m.id = $2"}
	args, where = appendPathFilters(args, where, "m.path", "/", allowed)
	return scanMemory(tx.QueryRow(ctx, `
		SELECT `+qualifyColumns("m")+`
		  FROM memories m
		 WHERE `+strings.Join(where, " AND ")+`
		 FOR SHARE`, args...))
}

// wouldCycle checks whether adding source -> target would create a cycle in the
// supersedes/corrects DAG. It traverses forward from target to see if source
// is reachable.
func (s *Service) wouldCycle(ctx context.Context, tx pgx.Tx, workspaceID, sourceID, targetID uuid.UUID) (bool, error) {
	// BFS from target following source edges: if we ever reach sourceID, there
	// is a cycle.
	var found bool
	err := tx.QueryRow(ctx, `
		WITH RECURSIVE chain(id) AS (
			SELECT $3::uuid
			UNION
			SELECT r.source_id
			  FROM memory_relations r
			  JOIN chain c ON c.id = r.target_id
			 WHERE r.workspace_id = $1
			   AND r.relation_type IN ('supersedes', 'corrects')
		)
		SELECT EXISTS(SELECT 1 FROM chain WHERE id = $2)`,
		workspaceID, sourceID, targetID,
	).Scan(&found)
	if err != nil {
		return false, err
	}
	return found, nil
}

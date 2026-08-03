package indexgeneration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/workspacelock"
)

const defaultRetention = 7 * 24 * time.Hour
const defaultTargetLease = 15 * time.Minute

// Service owns canonical generation lifecycle semantics. Worker and search
// adapters are intentionally outside this package.
type Service struct {
	pool        *pgxpool.Pool
	enabled     map[string]struct{}
	now         func() time.Time
	retention   time.Duration
	targetLease time.Duration

	// afterClaimBuildLocked is a deterministic integration-test barrier. It is
	// nil in production and runs only after ClaimTarget holds the build row in
	// SHARE mode, before it attempts any target lock.
	afterClaimBuildLocked func()
}

func New(pool *pgxpool.Pool, enabledProfiles ...string) *Service {
	enabled := make(map[string]struct{}, len(enabledProfiles))
	if len(enabledProfiles) == 0 {
		enabled[aiprofile.LocalFastV2] = struct{}{}
		enabled[aiprofile.IdealabQualityV2] = struct{}{}
	} else {
		for _, id := range enabledProfiles {
			enabled[strings.TrimSpace(id)] = struct{}{}
		}
	}
	return &Service{
		pool: pool, enabled: enabled, now: time.Now, retention: defaultRetention,
		targetLease: defaultTargetLease,
	}
}

func (s *Service) Create(
	ctx context.Context,
	workspaceID, actorID uuid.UUID,
	profileID string,
) (*Build, error) {
	if workspaceID == uuid.Nil {
		return nil, ErrWorkspaceRequired
	}
	if actorID == uuid.Nil {
		return nil, ErrActorRequired
	}
	if s == nil || s.pool == nil {
		return nil, ErrUnavailable
	}
	definition, ok := aiprofile.Find(strings.TrimSpace(profileID))
	if !ok || definition.ID == aiprofile.LocalFastV1 ||
		definition.ID == aiprofile.IdealabQualityV1 {
		return nil, ErrProfileUnavailable
	}
	if _, ok := s.enabled[definition.ID]; !ok {
		return nil, ErrProfileUnavailable
	}
	if err := definition.Validate(); err != nil {
		return nil, ErrProfileUnavailable
	}
	snapshot := snapshotFromDefinition(definition)
	if err := validateProfileSnapshot(snapshot); err != nil {
		return nil, ErrProfileUnavailable
	}
	encodedSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("%w: encode profile snapshot: %v", ErrUnavailable, err)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: begin create: %v", ErrUnavailable, err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	// A generation's target rows are the authoritative, exact corpus
	// membership. FOR UPDATE conflicts with every content writer's first
	// FOR KEY SHARE action, so all files committed before this lock are visible
	// and files committed after it are intentionally left for a later build.
	var ownerID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT resource_owner_user_id FROM workspaces
		 WHERE id = $1 FOR UPDATE
	`, workspaceID).Scan(&ownerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%w: lock workspace: %v", ErrUnavailable, err)
	}

	if existing, findErr := getInflightIdentity(
		ctx, tx, workspaceID, definition,
	); findErr == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("%w: commit idempotent create: %v", ErrUnavailable, err)
		}
		return s.Get(ctx, workspaceID, existing)
	} else if !errors.Is(findErr, ErrNotFound) {
		return nil, findErr
	}

	now := s.now().UTC()
	var corpusFileCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM files WHERE user_id = $1`, ownerID).
		Scan(&corpusFileCount); err != nil {
		return nil, fmt.Errorf("%w: count corpus snapshot: %v", ErrUnavailable, err)
	}
	buildID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO index_generation_builds (
			id, workspace_id, profile_id, profile_revision,
			pipeline_revision, allowed_mime_types, profile_snapshot,
			corpus_captured_at, corpus_file_count, created_by_user_id,
			created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$8,$8)
	`, buildID, workspaceID, definition.ID, definition.Revision,
		definition.PipelineRevision, definition.AllowedMIMETypes, encodedSnapshot,
		now, corpusFileCount, actorID,
	); err != nil {
		return nil, fmt.Errorf("%w: insert build: %v", ErrUnavailable, err)
	}

	type route struct {
		kind  string
		stage string
		spec  aiprofile.Stage
	}
	routes := []route{{RouteText, "text_embedding", definition.Embedding}}
	if definition.VisualEmbedding.Enabled {
		routes = append(routes, route{RouteVisual, "visual_embedding", definition.VisualEmbedding})
	}
	for _, route := range routes {
		provider, model, valid := strings.Cut(route.spec.Provider, ":")
		if !valid || provider == "" || model == "" {
			return nil, ErrProfileUnavailable
		}
		generationID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO index_generations (
				id, build_id, workspace_id, route_kind, provider,
				model_revision, output_dimension, pipeline_revision,
				profile_id, profile_revision, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
		`, generationID, buildID, workspaceID, route.kind, provider, model,
			route.spec.Dimensions, definition.PipelineRevision, definition.ID,
			definition.Revision, now,
		); err != nil {
			return nil, fmt.Errorf("%w: insert route generation: %v", ErrUnavailable, err)
		}

		mimePredicate := "TRUE"
		if route.kind == RouteVisual {
			mimePredicate = "f.mime LIKE 'image/%'"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO index_generation_targets (
				generation_id, workspace_id, file_id, content_sha256,
				stage, created_at, updated_at
			)
			SELECT $1, $2, f.id, f.sha256, $3, $4, $4
			  FROM files f
			 WHERE f.user_id = $5
			   AND `+mimePredicate+`
			   AND EXISTS (
			       SELECT 1 FROM unnest($6::text[]) pattern
			        WHERE f.mime LIKE replace(pattern, '*', '%')
			   )
		`, generationID, workspaceID, route.stage, now, ownerID,
			definition.AllowedMIMETypes,
		); err != nil {
			return nil, fmt.Errorf("%w: seed build targets: %v", ErrUnavailable, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE index_generation_builds b
		   SET required_targets = (
		       SELECT count(*) FROM index_generation_targets t
		       JOIN index_generations g ON g.id = t.generation_id
		       WHERE g.build_id = b.id
		   ), updated_at = $2
		 WHERE b.id = $1
	`, buildID, now); err != nil {
		return nil, fmt.Errorf("%w: count build targets: %v", ErrUnavailable, err)
	}
	var requiredTargets int
	if err := tx.QueryRow(ctx, `
		SELECT required_targets FROM index_generation_builds WHERE id = $1
	`, buildID).Scan(&requiredTargets); err != nil {
		return nil, fmt.Errorf("%w: read build target count: %v", ErrUnavailable, err)
	}
	if requiredTargets == 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE index_generation_builds
			   SET state = 'ready', ready_at = $2, updated_at = $2
			 WHERE id = $1
		`, buildID, now); err != nil {
			return nil, fmt.Errorf("%w: mark empty build ready: %v", ErrUnavailable, err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE index_generations
			   SET state = 'ready', updated_at = $2
			 WHERE build_id = $1
		`, buildID, now); err != nil {
			return nil, fmt.Errorf("%w: mark empty generations ready: %v", ErrUnavailable, err)
		}
	}
	if err := insertEvent(ctx, tx, buildID, workspaceID, &actorID,
		"created", nil, stringPointer(StateBuilding), map[string]any{
			"profile_id": definition.ID, "route_count": len(routes),
		}); err != nil {
		return nil, err
	}
	if requiredTargets == 0 {
		if err := insertEvent(ctx, tx, buildID, workspaceID, nil,
			"ready", stringPointer(StateBuilding), stringPointer(StateReady),
			map[string]any{"quality_gate": "all_targets"}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit create: %v", ErrUnavailable, err)
	}
	return s.Get(ctx, workspaceID, buildID)
}

func (s *Service) Get(
	ctx context.Context,
	workspaceID, buildID uuid.UUID,
) (*Build, error) {
	if workspaceID == uuid.Nil {
		return nil, ErrWorkspaceRequired
	}
	if buildID == uuid.Nil {
		return nil, ErrNotFound
	}
	if s == nil || s.pool == nil {
		return nil, ErrUnavailable
	}
	build, err := scanBuild(s.pool.QueryRow(ctx, buildSelect+`
		WHERE b.workspace_id = $1 AND b.id = $2`, workspaceID, buildID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: get build: %v", ErrUnavailable, err)
	}
	generations, err := listGenerations(ctx, s.pool, workspaceID, buildID)
	if err != nil {
		return nil, err
	}
	build.Generations = generations
	return build, nil
}

func (s *Service) List(ctx context.Context, workspaceID uuid.UUID, limit int) ([]Build, error) {
	if workspaceID == uuid.Nil {
		return nil, ErrWorkspaceRequired
	}
	if s == nil || s.pool == nil {
		return nil, ErrUnavailable
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, buildSelect+`
		WHERE b.workspace_id = $1
		ORDER BY b.created_at DESC, b.id DESC LIMIT $2`, workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: list builds: %v", ErrUnavailable, err)
	}
	defer rows.Close()
	out := make([]Build, 0, limit)
	for rows.Next() {
		build, err := scanBuild(rows)
		if err != nil {
			return nil, fmt.Errorf("%w: scan build: %v", ErrUnavailable, err)
		}
		generations, err := listGenerations(ctx, s.pool, workspaceID, build.ID)
		if err != nil {
			return nil, err
		}
		build.Generations = generations
		out = append(out, *build)
	}
	return out, rows.Err()
}

func (s *Service) Cancel(
	ctx context.Context,
	workspaceID, actorID, buildID uuid.UUID,
) (*Build, error) {
	return s.transition(ctx, workspaceID, actorID, buildID,
		[]string{StateBuilding}, StateCancelled, "cancelled")
}

func (s *Service) Resume(
	ctx context.Context,
	workspaceID, actorID, buildID uuid.UUID,
) (*Build, error) {
	if workspaceID == uuid.Nil {
		return nil, ErrWorkspaceRequired
	}
	if actorID == uuid.Nil {
		return nil, ErrActorRequired
	}
	if s == nil || s.pool == nil {
		return nil, ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: begin resume: %v", ErrUnavailable, err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err := workspacelock.ForAIProfileCoordination(ctx, tx, workspaceID); err != nil {
		return nil, mapLockError(err)
	}
	from, err := lockedBuildState(ctx, tx, workspaceID, buildID)
	if err != nil {
		return nil, err
	}
	if from != StateCancelled && from != StateFailed {
		return nil, ErrInvalidTransition
	}
	now := s.now().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE index_generation_targets t
		   SET state = 'pending', error_code = NULL, updated_at = $2,
		       attempt_token = NULL, lease_expires_at = NULL,
		       started_at = NULL, completed_at = NULL
		  FROM index_generations g
		 WHERE t.generation_id = g.id AND g.build_id = $1
		   AND t.state IN ('failed', 'processing')
	`, buildID, now); err != nil {
		return nil, fmt.Errorf("%w: reset targets: %v", ErrUnavailable, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE index_generation_builds
		   SET state = 'building', failed_targets = 0, failure_code = NULL,
		       failed_at = NULL, cancelled_at = NULL, updated_at = $2
		 WHERE id = $1 AND workspace_id = $3
	`, buildID, now, workspaceID); err != nil {
		return nil, fmt.Errorf("%w: resume build: %v", ErrUnavailable, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE index_generations
		   SET state = 'building', updated_at = $2
		 WHERE build_id = $1 AND workspace_id = $3
	`, buildID, now, workspaceID); err != nil {
		return nil, fmt.Errorf("%w: resume generations: %v", ErrUnavailable, err)
	}
	if err := insertEvent(ctx, tx, buildID, workspaceID, &actorID,
		"resumed", &from, stringPointer(StateBuilding), nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit resume: %v", ErrUnavailable, err)
	}
	return s.Get(ctx, workspaceID, buildID)
}

// ClaimTarget atomically leases one pending target. It is the intended Worker
// integration seam and is not exposed to untrusted HTTP clients.
func (s *Service) ClaimTarget(
	ctx context.Context,
	workspaceID, buildID uuid.UUID,
) (*Target, error) {
	if workspaceID == uuid.Nil {
		return nil, ErrWorkspaceRequired
	}
	if s == nil || s.pool == nil {
		return nil, ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: begin claim: %v", ErrUnavailable, err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	var buildState string
	if err := tx.QueryRow(ctx, `
		SELECT state FROM index_generation_builds
		 WHERE workspace_id = $1 AND id = $2 FOR SHARE
	`, workspaceID, buildID).Scan(&buildState); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTargetUnavailable
	} else if err != nil {
		return nil, fmt.Errorf("%w: lock claim build: %v", ErrUnavailable, err)
	}
	if buildState != StateBuilding {
		return nil, ErrTargetUnavailable
	}
	if s.afterClaimBuildLocked != nil {
		s.afterClaimBuildLocked()
	}
	var target Target
	var previousState string
	now := s.now().UTC()
	attemptToken := uuid.New()
	leaseExpiresAt := now.Add(s.targetLease)
	err = tx.QueryRow(ctx, `
		WITH candidate AS (
			SELECT t.generation_id, t.file_id, t.stage, t.state AS previous_state
			  FROM index_generation_targets t
			  JOIN index_generations g ON g.id = t.generation_id
			  JOIN index_generation_builds b ON b.id = g.build_id
			 WHERE b.workspace_id = $1 AND b.id = $2
			   AND b.state = 'building' AND g.state = 'building'
			   AND t.source_present
			   AND (t.state = 'pending' OR
			        (t.state = 'processing' AND t.lease_expires_at <= $3))
			 -- Reclaim expired work before leasing untouched targets, and resume
			 -- previously attempted pending work before starting new work. This
			 -- keeps retries bounded behind the rest of a large corpus.
			 ORDER BY CASE WHEN t.state = 'processing' THEN 0 ELSE 1 END,
			          t.attempts DESC, t.updated_at, t.file_id, t.stage
			 FOR UPDATE OF t SKIP LOCKED LIMIT 1
		)
		UPDATE index_generation_targets t
		   SET state = 'processing', attempts = attempts + 1,
		       attempt_token = $4, lease_expires_at = $5,
		       started_at = $3, updated_at = $3, error_code = NULL
		  FROM candidate c, index_generations g
		 WHERE t.generation_id = c.generation_id
		   AND t.file_id = c.file_id AND t.stage = c.stage
		   AND g.id = t.generation_id
		RETURNING t.generation_id, t.workspace_id, t.file_id, t.content_sha256,
		          t.stage, t.state, t.attempts, t.attempt_token,
		          t.lease_expires_at, t.source_present, c.previous_state,
		          g.provider, g.model_revision,
		          g.output_dimension, g.profile_id, g.profile_revision,
		          g.pipeline_revision
	`, workspaceID, buildID, now, attemptToken, leaseExpiresAt).Scan(
		&target.GenerationID, &target.WorkspaceID, &target.FileID,
		&target.ContentSHA256, &target.Stage, &target.State, &target.Attempts,
		&target.AttemptToken, &target.LeaseExpiresAt, &target.SourcePresent,
		&previousState,
		&target.Provider, &target.ModelRevision, &target.OutputDimension,
		&target.ProfileID, &target.ProfileRevision, &target.PipelineRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTargetUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("%w: claim target: %v", ErrUnavailable, err)
	}
	if err := insertEvent(ctx, tx, buildID, workspaceID, nil,
		"target_claimed", &previousState,
		stringPointer(TargetProcessing), map[string]any{
			"generation_id": target.GenerationID, "file_id": target.FileID,
			"stage": target.Stage, "attempt": target.Attempts,
		}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit claim: %v", ErrUnavailable, err)
	}
	return &target, nil
}

func (s *Service) SucceedTarget(
	ctx context.Context,
	target Target,
	vectors []Vector,
) (*Build, error) {
	if len(vectors) == 0 {
		return nil, ErrQualityGate
	}
	for _, vector := range vectors {
		if len(vector.Values) != target.OutputDimension {
			return nil, ErrDimensionMismatch
		}
	}
	return s.completeTarget(ctx, target, TargetSucceeded, "", vectors)
}

func (s *Service) SkipTarget(ctx context.Context, target Target) (*Build, error) {
	return s.completeTarget(ctx, target, TargetSkipped, "", nil)
}

func (s *Service) FailTarget(
	ctx context.Context,
	target Target,
	errorCode string,
) (*Build, error) {
	if !safeErrorCode(errorCode) {
		errorCode = "worker_failed"
	}
	return s.completeTarget(ctx, target, TargetFailed, errorCode, nil)
}

func (s *Service) Activate(
	ctx context.Context,
	workspaceID, actorID, buildID uuid.UUID,
) (*Build, error) {
	return s.activate(ctx, workspaceID, actorID, buildID, false)
}

func (s *Service) Rollback(
	ctx context.Context,
	workspaceID, actorID, buildID uuid.UUID,
) (*Build, error) {
	return s.activate(ctx, workspaceID, actorID, buildID, true)
}

func (s *Service) Discard(
	ctx context.Context,
	workspaceID, actorID, buildID uuid.UUID,
) (*Build, error) {
	if workspaceID == uuid.Nil {
		return nil, ErrWorkspaceRequired
	}
	if actorID == uuid.Nil {
		return nil, ErrActorRequired
	}
	if s == nil || s.pool == nil {
		return nil, ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: begin discard: %v", ErrUnavailable, err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err := workspacelock.ForAIProfileCoordination(ctx, tx, workspaceID); err != nil {
		return nil, mapLockError(err)
	}
	from, err := lockedBuildState(ctx, tx, workspaceID, buildID)
	if err != nil {
		return nil, err
	}
	if from != StateCancelled && from != StateFailed && from != StateInactive {
		return nil, ErrInvalidTransition
	}
	now := s.now().UTC()
	retentionUntil := now.Add(s.retention)
	if _, err := tx.Exec(ctx, `
		UPDATE index_generation_builds
		   SET state = 'discarded', retention_until = $1, updated_at = $2
		 WHERE id = $3 AND workspace_id = $4
	`, retentionUntil, now, buildID, workspaceID); err != nil {
		return nil, fmt.Errorf("%w: discard build: %v", ErrUnavailable, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE index_generations
		   SET state = 'discarded', updated_at = $1
		 WHERE build_id = $2 AND workspace_id = $3
	`, now, buildID, workspaceID); err != nil {
		return nil, fmt.Errorf("%w: discard generations: %v", ErrUnavailable, err)
	}
	if err := insertEvent(ctx, tx, buildID, workspaceID, &actorID,
		"discarded", &from, stringPointer(StateDiscarded), map[string]any{
			"retention_until": retentionUntil,
		}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit discard: %v", ErrUnavailable, err)
	}
	return s.Get(ctx, workspaceID, buildID)
}

// CleanupExpired physically removes discarded generations after their
// rollback retention window. Activation lineage is event-only, so no build
// pointer can form a cycle or keep an expired build alive.
func (s *Service) CleanupExpired(ctx context.Context, workspaceID uuid.UUID) (int64, error) {
	if workspaceID == uuid.Nil {
		return 0, ErrWorkspaceRequired
	}
	if s == nil || s.pool == nil {
		return 0, ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("%w: begin cleanup: %v", ErrUnavailable, err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err := workspacelock.ForAIProfileCoordination(ctx, tx, workspaceID); err != nil {
		return 0, mapLockError(err)
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM index_generation_builds
		 WHERE workspace_id = $1
		   AND state = 'discarded'
		   AND retention_until <= $2
	`, workspaceID, s.now().UTC())
	if err != nil {
		return 0, fmt.Errorf("%w: delete expired builds: %v", ErrUnavailable, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("%w: commit cleanup: %v", ErrUnavailable, err)
	}
	return tag.RowsAffected(), nil
}

func (s *Service) Events(
	ctx context.Context,
	workspaceID, buildID uuid.UUID,
) ([]Event, error) {
	if workspaceID == uuid.Nil {
		return nil, ErrWorkspaceRequired
	}
	if s == nil || s.pool == nil {
		return nil, ErrUnavailable
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM index_generation_builds
			 WHERE workspace_id = $1 AND id = $2
		)
	`, workspaceID, buildID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("%w: find event build: %v", ErrUnavailable, err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, build_id, workspace_id, actor_user_id, event_type,
		       from_state, to_state, details, created_at
		  FROM index_generation_events
		 WHERE workspace_id = $1 AND build_id = $2
		 ORDER BY id DESC
		 LIMIT 100
	`, workspaceID, buildID)
	if err != nil {
		return nil, fmt.Errorf("%w: list events: %v", ErrUnavailable, err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var event Event
		var details []byte
		var actorID *uuid.UUID
		if err := rows.Scan(&event.ID, &event.BuildID, &event.WorkspaceID,
			&actorID, &event.EventType, &event.FromState,
			&event.ToState, &details, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("%w: scan event: %v", ErrUnavailable, err)
		}
		if err := json.Unmarshal(details, &event.Details); err != nil {
			return nil, fmt.Errorf("%w: decode event: %v", ErrUnavailable, err)
		}
		event.ActorPresent = actorID != nil
		out = append(out, event)
	}
	return out, rows.Err()
}

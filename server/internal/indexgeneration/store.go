package indexgeneration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/workspacelock"
)

type rowScanner interface {
	Scan(...any) error
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

const buildSelect = `
	SELECT b.id, b.workspace_id, b.profile_id, b.profile_revision,
	       b.pipeline_revision, b.allowed_mime_types, b.profile_snapshot,
	       b.corpus_captured_at, b.corpus_file_count, b.state,
	       b.quality_gate, b.required_targets, b.succeeded_targets,
	       b.skipped_targets, b.failed_targets, b.created_by_user_id,
	       b.created_at, b.updated_at, b.ready_at, b.activated_at,
	       b.cancelled_at, b.failed_at, b.failure_code,
	       b.retention_until
	  FROM index_generation_builds b `

func scanBuild(row rowScanner) (*Build, error) {
	var build Build
	var quality, profileSnapshot []byte
	if err := row.Scan(
		&build.ID, &build.WorkspaceID, &build.ProfileID,
		&build.ProfileRevision, &build.PipelineRevision,
		&build.AllowedMIMETypes, &profileSnapshot,
		&build.CorpusCapturedAt, &build.CorpusFileCount,
		&build.State, &quality,
		&build.RequiredTargets, &build.SucceededTargets,
		&build.SkippedTargets, &build.FailedTargets,
		&build.CreatedByUserID, &build.CreatedAt, &build.UpdatedAt,
		&build.ReadyAt, &build.ActivatedAt, &build.CancelledAt,
		&build.FailedAt, &build.FailureCode, &build.RetentionUntil,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(quality, &build.QualityGate); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(profileSnapshot, &build.ProfileSnapshot); err != nil {
		return nil, err
	}
	build.CreatorPresent = build.CreatedByUserID != nil
	if err := validateProfileSnapshot(build.ProfileSnapshot); err != nil ||
		build.ProfileID != build.ProfileSnapshot.ID ||
		build.ProfileRevision != build.ProfileSnapshot.Revision ||
		build.PipelineRevision != build.ProfileSnapshot.PipelineRevision ||
		!slices.Equal(build.AllowedMIMETypes, build.ProfileSnapshot.AllowedMIMETypes) {
		return nil, fmt.Errorf("invalid persisted profile snapshot")
	}
	return &build, nil
}

func listGenerations(
	ctx context.Context,
	q queryer,
	workspaceID, buildID uuid.UUID,
) ([]Generation, error) {
	rows, err := q.Query(ctx, `
		SELECT id, build_id, workspace_id, route_kind, provider,
		       model_revision, output_dimension, pipeline_revision,
		       profile_id, profile_revision, state, created_at, updated_at
		  FROM index_generations
		 WHERE workspace_id = $1 AND build_id = $2
		 ORDER BY route_kind
	`, workspaceID, buildID)
	if err != nil {
		return nil, fmt.Errorf("%w: list route generations: %v", ErrUnavailable, err)
	}
	defer rows.Close()
	var out []Generation
	for rows.Next() {
		var generation Generation
		if err := rows.Scan(
			&generation.ID, &generation.BuildID, &generation.WorkspaceID,
			&generation.RouteKind, &generation.Provider,
			&generation.ModelRevision, &generation.OutputDimension,
			&generation.PipelineRevision, &generation.ProfileID,
			&generation.ProfileRevision, &generation.State,
			&generation.CreatedAt, &generation.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("%w: scan route generation: %v", ErrUnavailable, err)
		}
		out = append(out, generation)
	}
	return out, rows.Err()
}

func getInflightIdentity(
	ctx context.Context,
	q queryer,
	workspaceID uuid.UUID,
	definition aiprofile.Definition,
) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT id FROM index_generation_builds
		 WHERE workspace_id = $1 AND profile_id = $2
		   AND profile_revision = $3 AND pipeline_revision = $4
		   AND state IN ('building', 'cancelled', 'failed', 'ready')
		 ORDER BY created_at DESC LIMIT 1
	`, workspaceID, definition.ID, definition.Revision,
		definition.PipelineRevision).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: find existing build: %v", ErrUnavailable, err)
	}
	return id, nil
}

func (s *Service) transition(
	ctx context.Context,
	workspaceID, actorID, buildID uuid.UUID,
	allowed []string,
	to, eventType string,
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
		return nil, fmt.Errorf("%w: begin transition: %v", ErrUnavailable, err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err := workspacelock.ForAIProfileCoordination(ctx, tx, workspaceID); err != nil {
		return nil, mapLockError(err)
	}
	from, err := lockedBuildState(ctx, tx, workspaceID, buildID)
	if err != nil {
		return nil, err
	}
	if !contains(allowed, from) {
		return nil, ErrInvalidTransition
	}
	now := s.now().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE index_generation_builds
		   SET state = $1, updated_at = $2,
		       cancelled_at = CASE WHEN $1 = 'cancelled' THEN $2 ELSE cancelled_at END
		 WHERE id = $3 AND workspace_id = $4
	`, to, now, buildID, workspaceID); err != nil {
		return nil, fmt.Errorf("%w: transition build: %v", ErrUnavailable, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE index_generations
		   SET state = $1, updated_at = $2
		 WHERE build_id = $3 AND workspace_id = $4
	`, to, now, buildID, workspaceID); err != nil {
		return nil, fmt.Errorf("%w: transition generations: %v", ErrUnavailable, err)
	}
	if err := insertEvent(ctx, tx, buildID, workspaceID, &actorID,
		eventType, &from, &to, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit transition: %v", ErrUnavailable, err)
	}
	return s.Get(ctx, workspaceID, buildID)
}

func (s *Service) completeTarget(
	ctx context.Context,
	target Target,
	state, errorCode string,
	vectors []Vector,
) (*Build, error) {
	if target.WorkspaceID == uuid.Nil || target.GenerationID == uuid.Nil ||
		target.FileID == uuid.Nil {
		return nil, ErrTargetUnavailable
	}
	if s == nil || s.pool == nil {
		return nil, ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: begin complete target: %v", ErrUnavailable, err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	ownerID, err := workspacelock.ForAIProfileCoordination(
		ctx, tx, target.WorkspaceID,
	)
	if err != nil {
		return nil, mapLockError(err)
	}
	// Follow the canonical workspace -> file -> derived-row lock order. A
	// concurrent file deletion either wins first and makes this attempt stale,
	// or waits until this exact-byte commit completes; it cannot deadlock by
	// holding the file row while this transaction holds the target row.
	var sourceFileID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM files
		 WHERE id = $1 AND user_id = $2
		 FOR KEY SHARE
	`, target.FileID, ownerID).Scan(&sourceFileID); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTargetUnavailable
	} else if err != nil {
		return nil, fmt.Errorf("%w: lock target source: %v", ErrUnavailable, err)
	}

	var (
		buildID        uuid.UUID
		buildState     string
		dimension      int
		storedHash     string
		storedStage    string
		storedState    string
		storedToken    uuid.UUID
		leaseExpiresAt time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT build_id, output_dimension
		  FROM index_generations
		 WHERE workspace_id = $1 AND id = $2
	`, target.WorkspaceID, target.GenerationID).Scan(&buildID, &dimension)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTargetUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("%w: find target generation: %v", ErrUnavailable, err)
	}
	// Avoid a multi-relation FOR UPDATE whose executor lock order would be
	// plan-dependent. Every lifecycle path takes build before target.
	err = tx.QueryRow(ctx, `
		SELECT state FROM index_generation_builds
		 WHERE workspace_id = $1 AND id = $2
		 FOR UPDATE
	`, target.WorkspaceID, buildID).Scan(&buildState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTargetUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("%w: lock target build: %v", ErrUnavailable, err)
	}
	err = tx.QueryRow(ctx, `
		SELECT content_sha256, stage, state, attempt_token, lease_expires_at
		  FROM index_generation_targets
		 WHERE workspace_id = $1 AND generation_id = $2
		   AND file_id = $3 AND stage = $4
		 FOR UPDATE
	`, target.WorkspaceID, target.GenerationID, target.FileID,
		target.Stage).Scan(&storedHash, &storedStage, &storedState,
		&storedToken, &leaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTargetUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("%w: lock target: %v", ErrUnavailable, err)
	}
	if buildState != StateBuilding || storedState != TargetProcessing ||
		storedHash != target.ContentSHA256 || storedStage != target.Stage ||
		target.AttemptToken == uuid.Nil || storedToken != target.AttemptToken ||
		!leaseExpiresAt.After(s.now().UTC()) {
		return nil, ErrTargetUnavailable
	}
	for _, vector := range vectors {
		if len(vector.Values) != dimension {
			return nil, ErrDimensionMismatch
		}
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM index_generation_vectors
		 WHERE workspace_id = $1 AND generation_id = $2 AND file_id = $3
	`, target.WorkspaceID, target.GenerationID, target.FileID); err != nil {
		return nil, fmt.Errorf("%w: clear target vectors: %v", ErrUnavailable, err)
	}
	for _, vector := range vectors {
		if _, err := tx.Exec(ctx, `
			INSERT INTO index_generation_vectors (
				generation_id, workspace_id, file_id, ordinal,
				evidence_text, embedding
			) VALUES ($1,$2,$3,$4,$5,$6::vector)
		`, target.GenerationID, target.WorkspaceID, target.FileID,
			vector.Ordinal, vector.EvidenceText, vectorLiteral(vector.Values)); err != nil {
			return nil, fmt.Errorf("%w: insert target vector: %v", ErrUnavailable, err)
		}
	}
	now := s.now().UTC()
	var errorValue any
	if errorCode != "" {
		errorValue = errorCode
	}
	if _, err := tx.Exec(ctx, `
		UPDATE index_generation_targets
		   SET state = $1, error_code = $2, completed_at = $3, updated_at = $3,
		       attempt_token = NULL, lease_expires_at = NULL
		 WHERE workspace_id = $4 AND generation_id = $5
		   AND file_id = $6 AND stage = $7
	`, state, errorValue, now, target.WorkspaceID, target.GenerationID,
		target.FileID, target.Stage); err != nil {
		return nil, fmt.Errorf("%w: complete target: %v", ErrUnavailable, err)
	}
	counts, err := refreshProgress(ctx, tx, buildID, now)
	if err != nil {
		return nil, err
	}
	event := map[string]string{
		TargetSucceeded: "target_succeeded",
		TargetSkipped:   "target_skipped",
		TargetFailed:    "target_failed",
	}[state]
	if err := insertEvent(ctx, tx, buildID, target.WorkspaceID, nil,
		event, &storedState, &state, map[string]any{
			"generation_id": target.GenerationID, "file_id": target.FileID,
			"stage": target.Stage, "error_code": errorCode,
		}); err != nil {
		return nil, err
	}
	if counts.pending == 0 {
		if counts.failed == 0 {
			if _, err := tx.Exec(ctx, `
				UPDATE index_generation_builds
				   SET state = 'ready', ready_at = $2, updated_at = $2
				 WHERE id = $1 AND state = 'building'
			`, buildID, now); err != nil {
				return nil, fmt.Errorf("%w: mark build ready: %v", ErrUnavailable, err)
			}
			if _, err := tx.Exec(ctx, `
				UPDATE index_generations
				   SET state = 'ready', updated_at = $2
				 WHERE build_id = $1 AND state = 'building'
			`, buildID, now); err != nil {
				return nil, fmt.Errorf("%w: mark generations ready: %v", ErrUnavailable, err)
			}
			if err := insertEvent(ctx, tx, buildID, target.WorkspaceID, nil,
				"ready", stringPointer(StateBuilding), stringPointer(StateReady),
				map[string]any{"quality_gate": "all_targets"}); err != nil {
				return nil, err
			}
		} else {
			if _, err := tx.Exec(ctx, `
				UPDATE index_generation_builds
				   SET state = 'failed', failure_code = 'target_failures',
				       failed_at = $2, updated_at = $2
				 WHERE id = $1 AND state = 'building'
			`, buildID, now); err != nil {
				return nil, fmt.Errorf("%w: mark build failed: %v", ErrUnavailable, err)
			}
			if _, err := tx.Exec(ctx, `
				UPDATE index_generations
				   SET state = 'failed', updated_at = $2
				 WHERE build_id = $1 AND state = 'building'
			`, buildID, now); err != nil {
				return nil, fmt.Errorf("%w: mark generations failed: %v", ErrUnavailable, err)
			}
			if err := insertEvent(ctx, tx, buildID, target.WorkspaceID, nil,
				"failed", stringPointer(StateBuilding), stringPointer(StateFailed),
				map[string]any{"failure_code": "target_failures"}); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit target: %v", ErrUnavailable, err)
	}
	return s.Get(ctx, target.WorkspaceID, buildID)
}

type progressCounts struct{ pending, succeeded, skipped, failed int }

func refreshProgress(
	ctx context.Context,
	tx pgx.Tx,
	buildID uuid.UUID,
	now any,
) (progressCounts, error) {
	var counts progressCounts
	err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE t.state IN ('pending', 'processing')),
		       count(*) FILTER (WHERE t.state = 'succeeded'),
		       count(*) FILTER (WHERE t.state = 'skipped'),
		       count(*) FILTER (WHERE t.state = 'failed')
		  FROM index_generation_targets t
		  JOIN index_generations g ON g.id = t.generation_id
		 WHERE g.build_id = $1
	`, buildID).Scan(&counts.pending, &counts.succeeded, &counts.skipped,
		&counts.failed)
	if err != nil {
		return counts, fmt.Errorf("%w: count progress: %v", ErrUnavailable, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE index_generation_builds
		   SET succeeded_targets = $2, skipped_targets = $3,
		       failed_targets = $4, updated_at = $5
		 WHERE id = $1
	`, buildID, counts.succeeded, counts.skipped, counts.failed, now); err != nil {
		return counts, fmt.Errorf("%w: update progress: %v", ErrUnavailable, err)
	}
	return counts, nil
}

func (s *Service) activate(
	ctx context.Context,
	workspaceID, actorID, buildID uuid.UUID,
	rollback bool,
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
		return nil, fmt.Errorf("%w: begin activation: %v", ErrUnavailable, err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err := workspacelock.ForAIProfileCoordination(ctx, tx, workspaceID); err != nil {
		return nil, mapLockError(err)
	}
	build, err := scanBuild(tx.QueryRow(ctx, buildSelect+`
		WHERE b.workspace_id = $1 AND b.id = $2 FOR UPDATE`, workspaceID, buildID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: lock activation build: %v", ErrUnavailable, err)
	}
	fromState := build.State
	now := s.now().UTC()
	stateAllowed := build.State == StateReady
	if rollback {
		stateAllowed = build.State == StateInactive ||
			(build.State == StateDiscarded && build.RetentionUntil != nil &&
				build.RetentionUntil.After(now))
	}
	if !stateAllowed || build.FailedTargets != 0 ||
		build.SucceededTargets+build.SkippedTargets != build.RequiredTargets {
		return nil, ErrQualityGate
	}
	if _, ok := s.enabled[build.ProfileSnapshot.ID]; !ok {
		return nil, ErrProfileUnavailable
	}
	if err := validateProfileSnapshot(build.ProfileSnapshot); err != nil {
		return nil, ErrProfileUnavailable
	}
	var invalidVectors bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM index_generation_targets t
			  JOIN index_generations g ON g.id = t.generation_id
			 WHERE g.build_id = $1 AND t.state = 'succeeded'
			   AND NOT EXISTS (
			       SELECT 1 FROM index_generation_vectors v
			        WHERE v.generation_id = t.generation_id
			          AND v.file_id = t.file_id
			          AND vector_dims(v.embedding) = g.output_dimension
			   )
		)
	`, buildID).Scan(&invalidVectors); err != nil {
		return nil, fmt.Errorf("%w: validate activation vectors: %v", ErrUnavailable, err)
	}
	if invalidVectors {
		return nil, ErrQualityGate
	}
	var previousID *uuid.UUID
	var current uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM index_generation_builds
		 WHERE workspace_id = $1 AND state = 'active' FOR UPDATE
	`, workspaceID).Scan(&current); err == nil {
		previousID = &current
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: lock active build: %v", ErrUnavailable, err)
	}
	if rollback && previousID == nil {
		return nil, ErrInvalidTransition
	}
	if previousID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE index_generation_builds
			   SET state = 'inactive', updated_at = $2
			 WHERE id = $1 AND workspace_id = $3 AND state = 'active'
		`, *previousID, now, workspaceID); err != nil {
			return nil, fmt.Errorf("%w: deactivate current build: %v", ErrUnavailable, err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE index_generations
			   SET state = 'inactive', updated_at = $2
			 WHERE build_id = $1 AND workspace_id = $3 AND state = 'active'
		`, *previousID, now, workspaceID); err != nil {
			return nil, fmt.Errorf("%w: deactivate current generations: %v", ErrUnavailable, err)
		}
		if err := insertEvent(ctx, tx, *previousID, workspaceID, &actorID,
			"deactivated", stringPointer(StateActive), stringPointer(StateInactive),
			map[string]any{"replacement_build_id": buildID}); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE index_generation_builds
		   SET state = 'active', activated_at = $2, updated_at = $2
		 WHERE id = $1 AND workspace_id = $3
	`, buildID, now, workspaceID); err != nil {
		return nil, fmt.Errorf("%w: activate build: %v", ErrUnavailable, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE index_generations
		   SET state = 'active', updated_at = $2
		 WHERE build_id = $1 AND workspace_id = $3
	`, buildID, now, workspaceID); err != nil {
		return nil, fmt.Errorf("%w: activate generations: %v", ErrUnavailable, err)
	}
	if err := saveActiveProfile(ctx, tx, workspaceID, actorID, build.ProfileSnapshot, now); err != nil {
		return nil, err
	}
	eventType := "activated"
	if rollback {
		eventType = "rolled_back"
	}
	if err := insertEvent(ctx, tx, buildID, workspaceID, &actorID,
		eventType, &fromState, stringPointer(StateActive), map[string]any{
			"previous_build_id": previousID,
		}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit activation: %v", ErrUnavailable, err)
	}
	return s.Get(ctx, workspaceID, buildID)
}

func saveActiveProfile(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID, actorID uuid.UUID,
	d ProfileSnapshot,
	now any,
) error {
	optionalProvider := func(stage StageSnapshot) any {
		if !stage.Enabled {
			return nil
		}
		return stage.Provider
	}
	optionalDimension := func(stage StageSnapshot) any {
		if !stage.Enabled {
			return nil
		}
		return stage.Dimensions
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO workspace_ai_profiles (
			workspace_id, profile_id, profile_revision, pipeline_revision,
			embedding_provider, embedding_dimensions,
			visual_embedding_provider, visual_embedding_dimensions,
			llm_provider, vlm_provider, asr_provider, rerank_provider,
			data_egress, allowed_mime_types, selected_by_user_id,
			selected_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)
		ON CONFLICT (workspace_id) DO UPDATE SET
			profile_id = EXCLUDED.profile_id,
			profile_revision = EXCLUDED.profile_revision,
			pipeline_revision = EXCLUDED.pipeline_revision,
			embedding_provider = EXCLUDED.embedding_provider,
			embedding_dimensions = EXCLUDED.embedding_dimensions,
			visual_embedding_provider = EXCLUDED.visual_embedding_provider,
			visual_embedding_dimensions = EXCLUDED.visual_embedding_dimensions,
			llm_provider = EXCLUDED.llm_provider,
			vlm_provider = EXCLUDED.vlm_provider,
			asr_provider = EXCLUDED.asr_provider,
			rerank_provider = EXCLUDED.rerank_provider,
			data_egress = EXCLUDED.data_egress,
			allowed_mime_types = EXCLUDED.allowed_mime_types,
			selected_by_user_id = EXCLUDED.selected_by_user_id,
			selected_at = EXCLUDED.selected_at,
			updated_at = EXCLUDED.updated_at
	`, workspaceID, d.ID, d.Revision, d.PipelineRevision,
		d.Embedding.Provider, d.Embedding.Dimensions,
		optionalProvider(d.VisualEmbedding), optionalDimension(d.VisualEmbedding),
		optionalProvider(d.LLM), optionalProvider(d.VLM), optionalProvider(d.ASR),
		optionalProvider(d.Rerank), d.DataEgress, d.AllowedMIMETypes, actorID, now)
	if err != nil {
		return fmt.Errorf("%w: save activated profile: %v", ErrUnavailable, err)
	}
	return nil
}

func lockedBuildState(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID, buildID uuid.UUID,
) (string, error) {
	var state string
	err := tx.QueryRow(ctx, `
		SELECT state FROM index_generation_builds
		 WHERE workspace_id = $1 AND id = $2 FOR UPDATE
	`, workspaceID, buildID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("%w: lock build: %v", ErrUnavailable, err)
	}
	return state, nil
}

func insertEvent(
	ctx context.Context,
	tx pgx.Tx,
	buildID, workspaceID uuid.UUID,
	actorID *uuid.UUID,
	eventType string,
	from, to *string,
	details map[string]any,
) error {
	if details == nil {
		details = map[string]any{}
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("%w: encode event: %v", ErrUnavailable, err)
	}
	if len(encoded) > 4096 {
		return fmt.Errorf("%w: event details too large", ErrUnavailable)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO index_generation_events (
			build_id, workspace_id, actor_user_id, event_type,
			from_state, to_state, details
		) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb)
	`, buildID, workspaceID, actorID, eventType, from, to, encoded); err != nil {
		return fmt.Errorf("%w: insert event: %v", ErrUnavailable, err)
	}
	return nil
}

func mapLockError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("%w: lock workspace: %v", ErrUnavailable, err)
}

func vectorLiteral(values []float32) string {
	var b strings.Builder
	b.Grow(len(values)*8 + 2)
	b.WriteByte('[')
	for i, value := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%g", value)
	}
	b.WriteByte(']')
	return b.String()
}

var errorCodePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

func safeErrorCode(value string) bool { return errorCodePattern.MatchString(value) }

func stringPointer(value string) *string { return &value }

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

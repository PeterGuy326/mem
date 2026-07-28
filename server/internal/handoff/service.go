package handoff

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/workspacelock"
)

// Service persists immutable versioned checkpoints and resolves deterministic
// resume snapshots. It never calls an indexing Worker, embedding model, or
// answer-generating model.
type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

const checkpointColumns = `
	c.id,
	c.workspace_id,
	c.task_id,
	t.task_key,
	c.sequence,
	c.checkpoint_kind,
	c.contract_name,
	c.schema_version,
	c.base_checkpoint_id,
	c.scope_path,
	c.payload,
	c.payload_sha256,
	c.created_by_user_id,
	c.created_by_token_id,
	c.producer_agent,
	c.producer_session,
	c.created_at,
	c.idempotency_key,
	c.request_sha256`

const checkpointSummaryColumns = `
	c.id,
	c.workspace_id,
	c.task_id,
	t.task_key,
	c.sequence,
	c.checkpoint_kind,
	c.contract_name,
	c.schema_version,
	c.base_checkpoint_id,
	c.scope_path,
	c.payload #>> '{state,status}',
	left(c.payload #>> '{state,progress,summary}', 500),
	char_length(c.payload #>> '{state,progress,summary}'),
	jsonb_array_length(c.payload #> '{state,progress,completed}'),
	(
		SELECT count(*)::integer
		  FROM task_checkpoint_refs r
		 WHERE r.checkpoint_id = c.id
	),
	c.payload_sha256,
	c.producer_agent,
	c.producer_session,
	c.created_at`

// Checkpoint atomically creates or replays one checkpoint.
//
// The agent_tasks row is locked before checking and advancing the head, making
// BaseCheckpointID a database-backed compare-and-swap precondition. Replays are
// checked before the head precondition, so retrying a committed request still
// succeeds after newer checkpoints have advanced the task.
func (s *Service) Checkpoint(
	ctx context.Context,
	cmd CheckpointCommand,
) (*CheckpointResult, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("handoff service is not configured")
	}
	normalized, err := normalizeCheckpointCommand(cmd)
	if err != nil {
		return nil, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin checkpoint transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := workspacelock.ForContentWrite(ctx, tx, normalized.WorkspaceID); err != nil {
		return nil, err
	}

	if existing, ok, err := findIdempotentCheckpoint(
		ctx,
		tx,
		normalized.WorkspaceID,
		normalized.IdempotencyKey,
	); err != nil {
		return nil, err
	} else if ok {
		return commitReplay(ctx, tx, existing, normalized.requestSHA256)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_tasks (workspace_id, task_key, scope_path)
		VALUES ($1, $2, $3)
		ON CONFLICT (workspace_id, task_key) DO NOTHING
	`, normalized.WorkspaceID, normalized.TaskKey, normalized.Handoff.ScopePath); err != nil {
		return nil, fmt.Errorf("ensure agent task: %w", err)
	}

	task, err := lockTask(ctx, tx, normalized.WorkspaceID, normalized.TaskKey)
	if err != nil {
		return nil, err
	}

	// A concurrent writer can commit while INSERT ... ON CONFLICT waits. Check
	// the idempotency key again after acquiring the per-task lock.
	if existing, ok, err := findIdempotentCheckpoint(
		ctx,
		tx,
		normalized.WorkspaceID,
		normalized.IdempotencyKey,
	); err != nil {
		return nil, err
	} else if ok {
		return commitReplay(ctx, tx, existing, normalized.requestSHA256)
	}

	if task.ScopePath != normalized.Handoff.ScopePath {
		return nil, invalid(
			"scope_path %q does not match existing task scope %q",
			normalized.Handoff.ScopePath,
			task.ScopePath,
		)
	}
	if err := requireCurrentBase(task, normalized.Handoff.BaseCheckpointID); err != nil {
		return nil, err
	}

	checkpointID := uuid.New()
	sequence := task.HeadSequence + 1
	var createdAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO task_checkpoints (
			id,
			workspace_id,
			task_id,
			sequence,
			checkpoint_kind,
			contract_name,
			schema_version,
			base_checkpoint_id,
			scope_path,
			payload,
			payload_sha256,
			request_sha256,
			idempotency_key,
			created_by_user_id,
			created_by_token_id,
			producer_agent,
			producer_session
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12,
			$13, $14, $15, $16, $17
		)
		ON CONFLICT (workspace_id, idempotency_key) DO NOTHING
		RETURNING created_at
	`,
		checkpointID,
		normalized.WorkspaceID,
		task.ID,
		sequence,
		normalized.Handoff.CheckpointKind,
		normalized.Handoff.Contract,
		normalized.Handoff.SchemaVersion,
		normalized.Handoff.BaseCheckpointID,
		normalized.Handoff.ScopePath,
		string(normalized.payload),
		normalized.payloadSHA256,
		normalized.requestSHA256,
		normalized.IdempotencyKey,
		normalized.CreatedByUserID,
		normalized.CreatedByTokenID,
		normalized.Handoff.Producer.AgentID,
		normalized.Handoff.Producer.SessionID,
	).Scan(&createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, ok, loadErr := findIdempotentCheckpoint(
			ctx,
			tx,
			normalized.WorkspaceID,
			normalized.IdempotencyKey,
		)
		if loadErr != nil {
			return nil, loadErr
		}
		if !ok {
			return nil, fmt.Errorf("idempotent checkpoint disappeared")
		}
		return commitReplay(ctx, tx, existing, normalized.requestSHA256)
	}
	if err != nil {
		return nil, fmt.Errorf("insert task checkpoint: %w", err)
	}

	for i := range normalized.references {
		ref := &normalized.references[i]
		ref.CheckpointID = checkpointID
		if _, err := tx.Exec(ctx, `
			INSERT INTO task_checkpoint_refs (
				checkpoint_id,
				ordinal,
				relation,
				uri,
				expected_sha256,
				required,
				metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
		`,
			ref.CheckpointID,
			ref.Ordinal,
			ref.Relation,
			ref.URI,
			ref.ExpectedSHA256,
			ref.Required,
			string(ref.Metadata),
		); err != nil {
			return nil, fmt.Errorf("insert checkpoint reference %d: %w", ref.Ordinal, err)
		}
	}

	tag, err := tx.Exec(ctx, `
		UPDATE agent_tasks
		   SET head_checkpoint_id = $1,
		       head_sequence = $2,
		       updated_at = now()
		 WHERE id = $3
		   AND workspace_id = $4
		   AND head_sequence = $5
		   AND (
		       ($6::uuid IS NULL AND head_checkpoint_id IS NULL)
		       OR head_checkpoint_id = $6
		   )
	`,
		checkpointID,
		sequence,
		task.ID,
		normalized.WorkspaceID,
		task.HeadSequence,
		normalized.Handoff.BaseCheckpointID,
	)
	if err != nil {
		return nil, fmt.Errorf("advance task head: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrHeadConflict
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit checkpoint: %w", err)
	}
	record := CheckpointRecord{
		ID:               checkpointID,
		WorkspaceID:      normalized.WorkspaceID,
		TaskID:           task.ID,
		TaskKey:          normalized.TaskKey,
		Sequence:         sequence,
		CheckpointKind:   normalized.Handoff.CheckpointKind,
		Contract:         normalized.Handoff.Contract,
		SchemaVersion:    normalized.Handoff.SchemaVersion,
		BaseCheckpointID: cloneUUID(normalized.Handoff.BaseCheckpointID),
		ScopePath:        normalized.Handoff.ScopePath,
		Handoff:          normalized.Handoff,
		PayloadSHA256:    normalized.payloadSHA256,
		CreatedByUserID:  cloneUUID(normalized.CreatedByUserID),
		CreatedByTokenID: cloneUUID(normalized.CreatedByTokenID),
		ProducerAgent:    normalized.Handoff.Producer.AgentID,
		ProducerSession:  normalized.Handoff.Producer.SessionID,
		CreatedAt:        createdAt.UTC(),
		References:       cloneReferences(normalized.references),
		IdempotencyKey:   normalized.IdempotencyKey,
		RequestSHA256:    normalized.requestSHA256,
	}
	return &CheckpointResult{Checkpoint: record}, nil
}

func commitReplay(
	ctx context.Context,
	tx pgx.Tx,
	existing *CheckpointRecord,
	requestSHA256 string,
) (*CheckpointResult, error) {
	if existing.RequestSHA256 != requestSHA256 {
		return nil, ErrIdempotencyConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit checkpoint replay: %w", err)
	}
	return &CheckpointResult{Checkpoint: *existing, Replayed: true}, nil
}

func requireCurrentBase(task Task, base *uuid.UUID) error {
	if task.HeadCheckpointID == nil {
		if base != nil {
			return ErrHeadConflict
		}
		return nil
	}
	if base == nil {
		return ErrBaseRequired
	}
	if *base != *task.HeadCheckpointID {
		return ErrHeadConflict
	}
	return nil
}

// Resume returns the selected immutable checkpoint, or the current task head
// when CheckpointID is nil, after workspace, scope, and allowed-path filters.
func (s *Service) Resume(
	ctx context.Context,
	q ResumeQuery,
) (*ResumeSnapshot, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("handoff service is not configured")
	}
	if q.WorkspaceID == uuid.Nil {
		return nil, invalid("workspace_id is required")
	}
	q.TaskKey = strings.TrimSpace(q.TaskKey)
	if err := validateRequiredText("task_key", q.TaskKey, maxTaskKeyRunes); err != nil {
		return nil, err
	}
	if q.CheckpointID != nil && *q.CheckpointID == uuid.Nil {
		return nil, invalid("checkpoint_id must be a non-zero UUID")
	}
	if q.Limit < 0 {
		return nil, invalid("limit must not be negative")
	}
	if q.MaxChars < 0 {
		return nil, invalid("max_chars must not be negative")
	}
	scope, allowed, err := normalizeReadPaths(q.Scope, q.AllowedPaths)
	if err != nil {
		return nil, err
	}

	task, err := getTask(ctx, s.pool, q.WorkspaceID, q.TaskKey, scope, allowed, false)
	if err != nil {
		return nil, err
	}
	checkpointID := task.HeadCheckpointID
	if q.CheckpointID != nil {
		checkpointID = q.CheckpointID
	}
	if checkpointID == nil {
		return nil, ErrNotFound
	}

	record, err := getCheckpoint(
		ctx,
		s.pool,
		q.WorkspaceID,
		task.ID,
		*checkpointID,
		scope,
		allowed,
	)
	if err != nil {
		return nil, err
	}
	return &ResumeSnapshot{
		Contract:      ResumeContractName,
		SchemaVersion: SchemaVersionV1,
		Task:          task,
		Checkpoint:    *record,
		References:    cloneReferences(record.References),
		RetrievedAt:   time.Now().UTC(),
	}, nil
}

func (s *Service) GetCheckpoint(
	ctx context.Context,
	q GetCheckpointQuery,
) (*CheckpointRecord, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("handoff service is not configured")
	}
	if q.WorkspaceID == uuid.Nil {
		return nil, invalid("workspace_id is required")
	}
	if q.CheckpointID == uuid.Nil {
		return nil, invalid("checkpoint_id is required")
	}
	q.TaskKey = strings.TrimSpace(q.TaskKey)
	if q.TaskKey != "" {
		if err := validateRequiredText("task_key", q.TaskKey, maxTaskKeyRunes); err != nil {
			return nil, err
		}
	}
	scope, allowed, err := normalizeReadPaths(q.Scope, q.AllowedPaths)
	if err != nil {
		return nil, err
	}

	args := []any{q.WorkspaceID, q.CheckpointID}
	where := []string{"c.workspace_id = $1", "c.id = $2"}
	if q.TaskKey != "" {
		args = append(args, q.TaskKey)
		where = append(where, fmt.Sprintf("t.task_key = $%d", len(args)))
	}
	args, where = appendPathFilters(args, where, "c.scope_path", scope, allowed)
	record, err := scanCheckpoint(s.pool.QueryRow(ctx, `
		SELECT `+checkpointColumns+`
		  FROM task_checkpoints c
		  JOIN agent_tasks t ON t.id = c.task_id
		 WHERE `+strings.Join(where, " AND "), args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get checkpoint: %w", err)
	}
	refs, err := loadReferences(ctx, s.pool, record.ID)
	if err != nil {
		return nil, err
	}
	record.References = refs
	return &record, nil
}

func (s *Service) ListTasks(
	ctx context.Context,
	q ListTasksQuery,
) ([]Task, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("handoff service is not configured")
	}
	if q.WorkspaceID == uuid.Nil {
		return nil, invalid("workspace_id is required")
	}
	scope, allowed, err := normalizeReadPaths(q.Scope, q.AllowedPaths)
	if err != nil {
		return nil, err
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 200 {
		q.Limit = 200
	}
	args := []any{q.WorkspaceID}
	where := []string{"workspace_id = $1"}
	args, where = appendPathFilters(args, where, "scope_path", scope, allowed)
	if q.After != nil {
		if *q.After == uuid.Nil {
			return nil, invalid("after must be a non-zero UUID")
		}
		args = append(args, *q.After)
		where = append(where, fmt.Sprintf("id > $%d", len(args)))
	}
	args = append(args, q.Limit)
	rows, err := s.pool.Query(ctx, `
		SELECT id, workspace_id, task_key, scope_path, head_checkpoint_id,
		       head_sequence, created_at, updated_at
		  FROM agent_tasks
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY id
		 LIMIT $`+fmt.Sprintf("%d", len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("list agent tasks: %w", err)
	}
	defer rows.Close()
	out := make([]Task, 0, q.Limit)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent task: %w", err)
		}
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent tasks: %w", err)
	}
	return out, nil
}

func (s *Service) ListCheckpoints(
	ctx context.Context,
	q ListCheckpointsQuery,
) ([]CheckpointSummary, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("handoff service is not configured")
	}
	if q.WorkspaceID == uuid.Nil {
		return nil, invalid("workspace_id is required")
	}
	q.TaskKey = strings.TrimSpace(q.TaskKey)
	if err := validateRequiredText("task_key", q.TaskKey, maxTaskKeyRunes); err != nil {
		return nil, err
	}
	scope, allowed, err := normalizeReadPaths(q.Scope, q.AllowedPaths)
	if err != nil {
		return nil, err
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 200 {
		q.Limit = 200
	}
	args := []any{q.WorkspaceID, q.TaskKey}
	where := []string{"c.workspace_id = $1", "t.task_key = $2"}
	args, where = appendPathFilters(args, where, "c.scope_path", scope, allowed)
	if q.Before != nil {
		if *q.Before <= 0 {
			return nil, invalid("before must be greater than zero")
		}
		args = append(args, *q.Before)
		where = append(where, fmt.Sprintf("c.sequence < $%d", len(args)))
	}
	args = append(args, q.Limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+checkpointSummaryColumns+`
		  FROM task_checkpoints c
		  JOIN agent_tasks t ON t.id = c.task_id
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY c.sequence DESC
		 LIMIT $`+fmt.Sprintf("%d", len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("list task checkpoints: %w", err)
	}
	defer rows.Close()
	out := make([]CheckpointSummary, 0, q.Limit)
	for rows.Next() {
		record, err := scanCheckpointSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task checkpoint summary: %w", err)
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task checkpoints: %w", err)
	}
	return out, nil
}

func findIdempotentCheckpoint(
	ctx context.Context,
	q queryer,
	workspaceID uuid.UUID,
	key string,
) (*CheckpointRecord, bool, error) {
	record, err := scanCheckpoint(q.QueryRow(ctx, `
		SELECT `+checkpointColumns+`
		  FROM task_checkpoints c
		  JOIN agent_tasks t ON t.id = c.task_id
		 WHERE c.workspace_id = $1
		   AND c.idempotency_key = $2
	`, workspaceID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load idempotent checkpoint: %w", err)
	}
	refs, err := loadReferences(ctx, q, record.ID)
	if err != nil {
		return nil, false, err
	}
	record.References = refs
	return &record, true, nil
}

func lockTask(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
	taskKey string,
) (Task, error) {
	task, err := scanTask(tx.QueryRow(ctx, `
		SELECT id, workspace_id, task_key, scope_path, head_checkpoint_id,
		       head_sequence, created_at, updated_at
		  FROM agent_tasks
		 WHERE workspace_id = $1
		   AND task_key = $2
		 FOR UPDATE
	`, workspaceID, taskKey))
	if err != nil {
		return Task{}, fmt.Errorf("lock agent task: %w", err)
	}
	return task, nil
}

func getTask(
	ctx context.Context,
	q queryer,
	workspaceID uuid.UUID,
	taskKey string,
	scope string,
	allowed []string,
	forUpdate bool,
) (Task, error) {
	args := []any{workspaceID, taskKey}
	where := []string{"workspace_id = $1", "task_key = $2"}
	args, where = appendPathFilters(args, where, "scope_path", scope, allowed)
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	task, err := scanTask(q.QueryRow(ctx, `
		SELECT id, workspace_id, task_key, scope_path, head_checkpoint_id,
		       head_sequence, created_at, updated_at
		  FROM agent_tasks
		 WHERE `+strings.Join(where, " AND ")+suffix, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("get agent task: %w", err)
	}
	return task, nil
}

func getCheckpoint(
	ctx context.Context,
	q queryer,
	workspaceID uuid.UUID,
	taskID uuid.UUID,
	checkpointID uuid.UUID,
	scope string,
	allowed []string,
) (*CheckpointRecord, error) {
	args := []any{workspaceID, taskID, checkpointID}
	where := []string{
		"c.workspace_id = $1",
		"c.task_id = $2",
		"c.id = $3",
	}
	args, where = appendPathFilters(args, where, "c.scope_path", scope, allowed)
	record, err := scanCheckpoint(q.QueryRow(ctx, `
		SELECT `+checkpointColumns+`
		  FROM task_checkpoints c
		  JOIN agent_tasks t ON t.id = c.task_id
		 WHERE `+strings.Join(where, " AND "), args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get task checkpoint: %w", err)
	}
	refs, err := loadReferences(ctx, q, record.ID)
	if err != nil {
		return nil, err
	}
	record.References = refs
	return &record, nil
}

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type rowScanner interface {
	Scan(...any) error
}

func scanTask(row rowScanner) (Task, error) {
	var task Task
	err := row.Scan(
		&task.ID,
		&task.WorkspaceID,
		&task.TaskKey,
		&task.ScopePath,
		&task.HeadCheckpointID,
		&task.HeadSequence,
		&task.CreatedAt,
		&task.UpdatedAt,
	)
	if err == nil {
		task.CreatedAt = task.CreatedAt.UTC()
		task.UpdatedAt = task.UpdatedAt.UTC()
	}
	return task, err
}

func scanCheckpoint(row rowScanner) (CheckpointRecord, error) {
	var (
		record  CheckpointRecord
		payload []byte
	)
	err := row.Scan(
		&record.ID,
		&record.WorkspaceID,
		&record.TaskID,
		&record.TaskKey,
		&record.Sequence,
		&record.CheckpointKind,
		&record.Contract,
		&record.SchemaVersion,
		&record.BaseCheckpointID,
		&record.ScopePath,
		&payload,
		&record.PayloadSHA256,
		&record.CreatedByUserID,
		&record.CreatedByTokenID,
		&record.ProducerAgent,
		&record.ProducerSession,
		&record.CreatedAt,
		&record.IdempotencyKey,
		&record.RequestSHA256,
	)
	if err != nil {
		return CheckpointRecord{}, err
	}
	if record.SchemaVersion != SchemaVersionV1 {
		return CheckpointRecord{}, fmt.Errorf(
			"%w: stored schema_version %d",
			ErrUnsupportedVersion,
			record.SchemaVersion,
		)
	}
	handoff, err := DecodeV1(payload)
	if err != nil {
		return CheckpointRecord{}, fmt.Errorf("decode stored handoff payload: %v", err)
	}
	handoff, err = NormalizeV1(handoff, record.TaskKey)
	if err != nil {
		return CheckpointRecord{}, fmt.Errorf("validate stored handoff payload: %v", err)
	}
	if handoff.Contract != record.Contract ||
		handoff.SchemaVersion != record.SchemaVersion ||
		handoff.CheckpointKind != record.CheckpointKind ||
		handoff.ScopePath != record.ScopePath ||
		!sameUUID(handoff.BaseCheckpointID, record.BaseCheckpointID) ||
		handoff.Producer.AgentID != record.ProducerAgent ||
		handoff.Producer.SessionID != record.ProducerSession {
		return CheckpointRecord{}, fmt.Errorf("stored handoff columns disagree with payload")
	}
	canonical, err := json.Marshal(handoff)
	if err != nil {
		return CheckpointRecord{}, fmt.Errorf("canonicalize stored handoff payload: %w", err)
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != record.PayloadSHA256 {
		return CheckpointRecord{}, fmt.Errorf("stored handoff payload hash mismatch")
	}
	record.Handoff = handoff
	record.CreatedAt = record.CreatedAt.UTC()
	record.References = []Reference{}
	return record, nil
}

func scanCheckpointSummary(row rowScanner) (CheckpointSummary, error) {
	var summary CheckpointSummary
	err := row.Scan(
		&summary.ID,
		&summary.WorkspaceID,
		&summary.TaskID,
		&summary.TaskKey,
		&summary.Sequence,
		&summary.CheckpointKind,
		&summary.Contract,
		&summary.SchemaVersion,
		&summary.BaseCheckpointID,
		&summary.ScopePath,
		&summary.Status,
		&summary.ProgressExcerpt,
		&summary.ProgressLength,
		&summary.CompletedCount,
		&summary.ReferenceCount,
		&summary.PayloadSHA256,
		&summary.ProducerAgent,
		&summary.ProducerSession,
		&summary.CreatedAt,
	)
	if err != nil {
		return CheckpointSummary{}, err
	}
	if summary.SchemaVersion != SchemaVersionV1 {
		return CheckpointSummary{}, fmt.Errorf(
			"%w: stored schema_version %d",
			ErrUnsupportedVersion,
			summary.SchemaVersion,
		)
	}
	summary.CreatedAt = summary.CreatedAt.UTC()
	return summary, nil
}

func loadReferences(
	ctx context.Context,
	q queryer,
	checkpointID uuid.UUID,
) ([]Reference, error) {
	rows, err := q.Query(ctx, `
		SELECT checkpoint_id, ordinal, relation, uri, expected_sha256,
		       required, metadata
		  FROM task_checkpoint_refs
		 WHERE checkpoint_id = $1
		 ORDER BY ordinal
	`, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("load checkpoint references: %w", err)
	}
	defer rows.Close()
	out := make([]Reference, 0)
	for rows.Next() {
		var (
			ref      Reference
			metadata []byte
		)
		if err := rows.Scan(
			&ref.CheckpointID,
			&ref.Ordinal,
			&ref.Relation,
			&ref.URI,
			&ref.ExpectedSHA256,
			&ref.Required,
			&metadata,
		); err != nil {
			return nil, fmt.Errorf("scan checkpoint reference: %w", err)
		}
		ref.Metadata = append(ref.Metadata[:0], metadata...)
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate checkpoint references: %w", err)
	}
	return out, nil
}

func normalizeReadPaths(scopeRaw string, allowedRaw []string) (string, []string, error) {
	scope, err := normalizeQueryScope(scopeRaw)
	if err != nil {
		return "", nil, err
	}
	allowed, err := normalizeAllowedPaths(allowedRaw)
	if err != nil {
		return "", nil, err
	}
	return scope, allowed, nil
}

func appendPathFilters(
	args []any,
	where []string,
	column string,
	scope string,
	allowed []string,
) ([]any, []string) {
	if scope != "" && scope != "/" {
		args = append(args, scope)
		where = append(where, descendantSQL(column, len(args)))
	}
	if len(allowed) == 0 || (len(allowed) == 1 && allowed[0] == "/") {
		return args, where
	}
	clauses := make([]string, 0, len(allowed))
	for _, path := range allowed {
		args = append(args, path)
		clauses = append(clauses, descendantSQL(column, len(args)))
	}
	where = append(where, "("+strings.Join(clauses, " OR ")+")")
	return args, where
}

func descendantSQL(column string, argument int) string {
	return fmt.Sprintf(
		"(%s = $%d OR left(%s, char_length($%d) + 1) = $%d || '/')",
		column,
		argument,
		column,
		argument,
		argument,
	)
}

func sameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func cloneUUID(in *uuid.UUID) *uuid.UUID {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneReferences(in []Reference) []Reference {
	if in == nil {
		return nil
	}
	out := make([]Reference, len(in))
	copy(out, in)
	for i := range out {
		out[i].Metadata = append(json.RawMessage(nil), in[i].Metadata...)
	}
	return out
}

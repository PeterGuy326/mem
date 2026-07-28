package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/PeterGuy326/mem/server/internal/workspacelock"
)

const memoryColumnNames = `
	id,
	workspace_id,
	created_by_user_id,
	created_by_token_id,
	kind,
	content,
	attributes,
	path,
	event_at,
	source_type,
	source_ref,
	source_file_id,
	source_file_sha256,
	source_locator,
	producer_agent,
	producer_session,
	producer_task,
	idempotency_key_sha256,
		request_sha256,
		content_sha256,
		lifecycle_status,
		state_version,
		pinned_at,
		useful_count,
		not_useful_count,
		feedback_at,
		forgotten_at,
		forgotten_by_user_id,
		forgotten_by_token_id,
		created_at,
		updated_at`

// Remember atomically creates or replays one memory occurrence.
//
// PostgreSQL's unique-index arbitration makes the no-row insert path wait for
// a concurrent writer of the same key. The following statement therefore sees
// the committed winner under READ COMMITTED and can compare request hashes
// without a check-then-insert race.
func (s *Service) Remember(ctx context.Context, cmd Command) (*RememberResult, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("memory service is not configured")
	}
	normalized, err := normalizeCommand(cmd)
	if err != nil {
		return nil, err
	}
	allowed, err := normalizeAllowedPaths(cmd.AllowedPaths)
	if err != nil {
		return nil, err
	}
	if !pathAllowed(normalized.Path, allowed) {
		return nil, ErrNotFound
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin remember transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := workspacelock.ForContentWrite(ctx, tx, normalized.WorkspaceID); err != nil {
		return nil, err
	}

	insertSQL := `
		INSERT INTO memories (
			workspace_id,
			created_by_user_id,
			created_by_token_id,
			kind,
			content,
			attributes,
			path,
			event_at,
			source_type,
			source_ref,
			source_file_id,
			source_file_sha256,
			source_locator,
			producer_agent,
			producer_session,
			producer_task,
			idempotency_key_sha256,
			request_sha256,
			content_sha256
		)
		VALUES (
			$1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10,
			$11, $12, $13::jsonb, $14, $15, $16, $17, $18, $19
		)
		ON CONFLICT (workspace_id, idempotency_key_sha256) DO NOTHING
		RETURNING ` + memoryColumnNames

	record, err := scanMemory(tx.QueryRow(ctx, insertSQL,
		normalized.WorkspaceID,
		normalized.CreatedByUserID,
		normalized.CreatedByTokenID,
		normalized.Kind,
		normalized.Content,
		string(normalized.Attributes),
		normalized.Path,
		normalized.EventAt,
		normalized.SourceType,
		normalized.SourceRef,
		normalized.SourceFileID,
		normalized.SourceFileSHA256,
		string(normalized.SourceLocator),
		normalized.ProducerAgent,
		normalized.ProducerSession,
		normalized.ProducerTask,
		normalized.idempotencyKeySHA256,
		normalized.requestSHA256,
		normalized.contentSHA256,
	))
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit memory: %w", err)
		}
		return &RememberResult{Memory: record, Replayed: false}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("insert memory: %w", err)
	}

	args := []any{normalized.WorkspaceID, normalized.idempotencyKeySHA256}
	where := []string{"m.workspace_id = $1", "m.idempotency_key_sha256 = $2"}
	args, where = appendPathFilters(args, where, "m.path", "/", allowed)
	record, err = scanMemory(tx.QueryRow(ctx, `
			SELECT `+qualifyColumns("m")+`
			  FROM memories m
			 WHERE `+strings.Join(where, " AND ")+`
			 FOR SHARE`, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load idempotent memory: %w", err)
	}
	if record.LifecycleStatus == StatusForgotten {
		return nil, ErrForgotten
	}
	if record.RequestSHA256 != normalized.requestSHA256 {
		return nil, ErrIdempotencyConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit memory replay: %w", err)
	}
	return &RememberResult{Memory: record, Replayed: true}, nil
}

// Get resolves one record with workspace and segment-safe path filters.
func (s *Service) Get(ctx context.Context, q Query) (*Memory, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("memory service is not configured")
	}
	if q.WorkspaceID == uuid.Nil {
		return nil, invalid("workspace_id is required")
	}
	if q.MemoryID == uuid.Nil {
		return nil, invalid("memory_id is required")
	}
	scope, err := normalizeScope(q.Scope)
	if err != nil {
		return nil, err
	}
	allowed, err := normalizeAllowedPaths(q.AllowedPaths)
	if err != nil {
		return nil, err
	}

	args := []any{q.WorkspaceID, q.MemoryID}
	where := []string{"m.workspace_id = $1", "m.id = $2"}
	args, where = appendPathFilters(args, where, "m.path", scope, allowed)

	record, err := scanMemory(s.pool.QueryRow(ctx, `
		SELECT `+qualifyColumns("m")+`
		  FROM memories m
		 WHERE `+strings.Join(where, " AND "), args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get memory: %w", err)
	}
	if record.LifecycleStatus == StatusForgotten {
		return nil, ErrForgotten
	}
	return &record, nil
}

// Recall returns deterministic lexical matches after applying every
// authorization and metadata filter inside PostgreSQL.
func (s *Service) Recall(ctx context.Context, q RecallQuery) ([]RecallHit, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("memory service is not configured")
	}
	if q.WorkspaceID == uuid.Nil {
		return nil, invalid("workspace_id is required")
	}
	q.Text = strings.TrimSpace(q.Text)
	if q.Text == "" {
		return nil, invalid("recall text must not be empty")
	}
	if !utf8.ValidString(q.Text) {
		return nil, invalid("recall text must be valid UTF-8")
	}
	if len([]byte(q.Text)) > maxContentBytes {
		return nil, invalid("recall text exceeds 65536 bytes")
	}
	scope, err := normalizeScope(q.Scope)
	if err != nil {
		return nil, err
	}
	allowed, err := normalizeAllowedPaths(q.AllowedPaths)
	if err != nil {
		return nil, err
	}
	status := strings.ToLower(strings.TrimSpace(q.LifecycleStatus))
	if status == "" {
		status = StatusActive
	}
	if status != StatusActive {
		return nil, invalid("recall only returns active memories")
	}
	kinds, err := normalizeKinds(q.Kinds)
	if err != nil {
		return nil, err
	}
	if q.Since != nil && q.Until != nil && q.Since.After(*q.Until) {
		return nil, invalid("since must be before or equal to until")
	}
	if q.Limit <= 0 {
		q.Limit = 10
	}
	if q.Limit > 100 {
		q.Limit = 100
	}

	args := []any{q.WorkspaceID, status}
	where := []string{"m.workspace_id = $1", "m.lifecycle_status = $2"}
	args, where = appendPathFilters(args, where, "m.path", scope, allowed)
	if q.Since != nil {
		args = append(args, q.Since.UTC())
		where = append(where, fmt.Sprintf(
			"COALESCE(m.event_at, m.created_at) >= $%d", len(args),
		))
	}
	if q.Until != nil {
		args = append(args, q.Until.UTC())
		where = append(where, fmt.Sprintf(
			"COALESCE(m.event_at, m.created_at) <= $%d", len(args),
		))
	}
	if len(kinds) > 0 {
		args = append(args, kinds)
		where = append(where, fmt.Sprintf("m.kind = ANY($%d::text[])", len(args)))
	}
	args = append(args, q.Text)
	textArg := len(args)
	args = append(args, q.Limit)
	limitArg := len(args)

	sql := fmt.Sprintf(`
		WITH candidates AS (
			SELECT %s,
			       lower(m.content) = lower($%d) AS exact_equal,
			       strpos(lower(m.content), lower($%d)) > 0 AS exact_phrase,
			       m.search_tsv @@ plainto_tsquery('simple', $%d) AS fts_match,
			       ts_rank_cd(
			           m.search_tsv,
			           plainto_tsquery('simple', $%d)
			       )::double precision AS fts_rank,
			       word_similarity(
			           lower($%d),
			           lower(m.content)
			       )::double precision AS trigram_score
			  FROM memories m
			 WHERE %s
		),
		ranked AS (
			SELECT candidates.*,
				       CASE
				           WHEN exact_equal THEN 1.0::double precision
				           WHEN exact_phrase THEN
				               0.95::double precision
				               + CASE WHEN pinned_at IS NOT NULL
				                      THEN 0.01::double precision
				                      ELSE 0.0::double precision END
				           WHEN fts_match THEN LEAST(
				               0.949::double precision,
				               0.70::double precision + 0.24::double precision * fts_rank
				               + CASE WHEN pinned_at IS NOT NULL
				                      THEN 0.009::double precision
				                      ELSE 0.0::double precision END
				           )
				           ELSE LEAST(
				               0.699::double precision,
				               0.20::double precision + 0.49::double precision * trigram_score
				               + CASE WHEN pinned_at IS NOT NULL
				                      THEN 0.009::double precision
				                      ELSE 0.0::double precision END
				           )
				       END AS recall_score,
				       (CASE
				           WHEN exact_phrase THEN 'exact'
				           WHEN fts_match THEN 'fts'
				           ELSE 'trigram'
				       END
				       || CASE WHEN pinned_at IS NOT NULL
				               THEN '+pinned'
				               ELSE '' END) AS recall_reason
			  FROM candidates
			 WHERE exact_phrase
			    OR fts_match
			    OR trigram_score >= 0.12
		)
		SELECT %s, recall_score, recall_reason
		  FROM ranked r
		 ORDER BY recall_score DESC,
		          COALESCE(r.event_at, r.created_at) DESC,
		          r.id
		 LIMIT $%d
	`,
		qualifyColumns("m"),
		textArg, textArg, textArg, textArg, textArg,
		strings.Join(where, " AND "),
		qualifyColumns("r"),
		limitArg,
	)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("recall memories: %w", err)
	}
	defer rows.Close()

	hits := make([]RecallHit, 0, q.Limit)
	for rows.Next() {
		var (
			record Memory
			score  float64
			reason string
		)
		record, err = scanMemoryAnd(rows, &score, &reason)
		if err != nil {
			return nil, fmt.Errorf("scan recalled memory: %w", err)
		}
		hits = append(hits, RecallHit{
			Memory:     record,
			Citation:   record.Citation(),
			Reason:     reason,
			Score:      score,
			Provenance: record.Provenance(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recalled memories: %w", err)
	}
	return hits, nil
}

func normalizeKinds(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(raw))
	kinds := make([]string, 0, len(raw))
	for _, kind := range raw {
		kind = strings.ToLower(strings.TrimSpace(kind))
		if _, ok := validKinds[kind]; !ok {
			return nil, invalid("invalid memory kind %q", kind)
		}
		if _, exists := seen[kind]; !exists {
			seen[kind] = struct{}{}
			kinds = append(kinds, kind)
		}
	}
	return kinds, nil
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
		n := len(args)
		where = append(where, descendantSQL(column, n))
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
		column, argument, column, argument, argument,
	)
}

func qualifyColumns(alias string) string {
	parts := strings.Split(memoryColumnNames, ",")
	for i := range parts {
		parts[i] = alias + "." + strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ", ")
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMemory(row rowScanner) (Memory, error) {
	return scanMemoryAnd(row)
}

func scanMemoryAnd(row rowScanner, trailing ...any) (Memory, error) {
	var (
		record     Memory
		attributes []byte
		locator    []byte
	)
	dest := []any{
		&record.ID,
		&record.WorkspaceID,
		&record.CreatedByUserID,
		&record.CreatedByTokenID,
		&record.Kind,
		&record.Content,
		&attributes,
		&record.Path,
		&record.EventAt,
		&record.SourceType,
		&record.SourceRef,
		&record.SourceFileID,
		&record.SourceFileSHA256,
		&locator,
		&record.ProducerAgent,
		&record.ProducerSession,
		&record.ProducerTask,
		&record.IdempotencyKeySHA256,
		&record.RequestSHA256,
		&record.ContentSHA256,
		&record.LifecycleStatus,
		&record.StateVersion,
		&record.PinnedAt,
		&record.UsefulCount,
		&record.NotUsefulCount,
		&record.FeedbackAt,
		&record.ForgottenAt,
		&record.ForgottenByUserID,
		&record.ForgottenByTokenID,
		&record.CreatedAt,
		&record.UpdatedAt,
	}
	dest = append(dest, trailing...)
	if err := row.Scan(dest...); err != nil {
		return Memory{}, err
	}
	record.Attributes = append(record.Attributes[:0], attributes...)
	record.SourceLocator = append(record.SourceLocator[:0], locator...)
	record.Pinned = record.PinnedAt != nil
	record.FeedbackScore = record.UsefulCount - record.NotUsefulCount
	record.FeedbackCount = record.UsefulCount + record.NotUsefulCount
	return record, nil
}

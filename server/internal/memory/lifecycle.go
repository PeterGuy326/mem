package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const memoryEventColumnNames = `
	id,
	workspace_id,
	memory_id,
	action,
	actor_user_id,
	actor_token_id,
	idempotency_key_sha256,
	request_sha256,
	replay_principal_sha256,
	expected_version,
	resulting_version,
	reason,
	created_at`

var validFeedbackActions = map[string]struct{}{
	FeedbackPin:       {},
	FeedbackUnpin:     {},
	FeedbackUseful:    {},
	FeedbackNotUseful: {},
}

var validForgetReasons = map[string]struct{}{
	ForgetReasonUserRequest: {},
	ForgetReasonIncorrect:   {},
	ForgetReasonSensitive:   {},
	ForgetReasonExpired:     {},
	ForgetReasonOther:       {},
}

type mutationBase struct {
	workspaceID           uuid.UUID
	memoryID              uuid.UUID
	allowedPaths          []string
	actorUserID           *uuid.UUID
	actorTokenID          *uuid.UUID
	action                string
	idempotencyKeySHA256  string
	expectedVersion       int64
	reason                string
	requestSHA256         string
	replayPrincipalSHA256 string
}

type mutationHashPayload struct {
	WorkspaceID     string `json:"workspace_id"`
	MemoryID        string `json:"memory_id"`
	Action          string `json:"action"`
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason,omitempty"`
}

// Feedback appends an explicit feedback event and updates its bounded
// projection. Useful/not_useful are signals only; they do not turn an
// unverified occurrence into a fact.
func (s *Service) Feedback(ctx context.Context, cmd FeedbackCommand) (*MutationResult, error) {
	action := strings.ToLower(strings.TrimSpace(cmd.Action))
	if _, ok := validFeedbackActions[action]; !ok {
		return nil, invalid("feedback action must be pin, unpin, useful, or not_useful")
	}
	base, err := normalizeMutationBase(
		cmd.WorkspaceID,
		cmd.MemoryID,
		cmd.AllowedPaths,
		cmd.ActorUserID,
		cmd.ActorTokenID,
		action,
		cmd.IdempotencyKey,
		cmd.ExpectedVersion,
		"",
	)
	if err != nil {
		return nil, err
	}
	return s.applyMutation(ctx, base)
}

// Archive removes an active occurrence from default recall without deleting
// content or provenance.
func (s *Service) Archive(ctx context.Context, cmd LifecycleCommand) (*MutationResult, error) {
	base, err := normalizeLifecycleCommand(cmd, "archive")
	if err != nil {
		return nil, err
	}
	return s.applyMutation(ctx, base)
}

// Restore returns an archived occurrence to active recall.
func (s *Service) Restore(ctx context.Context, cmd LifecycleCommand) (*MutationResult, error) {
	base, err := normalizeLifecycleCommand(cmd, "restore")
	if err != nil {
		return nil, err
	}
	return s.applyMutation(ctx, base)
}

// Forget synchronously redacts the live payload and returns only a tombstone.
// This is a logical/database erasure contract, not a claim that PostgreSQL
// MVCC pages, WAL, or backups have been cryptographically erased.
func (s *Service) Forget(ctx context.Context, cmd ForgetCommand) (*ForgetResult, error) {
	reason := strings.ToLower(strings.TrimSpace(cmd.Reason))
	if reason == "" {
		reason = ForgetReasonUserRequest
	}
	if _, ok := validForgetReasons[reason]; !ok {
		return nil, invalid(
			"forget reason must be user_request, incorrect, sensitive, expired, or other",
		)
	}
	base, err := normalizeMutationBase(
		cmd.WorkspaceID,
		cmd.MemoryID,
		cmd.AllowedPaths,
		cmd.ActorUserID,
		cmd.ActorTokenID,
		"forget",
		cmd.IdempotencyKey,
		cmd.ExpectedVersion,
		reason,
	)
	if err != nil {
		return nil, err
	}
	if base.actorUserID == nil {
		return nil, invalid(
			"forget requires actor_user_id for safe retry authorization",
		)
	}
	result, err := s.applyMutation(ctx, base)
	if err != nil {
		return nil, err
	}
	return &ForgetResult{
		Tombstone: tombstoneFromMemory(result.Memory),
		Event:     result.Event,
		Replayed:  result.Replayed,
	}, nil
}

func normalizeLifecycleCommand(cmd LifecycleCommand, action string) (mutationBase, error) {
	return normalizeMutationBase(
		cmd.WorkspaceID,
		cmd.MemoryID,
		cmd.AllowedPaths,
		cmd.ActorUserID,
		cmd.ActorTokenID,
		action,
		cmd.IdempotencyKey,
		cmd.ExpectedVersion,
		"",
	)
}

func normalizeMutationBase(
	workspaceID, memoryID uuid.UUID,
	allowedPaths []string,
	actorUserID, actorTokenID *uuid.UUID,
	action, idempotencyKey string,
	expectedVersion int64,
	reason string,
) (mutationBase, error) {
	if workspaceID == uuid.Nil {
		return mutationBase{}, invalid("workspace_id is required")
	}
	if memoryID == uuid.Nil {
		return mutationBase{}, invalid("memory_id is required")
	}
	if actorUserID != nil && *actorUserID == uuid.Nil {
		return mutationBase{}, invalid("actor_user_id must not be nil UUID")
	}
	if actorTokenID != nil && *actorTokenID == uuid.Nil {
		return mutationBase{}, invalid("actor_token_id must not be nil UUID")
	}
	allowed, err := normalizeAllowedPaths(allowedPaths)
	if err != nil {
		return mutationBase{}, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !utf8.ValidString(idempotencyKey) {
		return mutationBase{}, invalid("idempotency key must be valid UTF-8")
	}
	keyLen := utf8.RuneCountInString(idempotencyKey)
	if keyLen < 1 || keyLen > maxIdempotencyKeyRunes {
		return mutationBase{}, invalid(
			"idempotency key length must be between 1 and 200 characters",
		)
	}
	if expectedVersion < 1 || expectedVersion == math.MaxInt64 {
		return mutationBase{}, invalid("expected_version must be between 1 and %d", int64(math.MaxInt64-1))
	}
	payload := mutationHashPayload{
		WorkspaceID:     workspaceID.String(),
		MemoryID:        memoryID.String(),
		Action:          action,
		ExpectedVersion: expectedVersion,
		Reason:          reason,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return mutationBase{}, fmt.Errorf("encode memory mutation hash: %w", err)
	}
	sum := sha256.Sum256(encoded)
	keySum := sha256.Sum256([]byte(idempotencyKey))
	replayPrincipalSum := sha256.Sum256([]byte(fmt.Sprintf(
		"mem/forget-replay/v1|%s|%s|%s|%s",
		workspaceID,
		memoryID,
		uuidString(actorUserID),
		hex.EncodeToString(keySum[:]),
	)))
	return mutationBase{
		workspaceID:           workspaceID,
		memoryID:              memoryID,
		allowedPaths:          allowed,
		actorUserID:           cloneUUID(actorUserID),
		actorTokenID:          cloneUUID(actorTokenID),
		action:                action,
		idempotencyKeySHA256:  hex.EncodeToString(keySum[:]),
		expectedVersion:       expectedVersion,
		reason:                reason,
		requestSHA256:         hex.EncodeToString(sum[:]),
		replayPrincipalSHA256: hex.EncodeToString(replayPrincipalSum[:]),
	}, nil
}

func (s *Service) applyMutation(ctx context.Context, cmd mutationBase) (*MutationResult, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("memory service is not configured")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin memory mutation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if cmd.action == "forget" {
		record, event, replayed, err := loadExactForgetReplay(ctx, tx, cmd)
		if err != nil {
			return nil, fmt.Errorf("load forget replay receipt: %w", err)
		}
		if replayed {
			if err := tx.Commit(ctx); err != nil {
				return nil, fmt.Errorf("commit forget replay: %w", err)
			}
			return &MutationResult{Memory: record, Event: event, Replayed: true}, nil
		}
	}

	record, err := loadMemoryForMutation(ctx, tx, cmd)
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent Forget can replace the old path with the generic
		// tombstone path while this SELECT waits for its row lock. Retry the
		// principal receipt after that wait so an exact retry remains safe.
		if cmd.action == "forget" {
			record, event, replayed, replayErr := loadExactForgetReplay(ctx, tx, cmd)
			if replayErr != nil {
				return nil, fmt.Errorf("reload forget replay receipt: %w", replayErr)
			}
			if replayed {
				if err := tx.Commit(ctx); err != nil {
					return nil, fmt.Errorf("commit concurrent forget replay: %w", err)
				}
				return &MutationResult{Memory: record, Event: event, Replayed: true}, nil
			}
		}
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock memory for mutation: %w", err)
	}
	// An exact Forget replay is authorized only by the principal receipt above.
	// Check the terminal state before generic event replay so a different token
	// cannot use a guessed key to receive a tombstone outside its former path.
	if record.LifecycleStatus == StatusForgotten {
		return nil, ErrForgotten
	}

	event, err := loadMemoryEventByKey(
		ctx, tx, cmd.workspaceID, cmd.idempotencyKeySHA256,
	)
	if err == nil {
		if event.RequestSHA256 != cmd.requestSHA256 {
			return nil, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit memory mutation replay: %w", err)
		}
		return &MutationResult{Memory: record, Event: event, Replayed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("load memory mutation replay: %w", err)
	}

	if record.StateVersion != cmd.expectedVersion {
		return nil, fmt.Errorf(
			"%w: expected %d, current %d",
			ErrVersionConflict,
			cmd.expectedVersion,
			record.StateVersion,
		)
	}
	if err := validateTransition(record, cmd.action); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	nextVersion := record.StateVersion + 1
	record, err = updateMemoryProjection(ctx, tx, record, cmd, nextVersion, now)
	if err != nil {
		return nil, err
	}
	if cmd.action == "forget" {
		if err := redactMemoryEventMetadata(ctx, tx, cmd.workspaceID, cmd.memoryID); err != nil {
			return nil, err
		}
		if err := redactMemoryRelations(ctx, tx, cmd.workspaceID, cmd.memoryID); err != nil {
			return nil, err
		}
	}
	event, inserted, err := insertMemoryEvent(ctx, tx, cmd, nextVersion, now)
	if err != nil {
		return nil, err
	}
	if !inserted {
		// A shared key used concurrently against a different memory/action must
		// roll back this projection update and report a conflict.
		return nil, ErrIdempotencyConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit memory mutation: %w", err)
	}
	return &MutationResult{Memory: record, Event: event, Replayed: false}, nil
}

func redactMemoryEventMetadata(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID, memoryID uuid.UUID,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE memory_events
		   SET actor_user_id = NULL,
		       actor_token_id = NULL,
		       idempotency_key_sha256 = encode(
		           digest(
		               convert_to('mem/redacted-event/v1|' || id::text, 'UTF8'),
		               'sha256'
		           ),
		           'hex'
		       ),
		       request_sha256 = repeat('0', 64)
		 WHERE workspace_id = $1
		   AND memory_id = $2
		   AND action <> 'forget'`,
		workspaceID,
		memoryID,
	); err != nil {
		return fmt.Errorf("redact memory event metadata: %w", err)
	}
	return nil
}

func loadMemoryForMutation(
	ctx context.Context,
	tx pgx.Tx,
	cmd mutationBase,
) (Memory, error) {
	args := []any{cmd.workspaceID, cmd.memoryID}
	where := []string{"m.workspace_id = $1", "m.id = $2"}
	args, where = appendPathFilters(args, where, "m.path", "/", cmd.allowedPaths)
	return scanMemory(tx.QueryRow(ctx, `
		SELECT `+qualifyColumns("m")+`
		  FROM memories AS m
		 WHERE `+strings.Join(where, " AND ")+`
		 FOR UPDATE`, args...))
}

// loadExactForgetReplay authorizes a terminal replay without consulting the
// erased path. The receipt binds the event key to the original workspace,
// memory and stable user identity; the actor ID is not retained on the
// forgotten memory or its event. Token rotation therefore does not break a
// legitimate retry.
func loadExactForgetReplay(
	ctx context.Context,
	tx pgx.Tx,
	cmd mutationBase,
) (Memory, MemoryEvent, bool, error) {
	event, err := scanMemoryEvent(tx.QueryRow(ctx, `
		SELECT `+memoryEventColumnNames+`
		  FROM memory_events
		 WHERE workspace_id = $1
		   AND memory_id = $2
		   AND action = 'forget'
		   AND idempotency_key_sha256 = $3
		   AND request_sha256 = $4
		   AND replay_principal_sha256 = $5`,
		cmd.workspaceID,
		cmd.memoryID,
		cmd.idempotencyKeySHA256,
		cmd.requestSHA256,
		cmd.replayPrincipalSHA256,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Memory{}, MemoryEvent{}, false, nil
	}
	if err != nil {
		return Memory{}, MemoryEvent{}, false, err
	}

	record, err := scanMemory(tx.QueryRow(ctx, `
		SELECT `+qualifyColumns("m")+`
		  FROM memories AS m
		 WHERE m.workspace_id = $1
		   AND m.id = $2
		   AND m.lifecycle_status = 'forgotten'
		 FOR SHARE`,
		cmd.workspaceID,
		cmd.memoryID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		// Do not reveal whether an orphaned or inconsistent receipt exists.
		return Memory{}, MemoryEvent{}, false, nil
	}
	if err != nil {
		return Memory{}, MemoryEvent{}, false, err
	}
	return record, event, true, nil
}

func validateTransition(record Memory, action string) error {
	if record.LifecycleStatus == StatusForgotten {
		return fmt.Errorf("%w: forgotten memory is terminal", ErrInvalidTransition)
	}
	switch action {
	case FeedbackPin:
		if record.Pinned {
			return fmt.Errorf("%w: memory is already pinned", ErrInvalidTransition)
		}
	case FeedbackUnpin:
		if !record.Pinned {
			return fmt.Errorf("%w: memory is not pinned", ErrInvalidTransition)
		}
	case FeedbackUseful, FeedbackNotUseful:
		// Both active and archived records can receive explicit quality signals.
	case "archive":
		if record.LifecycleStatus != StatusActive {
			return fmt.Errorf("%w: only active memories can be archived", ErrInvalidTransition)
		}
	case "restore":
		if record.LifecycleStatus != StatusArchived {
			return fmt.Errorf("%w: only archived memories can be restored", ErrInvalidTransition)
		}
	case "forget":
		if record.LifecycleStatus != StatusActive &&
			record.LifecycleStatus != StatusArchived {
			return fmt.Errorf("%w: memory cannot be forgotten", ErrInvalidTransition)
		}
	default:
		return invalid("unsupported memory action %q", action)
	}
	return nil
}

func updateMemoryProjection(
	ctx context.Context,
	tx pgx.Tx,
	record Memory,
	cmd mutationBase,
	nextVersion int64,
	now time.Time,
) (Memory, error) {
	var sql string
	args := []any{record.ID, record.WorkspaceID, nextVersion, now}
	switch cmd.action {
	case FeedbackPin:
		sql = `UPDATE memories
		          SET pinned_at = $4,
		              state_version = $3,
		              updated_at = $4
		        WHERE id = $1 AND workspace_id = $2
		    RETURNING ` + memoryColumnNames
	case FeedbackUnpin:
		sql = `UPDATE memories
		          SET pinned_at = NULL,
		              state_version = $3,
		              updated_at = $4
		        WHERE id = $1 AND workspace_id = $2
		    RETURNING ` + memoryColumnNames
	case FeedbackUseful:
		sql = `UPDATE memories
		          SET useful_count = useful_count + 1,
		              feedback_at = $4,
		              state_version = $3,
		              updated_at = $4
		        WHERE id = $1 AND workspace_id = $2
		    RETURNING ` + memoryColumnNames
	case FeedbackNotUseful:
		sql = `UPDATE memories
		          SET not_useful_count = not_useful_count + 1,
		              feedback_at = $4,
		              state_version = $3,
		              updated_at = $4
		        WHERE id = $1 AND workspace_id = $2
		    RETURNING ` + memoryColumnNames
	case "archive":
		sql = `UPDATE memories
		          SET lifecycle_status = 'archived',
		              state_version = $3,
		              updated_at = $4
		        WHERE id = $1 AND workspace_id = $2
		    RETURNING ` + memoryColumnNames
	case "restore":
		sql = `UPDATE memories
		          SET lifecycle_status = 'active',
		              state_version = $3,
		              updated_at = $4
		        WHERE id = $1 AND workspace_id = $2
		    RETURNING ` + memoryColumnNames
	case "forget":
		sql = `UPDATE memories
		          SET created_by_user_id = NULL,
		              created_by_token_id = NULL,
		              kind = 'forgotten',
		              content = '',
		              attributes = '{}'::jsonb,
		              path = '/',
		              event_at = NULL,
		              source_type = 'forgotten',
		              source_ref = '',
		              source_file_id = NULL,
		              source_file_sha256 = '',
		              source_locator = '{}'::jsonb,
		              producer_agent = '',
		              producer_session = '',
		              producer_task = '',
		              request_sha256 = repeat('0', 64),
		              content_sha256 = repeat('0', 64),
		              lifecycle_status = 'forgotten',
		              pinned_at = NULL,
		              useful_count = 0,
		              not_useful_count = 0,
		              feedback_at = NULL,
		              forgotten_at = $4,
		              forgotten_by_user_id = NULL,
		              forgotten_by_token_id = NULL,
		              state_version = $3,
		              created_at = $4,
		              updated_at = $4
		        WHERE id = $1 AND workspace_id = $2
		    RETURNING ` + memoryColumnNames
	default:
		return Memory{}, invalid("unsupported memory action %q", cmd.action)
	}
	updated, err := scanMemory(tx.QueryRow(ctx, sql, args...))
	if err != nil {
		return Memory{}, fmt.Errorf("update memory %s projection: %w", cmd.action, err)
	}
	return updated, nil
}

func insertMemoryEvent(
	ctx context.Context,
	tx pgx.Tx,
	cmd mutationBase,
	resultingVersion int64,
	createdAt time.Time,
) (MemoryEvent, bool, error) {
	actorUserID := cmd.actorUserID
	actorTokenID := cmd.actorTokenID
	replayPrincipalSHA256 := ""
	if cmd.action == "forget" {
		// The terminal event retains only a one-way retry receipt. Raw actor
		// identifiers would outlive the payload and are not needed for audit.
		actorUserID = nil
		actorTokenID = nil
		replayPrincipalSHA256 = cmd.replayPrincipalSHA256
	}
	event, err := scanMemoryEvent(tx.QueryRow(ctx, `
		INSERT INTO memory_events (
			workspace_id,
			memory_id,
			action,
			actor_user_id,
			actor_token_id,
			idempotency_key_sha256,
			request_sha256,
			replay_principal_sha256,
			expected_version,
			resulting_version,
			reason,
			created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (workspace_id, idempotency_key_sha256) DO NOTHING
		RETURNING `+memoryEventColumnNames,
		cmd.workspaceID,
		cmd.memoryID,
		cmd.action,
		actorUserID,
		actorTokenID,
		cmd.idempotencyKeySHA256,
		cmd.requestSHA256,
		replayPrincipalSHA256,
		cmd.expectedVersion,
		resultingVersion,
		cmd.reason,
		createdAt,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return MemoryEvent{}, false, nil
	}
	if err != nil {
		return MemoryEvent{}, false, fmt.Errorf("insert memory event: %w", err)
	}
	return event, true, nil
}

func loadMemoryEventByKey(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID uuid.UUID,
	keySHA256 string,
) (MemoryEvent, error) {
	return scanMemoryEvent(tx.QueryRow(ctx, `
		SELECT `+memoryEventColumnNames+`
		  FROM memory_events
		 WHERE workspace_id = $1
		   AND idempotency_key_sha256 = $2`,
		workspaceID, keySHA256,
	))
}

func scanMemoryEvent(row rowScanner) (MemoryEvent, error) {
	var event MemoryEvent
	err := row.Scan(
		&event.ID,
		&event.WorkspaceID,
		&event.MemoryID,
		&event.Action,
		&event.ActorUserID,
		&event.ActorTokenID,
		&event.IdempotencyKeySHA256,
		&event.RequestSHA256,
		&event.ReplayPrincipalSHA256,
		&event.ExpectedVersion,
		&event.ResultingVersion,
		&event.Reason,
		&event.CreatedAt,
	)
	return event, err
}

func tombstoneFromMemory(record Memory) Tombstone {
	return Tombstone{
		ID:              record.ID,
		LifecycleStatus: record.LifecycleStatus,
		StateVersion:    record.StateVersion,
		ForgottenAt:     record.ForgottenAt,
	}
}

func cloneUUID(in *uuid.UUID) *uuid.UUID {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func uuidString(in *uuid.UUID) string {
	if in == nil {
		return ""
	}
	return in.String()
}

// redactMemoryRelations removes actor metadata from relation edges touching a
// forgotten memory. The structural edges (source_id, target_id, relation_type)
// are preserved so the DAG remains consistent.
func redactMemoryRelations(ctx context.Context, tx pgx.Tx, workspaceID, memoryID uuid.UUID) error {
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

package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	defaultListLimit = 50
	maxListLimit     = 100
	maxCursorBytes   = 4096
	listCursorV1     = 1
)

type normalizedListQuery struct {
	workspaceID uuid.UUID
	scope       string
	recursive   bool
	allowed     []string
	kinds       []string
	statuses    []string
	pinned      *bool
	limit       int
	cursor      *decodedListCursor
	filterHash  string
}

type decodedListCursor struct {
	createdAt time.Time
	id        uuid.UUID
}

type listCursorEnvelope struct {
	Version    int    `json:"v"`
	CreatedAt  string `json:"created_at"`
	ID         string `json:"id"`
	FilterHash string `json:"filter"`
}

type listFilterFingerprint struct {
	WorkspaceID  string   `json:"workspace_id"`
	Scope        string   `json:"scope"`
	Recursive    bool     `json:"recursive"`
	AllowedPaths []string `json:"allowed_paths"`
	Kinds        []string `json:"kinds"`
	Statuses     []string `json:"statuses"`
	Pinned       string   `json:"pinned"`
}

// List returns a newest-first, authorization-filtered page using a stable
// (created_at, id) keyset. Full content and JSON payloads are never selected.
func (s *Service) List(ctx context.Context, q ListQuery) (*ListResult, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("memory service is not configured")
	}
	nq, err := normalizeListQuery(q)
	if err != nil {
		return nil, err
	}

	args := []any{nq.workspaceID, nq.statuses}
	where := []string{
		"m.workspace_id = $1",
		"m.lifecycle_status = ANY($2::text[])",
		"m.lifecycle_status <> 'forgotten'",
	}
	if nq.scope != "/" {
		args = append(args, nq.scope)
		if nq.recursive {
			where = append(where, descendantSQL("m.path", len(args)))
		} else {
			where = append(where, fmt.Sprintf("m.path = $%d", len(args)))
		}
	} else if !nq.recursive {
		where = append(where, "m.path = '/'")
	}
	args, where = appendPathFilters(args, where, "m.path", "/", nq.allowed)
	if len(nq.kinds) > 0 {
		args = append(args, nq.kinds)
		where = append(where, fmt.Sprintf("m.kind = ANY($%d::text[])", len(args)))
	}
	if nq.pinned != nil {
		if *nq.pinned {
			where = append(where, "m.pinned_at IS NOT NULL")
		} else {
			where = append(where, "m.pinned_at IS NULL")
		}
	}
	if nq.cursor != nil {
		args = append(args, nq.cursor.createdAt, nq.cursor.id)
		timeArg, idArg := len(args)-1, len(args)
		where = append(where, fmt.Sprintf(
			"(m.created_at < $%d OR (m.created_at = $%d AND m.id < $%d))",
			timeArg, timeArg, idArg,
		))
	}
	args = append(args, nq.limit+1)
	limitArg := len(args)

	rows, err := s.pool.Query(ctx, `
		SELECT
			m.id,
			m.workspace_id,
			m.kind,
			left(m.content, 500),
			char_length(m.content),
			m.path,
			m.event_at,
			m.source_type,
			m.source_ref,
			m.source_file_id,
			m.source_file_sha256,
			m.producer_agent,
			m.producer_session,
			m.producer_task,
			m.content_sha256,
			m.lifecycle_status,
			m.state_version,
			m.pinned_at,
			m.useful_count,
			m.not_useful_count,
			m.feedback_at,
			m.created_at,
			m.updated_at
		  FROM memories AS m
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY m.created_at DESC, m.id DESC
		 LIMIT $`+fmt.Sprint(limitArg),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()

	summaries := make([]MemorySummary, 0, nq.limit+1)
	for rows.Next() {
		var summary MemorySummary
		if err := rows.Scan(
			&summary.ID,
			&summary.WorkspaceID,
			&summary.Kind,
			&summary.Excerpt,
			&summary.ContentLength,
			&summary.Path,
			&summary.EventAt,
			&summary.SourceType,
			&summary.SourceRef,
			&summary.SourceFileID,
			&summary.SourceFileSHA256,
			&summary.ProducerAgent,
			&summary.ProducerSession,
			&summary.ProducerTask,
			&summary.ContentSHA256,
			&summary.LifecycleStatus,
			&summary.StateVersion,
			&summary.PinnedAt,
			&summary.UsefulCount,
			&summary.NotUsefulCount,
			&summary.FeedbackAt,
			&summary.CreatedAt,
			&summary.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan memory summary: %w", err)
		}
		summary.Pinned = summary.PinnedAt != nil
		summary.FeedbackScore = summary.UsefulCount - summary.NotUsefulCount
		summary.FeedbackCount = summary.UsefulCount + summary.NotUsefulCount
		summary.Citation = fmt.Sprintf("mem://memories/%s", summary.ID)
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory summaries: %w", err)
	}

	result := &ListResult{Memories: summaries}
	if len(summaries) <= nq.limit {
		return result, nil
	}
	result.Memories = summaries[:nq.limit]
	last := result.Memories[len(result.Memories)-1]
	result.NextCursor, err = encodeListCursor(last.CreatedAt, last.ID, nq.filterHash)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func normalizeListQuery(q ListQuery) (normalizedListQuery, error) {
	if q.WorkspaceID == uuid.Nil {
		return normalizedListQuery{}, invalid("workspace_id is required")
	}
	scope, err := normalizeScope(q.Scope)
	if err != nil {
		return normalizedListQuery{}, err
	}
	allowed, err := normalizeAllowedPaths(q.AllowedPaths)
	if err != nil {
		return normalizedListQuery{}, err
	}
	kinds, err := normalizeKinds(q.Kinds)
	if err != nil {
		return normalizedListQuery{}, err
	}
	sort.Strings(kinds)
	statuses, err := normalizeListStatuses(q.LifecycleStatuses)
	if err != nil {
		return normalizedListQuery{}, err
	}
	recursive := true
	if q.Recursive != nil {
		recursive = *q.Recursive
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	filterHash, err := listFilterHash(
		q.WorkspaceID, scope, recursive, allowed, kinds, statuses, q.Pinned,
	)
	if err != nil {
		return normalizedListQuery{}, fmt.Errorf("hash memory list filters: %w", err)
	}
	var cursor *decodedListCursor
	if strings.TrimSpace(q.Cursor) != "" {
		decoded, err := decodeListCursor(q.Cursor, filterHash)
		if err != nil {
			return normalizedListQuery{}, err
		}
		cursor = &decoded
	}
	return normalizedListQuery{
		workspaceID: q.WorkspaceID,
		scope:       scope,
		recursive:   recursive,
		allowed:     allowed,
		kinds:       kinds,
		statuses:    statuses,
		pinned:      q.Pinned,
		limit:       limit,
		cursor:      cursor,
		filterHash:  filterHash,
	}, nil
}

func normalizeListStatuses(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return []string{StatusActive}, nil
	}
	seen := map[string]struct{}{}
	for _, status := range raw {
		status = strings.ToLower(strings.TrimSpace(status))
		switch status {
		case "all":
			seen[StatusActive] = struct{}{}
			seen[StatusArchived] = struct{}{}
		case StatusActive, StatusArchived:
			seen[status] = struct{}{}
		case StatusForgotten:
			return nil, invalid("forgotten memories are not listable")
		default:
			return nil, invalid("lifecycle status must be active, archived, or all")
		}
	}
	statuses := make([]string, 0, len(seen))
	for status := range seen {
		statuses = append(statuses, status)
	}
	sort.Strings(statuses)
	return statuses, nil
}

func listFilterHash(
	workspaceID uuid.UUID,
	scope string,
	recursive bool,
	allowed, kinds, statuses []string,
	pinned *bool,
) (string, error) {
	pinnedValue := "any"
	if pinned != nil {
		if *pinned {
			pinnedValue = "true"
		} else {
			pinnedValue = "false"
		}
	}
	payload := listFilterFingerprint{
		WorkspaceID:  workspaceID.String(),
		Scope:        scope,
		Recursive:    recursive,
		AllowedPaths: append([]string(nil), allowed...),
		Kinds:        append([]string(nil), kinds...),
		Statuses:     append([]string(nil), statuses...),
		Pinned:       pinnedValue,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func encodeListCursor(createdAt time.Time, id uuid.UUID, filterHash string) (string, error) {
	envelope := listCursorEnvelope{
		Version:    listCursorV1,
		CreatedAt:  createdAt.UTC().Format(timeFormat),
		ID:         id.String(),
		FilterHash: filterHash,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode memory list cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeListCursor(raw, expectedFilterHash string) (decodedListCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxCursorBytes {
		return decodedListCursor{}, fmt.Errorf("%w: malformed cursor", ErrInvalidCursor)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) == 0 || len(decoded) > maxCursorBytes {
		return decodedListCursor{}, fmt.Errorf("%w: malformed cursor", ErrInvalidCursor)
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var envelope listCursorEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return decodedListCursor{}, fmt.Errorf("%w: malformed cursor", ErrInvalidCursor)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return decodedListCursor{}, fmt.Errorf("%w: malformed cursor", ErrInvalidCursor)
	}
	if envelope.Version != listCursorV1 {
		return decodedListCursor{}, fmt.Errorf("%w: unsupported cursor version", ErrInvalidCursor)
	}
	if envelope.FilterHash != expectedFilterHash {
		return decodedListCursor{}, fmt.Errorf("%w: cursor filters changed", ErrInvalidCursor)
	}
	createdAt, err := time.Parse(timeFormat, envelope.CreatedAt)
	if err != nil {
		return decodedListCursor{}, fmt.Errorf("%w: malformed created_at", ErrInvalidCursor)
	}
	id, err := uuid.Parse(envelope.ID)
	if err != nil || id == uuid.Nil {
		return decodedListCursor{}, fmt.Errorf("%w: malformed id", ErrInvalidCursor)
	}
	return decodedListCursor{createdAt: createdAt.UTC(), id: id}, nil
}

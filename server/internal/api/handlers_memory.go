package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/file"
	"github.com/PeterGuy326/mem/server/internal/memory"
)

const (
	maxRememberBodyBytes      = 256 << 10
	maxMemoryControlBodyBytes = 16 << 10
)

type rememberSourceRequest struct {
	Type    string          `json:"type"`
	Ref     string          `json:"ref,omitempty"`
	FileID  string          `json:"file_id,omitempty"`
	Locator json.RawMessage `json:"locator,omitempty"`
}

type rememberProducerRequest struct {
	AgentID   string `json:"agent_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
}

type rememberRequest struct {
	Kind       string                  `json:"kind"`
	Content    string                  `json:"content"`
	Path       string                  `json:"path"`
	EventAt    *string                 `json:"event_at,omitempty"`
	Source     rememberSourceRequest   `json:"source"`
	Producer   rememberProducerRequest `json:"producer,omitempty"`
	Attributes json.RawMessage         `json:"attributes,omitempty"`
}

type memoryDetailResponse struct {
	memory.Memory
	Citation   string            `json:"citation"`
	Provenance memory.Provenance `json:"provenance"`
}

// handleRemember persists one immutable Agent memory occurrence. It never
// invokes a model; lexical recall is available immediately after commit.
func (s *Server) handleRemember(w http.ResponseWriter, r *http.Request) {
	if s.Memory == nil {
		writeError(w, http.StatusServiceUnavailable, "memory_disabled",
			"structured memory service not configured")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing_idempotency_key",
			"Idempotency-Key header is required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRememberBodyBytes)
	var req rememberRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "memory_too_large",
				"remember request exceeds 262144 bytes")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			writeError(w, http.StatusBadRequest, "bad_body",
				"request body must contain exactly one JSON value")
		} else {
			writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		}
		return
	}

	var eventAt *time.Time
	if req.EventAt != nil {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*req.EventAt))
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_event_at",
				"event_at must be RFC 3339: "+err.Error())
			return
		}
		parsed = parsed.UTC()
		eventAt = &parsed
	}

	tok := r.Context().Value(ctxToken).(*auth.Token)
	actor := r.Context().Value(ctxActor).(*auth.User)
	owner := r.Context().Value(ctxUser).(*auth.User)
	ws := currentWorkspace(r)

	if !requireTokenPath(w, r, req.Path) {
		return
	}

	var sourceFileID *uuid.UUID
	var sourceFileSHA256 string
	if strings.TrimSpace(req.Source.FileID) != "" {
		if !auth.HasScope(tok, auth.ScopeRead) {
			writeError(w, http.StatusForbidden, "forbidden",
				"linking source.file_id requires read scope")
			return
		}
		id, err := uuid.Parse(strings.TrimSpace(req.Source.FileID))
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_source_file_id",
				"source.file_id must be a UUID")
			return
		}
		if s.File == nil {
			writeError(w, http.StatusServiceUnavailable, "file_disabled",
				"file service not configured")
			return
		}
		sourceFile, err := s.File.Get(r.Context(), owner.ID, id)
		if err != nil {
			if errors.Is(err, file.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "no such source file")
				return
			}
			writeError(w, http.StatusInternalServerError, "source_file_lookup_failed",
				"source file could not be resolved")
			return
		}
		if !hideUnauthorizedFile(w, r, sourceFile.Path) {
			return
		}
		sourceFileID = &sourceFile.ID
		sourceFileSHA256 = sourceFile.SHA256
	}

	actorID := actor.ID
	tokenID := tok.ID
	result, err := s.Memory.Remember(r.Context(), memory.Command{
		WorkspaceID:      ws.ID,
		CreatedByUserID:  &actorID,
		CreatedByTokenID: &tokenID,
		Kind:             req.Kind,
		Content:          req.Content,
		Attributes:       req.Attributes,
		Path:             req.Path,
		EventAt:          eventAt,
		SourceType:       req.Source.Type,
		SourceRef:        req.Source.Ref,
		SourceFileID:     sourceFileID,
		SourceFileSHA256: sourceFileSHA256,
		SourceLocator:    req.Source.Locator,
		ProducerAgent:    req.Producer.AgentID,
		ProducerSession:  req.Producer.SessionID,
		ProducerTask:     req.Producer.TaskID,
		IdempotencyKey:   key,
		AllowedPaths:     tok.Paths,
	})
	if err != nil {
		switch {
		case errors.Is(err, memory.ErrInvalidCommand):
			writeError(w, http.StatusBadRequest, "invalid_memory", err.Error())
		case errors.Is(err, memory.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict",
				"the idempotency key was already used with a different request")
		case errors.Is(err, memory.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "no such memory")
		case errors.Is(err, memory.ErrForgotten):
			writeError(w, http.StatusGone, "memory_forgotten",
				"the original memory occurrence has been forgotten")
		default:
			if s.Log != nil {
				s.Log.Error("memory.remember_failed", "workspace_id", ws.ID, "err", err)
			}
			writeError(w, http.StatusInternalServerError, "memory_write_failed",
				"memory could not be persisted")
		}
		return
	}
	// A replay resolves the persisted occurrence, whose path may have changed
	// since the original request. Authorize that current path before returning
	// content so an old path + idempotency key cannot cross a Token boundary.
	if !tokenAllowsPath(r, result.Memory.Path) {
		writeError(w, http.StatusNotFound, "not_found", "no such memory")
		return
	}

	w.Header().Set("Location", "/v1/memories/"+result.Memory.ID.String())
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

// handleListMemories returns a drive-style, newest-first page. The opaque
// cursor is keyset-based rather than an offset, so concurrent new memories do
// not duplicate or skip records already behind the page boundary.
func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	if s.Memory == nil {
		writeError(w, http.StatusServiceUnavailable, "memory_disabled",
			"structured memory service not configured")
		return
	}
	limit, err := positiveQueryInt(r, "limit", 50, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_limit", err.Error())
		return
	}

	recursive, err := optionalBoolQuery(r, "recursive")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_recursive", err.Error())
		return
	}
	pinned, err := optionalBoolQuery(r, "pinned")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_pinned", err.Error())
		return
	}
	lifecycles, err := memoryLifecycleFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_lifecycle", err.Error())
		return
	}

	tok := r.Context().Value(ctxToken).(*auth.Token)
	result, err := s.Memory.List(r.Context(), memory.ListQuery{
		WorkspaceID:       currentWorkspace(r).ID,
		Scope:             r.URL.Query().Get("scope"),
		Recursive:         recursive,
		AllowedPaths:      tok.Paths,
		Kinds:             memoryKindFilters(r),
		LifecycleStatuses: lifecycles,
		Pinned:            pinned,
		Limit:             limit,
		Cursor:            r.URL.Query().Get("cursor"),
	})
	if err != nil {
		if errors.Is(err, memory.ErrInvalidCommand) ||
			errors.Is(err, memory.ErrInvalidCursor) {
			writeError(w, http.StatusBadRequest, "invalid_memory_query", err.Error())
			return
		}
		if s.Log != nil {
			s.Log.Error("memory.list_failed", "err", err)
		}
		writeError(w, http.StatusInternalServerError, "memory_list_failed",
			"memories could not be listed")
		return
	}

	if result.Memories == nil {
		result.Memories = []memory.MemorySummary{}
	}
	writeJSON(w, http.StatusOK, result)
}

// handleGetMemory resolves a stable mem://memories/<id> citation while
// applying workspace and Token path filters. Unauthorized records are hidden
// behind the same 404 contract as files.
func (s *Server) handleGetMemory(w http.ResponseWriter, r *http.Request) {
	if s.Memory == nil {
		writeError(w, http.StatusServiceUnavailable, "memory_disabled",
			"structured memory service not configured")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", "memory id must be a UUID")
		return
	}
	tok := r.Context().Value(ctxToken).(*auth.Token)
	record, err := s.Memory.Get(r.Context(), memory.Query{
		WorkspaceID:  currentWorkspace(r).ID,
		MemoryID:     id,
		Scope:        r.URL.Query().Get("scope"),
		AllowedPaths: tok.Paths,
	})
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such memory")
			return
		}
		if errors.Is(err, memory.ErrForgotten) {
			writeError(w, http.StatusGone, "memory_forgotten",
				"the memory has been forgotten")
			return
		}
		if errors.Is(err, memory.ErrInvalidCommand) {
			writeError(w, http.StatusBadRequest, "invalid_memory_query", err.Error())
			return
		}
		if s.Log != nil {
			s.Log.Error("memory.get_failed", "memory_id", id, "err", err)
		}
		writeError(w, http.StatusInternalServerError, "memory_read_failed",
			"memory could not be read")
		return
	}
	writeJSON(w, http.StatusOK, memoryDetailResponse{
		Memory:     *record,
		Citation:   record.Citation(),
		Provenance: record.Provenance(),
	})
}

type memoryFeedbackRequest struct {
	Action          string `json:"action"`
	ExpectedVersion int64  `json:"expected_version"`
}

type memoryVersionRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

type memoryForgetRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
}

type memoryForgetResponse struct {
	MemoryID     uuid.UUID           `json:"memory_id"`
	StateVersion int64               `json:"state_version"`
	ForgottenAt  *time.Time          `json:"forgotten_at,omitempty"`
	Event        memoryEventResponse `json:"event"`
	Replayed     bool                `json:"replayed"`
}

// memoryControlStateResponse is deliberately smaller than MemorySummary.
// Control calls are often made through MCP, where returning the full memory
// payload would unnecessarily inject up to 64 KiB of untrusted content into
// the Agent's context.
type memoryControlStateResponse struct {
	ID              uuid.UUID  `json:"id"`
	LifecycleStatus string     `json:"lifecycle_status"`
	StateVersion    int64      `json:"state_version"`
	Pinned          bool       `json:"pinned"`
	PinnedAt        *time.Time `json:"pinned_at,omitempty"`
	UsefulCount     int        `json:"useful_count"`
	NotUsefulCount  int        `json:"not_useful_count"`
	FeedbackScore   int        `json:"feedback_score"`
	FeedbackCount   int        `json:"feedback_count"`
	FeedbackAt      *time.Time `json:"feedback_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// memoryEventResponse keeps retry/audit facts while withholding workspace and
// actor identifiers that the caller does not need to complete the operation.
type memoryEventResponse struct {
	ID               uuid.UUID `json:"id"`
	MemoryID         uuid.UUID `json:"memory_id"`
	Action           string    `json:"action"`
	ExpectedVersion  int64     `json:"expected_version"`
	ResultingVersion int64     `json:"resulting_version"`
	Reason           string    `json:"reason,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type memoryMutationResponse struct {
	Memory   memoryControlStateResponse `json:"memory"`
	Event    memoryEventResponse        `json:"event"`
	Replayed bool                       `json:"replayed"`
}

func (s *Server) handleMemoryFeedback(w http.ResponseWriter, r *http.Request) {
	var req memoryFeedbackRequest
	if !s.decodeMemoryControlRequest(w, r, &req) {
		return
	}
	id, ok := memoryIDFromRequest(w, r)
	if !ok {
		return
	}
	key, ok := memoryIdempotencyKey(w, r)
	if !ok {
		return
	}
	actorID, tokenID := memoryActorIDs(r)
	tok := r.Context().Value(ctxToken).(*auth.Token)
	result, err := s.Memory.Feedback(r.Context(), memory.FeedbackCommand{
		WorkspaceID:     currentWorkspace(r).ID,
		MemoryID:        id,
		AllowedPaths:    tok.Paths,
		ActorUserID:     &actorID,
		ActorTokenID:    &tokenID,
		Action:          req.Action,
		IdempotencyKey:  key,
		ExpectedVersion: req.ExpectedVersion,
	})
	if err != nil {
		writeMemoryControlError(w, err)
		return
	}
	writeMemoryMutationResult(w, result)
}

func (s *Server) handleArchiveMemory(w http.ResponseWriter, r *http.Request) {
	s.handleMemoryLifecycle(w, r, "archive")
}

func (s *Server) handleRestoreMemory(w http.ResponseWriter, r *http.Request) {
	s.handleMemoryLifecycle(w, r, "restore")
}

func (s *Server) handleMemoryLifecycle(
	w http.ResponseWriter,
	r *http.Request,
	action string,
) {
	var req memoryVersionRequest
	if !s.decodeMemoryControlRequest(w, r, &req) {
		return
	}
	id, ok := memoryIDFromRequest(w, r)
	if !ok {
		return
	}
	key, ok := memoryIdempotencyKey(w, r)
	if !ok {
		return
	}
	actorID, tokenID := memoryActorIDs(r)
	tok := r.Context().Value(ctxToken).(*auth.Token)
	command := memory.LifecycleCommand{
		WorkspaceID:     currentWorkspace(r).ID,
		MemoryID:        id,
		AllowedPaths:    tok.Paths,
		ActorUserID:     &actorID,
		ActorTokenID:    &tokenID,
		IdempotencyKey:  key,
		ExpectedVersion: req.ExpectedVersion,
	}

	var (
		result *memory.MutationResult
		err    error
	)
	if action == "archive" {
		result, err = s.Memory.Archive(r.Context(), command)
	} else {
		result, err = s.Memory.Restore(r.Context(), command)
	}
	if err != nil {
		writeMemoryControlError(w, err)
		return
	}
	writeMemoryMutationResult(w, result)
}

func (s *Server) handleForgetMemory(w http.ResponseWriter, r *http.Request) {
	var req memoryForgetRequest
	if !s.decodeMemoryControlRequest(w, r, &req) {
		return
	}
	id, ok := memoryIDFromRequest(w, r)
	if !ok {
		return
	}
	key, ok := memoryIdempotencyKey(w, r)
	if !ok {
		return
	}
	actorID, tokenID := memoryActorIDs(r)
	tok := r.Context().Value(ctxToken).(*auth.Token)
	result, err := s.Memory.Forget(r.Context(), memory.ForgetCommand{
		LifecycleCommand: memory.LifecycleCommand{
			WorkspaceID:     currentWorkspace(r).ID,
			MemoryID:        id,
			AllowedPaths:    tok.Paths,
			ActorUserID:     &actorID,
			ActorTokenID:    &tokenID,
			IdempotencyKey:  key,
			ExpectedVersion: req.ExpectedVersion,
		},
		Reason: req.Reason,
	})
	if err != nil {
		writeMemoryControlError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, memoryForgetResponse{
		MemoryID:     result.Tombstone.ID,
		StateVersion: result.Tombstone.StateVersion,
		ForgottenAt:  result.Tombstone.ForgottenAt,
		Event:        newMemoryEventResponse(result.Event),
		Replayed:     result.Replayed,
	})
}

func (s *Server) decodeMemoryControlRequest(
	w http.ResponseWriter,
	r *http.Request,
	out any,
) bool {
	if s.Memory == nil {
		writeError(w, http.StatusServiceUnavailable, "memory_disabled",
			"structured memory service not configured")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMemoryControlBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "memory_control_too_large",
				"memory control request exceeds 16384 bytes")
		} else {
			writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		}
		return false
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return false
	}
	return true
}

func memoryIDFromRequest(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil || id == uuid.Nil {
		writeError(w, http.StatusBadRequest, "bad_id", "memory id must be a UUID")
		return uuid.Nil, false
	}
	return id, true
}

func memoryIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing_idempotency_key",
			"Idempotency-Key header is required")
		return "", false
	}
	return key, true
}

func memoryActorIDs(r *http.Request) (uuid.UUID, uuid.UUID) {
	actor := r.Context().Value(ctxActor).(*auth.User)
	tok := r.Context().Value(ctxToken).(*auth.Token)
	return actor.ID, tok.ID
}

func writeMemoryMutationResult(w http.ResponseWriter, result *memory.MutationResult) {
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, memoryMutationResponse{
		Memory:   newMemoryControlStateResponse(result.Memory),
		Event:    newMemoryEventResponse(result.Event),
		Replayed: result.Replayed,
	})
}

func newMemoryControlStateResponse(record memory.Memory) memoryControlStateResponse {
	return memoryControlStateResponse{
		ID:              record.ID,
		LifecycleStatus: record.LifecycleStatus,
		StateVersion:    record.StateVersion,
		Pinned:          record.Pinned,
		PinnedAt:        record.PinnedAt,
		UsefulCount:     record.UsefulCount,
		NotUsefulCount:  record.NotUsefulCount,
		FeedbackScore:   record.FeedbackScore,
		FeedbackCount:   record.FeedbackCount,
		FeedbackAt:      record.FeedbackAt,
		UpdatedAt:       record.UpdatedAt,
	}
}

func newMemoryEventResponse(event memory.MemoryEvent) memoryEventResponse {
	return memoryEventResponse{
		ID:               event.ID,
		MemoryID:         event.MemoryID,
		Action:           event.Action,
		ExpectedVersion:  event.ExpectedVersion,
		ResultingVersion: event.ResultingVersion,
		Reason:           event.Reason,
		CreatedAt:        event.CreatedAt,
	}
}

func writeMemoryControlError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, memory.ErrInvalidCommand):
		writeError(w, http.StatusBadRequest, "invalid_memory_control", err.Error())
	case errors.Is(err, memory.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict",
			"the idempotency key was already used with a different request")
	case errors.Is(err, memory.ErrVersionConflict):
		writeError(w, http.StatusConflict, "memory_version_conflict",
			"memory changed since it was read; reload it and retry with a new key")
	case errors.Is(err, memory.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "invalid_memory_transition", err.Error())
	case errors.Is(err, memory.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such memory")
	case errors.Is(err, memory.ErrForgotten):
		writeError(w, http.StatusGone, "memory_forgotten",
			"the memory has been forgotten")
	default:
		writeError(w, http.StatusInternalServerError, "memory_control_failed",
			"memory control operation failed")
	}
}

func optionalBoolQuery(r *http.Request, name string) (*bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	if raw != "true" && raw != "false" {
		return nil, errors.New(name + " must be true or false")
	}
	value, _ := strconv.ParseBool(raw)
	return &value, nil
}

func memoryLifecycleFilters(r *http.Request) ([]string, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("lifecycle"))
	legacy := strings.TrimSpace(r.URL.Query().Get("lifecycle_status"))
	if raw != "" && legacy != "" && raw != legacy {
		return nil, errors.New("lifecycle and lifecycle_status must not conflict")
	}
	if raw == "" {
		raw = legacy
	}
	switch raw {
	case "":
		return nil, nil
	case memory.StatusActive:
		return []string{memory.StatusActive}, nil
	case memory.StatusArchived:
		return []string{memory.StatusArchived}, nil
	case "all":
		return []string{memory.StatusActive, memory.StatusArchived}, nil
	default:
		return nil, errors.New("lifecycle must be active, archived, or all")
	}
}

func memoryKindFilters(r *http.Request) []string {
	raw := r.URL.Query()["kind"]
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, splitTags(item)...)
	}
	return out
}

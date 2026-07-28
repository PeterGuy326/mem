package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/contextpack"
	"github.com/PeterGuy326/mem/server/internal/handoff"
	"github.com/PeterGuy326/mem/server/internal/memory"
)

const maxCheckpointBodyBytes = 512 << 10

type resumeRequest struct {
	CheckpointID string `json:"checkpoint_id,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Focus        string `json:"focus,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	MaxChars     int    `json:"max_chars,omitempty"`
}

type resolvedHandoffReference struct {
	URI          string `json:"uri"`
	Relation     string `json:"relation"`
	Required     bool   `json:"required"`
	Status       string `json:"status"`
	Citation     string `json:"citation,omitempty"`
	ActualSHA256 string `json:"actual_sha256,omitempty"`
}

type resumeWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type resumeResponse struct {
	Contract      string                     `json:"contract"`
	SchemaVersion int                        `json:"schema_version"`
	Task          handoff.Task               `json:"task"`
	Checkpoint    handoff.CheckpointRecord   `json:"checkpoint"`
	Resolved      []resolvedHandoffReference `json:"resolved"`
	Missing       []resolvedHandoffReference `json:"missing"`
	Complete      bool                       `json:"complete"`
	Context       *contextpack.Pack          `json:"context,omitempty"`
	Warnings      []resumeWarning            `json:"warnings,omitempty"`
	RetrievedAt   time.Time                  `json:"retrieved_at"`
}

// handleCheckpoint persists one immutable, versioned task revision.
func (s *Server) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	if s.Handoff == nil {
		writeError(w, http.StatusServiceUnavailable, "handoff_disabled",
			"task handoff service is not configured")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing_idempotency_key",
			"Idempotency-Key header is required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCheckpointBodyBytes)
	var document handoff.HandoffV1
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "handoff_too_large",
				"checkpoint request exceeds 524288 bytes")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	taskKey, err := handoffTaskKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_task_key", err.Error())
		return
	}
	if !requireTokenPath(w, r, document.ScopePath) {
		return
	}

	// Required mem:// references must be available and hash-consistent at
	// write time. External URIs are retained as explicitly unverified.
	owner := r.Context().Value(ctxUser).(*auth.User)
	tok := r.Context().Value(ctxToken).(*auth.Token)
	ws := currentWorkspace(r)
	for _, ref := range handoffDocumentReferences(document) {
		if !strings.HasPrefix(strings.ToLower(ref.URI), "mem://") {
			continue
		}
		if !auth.HasScope(tok, auth.ScopeRead) {
			writeError(w, http.StatusForbidden, "handoff_reference_forbidden",
				"read scope is required to attach mem:// evidence to a checkpoint")
			return
		}
		resolved := s.resolveHandoffReference(r, owner.ID, ws.ID, tok.Paths, ref)
		if resolved.Status != "available" && ref.Required {
			writeError(w, http.StatusBadRequest, "invalid_handoff_reference",
				"required handoff reference is unavailable or does not match its hash")
			return
		}
	}

	actor := r.Context().Value(ctxActor).(*auth.User)
	actorID := actor.ID
	tokenID := tok.ID
	result, err := s.Handoff.Checkpoint(r.Context(), handoff.CheckpointCommand{
		WorkspaceID:      ws.ID,
		CreatedByUserID:  &actorID,
		CreatedByTokenID: &tokenID,
		TaskKey:          taskKey,
		Handoff:          document,
		IdempotencyKey:   key,
	})
	if err != nil {
		writeHandoffError(w, err)
		return
	}
	w.Header().Set("Location", fmt.Sprintf(
		"/v1/tasks/%s/checkpoints/%s",
		url.PathEscape(taskKey),
		result.Checkpoint.ID,
	))
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

// handleResume deterministically resolves one task revision, verifies every
// explicit reference, and optionally adds a bounded semantic Context Pack.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if s.Handoff == nil {
		writeError(w, http.StatusServiceUnavailable, "handoff_disabled",
			"task handoff service is not configured")
		return
	}
	var req resumeRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	var checkpointID *uuid.UUID
	if strings.TrimSpace(req.CheckpointID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(req.CheckpointID))
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_checkpoint_id",
				"checkpoint_id must be a UUID")
			return
		}
		checkpointID = &parsed
	}
	taskKey, err := handoffTaskKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_task_key", err.Error())
		return
	}
	tok := r.Context().Value(ctxToken).(*auth.Token)
	owner := r.Context().Value(ctxUser).(*auth.User)
	ws := currentWorkspace(r)
	snapshot, err := s.Handoff.Resume(r.Context(), handoff.ResumeQuery{
		WorkspaceID:  ws.ID,
		TaskKey:      taskKey,
		CheckpointID: checkpointID,
		Scope:        req.Scope,
		AllowedPaths: tok.Paths,
		Focus:        req.Focus,
		Limit:        req.Limit,
		MaxChars:     req.MaxChars,
	})
	if err != nil {
		writeHandoffError(w, err)
		return
	}

	response := resumeResponse{
		Contract:      handoff.ResumeContractName,
		SchemaVersion: handoff.SchemaVersionV1,
		Task:          snapshot.Task,
		Checkpoint:    snapshot.Checkpoint,
		Resolved:      []resolvedHandoffReference{},
		Missing:       []resolvedHandoffReference{},
		Complete:      true,
		RetrievedAt:   snapshot.RetrievedAt,
	}
	for _, ref := range snapshot.References {
		resolved := s.resolveHandoffReference(r, owner.ID, ws.ID, tok.Paths, ref)
		if resolved.Status == "available" {
			response.Resolved = append(response.Resolved, resolved)
			continue
		}
		response.Missing = append(response.Missing, resolved)
		if ref.Required {
			response.Complete = false
		}
	}

	// Restoring the deterministic checkpoint only requires read permission.
	// Related semantic evidence is an optional enrichment and therefore runs
	// only when the caller also has search permission.
	if s.Context != nil && auth.HasScope(tok, auth.ScopeSearch) {
		focus := strings.TrimSpace(req.Focus)
		if focus == "" {
			focus = resumeFocus(snapshot.Checkpoint.Handoff)
		}
		scope := strings.TrimSpace(req.Scope)
		if scope == "" {
			scope = snapshot.Task.ScopePath
		}
		pack, contextErr := s.Context.Build(r.Context(), contextpack.Request{
			UserID:       owner.ID,
			WorkspaceID:  ws.ID,
			Query:        focus,
			Scope:        scope,
			AllowedPaths: tok.Paths,
			Source:       contextpack.SourceAll,
			Limit:        req.Limit,
			MaxChars:     req.MaxChars,
		})
		if contextErr != nil {
			response.Warnings = append(response.Warnings, resumeWarning{
				Code:    "context_unavailable",
				Message: "the checkpoint is available, but related semantic evidence could not be retrieved",
			})
		} else {
			response.Context = pack
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	if s.Handoff == nil {
		writeError(w, http.StatusServiceUnavailable, "handoff_disabled",
			"task handoff service is not configured")
		return
	}
	limit, err := positiveQueryInt(r, "limit", 50, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_limit", err.Error())
		return
	}
	var after *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_after", "after must be a UUID")
			return
		}
		after = &id
	}
	tok := r.Context().Value(ctxToken).(*auth.Token)
	tasks, err := s.Handoff.ListTasks(r.Context(), handoff.ListTasksQuery{
		WorkspaceID:  currentWorkspace(r).ID,
		Scope:        r.URL.Query().Get("scope"),
		AllowedPaths: tok.Paths,
		Limit:        limit,
		After:        after,
	})
	if err != nil {
		writeHandoffError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	if s.Handoff == nil {
		writeError(w, http.StatusServiceUnavailable, "handoff_disabled",
			"task handoff service is not configured")
		return
	}
	limit, err := positiveQueryInt(r, "limit", 50, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_limit", err.Error())
		return
	}
	var before *int64
	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			writeError(w, http.StatusBadRequest, "bad_before",
				"before must be a positive checkpoint sequence")
			return
		}
		before = &value
	}
	tok := r.Context().Value(ctxToken).(*auth.Token)
	taskKey, err := handoffTaskKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_task_key", err.Error())
		return
	}
	checkpoints, err := s.Handoff.ListCheckpoints(r.Context(), handoff.ListCheckpointsQuery{
		WorkspaceID:  currentWorkspace(r).ID,
		TaskKey:      taskKey,
		Scope:        r.URL.Query().Get("scope"),
		AllowedPaths: tok.Paths,
		Limit:        limit,
		Before:       before,
	})
	if err != nil {
		writeHandoffError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkpoints": checkpoints})
}

func (s *Server) handleGetCheckpoint(w http.ResponseWriter, r *http.Request) {
	if s.Handoff == nil {
		writeError(w, http.StatusServiceUnavailable, "handoff_disabled",
			"task handoff service is not configured")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "checkpointID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_checkpoint_id",
			"checkpoint id must be a UUID")
		return
	}
	tok := r.Context().Value(ctxToken).(*auth.Token)
	taskKey, err := handoffTaskKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_task_key", err.Error())
		return
	}
	checkpoint, err := s.Handoff.GetCheckpoint(r.Context(), handoff.GetCheckpointQuery{
		WorkspaceID:  currentWorkspace(r).ID,
		CheckpointID: id,
		TaskKey:      taskKey,
		Scope:        r.URL.Query().Get("scope"),
		AllowedPaths: tok.Paths,
	})
	if err != nil {
		writeHandoffError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, checkpoint)
}

func writeHandoffError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, handoff.ErrUnsupportedVersion):
		writeError(w, http.StatusUnprocessableEntity, "unsupported_handoff_version", err.Error())
	case errors.Is(err, handoff.ErrInvalidCommand):
		writeError(w, http.StatusBadRequest, "invalid_handoff", err.Error())
	case errors.Is(err, handoff.ErrBaseRequired):
		writeError(w, http.StatusPreconditionRequired, "base_checkpoint_required", err.Error())
	case errors.Is(err, handoff.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", err.Error())
	case errors.Is(err, handoff.ErrHeadConflict):
		writeError(w, http.StatusConflict, "checkpoint_head_conflict", err.Error())
	case errors.Is(err, handoff.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "task checkpoint was not found")
	default:
		writeError(w, http.StatusInternalServerError, "handoff_failed",
			"task checkpoint operation failed")
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("request body must contain exactly one JSON value")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Chi routes against URL.RawPath so an escaped slash remains inside one route
// segment, and consequently exposes the captured parameter still escaped.
// Decode exactly once here: clients can use stable keys containing spaces,
// Unicode, or "/" without the route key diverging from handoff.task_key.
func handoffTaskKey(r *http.Request) (string, error) {
	raw := chi.URLParam(r, "taskKey")
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", errors.New("task key contains invalid URL escaping")
	}
	decoded = strings.TrimSpace(decoded)
	if decoded == "" {
		return "", errors.New("task key is required")
	}
	return decoded, nil
}

func positiveQueryInt(r *http.Request, name string, defaultValue, maxValue int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > maxValue {
		return 0, fmt.Errorf("%s must be between 1 and %d", name, maxValue)
	}
	return value, nil
}

func handoffDocumentReferences(document handoff.HandoffV1) []handoff.Reference {
	refs := make([]handoff.Reference, 0)
	appendRef := func(relation, uri, expected string, required bool) {
		refs = append(refs, handoff.Reference{
			Relation: relation, URI: uri, ExpectedSHA256: expected, Required: required,
		})
	}
	for _, decision := range document.State.Decisions {
		for _, ref := range decision.References {
			appendRef("decision_source", ref, "", false)
		}
	}
	for _, step := range document.State.NextSteps {
		for _, ref := range step.References {
			appendRef("next_step_source", ref, "", false)
		}
	}
	for _, blocker := range document.State.Blockers {
		for _, ref := range blocker.References {
			appendRef("blocker_source", ref, "", false)
		}
	}
	for _, artifact := range document.State.Artifacts {
		required := artifact.Required != nil && *artifact.Required
		appendRef("artifact", artifact.URI, artifact.SHA256, required)
	}
	return refs
}

func (s *Server) resolveHandoffReference(
	r *http.Request,
	ownerID, workspaceID uuid.UUID,
	allowedPaths []string,
	ref handoff.Reference,
) resolvedHandoffReference {
	out := resolvedHandoffReference{
		URI: ref.URI, Relation: ref.Relation, Required: ref.Required,
		Status: "unavailable",
	}
	parsed, err := url.Parse(strings.TrimSpace(ref.URI))
	if err != nil || !strings.EqualFold(parsed.Scheme, "mem") {
		out.Status = "external_unverified"
		return out
	}
	id, err := uuid.Parse(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if err != nil {
		return out
	}
	switch strings.ToLower(parsed.Host) {
	case "files":
		if s.File == nil {
			return out
		}
		record, err := s.File.Get(r.Context(), ownerID, id)
		if err != nil || !tokenAllowsPath(r, record.Path) {
			return out
		}
		out.Citation = "mem://files/" + id.String()
		out.ActualSHA256 = record.SHA256
	case "memories":
		if s.Memory == nil {
			return out
		}
		record, err := s.Memory.Get(r.Context(), memory.Query{
			WorkspaceID: workspaceID, MemoryID: id, AllowedPaths: allowedPaths,
		})
		if err != nil {
			return out
		}
		out.Citation = record.Citation()
		out.ActualSHA256 = record.ContentSHA256
	default:
		return out
	}
	if ref.ExpectedSHA256 != "" &&
		!strings.EqualFold(ref.ExpectedSHA256, out.ActualSHA256) {
		out.Status = "hash_mismatch"
		return out
	}
	out.Status = "available"
	return out
}

func resumeFocus(document handoff.HandoffV1) string {
	parts := []string{document.State.Goal, document.State.Progress.Summary}
	for _, step := range document.State.NextSteps {
		parts = append(parts, step.Summary)
	}
	return strings.Join(parts, "\n")
}

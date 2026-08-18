package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/indexgeneration"
)

func (s *Server) handleListIndexGenerations(w http.ResponseWriter, r *http.Request) {
	if s.IndexGenerations == nil {
		writeError(w, http.StatusServiceUnavailable, "index_generations_disabled",
			"index generation status is not configured")
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "bad_limit", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	builds, err := s.IndexGenerations.List(r.Context(), currentWorkspace(r).ID, limit)
	if err != nil {
		writeIndexGenerationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":           builds,
		"execution_wired": true,
	})
}

func (s *Server) handleGetIndexGeneration(w http.ResponseWriter, r *http.Request) {
	if s.IndexGenerations == nil {
		writeError(w, http.StatusServiceUnavailable, "index_generations_disabled",
			"index generation status is not configured")
		return
	}
	id, err := indexGenerationBuildID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_index_generation_id",
			"index generation build ID must be a UUID")
		return
	}
	build, err := s.IndexGenerations.Get(r.Context(), currentWorkspace(r).ID, id)
	if err != nil {
		writeIndexGenerationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"generation":      build,
		"execution_wired": true,
	})
}

func (s *Server) handleListIndexGenerationEvents(w http.ResponseWriter, r *http.Request) {
	if s.IndexGenerations == nil {
		writeError(w, http.StatusServiceUnavailable, "index_generations_disabled",
			"index generation status is not configured")
		return
	}
	id, err := indexGenerationBuildID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_index_generation_id",
			"index generation build ID must be a UUID")
		return
	}
	events, err := s.IndexGenerations.Events(r.Context(), currentWorkspace(r).ID, id)
	if err != nil {
		writeIndexGenerationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": events})
}

func (s *Server) handleCreateIndexGeneration(w http.ResponseWriter, r *http.Request) {
	if s.IndexGenerations == nil {
		writeError(w, http.StatusServiceUnavailable, "index_generations_disabled",
			"index generation status is not configured")
		return
	}
	var req struct {
		ProfileID string `json:"profile_id"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if err := requireJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	profileID := strings.TrimSpace(req.ProfileID)
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_profile_id", "profile_id is required")
		return
	}
	actor := r.Context().Value(ctxActor).(*auth.User)
	build, err := s.IndexGenerations.Create(r.Context(), currentWorkspace(r).ID, actor.ID, profileID)
	if err != nil {
		writeIndexGenerationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"generation":      build,
		"execution_wired": true,
	})
}

func (s *Server) handleCancelIndexGeneration(w http.ResponseWriter, r *http.Request) {
	s.indexGenerationBuildAction(w, r, "cancel")
}

func (s *Server) handleResumeIndexGeneration(w http.ResponseWriter, r *http.Request) {
	s.indexGenerationBuildAction(w, r, "resume")
}

func (s *Server) handleActivateIndexGeneration(w http.ResponseWriter, r *http.Request) {
	s.indexGenerationBuildAction(w, r, "activate")
}

func (s *Server) handleRollbackIndexGeneration(w http.ResponseWriter, r *http.Request) {
	s.indexGenerationBuildAction(w, r, "rollback")
}

func (s *Server) handleDiscardIndexGeneration(w http.ResponseWriter, r *http.Request) {
	s.indexGenerationBuildAction(w, r, "discard")
}

func (s *Server) indexGenerationBuildAction(w http.ResponseWriter, r *http.Request, action string) {
	if s.IndexGenerations == nil {
		writeError(w, http.StatusServiceUnavailable, "index_generations_disabled",
			"index generation status is not configured")
		return
	}
	id, err := indexGenerationBuildID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_index_generation_id",
			"index generation build ID must be a UUID")
		return
	}
	// Drain and discard body so downstream HTTP/2 connections are not leaked.
	_, _ = io.Copy(io.Discard, r.Body)

	actor := r.Context().Value(ctxActor).(*auth.User)
	ctx := r.Context()
	ws := currentWorkspace(r).ID

	var build *indexgeneration.Build
	switch action {
	case "cancel":
		build, err = s.IndexGenerations.Cancel(ctx, ws, actor.ID, id)
	case "resume":
		build, err = s.IndexGenerations.Resume(ctx, ws, actor.ID, id)
	case "activate":
		build, err = s.IndexGenerations.Activate(ctx, ws, actor.ID, id)
	case "rollback":
		build, err = s.IndexGenerations.Rollback(ctx, ws, actor.ID, id)
	case "discard":
		build, err = s.IndexGenerations.Discard(ctx, ws, actor.ID, id)
	default:
		writeError(w, http.StatusBadRequest, "bad_action", "unsupported action")
		return
	}
	if err != nil {
		writeIndexGenerationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"generation":      build,
		"execution_wired": true,
	})
}

func indexGenerationBuildID(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(strings.TrimSpace(chi.URLParam(r, "buildID")))
}

func writeIndexGenerationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, indexgeneration.ErrNotFound):
		writeError(w, http.StatusNotFound, "index_generation_not_found",
			"index generation was not found")
	case errors.Is(err, indexgeneration.ErrWorkspaceRequired):
		writeError(w, http.StatusBadRequest, "bad_index_generation_request",
			"workspace is required")
	case errors.Is(err, indexgeneration.ErrActorRequired):
		writeError(w, http.StatusBadRequest, "bad_index_generation_request",
			"actor is required")
	case errors.Is(err, indexgeneration.ErrProfileUnavailable):
		writeError(w, http.StatusBadRequest, "bad_index_generation_request",
			"profile is unavailable")
	case errors.Is(err, indexgeneration.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "index_generation_conflict",
			"state transition is not allowed")
	case errors.Is(err, indexgeneration.ErrQualityGate):
		writeError(w, http.StatusConflict, "index_generation_conflict",
			"quality gate is not satisfied")
	case errors.Is(err, indexgeneration.ErrDimensionMismatch):
		writeError(w, http.StatusConflict, "index_generation_conflict",
			"vector dimension mismatch")
	default:
		writeError(w, http.StatusServiceUnavailable, "index_generation_unavailable",
			"index generation status could not be loaded")
	}
}

package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

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
		"items": builds,
		// Mutation stays unavailable until Worker execution and active search
		// both consume the same generation identity.
		"execution_wired": false,
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
		"execution_wired": false,
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
	default:
		writeError(w, http.StatusServiceUnavailable, "index_generation_unavailable",
			"index generation status could not be loaded")
	}
}

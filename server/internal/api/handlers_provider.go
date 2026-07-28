package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/indexmeta"
	"github.com/PeterGuy326/mem/server/internal/provider"
	"github.com/PeterGuy326/mem/server/internal/queue"
)

// handleListProviders → GET /v1/providers
// Returns the user's saved provider settings (one row per kind, possibly
// empty) + a constant list of supported kinds for client UIs.
func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Provider == nil {
		writeError(w, http.StatusServiceUnavailable, "provider_disabled", "provider service not configured")
		return
	}
	settings, err := s.Provider.List(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if settings == nil {
		settings = []provider.Setting{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": settings,
		"kinds":    provider.ValidKinds,
	})
}

// handleSetProvider → PUT /v1/providers/{kind}
// Body: { "spec": "<vendor>:<model>" }
// Embedding changes are probed before persistence and refused once a corpus
// exists until versioned index generations can make the switch atomically.
func (s *Server) handleSetProvider(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Provider == nil {
		writeError(w, http.StatusServiceUnavailable, "provider_disabled", "provider service not configured")
		return
	}
	kind := chi.URLParam(r, "kind")
	var body struct {
		Spec string `json:"spec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	res, err := s.Provider.Set(r.Context(), u.ID, kind, body.Spec)
	if err != nil {
		if errors.Is(err, provider.ErrEmbeddingGeneration) {
			writeError(w, http.StatusConflict, "embedding_generation_required", err.Error())
			return
		}
		if errors.Is(err, provider.ErrEmbeddingDimConflict) {
			writeError(w, http.StatusConflict, "embedding_dimension_conflict", err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, "set_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleTestProvider → POST /v1/providers/{kind}/test
// Body (optional): { "spec": "..." } — if omitted, tests the saved spec.
func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Provider == nil {
		writeError(w, http.StatusServiceUnavailable, "provider_disabled", "provider service not configured")
		return
	}
	kind := chi.URLParam(r, "kind")
	var body struct {
		Spec string `json:"spec,omitempty"`
	}
	// Body is optional — ignore decode errors when empty.
	_ = json.NewDecoder(r.Body).Decode(&body)

	out, err := s.Provider.Test(r.Context(), u.ID, kind, body.Spec)
	if err != nil {
		if errors.Is(err, provider.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found",
				"no provider setting for kind; PUT /v1/providers/"+kind+" with spec first")
			return
		}
		writeError(w, http.StatusBadRequest, "test_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleReindexEmbedding explicitly rebuilds every file with the currently
// selected embedding provider. This is the recovery path for legacy rows whose
// historical provider could not be proven during migration.
func (s *Server) handleReindexEmbedding(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Provider == nil {
		writeError(w, http.StatusServiceUnavailable, "provider_disabled", "provider service not configured")
		return
	}
	setting, err := s.Provider.Get(r.Context(), u.ID, provider.KindEmbedding)
	if err != nil {
		if errors.Is(err, provider.ErrNotFound) {
			writeError(w, http.StatusConflict, "embedding_provider_required",
				"set an embedding provider explicitly before rebuilding")
			return
		}
		writeError(w, http.StatusInternalServerError, "provider_lookup_failed", err.Error())
		return
	}
	queueEnabled := s.Queue != nil && s.Queue.Enabled()
	if !queueEnabled && s.Indexer == nil {
		writeError(w, http.StatusServiceUnavailable, "indexer_disabled",
			"neither the durable queue nor inline indexer is configured")
		return
	}

	unlockSwitch := indexmeta.LockProviderSwitch(u.ID)
	defer unlockSwitch()

	rows, err := s.File.Pool().Query(r.Context(),
		`SELECT id FROM files WHERE user_id = $1 ORDER BY created_at`, u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "file_list_failed", err.Error())
		return
	}
	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "file_scan_failed", err.Error())
			return
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "file_scan_failed", err.Error())
		return
	}
	rows.Close()

	if _, err := s.File.Pool().Exec(r.Context(),
		`UPDATE files SET index_status = 'pending', updated_at = now() WHERE user_id = $1`,
		u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "reindex_prepare_failed", err.Error())
		return
	}

	queued := 0
	failed := 0
	for _, id := range ids {
		if queueEnabled {
			err = s.Queue.EnqueueIndexFile(r.Context(), queue.IndexFilePayload{
				FileID: id,
				UserID: u.ID,
			})
			if err != nil {
				failed++
				if s.Log != nil {
					s.Log.Error("reindex.enqueue_failed", "file_id", id, "err", err)
				}
				continue
			}
			queued++
			continue
		}
		queued++
		go func(fileID uuid.UUID) {
			if err := s.Indexer.IndexFileByID(context.Background(), fileID, u.ID); err != nil && s.Log != nil {
				s.Log.Error("reindex.inline_failed", "file_id", fileID, "err", err)
			}
		}(id)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"provider": setting.Spec,
		"files":    len(ids),
		"queued":   queued,
		"failed":   failed,
	})
}

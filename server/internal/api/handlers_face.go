package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/face"
)

// handleFaceList → GET /v1/faces
func (s *Server) handleFaceList(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Face == nil {
		writeError(w, http.StatusServiceUnavailable, "face_disabled", "face service not configured")
		return
	}
	cs, err := s.Face.List(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if cs == nil {
		cs = []face.Cluster{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"clusters": cs})
}

// handleFaceFiles → GET /v1/faces/{id}/files — photos in which this person appears.
func (s *Server) handleFaceFiles(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Face == nil {
		writeError(w, http.StatusServiceUnavailable, "face_disabled", "face service not configured")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", "invalid cluster id")
		return
	}
	files, err := s.Face.Files(r.Context(), u.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if files == nil {
		files = []face.FileRef{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cluster_id": id, "files": files})
}

// handleFaceName → POST /v1/faces/{id}/name  body: { "name": "..." }
func (s *Server) handleFaceName(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Face == nil {
		writeError(w, http.StatusServiceUnavailable, "face_disabled", "face service not configured")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	if err := s.Face.Name(r.Context(), u.ID, id, body.Name); err != nil {
		writeError(w, http.StatusBadRequest, "name_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleFaceMerge → POST /v1/faces/{a}/merge  body: { "into": "<id_b>" }
// Merges b into a — meaning a survives, b is removed; embeddings_face rows
// pointing to b are repointed to a.
func (s *Server) handleFaceMerge(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Face == nil {
		writeError(w, http.StatusServiceUnavailable, "face_disabled", "face service not configured")
		return
	}
	idA, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	var body struct {
		Into string `json:"into"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	idB, err := uuid.Parse(body.Into)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_into", err.Error())
		return
	}
	if err := s.Face.Merge(r.Context(), u.ID, idA, idB); err != nil {
		writeError(w, http.StatusBadRequest, "merge_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

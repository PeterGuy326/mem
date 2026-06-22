// Package api wires the HTTP REST surface for memd.
//
// Routes (W1):
//
//	POST   /v1/auth/login
//	POST   /v1/auth/tokens       (requires admin)
//	GET    /v1/auth/tokens       (requires admin)
//	DELETE /v1/auth/tokens/{id}  (requires admin)
//	POST   /v1/files             (requires write)
//	GET    /v1/files             (requires read)
//	GET    /v1/files/{id}        (requires read)
//	GET    /v1/files/{id}/content (requires read)
//	GET    /healthz
//	GET    /v1/version
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/ask"
	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/face"
	"github.com/PeterGuy326/mem/server/internal/file"
	"github.com/PeterGuy326/mem/server/internal/folder"
	"github.com/PeterGuy326/mem/server/internal/indexer"
	"github.com/PeterGuy326/mem/server/internal/provider"
	"github.com/PeterGuy326/mem/server/internal/queue"
	"github.com/PeterGuy326/mem/server/internal/relator"
	"github.com/PeterGuy326/mem/server/internal/search"
)

// Version is overridden by ldflags at release-build time.
var Version = "dev"

// Server bundles the dependencies a handler needs.
type Server struct {
	Auth     *auth.Service
	File     *file.Service
	Folder   *folder.Service
	Indexer  *indexer.Service  // legacy inline path (only used if Queue is nil)
	Queue    *queue.Client     // preferred: async indexing via Asynq
	Search   *search.Service   // optional; nil disables /v1/search
	Ask      *ask.Service      // optional; nil disables /v1/ask
	Provider *provider.Service // optional; nil disables /v1/providers
	Relator  *relator.Service  // optional; nil falls back to stub response
	Face     *face.Service     // optional; nil disables /v1/faces
	Log      *slog.Logger
}

// Router returns a chi.Router with all v1 routes wired.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.logRequest)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	r.Get("/v1/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"version": Version})
	})

	// Public auth
	r.Post("/v1/auth/register", s.handleRegister)
	r.Post("/v1/auth/login", s.handleLogin)

	// Token-authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware)

		r.With(s.requireScope(auth.ScopeAdmin)).Post("/v1/auth/tokens", s.handleCreateToken)
		r.With(s.requireScope(auth.ScopeAdmin)).Get("/v1/auth/tokens", s.handleListTokens)
		r.With(s.requireScope(auth.ScopeAdmin)).Delete("/v1/auth/tokens/{id}", s.handleRevokeToken)

		r.With(s.requireScope(auth.ScopeWrite)).Post("/v1/files", s.handlePutFile)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/files", s.handleListFiles)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/files/{id}", s.handleGetFile)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/files/{id}/content", s.handleGetContent)
		r.With(s.requireScope(auth.ScopeWrite)).Patch("/v1/files/{id}", s.handlePatchFile)
		r.With(s.requireScope(auth.ScopeDelete)).Delete("/v1/files/{id}", s.handleDeleteFile)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/files/{id}/related", s.handleFileRelated)

		// Search (SPEC §F3)
		r.With(s.requireScope(auth.ScopeSearch)).Post("/v1/search", s.handleSearch)

		// Ask — RAG cross-file QA (SPEC §F5)
		r.With(s.requireScope(auth.ScopeSearch)).Post("/v1/ask", s.handleAsk)
		r.With(s.requireScope(auth.ScopeSearch)).Post("/v1/ask/stream", s.handleAskStream)

		// Providers (SPEC §F8)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/providers", s.handleListProviders)
		r.With(s.requireScope(auth.ScopeAdmin)).Put("/v1/providers/{kind}", s.handleSetProvider)
		r.With(s.requireScope(auth.ScopeAdmin)).Post("/v1/providers/{kind}/test", s.handleTestProvider)

		// Timeline (SPEC §F6.3)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/timeline", s.handleTimeline)

		// Faces (SPEC §F6.1, F6.2)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/faces", s.handleFaceList)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/faces/{id}/files", s.handleFaceFiles)
		r.With(s.requireScope(auth.ScopeWrite)).Post("/v1/faces/{id}/name", s.handleFaceName)
		r.With(s.requireScope(auth.ScopeWrite)).Post("/v1/faces/{id}/merge", s.handleFaceMerge)

		// Folders (SPEC §6.3)
		r.With(s.requireScope(auth.ScopeWrite)).Post("/v1/folders", s.handleCreateFolder)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/folders", s.handleListFolders)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/folders/tree", s.handleFolderTree)
		r.With(s.requireScope(auth.ScopeWrite)).Patch("/v1/folders/{id}", s.handlePatchFolder)
		r.With(s.requireScope(auth.ScopeDelete)).Delete("/v1/folders/{id}", s.handleDeleteFolder)
	})

	return r
}

// --- middleware ---

type ctxKey string

const (
	ctxUser  ctxKey = "user"
	ctxToken ctxKey = "token"
)

func (s *Server) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.Log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
			writeError(w, http.StatusUnauthorized, "missing_bearer", "Authorization: Bearer <token> required")
			return
		}
		plaintext := header[len(prefix):]
		u, t, err := s.Auth.ResolveToken(r.Context(), plaintext)
		if err != nil {
			status := http.StatusUnauthorized
			code := "invalid_token"
			hint := "create a token via `mem token create` and pass it as Authorization: Bearer <token>"
			if errors.Is(err, auth.ErrTokenExpired) {
				code = "token_expired"
				hint = "token has expired; create a new one"
			}
			writeError(w, status, code, hint)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUser, u)
		ctx = context.WithValue(ctx, ctxToken, t)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t, _ := r.Context().Value(ctxToken).(*auth.Token)
			if !auth.HasScope(t, scope) {
				writeError(w, http.StatusForbidden, "forbidden", "token is missing scope: "+scope)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- handlers ---

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	u, err := s.Auth.VerifyPassword(r.Context(), req.Email, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
		return
	}
	s.issueSession(w, u, http.StatusOK)
}

// handleRegister provisions a new user (bcrypt-hashed password) and logs them
// straight in. Public endpoint — this is how a fresh self-hosted install gets
// its first account without touching the database by hand.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !strings.Contains(req.Email, "@") {
		writeError(w, http.StatusBadRequest, "bad_email", "a valid email is required")
		return
	}
	if len(req.Password) < 6 {
		writeError(w, http.StatusBadRequest, "weak_password", "password must be at least 6 characters")
		return
	}
	u, err := s.Auth.CreateUser(r.Context(), req.Email, req.Password)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "23505") {
			writeError(w, http.StatusConflict, "email_taken", "an account with this email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "register_failed", err.Error())
		return
	}
	s.issueSession(w, u, http.StatusCreated)
}

// issueSession mints a 24h admin-scope session token for u and writes the
// standard {user, token, token_meta} envelope. Shared by login + register.
func (s *Server) issueSession(w http.ResponseWriter, u *auth.User, status int) {
	exp := time.Now().Add(24 * time.Hour)
	plain, tok, err := s.Auth.CreateToken(context.Background(), u.ID, "session-"+time.Now().Format("20060102-150405"),
		auth.AllScopes, nil, &exp, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, status, map[string]any{
		"user":  map[string]any{"id": u.ID, "email": u.Email},
		"token": plain,
		"token_meta": map[string]any{
			"id":         tok.ID,
			"name":       tok.Name,
			"scopes":     tok.Scopes,
			"expires_at": tok.ExpiresAt,
		},
	})
}

type createTokenReq struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	Paths     []string `json:"paths,omitempty"`
	ExpiresIn string   `json:"expires_in,omitempty"` // duration string
	RedactPII bool     `json:"redact_pii,omitempty"`
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	var req createTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	var exp *time.Time
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_duration", err.Error())
			return
		}
		t := time.Now().Add(d)
		exp = &t
	}
	plain, tok, err := s.Auth.CreateToken(r.Context(), u.ID, req.Name, req.Scopes, req.Paths, exp, req.RedactPII)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         tok.ID,
		"name":       tok.Name,
		"scopes":     tok.Scopes,
		"paths":      tok.Paths,
		"expires_at": tok.ExpiresAt,
		"token":      plain, // one-time plaintext
	})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	tokens, err := s.Auth.ListTokens(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	if err := s.Auth.RevokeToken(r.Context(), u.ID, id); err != nil {
		if errors.Is(err, auth.ErrTokenNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such token")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- files ---

func (s *Server) handlePutFile(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)

	var (
		name       string
		mime       string
		targetPath string
		size       int64 = -1
		tags       []string
		body       = r.Body
	)
	stream := r.URL.Query().Get("stream") == "1"
	if stream {
		name = r.URL.Query().Get("name")
		if name == "" {
			writeError(w, http.StatusBadRequest, "missing_name", "?name=<filename> required with ?stream=1")
			return
		}
		mime = r.URL.Query().Get("mime")
		if v := r.URL.Query().Get("size"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				size = n
			}
		}
		tags = splitTags(r.URL.Query().Get("tags"))
		targetPath = r.URL.Query().Get("path")
	} else {
		// multipart/form-data — single "file" field
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "bad_form", err.Error())
			return
		}
		f, header, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "missing_file", "multipart field `file` required (or use ?stream=1)")
			return
		}
		defer f.Close()
		body = f
		name = header.Filename
		mime = header.Header.Get("Content-Type")
		size = header.Size
		if v := r.FormValue("name"); v != "" {
			name = v
		}
		if v := r.FormValue("tags"); v != "" {
			tags = splitTags(v)
		}
		targetPath = r.FormValue("path")
	}

	res, err := s.File.Put(r.Context(), u.ID, name, mime, targetPath, size, tags, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "put_failed", err.Error())
		return
	}
	status := http.StatusCreated
	if res.Deduped {
		status = http.StatusOK
	}
	// AI indexing. Skip on dedup — that file is already indexed (or queued)
	// under the original upload.
	if !res.Deduped {
		f := res.File
		switch {
		case s.Queue != nil && s.Queue.Enabled():
			// Preferred: durable Redis-backed queue (retry + crash-safe).
			if err := s.Queue.EnqueueIndexFile(r.Context(), queue.IndexFilePayload{
				FileID: f.ID, UserID: f.UserID,
			}); err != nil {
				s.Log.Error("enqueue.failed", "file_id", f.ID, "err", err)
			}
		case s.Indexer != nil:
			// Fallback: in-process goroutine. Dev-mode only — work lost on
			// crash. Kept so memd starts even when redis is down.
			go s.Indexer.IndexFile(context.Background(), f)
		}
	}
	writeJSON(w, status, map[string]any{
		"file":    res.File,
		"deduped": res.Deduped,
	})
}

func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	f, err := s.File.Get(r.Context(), u.ID, id)
	if err != nil {
		if errors.Is(err, file.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such file")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) handleGetContent(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	f, rc, err := s.File.Content(r.Context(), u.ID, id)
	if err != nil {
		if errors.Is(err, file.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such file")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", f.MIME)
	if f.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	}
	w.Header().Set("Content-Disposition", `inline; filename="`+f.Name+`"`)
	w.WriteHeader(http.StatusOK)
	// best-effort copy; client may disconnect
	_, _ = copyTo(w, rc)
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	q := r.URL.Query()
	f := file.ListFilter{
		Tag:    q.Get("tag"),
		Type:   q.Get("type"),
		Path:   q.Get("path"),
		Prefix: q.Get("prefix"),
	}
	if f.Path != "" && f.Prefix != "" {
		writeError(w, http.StatusBadRequest, "bad_filter", "use either ?path= or ?prefix=, not both")
		return
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_since", err.Error())
			return
		}
		f.Since = &t
	}
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_until", err.Error())
			return
		}
		f.Until = &t
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	if v := q.Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Page = n
		}
	}
	files, err := s.File.List(r.Context(), u.ID, f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"files": files,
		"page":  f.Page,
		"limit": f.Limit,
	})
}

// --- file PATCH + related ---

type patchFileReq struct {
	Name *string `json:"name,omitempty"`
	Path *string `json:"path,omitempty"`
}

func (s *Server) handlePatchFile(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	var req patchFileReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if req.Name == nil && req.Path == nil {
		writeError(w, http.StatusBadRequest, "no_op", "supply at least one of `name` or `path`")
		return
	}
	var (
		out *file.File
	)
	if req.Path != nil {
		out, err = s.File.Move(r.Context(), u.ID, id, *req.Path)
		if err != nil {
			writeFileMutateError(w, err)
			return
		}
	}
	if req.Name != nil {
		out, err = s.File.Rename(r.Context(), u.ID, id, *req.Name)
		if err != nil {
			writeFileMutateError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteFile implements DELETE /v1/files/{id} — removes the file row
// (cascading to its embeddings + face links) and its blob.
func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	if err := s.File.Delete(r.Context(), u.ID, id); err != nil {
		writeFileMutateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSearch implements POST /v1/search.
// Body: { "query": "...", "type": "image|doc|...", "since": "2012-01-01",
//         "until": "2012-12-31", "limit": 10 }
// Returns: { "results": [Hit, ...], "_meta": { "latency_ms": N } }
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Search == nil {
		writeError(w, http.StatusServiceUnavailable, "search_disabled",
			"search service not configured (MEM_WORKER_GRPC missing?)")
		return
	}
	var req struct {
		Query string  `json:"query"`
		Type  string  `json:"type,omitempty"`
		Route string  `json:"route,omitempty"`
		Since *string `json:"since,omitempty"`
		Until *string `json:"until,omitempty"`
		Limit int     `json:"limit,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_body", err.Error())
		return
	}
	q := search.Query{
		UserID: u.ID,
		Text:   req.Query,
		Route:  req.Route,
		Type:   req.Type,
		Limit:  req.Limit,
	}
	if req.Since != nil {
		t, err := time.Parse("2006-01-02", *req.Since)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_since", err.Error())
			return
		}
		q.Since = &t
	}
	if req.Until != nil {
		t, err := time.Parse("2006-01-02", *req.Until)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_until", err.Error())
			return
		}
		// Inclusive end-of-day.
		t = t.Add(24*time.Hour - time.Nanosecond)
		q.Until = &t
	}

	start := time.Now()
	hits, err := s.Search.Search(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusBadRequest, "search_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": hits,
		"_meta":   map[string]any{"latency_ms": time.Since(start).Milliseconds()},
	})
}

// handleFileRelated → GET /v1/files/{id}/related?type=<t>&limit=N
// Returns the top-K related files by embedding similarity.
func (s *Server) handleFileRelated(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	// Ensure the file actually belongs to the user — keeps `?file_id=` from
	// leaking existence between users.
	if _, err := s.File.Get(r.Context(), u.ID, id); err != nil {
		if errors.Is(err, file.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such file")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if s.Relator == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"file_id": id, "related": []any{}, "note": "relator not configured",
		})
		return
	}
	typ := r.URL.Query().Get("type")
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	hits, err := s.Relator.Get(r.Context(), u.ID, id, typ, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if hits == nil {
		hits = []relator.Hit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"file_id": id,
		"related": hits,
	})
}

func writeFileMutateError(w http.ResponseWriter, err error) {
	if errors.Is(err, file.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no such file")
		return
	}
	writeError(w, http.StatusBadRequest, "mutate_failed", err.Error())
}

// --- folders ---

type folderCreateReq struct {
	Path string `json:"path"`
}

func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	var req folderCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "missing_path", "supply `path` (absolute, e.g. `/Photos/2012`)")
		return
	}
	f, err := s.Folder.Create(r.Context(), u.ID, req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	if f == nil {
		// root — treat as no-op success
		writeJSON(w, http.StatusOK, map[string]any{"path": "/"})
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) handleListFolders(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	parent := r.URL.Query().Get("parent")
	if parent == "" {
		parent = "/"
	}
	folders, files, err := s.Folder.List(r.Context(), u.ID, parent)
	if err != nil {
		if errors.Is(err, folder.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such folder")
			return
		}
		writeError(w, http.StatusBadRequest, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"parent":  parent,
		"folders": folders,
		"files":   files,
	})
}

func (s *Server) handleFolderTree(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	tree, err := s.Folder.Tree(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

type folderPatchReq struct {
	Name       *string `json:"name,omitempty"`
	ParentPath *string `json:"parent_path,omitempty"`
}

func (s *Server) handlePatchFolder(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	cur, err := s.Folder.GetByID(r.Context(), u.ID, id)
	if err != nil {
		if errors.Is(err, folder.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such folder")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	var req folderPatchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if req.Name == nil && req.ParentPath == nil {
		writeError(w, http.StatusBadRequest, "no_op", "supply `name` and/or `parent_path`")
		return
	}
	curPath := cur.Path
	if req.ParentPath != nil {
		if err := s.Folder.Move(r.Context(), u.ID, curPath, *req.ParentPath); err != nil {
			writeFolderMutateError(w, err)
			return
		}
		curPath = *req.ParentPath
		if curPath == "/" {
			curPath = "/" + cur.Name
		} else {
			curPath = curPath + "/" + cur.Name
		}
	}
	if req.Name != nil {
		if err := s.Folder.Rename(r.Context(), u.ID, curPath, *req.Name); err != nil {
			writeFolderMutateError(w, err)
			return
		}
	}
	updated, err := s.Folder.GetByID(r.Context(), u.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	recursive := r.URL.Query().Get("recursive") == "true"
	cur, err := s.Folder.GetByID(r.Context(), u.ID, id)
	if err != nil {
		if errors.Is(err, folder.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such folder")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if err := s.Folder.Delete(r.Context(), u.ID, cur.Path, recursive); err != nil {
		writeFolderMutateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeFolderMutateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, folder.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such folder")
	case errors.Is(err, folder.ErrNotEmpty):
		writeError(w, http.StatusConflict, "not_empty", "folder is not empty; pass ?recursive=true to delete its contents")
	case errors.Is(err, folder.ErrCycle):
		writeError(w, http.StatusBadRequest, "cycle", "cannot move folder into itself or a descendant")
	case errors.Is(err, folder.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "a folder with that path already exists")
	case errors.Is(err, folder.ErrRootOp):
		writeError(w, http.StatusBadRequest, "root_op", "this operation is not allowed on the root folder")
	default:
		writeError(w, http.StatusBadRequest, "mutate_failed", err.Error())
	}
}

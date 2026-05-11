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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/file"
)

// Version is overridden by ldflags at release-build time.
var Version = "dev"

// Server bundles the dependencies a handler needs.
type Server struct {
	Auth *auth.Service
	File *file.Service
	Log  *slog.Logger
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
	// Dev convenience: issue a 24h admin-scope token so the CLI can immediately
	// operate. Real OAuth lives in a future phase.
	exp := time.Now().Add(24 * time.Hour)
	plain, tok, err := s.Auth.CreateToken(r.Context(), u.ID, "session-"+time.Now().Format("20060102-150405"),
		auth.AllScopes, nil, &exp, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
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
		name string
		mime string
		size int64 = -1
		tags []string
		body = r.Body
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
	}

	res, err := s.File.Put(r.Context(), u.ID, name, mime, size, tags, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "put_failed", err.Error())
		return
	}
	status := http.StatusCreated
	if res.Deduped {
		status = http.StatusOK
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
		Tag:  q.Get("tag"),
		Type: q.Get("type"),
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

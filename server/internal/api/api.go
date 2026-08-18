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
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/contextpack"
	"github.com/PeterGuy326/mem/server/internal/durablecontext"
	"github.com/PeterGuy326/mem/server/internal/entitlement"
	"github.com/PeterGuy326/mem/server/internal/face"
	"github.com/PeterGuy326/mem/server/internal/file"
	"github.com/PeterGuy326/mem/server/internal/folder"
	"github.com/PeterGuy326/mem/server/internal/handoff"
	"github.com/PeterGuy326/mem/server/internal/indexer"
	"github.com/PeterGuy326/mem/server/internal/indexgeneration"
	"github.com/PeterGuy326/mem/server/internal/memory"
	"github.com/PeterGuy326/mem/server/internal/pathx"
	"github.com/PeterGuy326/mem/server/internal/provider"
	"github.com/PeterGuy326/mem/server/internal/queue"
	"github.com/PeterGuy326/mem/server/internal/relator"
	"github.com/PeterGuy326/mem/server/internal/search"
	"github.com/PeterGuy326/mem/server/internal/workspace"
	"github.com/PeterGuy326/mem/server/internal/workspacetransfer"
)

// Version is overridden by ldflags at release-build time.
var Version = "dev"

// MemoryService is the write/read port used by HTTP handlers. Keeping the
// handlers behind an interface makes authorization and error mapping testable
// without weakening the concrete persistence service.
type MemoryService interface {
	Remember(context.Context, memory.Command) (*memory.RememberResult, error)
	Get(context.Context, memory.Query) (*memory.Memory, error)
	List(context.Context, memory.ListQuery) (*memory.ListResult, error)
	Feedback(context.Context, memory.FeedbackCommand) (*memory.MutationResult, error)
	Archive(context.Context, memory.LifecycleCommand) (*memory.MutationResult, error)
	Restore(context.Context, memory.LifecycleCommand) (*memory.MutationResult, error)
	Forget(context.Context, memory.ForgetCommand) (*memory.ForgetResult, error)
	CreateRelation(context.Context, memory.CreateRelationCommand) (*memory.CreateRelationResult, error)
	ListRelations(context.Context, memory.ListRelationsQuery) ([]memory.Relation, error)
}

// DurableContextService is the scoped durable-context port (mem#70). Handlers
// stay behind an interface so contract pinning, denial, and degradation
// mapping are testable without a database.
type DurableContextService interface {
	Grant(context.Context, durablecontext.GrantCommand) (*durablecontext.Grant, error)
	Revoke(context.Context, durablecontext.RevokeCommand) (*durablecontext.Grant, error)
	ListGrantViews(context.Context, durablecontext.ListGrantsQuery) ([]durablecontext.GrantView, error)
	Recall(context.Context, durablecontext.RecallQuery) (*durablecontext.RecallResult, error)
	Get(context.Context, durablecontext.GetQuery) (*durablecontext.RecallHit, error)
}

// WorkspaceTransferService is the portable full-workspace import/export port.
// HTTP owns transport limits and temporary-file handling; the concrete service
// owns bundle validation and the database/object-store transaction boundary.
type WorkspaceTransferService interface {
	Export(
		context.Context,
		workspacetransfer.ExportRequest,
	) (*workspacetransfer.ExportResult, error)
	Import(
		context.Context,
		workspacetransfer.ImportRequest,
	) (*workspacetransfer.ImportResult, error)
	ImportHistory(
		context.Context,
		uuid.UUID,
		int,
	) ([]workspacetransfer.ImportHistoryEntry, error)
}

// SearchService keeps the provider invocation behind a testable port. Replay
// reconstructs a prior safe-reference result without another provider call.
type SearchService interface {
	Search(context.Context, search.Query) ([]search.Hit, error)
	EmbeddingSpec(context.Context, uuid.UUID) (string, error)
	Replay(
		context.Context,
		search.Query,
		[]entitlement.ReplayReference,
	) ([]search.Hit, error)
}

type EntitlementService interface {
	Ready(context.Context) error
	Summary(context.Context, uuid.UUID) (entitlement.Summary, error)
	Reserve(
		context.Context,
		entitlement.ReserveCommand,
	) (*entitlement.Reservation, error)
	Finalize(
		context.Context,
		uuid.UUID,
		[]entitlement.ReplayReference,
	) (entitlement.Summary, error)
	Release(context.Context, uuid.UUID) (entitlement.Summary, error)
	MarkIndeterminate(context.Context, uuid.UUID) (entitlement.Summary, error)
}

// AIProfileService is the workspace-scoped, server-owned model-routing port.
// It deliberately accepts profile IDs only: handler callers never supply a
// provider base URL, model name, credential, or prompt.
type AIProfileService interface {
	List() []aiprofile.Definition
	Get(context.Context, uuid.UUID) (*aiprofile.Selection, error)
	Select(context.Context, uuid.UUID, uuid.UUID, string) (*aiprofile.Selection, error)
}

// IndexGenerationService exposes the versioned generation lifecycle to HTTP
// handlers. Read methods are available to all authenticated clients; mutation
// methods require admin scope and workspace provider-write authorization.
type IndexGenerationService interface {
	List(context.Context, uuid.UUID, int) ([]indexgeneration.Build, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (*indexgeneration.Build, error)
	Events(context.Context, uuid.UUID, uuid.UUID) ([]indexgeneration.Event, error)
	Create(context.Context, uuid.UUID, uuid.UUID, string) (*indexgeneration.Build, error)
	Cancel(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*indexgeneration.Build, error)
	Resume(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*indexgeneration.Build, error)
	Activate(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*indexgeneration.Build, error)
	Rollback(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*indexgeneration.Build, error)
	Discard(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*indexgeneration.Build, error)
}

// Server bundles the dependencies a handler needs.
type Server struct {
	Auth                     *auth.Service
	File                     *file.Service
	FileAnnotations          FileAnnotationService
	Folder                   *folder.Service
	Indexer                  *indexer.Service // legacy inline path (only used if Queue is nil)
	Queue                    *queue.Client    // preferred: async indexing via Asynq
	Search                   SearchService    // optional; nil disables /v1/search
	Context                  *contextpack.Service
	Memory                   MemoryService
	DurableContext           DurableContextService
	Handoff                  handoff.ServicePort
	Provider                 *provider.Service      // optional; nil disables /v1/providers
	AIProfiles               AIProfileService       // optional; nil disables workspace profile API
	IndexGenerations         IndexGenerationService // optional; nil disables generation status API
	Relator                  *relator.Service       // optional; nil falls back to stub response
	Face                     *face.Service          // optional; nil disables /v1/faces
	Workspace                *workspace.Service
	WorkspaceTransfer        WorkspaceTransferService
	WorkspaceTransferTimeout time.Duration
	WorkspaceBundleMaxBytes  int64
	WorkspaceTransferGate    chan struct{}
	WorkspaceTransferTmpDir  string
	DeploymentMode           string
	// ManagedEmbeddingProvider is retained for compatibility with focused
	// tests and legacy wiring. Production also supplies the complete exact
	// generation allow-set in ManagedEmbeddingProviders.
	ManagedEmbeddingProvider  string
	ManagedEmbeddingProviders []string
	Entitlements              EntitlementService
	RegistrationMode          string
	SessionTTL                time.Duration
	CORSOrigins               []string // allowed browser origins; empty disables CORS
	Log                       *slog.Logger
}

// Router returns a chi.Router with all v1 routes wired.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	if len(s.CORSOrigins) > 0 {
		r.Use(s.corsMiddleware)
	}
	r.Use(s.logRequest)
	r.Use(middleware.Recoverer)
	r.Use(s.requestTimeoutMiddleware)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	r.Get("/readyz", s.handleReadiness)
	r.Get("/v1/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"version": Version})
	})

	// Public auth
	r.Post("/v1/auth/register", s.handleRegister)
	r.Post("/v1/auth/login", s.handleLogin)

	// Token-authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware)
		r.Use(s.workspaceMiddleware)

		r.Get("/v1/capabilities", s.handleCapabilities)
		r.Get("/v1/workspaces", s.handleListWorkspaces)
		r.Get("/v1/workspaces/current", s.handleCurrentWorkspace)
		r.With(s.requireScope(auth.ScopeRead)).Get(
			"/v1/workspaces/current/ai-profile", s.handleGetAIProfile,
		)
		r.With(
			s.requireScope(auth.ScopeAdmin),
			s.requireWorkspaceProviderWrite,
			s.requireUnrestrictedPaths,
		).Put("/v1/workspaces/current/ai-profile", s.handleSelectAIProfile)
		r.With(s.requireScope(auth.ScopeRead)).Get(
			"/v1/workspaces/current/index-generations", s.handleListIndexGenerations,
		)
		r.With(s.requireScope(auth.ScopeRead)).Get(
			"/v1/workspaces/current/index-generations/{buildID}",
			s.handleGetIndexGeneration,
		)
		r.With(s.requireScope(auth.ScopeRead)).Get(
			"/v1/workspaces/current/index-generations/{buildID}/events",
			s.handleListIndexGenerationEvents,
		)
		r.With(
			s.requireScope(auth.ScopeAdmin),
			s.requireWorkspaceProviderWrite,
		).Post("/v1/workspaces/current/index-generations", s.handleCreateIndexGeneration)
		r.With(
			s.requireScope(auth.ScopeAdmin),
			s.requireWorkspaceProviderWrite,
		).Post("/v1/workspaces/current/index-generations/{buildID}/cancel", s.handleCancelIndexGeneration)
		r.With(
			s.requireScope(auth.ScopeAdmin),
			s.requireWorkspaceProviderWrite,
		).Post("/v1/workspaces/current/index-generations/{buildID}/resume", s.handleResumeIndexGeneration)
		r.With(
			s.requireScope(auth.ScopeAdmin),
			s.requireWorkspaceProviderWrite,
		).Post("/v1/workspaces/current/index-generations/{buildID}/activate", s.handleActivateIndexGeneration)
		r.With(
			s.requireScope(auth.ScopeAdmin),
			s.requireWorkspaceProviderWrite,
		).Post("/v1/workspaces/current/index-generations/{buildID}/rollback", s.handleRollbackIndexGeneration)
		r.With(
			s.requireScope(auth.ScopeAdmin),
			s.requireWorkspaceProviderWrite,
		).Post("/v1/workspaces/current/index-generations/{buildID}/discard", s.handleDiscardIndexGeneration)
		r.With(s.requireScope(auth.ScopeRead)).
			Get("/v1/entitlements/current", s.handleEntitlementSummary)
		r.With(
			s.requireScope(auth.ScopeRead),
			s.requireScope(auth.ScopeAdmin),
			s.requireUnrestrictedPaths,
			s.requireWorkspaceTransfer,
			s.requireWorkspaceTransferCapacity,
		).Get("/v1/workspaces/current/export", s.handleWorkspaceExport)
		r.With(
			s.requireScope(auth.ScopeWrite),
			s.requireScope(auth.ScopeAdmin),
			s.requireUnrestrictedPaths,
			s.requireWorkspaceTransfer,
			s.requireWorkspaceTransferCapacity,
		).Post("/v1/workspaces/current/import", s.handleWorkspaceImport)
		r.With(
			s.requireScope(auth.ScopeRead),
			s.requireScope(auth.ScopeAdmin),
			s.requireUnrestrictedPaths,
			s.requireWorkspaceTransfer,
		).Get("/v1/workspaces/current/imports", s.handleWorkspaceImportHistory)

		r.With(s.requireScope(auth.ScopeAdmin)).Post("/v1/auth/tokens", s.handleCreateToken)
		r.With(s.requireScope(auth.ScopeAdmin)).Get("/v1/auth/tokens", s.handleListTokens)
		r.With(s.requireScope(auth.ScopeAdmin)).Delete("/v1/auth/tokens/{id}", s.handleRevokeToken)

		r.With(s.requireScope(auth.ScopeWrite)).Post("/v1/files", s.handlePutFile)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/files", s.handleListFiles)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/files/{id}", s.handleGetFile)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/files/{id}/content", s.handleGetContent)
		r.With(s.requireScope(auth.ScopeWrite)).Patch("/v1/files/{id}", s.handlePatchFile)
		r.With(
			s.requireScope(auth.ScopeRead),
			s.requireScope(auth.ScopeWrite),
		).Put(
			"/v1/files/{fileID}/annotations/{annotationID}",
			s.handleDecideFileAnnotation,
		)
		r.With(s.requireScope(auth.ScopeDelete), s.requireWorkspaceDelete).Delete("/v1/files/{id}", s.handleDeleteFile)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/files/{id}/related", s.handleFileRelated)
		r.With(s.requireScope(auth.ScopeWrite), s.requireUnrestrictedPaths).Post("/v1/relations/rebuild", s.handleRebuildRelations)

		// Structured Agent memory. API owns idempotency and provenance;
		// CLI and MCP remain adapters over this canonical contract.
		r.With(s.requireScope(auth.ScopeWrite)).Post("/v1/memories", s.handleRemember)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/memories", s.handleListMemories)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/memories/{id}", s.handleGetMemory)
		r.With(
			s.requireScope(auth.ScopeRead),
			s.requireScope(auth.ScopeWrite),
		).Post("/v1/memories/{id}/feedback", s.handleMemoryFeedback)
		r.With(
			s.requireScope(auth.ScopeRead),
			s.requireScope(auth.ScopeWrite),
		).Post("/v1/memories/{id}/archive", s.handleArchiveMemory)
		r.With(
			s.requireScope(auth.ScopeRead),
			s.requireScope(auth.ScopeWrite),
		).Post("/v1/memories/{id}/restore", s.handleRestoreMemory)
		r.With(
			s.requireScope(auth.ScopeDelete),
			s.requireWorkspaceDelete,
		).Post("/v1/memories/{id}/forget", s.handleForgetMemory)

		// Memory relations (correction/supersede/occurrence).
		r.With(
			s.requireScope(auth.ScopeRead),
			s.requireScope(auth.ScopeWrite),
		).Post("/v1/memory-relations", s.handleCreateMemoryRelation)
		r.With(
			s.requireScope(auth.ScopeRead),
		).Get("/v1/memories/{id}/relations", s.handleListMemoryRelations)

		// Scoped durable context (mem#70): version-pinned, read-only resume
		// over explicitly approved memories. Allowlist mutation is admin
		// policy; recall and get are read scope only.
		r.With(s.requireScope(auth.ScopeRead)).Post(
			"/v1/durable-context/recall", s.handleDurableContextRecall,
		)
		r.With(s.requireScope(auth.ScopeRead)).Get(
			"/v1/durable-context/memories/{id}", s.handleDurableContextGetMemory,
		)
		r.With(s.requireScope(auth.ScopeAdmin)).Post(
			"/v1/durable-context/grants", s.handleCreateDurableContextGrant,
		)
		r.With(s.requireScope(auth.ScopeAdmin)).Get(
			"/v1/durable-context/grants", s.handleListDurableContextGrants,
		)
		r.With(s.requireScope(auth.ScopeAdmin)).Post(
			"/v1/durable-context/grants/{grantID}/revoke", s.handleRevokeDurableContextGrant,
		)

		// Versioned, vendor-neutral task checkpoints. The task key is stable
		// across Claude Code, Codex and other Agent hosts.
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/tasks", s.handleListTasks)
		r.With(s.requireScope(auth.ScopeWrite)).Post(
			"/v1/tasks/{taskKey}/checkpoints", s.handleCheckpoint,
		)
		r.With(s.requireScope(auth.ScopeRead)).Get(
			"/v1/tasks/{taskKey}/checkpoints", s.handleListCheckpoints,
		)
		r.With(s.requireScope(auth.ScopeRead)).Get(
			"/v1/tasks/{taskKey}/checkpoints/{checkpointID}", s.handleGetCheckpoint,
		)
		r.With(s.requireScope(auth.ScopeRead)).Post(
			"/v1/tasks/{taskKey}/resume", s.handleResume,
		)

		// Search (SPEC §F3)
		r.With(s.requireScope(auth.ScopeSearch)).Post("/v1/search", s.handleSearch)

		// Context — bounded, source-verifiable evidence for external agents.
		r.With(s.requireScope(auth.ScopeSearch)).Post("/v1/context", s.handleContext)

		// Providers (SPEC §F8)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/providers", s.handleListProviders)
		r.With(
			s.requireScope(auth.ScopeAdmin),
			s.requireWorkspaceProviderWrite,
			s.requireUnrestrictedPaths,
		).Put("/v1/providers/{kind}", s.handleSetProvider)
		r.With(
			s.requireScope(auth.ScopeAdmin),
			s.requireWorkspaceProviderWrite,
			s.requireUnrestrictedPaths,
		).Post("/v1/providers/{kind}/test", s.handleTestProvider)
		r.With(
			s.requireScope(auth.ScopeAdmin),
			s.requireWorkspaceProviderWrite,
			s.requireUnrestrictedPaths,
		).Post("/v1/providers/embedding/reindex", s.handleReindexEmbedding)

		// Timeline (SPEC §F6.3)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/timeline", s.handleTimeline)

		// Faces (SPEC §F6.1, F6.2)
		r.With(s.requireScope(auth.ScopeRead), s.requireUnrestrictedPaths).Get("/v1/faces", s.handleFaceList)
		r.With(s.requireScope(auth.ScopeRead), s.requireUnrestrictedPaths).Get("/v1/faces/{id}/files", s.handleFaceFiles)
		r.With(s.requireScope(auth.ScopeWrite), s.requireUnrestrictedPaths).Post("/v1/faces/{id}/name", s.handleFaceName)
		r.With(s.requireScope(auth.ScopeWrite), s.requireUnrestrictedPaths).Post("/v1/faces/{id}/merge", s.handleFaceMerge)

		// Folders (SPEC §6.3)
		r.With(s.requireScope(auth.ScopeWrite)).Post("/v1/folders", s.handleCreateFolder)
		r.With(s.requireScope(auth.ScopeRead)).Get("/v1/folders", s.handleListFolders)
		r.With(s.requireScope(auth.ScopeRead), s.requireUnrestrictedPaths).Get("/v1/folders/tree", s.handleFolderTree)
		r.With(s.requireScope(auth.ScopeWrite)).Patch("/v1/folders/{id}", s.handlePatchFolder)
		r.With(s.requireScope(auth.ScopeDelete), s.requireWorkspaceDelete).Delete("/v1/folders/{id}", s.handleDeleteFolder)
	})

	return r
}

// --- middleware ---

type ctxKey string

const (
	ctxActor     ctxKey = "actor"
	ctxUser      ctxKey = "user"
	ctxToken     ctxKey = "token"
	ctxWorkspace ctxKey = "workspace"
)

// corsMiddleware allows browser clients served from a different origin (split
// front/back deployment) to call the API. Auth is Bearer-token based — no
// cookies — so Access-Control-Allow-Credentials is intentionally not set.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.corsOriginAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers",
					"Authorization, Content-Type, Idempotency-Key, X-Workspace-ID")
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) corsOriginAllowed(origin string) bool {
	for _, o := range s.CORSOrigins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

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

// requestTimeoutMiddleware keeps the bounded 60-second default for ordinary
// API calls while allowing a separately configured, longer transfer budget.
func (s *Server) requestTimeoutMiddleware(next http.Handler) http.Handler {
	bounded := middleware.Timeout(60 * time.Second)(next)
	transferTimeout := s.WorkspaceTransferTimeout
	if transferTimeout <= 0 {
		transferTimeout = 30 * time.Minute
	}
	transferBounded := middleware.Timeout(transferTimeout)(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workspaces/current/export", "/v1/workspaces/current/import":
			transferBounded.ServeHTTP(w, r)
		default:
			bounded.ServeHTTP(w, r)
		}
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
			hint := "create a token via `mem auth token create` and pass it as Authorization: Bearer <token>"
			if errors.Is(err, auth.ErrTokenExpired) {
				code = "token_expired"
				hint = "token has expired; create a new one"
			}
			writeError(w, status, code, hint)
			return
		}
		if t.RedactPII {
			// Older releases persisted this flag without applying redaction.
			// Refuse the token rather than returning unredacted source data under
			// a misleading privacy promise.
			writeError(w, http.StatusNotImplemented, "pii_redaction_unavailable",
				"this token requests PII redaction, which is not implemented; revoke it and create a scoped token")
			return
		}
		ctx := context.WithValue(r.Context(), ctxActor, u)
		ctx = context.WithValue(ctx, ctxUser, u)
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

// requireUnrestrictedPaths protects aggregate operations whose current
// implementation cannot safely project a partial virtual tree/entity graph.
// Path-restricted Agent tokens should use file/search/context operations.
func (s *Server) requireUnrestrictedPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tokenHasPathRestrictions(r) {
			writeError(w, http.StatusForbidden, "path_forbidden",
				"operation requires an unrestricted token path")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func tokenHasPathRestrictions(r *http.Request) bool {
	tok, _ := r.Context().Value(ctxToken).(*auth.Token)
	if tok == nil || len(tok.Paths) == 0 {
		return false
	}
	for _, p := range tok.Paths {
		if p == pathx.Root {
			return false
		}
	}
	return true
}

func tokenAllowsPath(r *http.Request, raw string) bool {
	if !tokenHasPathRestrictions(r) {
		return true
	}
	candidate, err := pathx.Normalize(raw)
	if err != nil {
		return false
	}
	tok := r.Context().Value(ctxToken).(*auth.Token)
	for _, allowed := range tok.Paths {
		if allowed == "" {
			continue
		}
		norm, err := pathx.Normalize(allowed)
		if err == nil && pathx.IsDescendantOrSelf(candidate, norm) {
			return true
		}
	}
	return false
}

func requireTokenPath(w http.ResponseWriter, r *http.Request, raw string) bool {
	if tokenAllowsPath(r, raw) {
		return true
	}
	writeError(w, http.StatusForbidden, "path_forbidden",
		"token is not authorized for virtual path "+raw)
	return false
}

func delegatedTokenPaths(parent, requested []string) ([]string, error) {
	parentRestricted := true
	if len(parent) == 0 {
		parentRestricted = false
	}
	for _, p := range parent {
		if p == pathx.Root {
			parentRestricted = false
			break
		}
	}
	if !parentRestricted {
		return requested, nil
	}
	if len(requested) == 0 {
		return append([]string(nil), parent...), nil
	}

	out := make([]string, 0, len(requested))
	for _, raw := range requested {
		if raw == "" {
			return nil, errors.New("delegated token path cannot be empty")
		}
		child, err := pathx.Normalize(raw)
		if err != nil {
			return nil, err
		}
		allowed := false
		for _, parentPath := range parent {
			if parentPath == "" {
				continue
			}
			normParent, err := pathx.Normalize(parentPath)
			if err == nil && pathx.IsDescendantOrSelf(child, normParent) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("delegated path %s exceeds caller authorization", child)
		}
		out = append(out, child)
	}
	return out, nil
}

func hideUnauthorizedFile(w http.ResponseWriter, r *http.Request, raw string) bool {
	if tokenAllowsPath(r, raw) {
		return true
	}
	writeError(w, http.StatusNotFound, "not_found", "no such file")
	return false
}

func (s *Server) workspaceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Workspace == nil {
			writeError(w, http.StatusServiceUnavailable, "workspace_disabled", "workspace service not configured")
			return
		}
		actor := r.Context().Value(ctxActor).(*auth.User)
		tok := r.Context().Value(ctxToken).(*auth.Token)
		requested, err := requestedWorkspace(r.Header.Get("X-Workspace-ID"), tok.WorkspaceID)
		if err != nil {
			if errors.Is(err, errTokenWorkspaceMismatch) {
				writeError(w, http.StatusForbidden, "token_workspace_forbidden",
					"token is bound to a different workspace")
				return
			}
			writeError(w, http.StatusBadRequest, "bad_workspace_id", "X-Workspace-ID must be a UUID")
			return
		}
		ws, err := s.Workspace.Resolve(r.Context(), actor.ID, requested)
		if err != nil {
			if errors.Is(err, workspace.ErrForbidden) {
				writeError(w, http.StatusForbidden, "workspace_forbidden", "workspace membership required")
				return
			}
			writeError(w, http.StatusInternalServerError, "workspace_resolve_failed", err.Error())
			return
		}
		owner := &auth.User{ID: ws.ResourceOwnerUserID}
		ctx := context.WithValue(r.Context(), ctxWorkspace, ws)
		ctx = context.WithValue(ctx, ctxUser, owner)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

var errTokenWorkspaceMismatch = errors.New("token workspace mismatch")

func requestedWorkspace(raw string, bound *uuid.UUID) (*uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return bound, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}
	if bound != nil && id != *bound {
		return nil, errTokenWorkspaceMismatch
	}
	return &id, nil
}

func (s *Server) requireWorkspaceDelete(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws := currentWorkspace(r)
		if !workspace.CanDelete(ws.Role) {
			writeError(w, http.StatusForbidden, "forbidden", "workspace role does not allow delete")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireWorkspaceProviderWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws := currentWorkspace(r)
		if !workspace.CanModifyProvider(ws.Role) {
			writeError(w, http.StatusForbidden, "forbidden", "workspace role does not allow provider changes")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireWorkspaceTransfer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch currentWorkspace(r).Role {
		case workspace.RoleOwner, workspace.RoleAdmin:
			next.ServeHTTP(w, r)
		default:
			writeError(
				w,
				http.StatusForbidden,
				"forbidden",
				"workspace role does not allow workspace transfer",
			)
		}
	})
}

func (s *Server) requireWorkspaceTransferCapacity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.WorkspaceTransferGate == nil {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case s.WorkspaceTransferGate <- struct{}{}:
			defer func() { <-s.WorkspaceTransferGate }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "5")
			writeError(
				w,
				http.StatusTooManyRequests,
				"workspace_transfer_busy",
				"workspace transfer capacity is full; retry later",
			)
		}
	})
}

func currentWorkspace(r *http.Request) *workspace.Workspace {
	return r.Context().Value(ctxWorkspace).(*workspace.Workspace)
}

func resourceOwnerID(r *http.Request) uuid.UUID {
	return currentWorkspace(r).ResourceOwnerUserID
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
	u, err := s.Auth.CreateUserWithRegistration(r.Context(), req.Email, req.Password, s.RegistrationMode)
	if err != nil {
		if errors.Is(err, auth.ErrRegistrationDisabled) {
			writeError(w, http.StatusForbidden, "registration_disabled", "registration is disabled by MEM_REGISTRATION_MODE")
			return
		}
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "23505") {
			writeError(w, http.StatusConflict, "email_taken", "an account with this email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "register_failed", err.Error())
		return
	}
	s.issueSession(w, u, http.StatusCreated)
}

// issueSession mints an admin-scope session token using the configured TTL and
// writes the standard {user, token, token_meta} envelope.
func (s *Server) issueSession(w http.ResponseWriter, u *auth.User, status int) {
	ttl := s.SessionTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	exp := time.Now().Add(ttl)
	plain, tok, err := s.Auth.CreateToken(context.Background(), u.ID, nil, "session-"+time.Now().Format("20060102-150405"),
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
	u := r.Context().Value(ctxActor).(*auth.User)
	var req createTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if req.RedactPII {
		writeError(w, http.StatusUnprocessableEntity, "pii_redaction_unavailable",
			"redact_pii is not implemented; use workspace/path scopes and do not rely on silent redaction")
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
	callerToken := r.Context().Value(ctxToken).(*auth.Token)
	// Session tokens are intentionally unbound root credentials used by a
	// person to mint long-lived Agent tokens. A workspace-bound Agent token
	// may delegate, but never outside its own path or beyond its own lifetime.
	if callerToken.WorkspaceID != nil {
		delegated, err := delegatedTokenPaths(callerToken.Paths, req.Paths)
		if err != nil {
			writeError(w, http.StatusForbidden, "token_delegation_forbidden", err.Error())
			return
		}
		req.Paths = delegated
		if callerToken.ExpiresAt != nil {
			if exp == nil {
				inherited := *callerToken.ExpiresAt
				exp = &inherited
			} else if exp.After(*callerToken.ExpiresAt) {
				writeError(w, http.StatusForbidden, "token_delegation_forbidden",
					"delegated token cannot outlive the caller token")
				return
			}
		}
	}
	ws := currentWorkspace(r)
	plain, tok, err := s.Auth.CreateToken(
		r.Context(), u.ID, &ws.ID, req.Name, req.Scopes, req.Paths, exp, req.RedactPII,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":           tok.ID,
		"name":         tok.Name,
		"scopes":       tok.Scopes,
		"paths":        tok.Paths,
		"workspace_id": tok.WorkspaceID,
		"expires_at":   tok.ExpiresAt,
		"token":        plain, // one-time plaintext
	})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxActor).(*auth.User)
	tokens, err := s.Auth.ListTokens(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	caller := r.Context().Value(ctxToken).(*auth.Token)
	if caller.WorkspaceID != nil {
		filtered := tokens[:0]
		for _, token := range tokens {
			if token.WorkspaceID != nil && *token.WorkspaceID == *caller.WorkspaceID {
				filtered = append(filtered, token)
			}
		}
		tokens = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxActor).(*auth.User)
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	caller := r.Context().Value(ctxToken).(*auth.Token)
	if caller.WorkspaceID != nil {
		err = s.Auth.RevokeTokenInWorkspace(r.Context(), u.ID, id, *caller.WorkspaceID)
	} else {
		err = s.Auth.RevokeToken(r.Context(), u.ID, id)
	}
	if err != nil {
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
		name              string
		mime              string
		targetPath        string
		size              int64 = -1
		tags              []string
		sourceMetadataRaw string
		body              = r.Body
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
		// Keep precise capture location out of URLs, which are routinely
		// retained by reverse proxies and access logs.
		sourceMetadataRaw = r.Header.Get(sourceMetadataHeader)
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
		sourceMetadataRaw = r.FormValue("source_metadata")
	}
	normalizedTarget, err := pathx.Normalize(targetPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_path", err.Error())
		return
	}
	if !requireTokenPath(w, r, normalizedTarget) {
		return
	}
	sourceMetadata, err := ParseSourceMetadataJSON(sourceMetadataRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_source_metadata", err.Error())
		return
	}

	res, err := s.File.Put(
		r.Context(),
		u.ID,
		name,
		mime,
		targetPath,
		size,
		tags,
		sourceMetadata,
		body,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, "put_failed", err.Error())
		return
	}
	if res.Deduped && !tokenAllowsPath(r, res.File.Path) {
		writeError(w, http.StatusForbidden, "path_forbidden",
			"matching content already exists outside this token's authorized paths")
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
	if !hideUnauthorizedFile(w, r, f.Path) {
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
	meta, err := s.File.Get(r.Context(), u.ID, id)
	if err != nil {
		if errors.Is(err, file.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such file")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !hideUnauthorizedFile(w, r, meta.Path) {
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
		Tag:          q.Get("tag"),
		Type:         q.Get("type"),
		Path:         q.Get("path"),
		Prefix:       q.Get("prefix"),
		AllowedPaths: r.Context().Value(ctxToken).(*auth.Token).Paths,
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
	current, err := s.File.Get(r.Context(), u.ID, id)
	if err != nil {
		writeFileMutateError(w, err)
		return
	}
	if !hideUnauthorizedFile(w, r, current.Path) {
		return
	}
	if req.Name == nil && req.Path == nil {
		writeError(w, http.StatusBadRequest, "no_op", "supply at least one of `name` or `path`")
		return
	}
	if req.Path != nil {
		dest, err := pathx.Normalize(*req.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_path", err.Error())
			return
		}
		if !requireTokenPath(w, r, dest) {
			return
		}
	}
	var out *file.File
	if req.Path != nil && req.Name != nil {
		out, err = s.File.Relocate(
			r.Context(),
			u.ID,
			id,
			current.Path,
			*req.Path,
			*req.Name,
		)
		if err != nil {
			writeFileMutateError(w, err)
			return
		}
	} else if req.Path != nil {
		out, err = s.File.Move(r.Context(), u.ID, id, *req.Path)
		if err != nil {
			writeFileMutateError(w, err)
			return
		}
	} else {
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
	f, err := s.File.Get(r.Context(), u.ID, id)
	if err != nil {
		writeFileMutateError(w, err)
		return
	}
	if !hideUnauthorizedFile(w, r, f.Path) {
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
//
//	"until": "2012-12-31", "limit": 10 }
//
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
		Scope string  `json:"scope,omitempty"`
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
	if strings.TrimSpace(req.Query) == "" {
		writeError(w, http.StatusBadRequest, "bad_query", "query is required")
		return
	}
	switch req.Route {
	case "", search.RouteAuto, search.RouteText, search.RouteVisual:
	default:
		writeError(w, http.StatusBadRequest, "bad_route", "route must be auto, text, or visual")
		return
	}
	scope, err := pathx.Normalize(req.Scope)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_scope", err.Error())
		return
	}
	tok := r.Context().Value(ctxToken).(*auth.Token)
	q := search.Query{
		UserID:       u.ID,
		Text:         req.Query,
		Route:        req.Route,
		Type:         req.Type,
		PathPrefix:   scope,
		AllowedPaths: tok.Paths,
		Limit:        req.Limit,
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

	requestSearcher, managed, err := s.managedSearcher(
		r,
		"search.query",
		req,
		q,
	)
	if err != nil {
		writeManagedEmbeddingError(w, err)
		return
	}
	start := time.Now()
	hits, err := requestSearcher.Search(r.Context(), q)
	if managed != nil {
		if summary, ok, replayed := managed.result(); ok {
			setManagedUsageHeaders(w, summary, replayed)
		}
	}
	if err != nil {
		if s.Log != nil {
			if managed != nil {
				// Managed-provider errors can contain upstream response text.
				// Keep the server log as redacted as the public response.
				s.Log.Error("search.managed_failed")
			} else {
				s.Log.Error("search.failed", "err", err)
			}
		}
		if managed != nil {
			writeManagedEmbeddingError(w, err)
			return
		}
		writeError(w, http.StatusBadGateway, "search_unavailable", "memory search is temporarily unavailable")
		return
	}
	meta := map[string]any{"latency_ms": time.Since(start).Milliseconds()}
	if managed != nil {
		if summary, ok, replayed := managed.result(); ok {
			meta["managed_embedding"] = map[string]any{
				"remaining": summary.Remaining,
				"reset_at":  summary.ResetAt,
				"replayed":  replayed,
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": hits, "_meta": meta})
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
	src, err := s.File.Get(r.Context(), u.ID, id)
	if err != nil {
		if errors.Is(err, file.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such file")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !hideUnauthorizedFile(w, r, src.Path) {
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
	tok := r.Context().Value(ctxToken).(*auth.Token)
	hits, err := s.Relator.Get(r.Context(), u.ID, id, typ, tok.Paths, limit)
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

// handleRebuildRelations → POST /v1/relations/rebuild
// Body (optional): { "file_id": "<uuid>" } — scope to one file. Otherwise
// recomputes every file owned by the caller. Returns a summary.
func (s *Server) handleRebuildRelations(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser).(*auth.User)
	if s.Relator == nil {
		writeError(w, http.StatusServiceUnavailable, "no_relator", "relator not configured")
		return
	}
	var req struct {
		FileID string `json:"file_id"`
	}
	// Body is optional — a bare POST rebuilds everything.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
	}
	var only *uuid.UUID
	if req.FileID != "" {
		id, err := uuid.Parse(req.FileID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_id", err.Error())
			return
		}
		only = &id
	}
	start := time.Now()
	res, err := s.Relator.RebuildForUser(r.Context(), u.ID, only)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"files":    res.Files,
		"failures": res.Failures,
		"_meta":    map[string]any{"latency_ms": time.Since(start).Milliseconds()},
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
	norm, err := pathx.Normalize(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_path", err.Error())
		return
	}
	if !requireTokenPath(w, r, norm) {
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
	norm, err := pathx.Normalize(parent)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_path", err.Error())
		return
	}
	if !requireTokenPath(w, r, norm) {
		return
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
	if !requireTokenPath(w, r, cur.Path) {
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
	destParent := pathx.Parent(cur.Path)
	if req.ParentPath != nil {
		destParent, err = pathx.Normalize(*req.ParentPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_parent_path", err.Error())
			return
		}
	}
	destName := cur.Name
	if req.Name != nil {
		destName = *req.Name
	}
	destPath, err := pathx.Join(destParent, destName)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_path", err.Error())
		return
	}
	if !requireTokenPath(w, r, destPath) {
		return
	}
	if req.ParentPath != nil && req.Name != nil {
		if err := s.Folder.Relocate(
			r.Context(),
			u.ID,
			id,
			cur.Path,
			*req.ParentPath,
			*req.Name,
		); err != nil {
			writeFolderMutateError(w, err)
			return
		}
	} else if req.ParentPath != nil {
		if err := s.Folder.Move(r.Context(), u.ID, cur.Path, *req.ParentPath); err != nil {
			writeFolderMutateError(w, err)
			return
		}
	} else {
		if err := s.Folder.Rename(r.Context(), u.ID, cur.Path, *req.Name); err != nil {
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
	if !requireTokenPath(w, r, cur.Path) {
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
	case errors.Is(err, folder.ErrContainsMemories):
		writeError(w, http.StatusConflict, "contains_memories",
			"folder contains durable memories; forget them explicitly before deleting the folder")
	case errors.Is(err, folder.ErrContainsTaskState):
		writeError(w, http.StatusConflict, "contains_task_checkpoints",
			"folder contains immutable Agent task checkpoints; explicitly re-scope the task before moving or deleting the folder")
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

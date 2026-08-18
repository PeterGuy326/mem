package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/memory"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

type memoryServiceStub struct {
	command               memory.Command
	query                 memory.Query
	listQuery             memory.ListQuery
	feedbackCommand       memory.FeedbackCommand
	lifecycleCommand      memory.LifecycleCommand
	forgetCommand         memory.ForgetCommand
	createRelationCommand memory.CreateRelationCommand
	listRelationsQuery    memory.ListRelationsQuery
	result                *memory.RememberResult
	record                *memory.Memory
	listResult            *memory.ListResult
	mutationResult        *memory.MutationResult
	forgetResult          *memory.ForgetResult
	relationResult        *memory.CreateRelationResult
	relations             []memory.Relation
	writeErr              error
	readErr               error
	listErr               error
	controlErr            error
	calls                 int
}

func (s *memoryServiceStub) Remember(_ context.Context, cmd memory.Command) (*memory.RememberResult, error) {
	s.calls++
	s.command = cmd
	return s.result, s.writeErr
}

func (s *memoryServiceStub) Get(_ context.Context, q memory.Query) (*memory.Memory, error) {
	s.calls++
	s.query = q
	return s.record, s.readErr
}

func (s *memoryServiceStub) List(_ context.Context, q memory.ListQuery) (*memory.ListResult, error) {
	s.calls++
	s.listQuery = q
	return s.listResult, s.listErr
}

func (s *memoryServiceStub) Feedback(
	_ context.Context,
	cmd memory.FeedbackCommand,
) (*memory.MutationResult, error) {
	s.calls++
	s.feedbackCommand = cmd
	return s.mutationResult, s.controlErr
}

func (s *memoryServiceStub) Archive(
	_ context.Context,
	cmd memory.LifecycleCommand,
) (*memory.MutationResult, error) {
	s.calls++
	s.lifecycleCommand = cmd
	return s.mutationResult, s.controlErr
}

func (s *memoryServiceStub) Restore(
	_ context.Context,
	cmd memory.LifecycleCommand,
) (*memory.MutationResult, error) {
	s.calls++
	s.lifecycleCommand = cmd
	return s.mutationResult, s.controlErr
}

func (s *memoryServiceStub) Forget(
	_ context.Context,
	cmd memory.ForgetCommand,
) (*memory.ForgetResult, error) {
	s.calls++
	s.forgetCommand = cmd
	return s.forgetResult, s.controlErr
}

func (s *memoryServiceStub) CreateRelation(
	_ context.Context,
	cmd memory.CreateRelationCommand,
) (*memory.CreateRelationResult, error) {
	s.calls++
	s.createRelationCommand = cmd
	return s.relationResult, s.controlErr
}

func (s *memoryServiceStub) ListRelations(
	_ context.Context,
	q memory.ListRelationsQuery,
) ([]memory.Relation, error) {
	s.calls++
	s.listRelationsQuery = q
	return s.relations, s.controlErr
}

func memoryHandlerContext(req *http.Request, paths []string) (*http.Request, uuid.UUID, uuid.UUID, uuid.UUID) {
	actorID := uuid.New()
	ownerID := uuid.New()
	tokenID := uuid.New()
	workspaceID := uuid.New()
	ctx := context.WithValue(req.Context(), ctxActor, &auth.User{ID: actorID})
	ctx = context.WithValue(ctx, ctxUser, &auth.User{ID: ownerID})
	ctx = context.WithValue(ctx, ctxToken, &auth.Token{ID: tokenID, Paths: paths})
	ctx = context.WithValue(ctx, ctxWorkspace, &workspace.Workspace{
		ID: workspaceID, ResourceOwnerUserID: ownerID, Role: workspace.RoleOwner,
	})
	return req.WithContext(ctx), actorID, tokenID, workspaceID
}

func TestHandleRememberPersistsAuthenticatedProvenance(t *testing.T) {
	memoryID := uuid.New()
	stub := &memoryServiceStub{result: &memory.RememberResult{
		Memory: memory.Memory{ID: memoryID, Kind: memory.KindDecision, Path: "/Projects/mem"},
	}}
	server := &Server{Memory: stub}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories", strings.NewReader(`{
		"kind":"decision",
		"content":"mem returns evidence and the Agent owns the answer",
		"path":"/Projects/mem",
		"source":{"type":"agent","ref":"agent://codex/task-1"},
		"producer":{"agent_id":"codex","session_id":"s1","task_id":"t1"},
		"attributes":{"reason":"portable"}
	}`))
	req.Header.Set("Idempotency-Key", "task-1-decision")
	req, actorID, tokenID, workspaceID := memoryHandlerContext(req, []string{"/Projects"})
	rec := httptest.NewRecorder()

	server.handleRemember(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/v1/memories/"+memoryID.String() {
		t.Fatalf("Location = %q", got)
	}
	if stub.command.WorkspaceID != workspaceID ||
		stub.command.CreatedByUserID == nil || *stub.command.CreatedByUserID != actorID ||
		stub.command.CreatedByTokenID == nil || *stub.command.CreatedByTokenID != tokenID {
		t.Fatalf("authenticated provenance = %+v", stub.command)
	}
	if stub.command.IdempotencyKey != "task-1-decision" ||
		stub.command.ProducerAgent != "codex" ||
		stub.command.Path != "/Projects/mem" {
		t.Fatalf("remember command = %+v", stub.command)
	}
	if len(stub.command.AllowedPaths) != 1 ||
		stub.command.AllowedPaths[0] != "/Projects" {
		t.Fatalf("remember allowed paths = %#v", stub.command.AllowedPaths)
	}
}

func TestHandleRememberReplayUsesOK(t *testing.T) {
	stub := &memoryServiceStub{result: &memory.RememberResult{
		Memory:   memory.Memory{ID: uuid.New()},
		Replayed: true,
	}}
	server := &Server{Memory: stub}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"kind":"note","content":"retry safe","path":"/","source":{"type":"agent"}}`))
	req.Header.Set("Idempotency-Key", "same-key")
	req, _, _, _ = memoryHandlerContext(req, nil)
	rec := httptest.NewRecorder()

	server.handleRemember(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"replayed":true`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRememberReplayReauthorizesPersistedPath(t *testing.T) {
	stub := &memoryServiceStub{result: &memory.RememberResult{
		Memory: memory.Memory{
			ID:      uuid.New(),
			Path:    "/Secret",
			Content: "must not cross the path boundary",
		},
		Replayed: true,
	}}
	server := &Server{Memory: stub}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"kind":"note","content":"old request","path":"/Work","source":{"type":"agent"}}`))
	req.Header.Set("Idempotency-Key", "moved-record-key")
	req, _, _, _ = memoryHandlerContext(req, []string{"/Work"})
	rec := httptest.NewRecorder()

	server.handleRemember(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "must not cross") ||
		strings.Contains(rec.Body.String(), "/Secret") {
		t.Fatalf("response leaked persisted record: %s", rec.Body.String())
	}
}

func TestHandleRememberRejectsMissingKeyAndUnauthorizedPath(t *testing.T) {
	t.Run("missing idempotency key", func(t *testing.T) {
		stub := &memoryServiceStub{}
		server := &Server{Memory: stub}
		req := httptest.NewRequest(http.MethodPost, "/v1/memories",
			strings.NewReader(`{"kind":"note","content":"x","path":"/Work","source":{"type":"agent"}}`))
		req, _, _, _ = memoryHandlerContext(req, []string{"/Work"})
		rec := httptest.NewRecorder()

		server.handleRemember(rec, req)

		if rec.Code != http.StatusBadRequest || stub.calls != 0 {
			t.Fatalf("status=%d calls=%d body=%s", rec.Code, stub.calls, rec.Body.String())
		}
	})

	t.Run("path boundary", func(t *testing.T) {
		stub := &memoryServiceStub{}
		server := &Server{Memory: stub}
		req := httptest.NewRequest(http.MethodPost, "/v1/memories",
			strings.NewReader(`{"kind":"note","content":"x","path":"/Worker","source":{"type":"agent"}}`))
		req.Header.Set("Idempotency-Key", "restricted")
		req, _, _, _ = memoryHandlerContext(req, []string{"/Work"})
		rec := httptest.NewRecorder()

		server.handleRemember(rec, req)

		if rec.Code != http.StatusForbidden || stub.calls != 0 {
			t.Fatalf("status=%d calls=%d body=%s", rec.Code, stub.calls, rec.Body.String())
		}
	})
}

func TestHandleRememberMapsIdempotencyConflict(t *testing.T) {
	stub := &memoryServiceStub{writeErr: memory.ErrIdempotencyConflict}
	server := &Server{Memory: stub}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"kind":"note","content":"changed","path":"/","source":{"type":"agent"}}`))
	req.Header.Set("Idempotency-Key", "already-used")
	req, _, _, _ = memoryHandlerContext(req, nil)
	rec := httptest.NewRecorder()

	server.handleRemember(rec, req)

	if rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "idempotency_conflict") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetMemoryPassesWorkspaceAndPathScope(t *testing.T) {
	memoryID := uuid.New()
	stub := &memoryServiceStub{record: &memory.Memory{ID: memoryID, Path: "/Work"}}
	server := &Server{Memory: stub}
	req := httptest.NewRequest(
		http.MethodGet,
		"/v1/memories/"+memoryID.String()+"?scope=/Work/Project",
		nil,
	)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", memoryID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req, _, _, workspaceID := memoryHandlerContext(req, []string{"/Work"})
	rec := httptest.NewRecorder()

	server.handleGetMemory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"citation":"mem://memories/`+memoryID.String()+`"`) ||
		!strings.Contains(rec.Body.String(), `"provenance"`) {
		t.Fatalf("detail omitted citation/provenance: %s", rec.Body.String())
	}
	if stub.query.WorkspaceID != workspaceID || stub.query.MemoryID != memoryID ||
		stub.query.Scope != "/Work/Project" ||
		len(stub.query.AllowedPaths) != 1 || stub.query.AllowedPaths[0] != "/Work" {
		t.Fatalf("memory query = %+v", stub.query)
	}
}

func TestHandleGetMemoryHidesMissingOrUnauthorized(t *testing.T) {
	stub := &memoryServiceStub{readErr: memory.ErrNotFound}
	server := &Server{Memory: stub}
	memoryID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/memories/"+memoryID.String(), nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", memoryID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	req, _, _, _ = memoryHandlerContext(req, []string{"/Work"})
	rec := httptest.NewRecorder()

	server.handleGetMemory(rec, req)

	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not_found") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleListMemoriesPassesBoundFiltersAndReturnsSummaries(t *testing.T) {
	memoryID := uuid.New()
	stub := &memoryServiceStub{listResult: &memory.ListResult{
		Memories: []memory.MemorySummary{{
			ID:            memoryID,
			Kind:          memory.KindDecision,
			Excerpt:       "bounded excerpt",
			ContentLength: 64000,
			Path:          "/Work/Project",
			Citation:      "mem://memories/" + memoryID.String(),
		}},
		NextCursor: "opaque-next",
	}}
	server := &Server{Memory: stub}
	req := httptest.NewRequest(
		http.MethodGet,
		"/v1/memories?scope=%2FWork&recursive=false&kind=decision,note&kind=fact&lifecycle=all&pinned=true&limit=25&cursor=opaque-current",
		nil,
	)
	req, _, _, workspaceID := memoryHandlerContext(req, []string{"/Work"})
	rec := httptest.NewRecorder()

	server.handleListMemories(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	q := stub.listQuery
	if q.WorkspaceID != workspaceID || q.Scope != "/Work" ||
		q.Recursive == nil || *q.Recursive ||
		q.Pinned == nil || !*q.Pinned ||
		q.Limit != 25 || q.Cursor != "opaque-current" {
		t.Fatalf("list query = %+v", q)
	}
	if got := strings.Join(q.Kinds, ","); got != "decision,note,fact" {
		t.Fatalf("kinds = %q", got)
	}
	if got := strings.Join(q.LifecycleStatuses, ","); got != "active,archived" {
		t.Fatalf("lifecycle statuses = %q", got)
	}
	if len(q.AllowedPaths) != 1 || q.AllowedPaths[0] != "/Work" {
		t.Fatalf("allowed paths = %#v", q.AllowedPaths)
	}
	if !strings.Contains(rec.Body.String(), `"excerpt":"bounded excerpt"`) ||
		strings.Contains(rec.Body.String(), `"content":`) ||
		!strings.Contains(rec.Body.String(), `"next_cursor":"opaque-next"`) {
		t.Fatalf("unexpected list response: %s", rec.Body.String())
	}
}

func TestHandleListMemoriesRejectsInvalidQueryBeforeService(t *testing.T) {
	tests := []string{
		"/v1/memories?recursive=sometimes",
		"/v1/memories?pinned=1",
		"/v1/memories?lifecycle=forgotten",
		"/v1/memories?limit=101",
	}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			stub := &memoryServiceStub{}
			server := &Server{Memory: stub}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req, _, _, _ = memoryHandlerContext(req, nil)
			rec := httptest.NewRecorder()

			server.handleListMemories(rec, req)

			if rec.Code != http.StatusBadRequest || stub.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, stub.calls, rec.Body.String())
			}
		})
	}
}

func TestHandleListMemoriesMapsOpaqueCursorErrors(t *testing.T) {
	stub := &memoryServiceStub{listErr: memory.ErrInvalidCursor}
	server := &Server{Memory: stub}
	req := httptest.NewRequest(http.MethodGet, "/v1/memories?cursor=not-for-these-filters", nil)
	req, _, _, _ = memoryHandlerContext(req, []string{"/Work"})
	rec := httptest.NewRecorder()

	server.handleListMemories(rec, req)

	if rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "invalid_memory_query") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleMemoryFeedbackPassesAuthenticatedCASCommand(t *testing.T) {
	memoryID := uuid.New()
	stub := &memoryServiceStub{mutationResult: &memory.MutationResult{
		Memory: memory.Memory{
			ID:              memoryID,
			Content:         "do not echo me",
			Path:            "/Private/secret",
			SourceRef:       "agent://secret",
			LifecycleStatus: memory.StatusActive,
			StateVersion:    8,
			Pinned:          true,
			UsefulCount:     2,
			NotUsefulCount:  1,
			FeedbackScore:   1,
			FeedbackCount:   3,
		},
		Event: memory.MemoryEvent{ID: uuid.New(), Action: memory.FeedbackUseful},
	}}
	server := &Server{Memory: stub}
	req := memoryRouteRequest(
		http.MethodPost,
		"/v1/memories/"+memoryID.String()+"/feedback",
		memoryID,
		`{"action":"useful","expected_version":7}`,
	)
	req.Header.Set("Idempotency-Key", "feedback-task-7")
	req, actorID, tokenID, workspaceID := memoryHandlerContext(req, []string{"/Work"})
	rec := httptest.NewRecorder()

	server.handleMemoryFeedback(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	cmd := stub.feedbackCommand
	if cmd.WorkspaceID != workspaceID || cmd.MemoryID != memoryID ||
		cmd.ActorUserID == nil || *cmd.ActorUserID != actorID ||
		cmd.ActorTokenID == nil || *cmd.ActorTokenID != tokenID ||
		cmd.Action != memory.FeedbackUseful ||
		cmd.ExpectedVersion != 7 || cmd.IdempotencyKey != "feedback-task-7" {
		t.Fatalf("feedback command = %+v", cmd)
	}
	if len(cmd.AllowedPaths) != 1 || cmd.AllowedPaths[0] != "/Work" {
		t.Fatalf("feedback allowed paths = %#v", cmd.AllowedPaths)
	}
	if strings.Contains(rec.Body.String(), "do not echo me") ||
		strings.Contains(rec.Body.String(), "/Private/secret") ||
		strings.Contains(rec.Body.String(), "agent://secret") {
		t.Fatalf("control response echoed memory payload: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"state_version":8`) ||
		!strings.Contains(rec.Body.String(), `"feedback_count":3`) {
		t.Fatalf("control response omitted bounded state: %s", rec.Body.String())
	}
}

func TestHandleMemoryArchiveAndRestoreUseSameLifecycleContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		handle func(*Server, http.ResponseWriter, *http.Request)
	}{
		{name: "archive", handle: (*Server).handleArchiveMemory},
		{name: "restore", handle: (*Server).handleRestoreMemory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memoryID := uuid.New()
			stub := &memoryServiceStub{mutationResult: &memory.MutationResult{
				Memory: memory.Memory{ID: memoryID, StateVersion: 4},
				Event:  memory.MemoryEvent{ID: uuid.New(), Action: tc.name},
			}}
			server := &Server{Memory: stub}
			req := memoryRouteRequest(
				http.MethodPost,
				"/v1/memories/"+memoryID.String()+"/"+tc.name,
				memoryID,
				`{"expected_version":3}`,
			)
			req.Header.Set("Idempotency-Key", tc.name+"-task-3")
			req, actorID, tokenID, workspaceID := memoryHandlerContext(req, []string{"/Work"})
			rec := httptest.NewRecorder()

			tc.handle(server, rec, req)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			cmd := stub.lifecycleCommand
			if cmd.WorkspaceID != workspaceID || cmd.MemoryID != memoryID ||
				cmd.ActorUserID == nil || *cmd.ActorUserID != actorID ||
				cmd.ActorTokenID == nil || *cmd.ActorTokenID != tokenID ||
				cmd.ExpectedVersion != 3 ||
				cmd.IdempotencyKey != tc.name+"-task-3" {
				t.Fatalf("lifecycle command = %+v", cmd)
			}
		})
	}
}

func TestHandleForgetMemoryReturnsMinimalRetrySafeTombstone(t *testing.T) {
	memoryID := uuid.New()
	forgottenAt := time.Now().UTC().Round(time.Microsecond)
	stub := &memoryServiceStub{forgetResult: &memory.ForgetResult{
		Tombstone: memory.Tombstone{
			ID:              memoryID,
			LifecycleStatus: memory.StatusForgotten,
			StateVersion:    10,
			ForgottenAt:     &forgottenAt,
		},
		Event: memory.MemoryEvent{
			ID:               uuid.New(),
			WorkspaceID:      uuid.New(),
			MemoryID:         memoryID,
			Action:           "forget",
			ActorUserID:      pointerUUID(uuid.New()),
			ExpectedVersion:  9,
			ResultingVersion: 10,
		},
	}}
	server := &Server{Memory: stub}
	req := memoryRouteRequest(
		http.MethodPost,
		"/v1/memories/"+memoryID.String()+"/forget",
		memoryID,
		`{"expected_version":9,"reason":"sensitive"}`,
	)
	req.Header.Set("Idempotency-Key", "forget-sensitive-task")
	req, actorID, tokenID, workspaceID := memoryHandlerContext(req, []string{"/Private"})
	rec := httptest.NewRecorder()

	server.handleForgetMemory(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	cmd := stub.forgetCommand
	if cmd.WorkspaceID != workspaceID || cmd.MemoryID != memoryID ||
		cmd.ActorUserID == nil || *cmd.ActorUserID != actorID ||
		cmd.ActorTokenID == nil || *cmd.ActorTokenID != tokenID ||
		cmd.ExpectedVersion != 9 || cmd.Reason != memory.ForgetReasonSensitive {
		t.Fatalf("forget command = %+v", cmd)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["memory_id"] != memoryID.String() ||
		body["state_version"] != float64(10) ||
		body["replayed"] != false {
		t.Fatalf("forget response = %#v", body)
	}
	if strings.Contains(rec.Body.String(), "/Private/secret") ||
		strings.Contains(rec.Body.String(), `"kind":"preference"`) ||
		strings.Contains(rec.Body.String(), "actor_user_id") ||
		strings.Contains(rec.Body.String(), "workspace_id") {
		t.Fatalf("forget response leaked tombstone metadata: %s", rec.Body.String())
	}
}

func pointerUUID(value uuid.UUID) *uuid.UUID {
	return &value
}

func TestHandleMemoryControlMapsConflictsAndGone(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
		text string
	}{
		{name: "idempotency", err: memory.ErrIdempotencyConflict, code: 409, text: "idempotency_conflict"},
		{name: "version", err: memory.ErrVersionConflict, code: 409, text: "memory_version_conflict"},
		{name: "transition", err: memory.ErrInvalidTransition, code: 409, text: "invalid_memory_transition"},
		{name: "hidden", err: memory.ErrNotFound, code: 404, text: "not_found"},
		{name: "forgotten", err: memory.ErrForgotten, code: 410, text: "memory_forgotten"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			memoryID := uuid.New()
			stub := &memoryServiceStub{controlErr: tc.err}
			server := &Server{Memory: stub}
			req := memoryRouteRequest(
				http.MethodPost,
				"/v1/memories/"+memoryID.String()+"/feedback",
				memoryID,
				`{"action":"pin","expected_version":1}`,
			)
			req.Header.Set("Idempotency-Key", "error-"+tc.name)
			req, _, _, _ = memoryHandlerContext(req, []string{"/Work"})
			rec := httptest.NewRecorder()

			server.handleMemoryFeedback(rec, req)

			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.text) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleMemoryControlRejectsMalformedBodyBeforeService(t *testing.T) {
	memoryID := uuid.New()
	stub := &memoryServiceStub{}
	server := &Server{Memory: stub}
	req := memoryRouteRequest(
		http.MethodPost,
		"/v1/memories/"+memoryID.String()+"/feedback",
		memoryID,
		`{"action":"pin","expected_version":1,"content":"not allowed"}`,
	)
	req.Header.Set("Idempotency-Key", "malformed-control")
	req, _, _, _ = memoryHandlerContext(req, nil)
	rec := httptest.NewRecorder()

	server.handleMemoryFeedback(rec, req)

	if rec.Code != http.StatusBadRequest || stub.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, stub.calls, rec.Body.String())
	}
}

func memoryRouteRequest(
	method string,
	target string,
	memoryID uuid.UUID,
	body string,
) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", memoryID.String())
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func TestHandleRememberMapsValidationError(t *testing.T) {
	stub := &memoryServiceStub{writeErr: errors.Join(memory.ErrInvalidCommand, errors.New("bad kind"))}
	server := &Server{Memory: stub}
	req := httptest.NewRequest(http.MethodPost, "/v1/memories",
		strings.NewReader(`{"kind":"unknown","content":"x","path":"/","source":{"type":"agent"}}`))
	req.Header.Set("Idempotency-Key", "invalid")
	req, _, _, _ = memoryHandlerContext(req, nil)
	rec := httptest.NewRecorder()

	server.handleRemember(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_memory") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRememberRejectsMalformedRequestBeforeService(t *testing.T) {
	tests := []struct {
		name string
		body string
		code int
	}{
		{
			name: "unknown field",
			body: `{"kind":"note","content":"x","path":"/","source":{"type":"agent"},"unknown":true}`,
			code: http.StatusBadRequest,
		},
		{
			name: "trailing value",
			body: `{"kind":"note","content":"x","path":"/","source":{"type":"agent"}} {}`,
			code: http.StatusBadRequest,
		},
		{
			name: "body too large",
			body: `{"kind":"note","content":"` + strings.Repeat("a", maxRememberBodyBytes) +
				`","path":"/","source":{"type":"agent"}}`,
			code: http.StatusRequestEntityTooLarge,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &memoryServiceStub{}
			server := &Server{Memory: stub}
			req := httptest.NewRequest(
				http.MethodPost,
				"/v1/memories",
				strings.NewReader(tc.body),
			)
			req.Header.Set("Idempotency-Key", "malformed")
			req, _, _, _ = memoryHandlerContext(req, nil)
			rec := httptest.NewRecorder()

			server.handleRemember(rec, req)

			if rec.Code != tc.code || stub.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, stub.calls, rec.Body.String())
			}
		})
	}
}

func TestHandleCreateMemoryRelationPersistsAuthenticatedEdge(t *testing.T) {
	sourceID := uuid.New()
	targetID := uuid.New()
	stub := &memoryServiceStub{relationResult: &memory.CreateRelationResult{
		Relation: memory.Relation{
			ID:           uuid.New(),
			SourceID:     sourceID,
			TargetID:     targetID,
			RelationType: memory.RelSupersedes,
		},
		Created: true,
	}}
	server := &Server{Memory: stub}
	req := httptest.NewRequest(http.MethodPost, "/v1/memory-relations", strings.NewReader(
		`{"source_id":"`+sourceID.String()+`",`+
			`"target_id":"`+targetID.String()+`",`+
			`"relation_type":"supersedes",`+
			`"reason":"decision updated"}`))
	req, actorID, tokenID, workspaceID := memoryHandlerContext(req, []string{"/Projects"})
	rec := httptest.NewRecorder()

	server.handleCreateMemoryRelation(rec, req)

	if rec.Code != http.StatusCreated ||
		!strings.Contains(rec.Body.String(), `"relation_type":"supersedes"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	cmd := stub.createRelationCommand
	if cmd.WorkspaceID != workspaceID || cmd.SourceID != sourceID || cmd.TargetID != targetID ||
		cmd.RelationType != "supersedes" || cmd.Reason != "decision updated" {
		t.Fatalf("create relation command = %+v", cmd)
	}
	if cmd.ActorUserID == nil || *cmd.ActorUserID != actorID ||
		cmd.ActorTokenID == nil || *cmd.ActorTokenID != tokenID {
		t.Fatalf("create relation actor = %+v", cmd)
	}
	if len(cmd.AllowedPaths) != 1 || cmd.AllowedPaths[0] != "/Projects" {
		t.Fatalf("create relation allowed paths = %#v", cmd.AllowedPaths)
	}
}

func TestHandleCreateMemoryRelationReplayUsesOK(t *testing.T) {
	stub := &memoryServiceStub{relationResult: &memory.CreateRelationResult{
		Relation: memory.Relation{
			ID:           uuid.New(),
			SourceID:     uuid.New(),
			TargetID:     uuid.New(),
			RelationType: memory.RelCorrects,
		},
		Created: false,
	}}
	server := &Server{Memory: stub}
	req := httptest.NewRequest(http.MethodPost, "/v1/memory-relations", strings.NewReader(
		`{"source_id":"`+stub.relationResult.Relation.SourceID.String()+`",`+
			`"target_id":"`+stub.relationResult.Relation.TargetID.String()+`",`+
			`"relation_type":"corrects"}`))
	req, _, _, _ = memoryHandlerContext(req, nil)
	rec := httptest.NewRecorder()

	server.handleCreateMemoryRelation(rec, req)

	if rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"relation_type":"corrects"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleCreateMemoryRelationRejectsMalformedIDsBeforeService(t *testing.T) {
	validID := uuid.New()
	tests := []struct {
		name string
		body string
		text string
	}{
		{
			name: "bad source",
			body: `{"source_id":"not-a-uuid","target_id":"` + validID.String() +
				`","relation_type":"supersedes"}`,
			text: "bad_source_id",
		},
		{
			name: "bad target",
			body: `{"source_id":"` + validID.String() +
				`","target_id":"not-a-uuid","relation_type":"supersedes"}`,
			text: "bad_target_id",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &memoryServiceStub{}
			server := &Server{Memory: stub}
			req := httptest.NewRequest(http.MethodPost, "/v1/memory-relations", strings.NewReader(tc.body))
			req, _, _, _ = memoryHandlerContext(req, nil)
			rec := httptest.NewRecorder()

			server.handleCreateMemoryRelation(rec, req)

			if rec.Code != http.StatusBadRequest || stub.calls != 0 ||
				!strings.Contains(rec.Body.String(), tc.text) {
				t.Fatalf("status=%d calls=%d body=%s", rec.Code, stub.calls, rec.Body.String())
			}
		})
	}
}

func TestHandleCreateMemoryRelationMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
		text string
	}{
		{name: "invalid", err: memory.ErrInvalidCommand, code: 400, text: "invalid_relation"},
		{name: "hidden", err: memory.ErrNotFound, code: 404, text: "not_found"},
		{name: "forgotten", err: memory.ErrForgotten, code: 410, text: "memory_forgotten"},
		{name: "cycle", err: memory.ErrRelationCycle, code: 409, text: "relation_cycle"},
		{name: "cross workspace", err: memory.ErrCrossWorkspace, code: 400, text: "cross_workspace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &memoryServiceStub{controlErr: tc.err}
			server := &Server{Memory: stub}
			req := httptest.NewRequest(http.MethodPost, "/v1/memory-relations", strings.NewReader(
				`{"source_id":"`+uuid.NewString()+`",`+
					`"target_id":"`+uuid.NewString()+`",`+
					`"relation_type":"supersedes"}`))
			req, _, _, _ = memoryHandlerContext(req, nil)
			rec := httptest.NewRecorder()

			server.handleCreateMemoryRelation(rec, req)

			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.text) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleListMemoryRelationsPassesFilters(t *testing.T) {
	memoryID := uuid.New()
	stub := &memoryServiceStub{relations: []memory.Relation{{
		ID:           uuid.New(),
		SourceID:     uuid.New(),
		TargetID:     memoryID,
		RelationType: memory.RelCorrects,
	}}}
	server := &Server{Memory: stub}
	req := memoryRouteRequest(
		http.MethodGet,
		"/v1/memories/"+memoryID.String()+"/relations?direction=target&relation_type=corrects&limit=5",
		memoryID,
		"",
	)
	req, _, _, workspaceID := memoryHandlerContext(req, []string{"/Work"})
	rec := httptest.NewRecorder()

	server.handleListMemoryRelations(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"relations":[`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	q := stub.listRelationsQuery
	if q.WorkspaceID != workspaceID || q.MemoryID != memoryID ||
		q.Direction != "target" || q.RelationType != "corrects" || q.Limit != 5 {
		t.Fatalf("list relations query = %+v", q)
	}
	if len(q.AllowedPaths) != 1 || q.AllowedPaths[0] != "/Work" {
		t.Fatalf("list relations allowed paths = %#v", q.AllowedPaths)
	}
}

func TestHandleListMemoryRelationsReturnsEmptyArray(t *testing.T) {
	memoryID := uuid.New()
	stub := &memoryServiceStub{}
	server := &Server{Memory: stub}
	req := memoryRouteRequest(
		http.MethodGet,
		"/v1/memories/"+memoryID.String()+"/relations",
		memoryID,
		"",
	)
	req, _, _, _ = memoryHandlerContext(req, nil)
	rec := httptest.NewRecorder()

	server.handleListMemoryRelations(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"relations":[]`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleListMemoryRelationsMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
		text string
	}{
		{name: "invalid", err: memory.ErrInvalidCommand, code: 400, text: "invalid_relation_query"},
		{name: "hidden", err: memory.ErrNotFound, code: 404, text: "not_found"},
		{name: "forgotten", err: memory.ErrForgotten, code: 410, text: "memory_forgotten"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			memoryID := uuid.New()
			stub := &memoryServiceStub{controlErr: tc.err}
			server := &Server{Memory: stub}
			req := memoryRouteRequest(
				http.MethodGet,
				"/v1/memories/"+memoryID.String()+"/relations",
				memoryID,
				"",
			)
			req, _, _, _ = memoryHandlerContext(req, nil)
			rec := httptest.NewRecorder()

			server.handleListMemoryRelations(rec, req)

			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.text) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleListMemoryRelationsRejectsBadLimit(t *testing.T) {
	memoryID := uuid.New()
	stub := &memoryServiceStub{}
	server := &Server{Memory: stub}
	req := memoryRouteRequest(
		http.MethodGet,
		"/v1/memories/"+memoryID.String()+"/relations?limit=0",
		memoryID,
		"",
	)
	req, _, _, _ = memoryHandlerContext(req, nil)
	rec := httptest.NewRecorder()

	server.handleListMemoryRelations(rec, req)

	if rec.Code != http.StatusBadRequest || stub.calls != 0 ||
		!strings.Contains(rec.Body.String(), "bad_limit") {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, stub.calls, rec.Body.String())
	}
}

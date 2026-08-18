package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/durablecontext"
	"github.com/PeterGuy326/mem/server/internal/memory"
)

func durableRouteRequest(
	method string,
	target string,
	params map[string]string,
	body io.Reader,
) *http.Request {
	req := httptest.NewRequest(method, target, body)
	routeCtx := chi.NewRouteContext()
	for key, value := range params {
		routeCtx.URLParams.Add(key, value)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

type durableContextStub struct {
	grantCommand durablecontext.GrantCommand
	revokeCmd    durablecontext.RevokeCommand
	listQuery    durablecontext.ListGrantsQuery
	recallQuery  durablecontext.RecallQuery
	getQuery     durablecontext.GetQuery
	grant        *durablecontext.Grant
	grantViews   []durablecontext.GrantView
	recallResult *durablecontext.RecallResult
	hit          *durablecontext.RecallHit
	err          error
	calls        int
}

func (s *durableContextStub) Grant(
	_ context.Context,
	cmd durablecontext.GrantCommand,
) (*durablecontext.Grant, error) {
	s.calls++
	s.grantCommand = cmd
	return s.grant, s.err
}

func (s *durableContextStub) Revoke(
	_ context.Context,
	cmd durablecontext.RevokeCommand,
) (*durablecontext.Grant, error) {
	s.calls++
	s.revokeCmd = cmd
	return s.grant, s.err
}

func (s *durableContextStub) ListGrantViews(
	_ context.Context,
	q durablecontext.ListGrantsQuery,
) ([]durablecontext.GrantView, error) {
	s.calls++
	s.listQuery = q
	return s.grantViews, s.err
}

func (s *durableContextStub) Recall(
	_ context.Context,
	q durablecontext.RecallQuery,
) (*durablecontext.RecallResult, error) {
	s.calls++
	s.recallQuery = q
	return s.recallResult, s.err
}

func (s *durableContextStub) Get(
	_ context.Context,
	q durablecontext.GetQuery,
) (*durablecontext.RecallHit, error) {
	s.calls++
	s.getQuery = q
	return s.hit, s.err
}

func TestHandleDurableContextRecallPinsContractAndPrincipal(t *testing.T) {
	memoryID := uuid.New()
	stub := &durableContextStub{recallResult: &durablecontext.RecallResult{
		Contract:  durablecontext.ContractVersion,
		Principal: "alice",
		Hits: []durablecontext.RecallHit{{
			Memory:       memory.Memory{ID: memoryID, StateVersion: 3},
			Locator:      durablecontext.Locator(memoryID, 3),
			StateVersion: 3,
		}},
	}}
	server := &Server{DurableContext: stub}
	req := httptest.NewRequest(http.MethodPost, "/v1/durable-context/recall",
		strings.NewReader(`{
			"contract": "durable-context.v1",
			"principal": "alice",
			"session_ref": "session-2",
			"limit": 25
		}`))
	req, _, _, workspaceID := memoryHandlerContext(req, []string{"/Work"})
	rec := httptest.NewRecorder()

	server.handleDurableContextRecall(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if stub.recallQuery.WorkspaceID != workspaceID {
		t.Fatalf("workspace = %v", stub.recallQuery.WorkspaceID)
	}
	if stub.recallQuery.Principal != "alice" ||
		stub.recallQuery.SessionRef != "session-2" ||
		stub.recallQuery.Limit != 25 {
		t.Fatalf("recall query = %+v", stub.recallQuery)
	}
	if len(stub.recallQuery.AllowedPaths) != 1 || stub.recallQuery.AllowedPaths[0] != "/Work" {
		t.Fatalf("token path boundary not enforced: %#v", stub.recallQuery.AllowedPaths)
	}
	if !strings.Contains(rec.Body.String(), "mem://memories/"+memoryID.String()+"@3") {
		t.Fatalf("response missing version-pinned locator: %s", rec.Body.String())
	}
}

func TestHandleDurableContextRecallRejectsUnsupportedContract(t *testing.T) {
	stub := &durableContextStub{}
	server := &Server{DurableContext: stub}
	req := httptest.NewRequest(http.MethodPost, "/v1/durable-context/recall",
		strings.NewReader(`{"contract":"durable-context.v2","principal":"alice"}`))
	req, _, _, _ = memoryHandlerContext(req, nil)
	rec := httptest.NewRecorder()

	server.handleDurableContextRecall(rec, req)

	if rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "contract_unsupported") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if stub.calls != 0 {
		t.Fatalf("service must not be called for an unsupported contract")
	}
}

func TestHandleDurableContextRecallMapsScopeDenial(t *testing.T) {
	stub := &durableContextStub{err: durablecontext.ErrScopeDenied}
	server := &Server{DurableContext: stub}
	req := httptest.NewRequest(http.MethodPost, "/v1/durable-context/recall",
		strings.NewReader(`{"contract":"durable-context.v1","principal":"mallory"}`))
	req, _, _, _ = memoryHandlerContext(req, nil)
	rec := httptest.NewRecorder()

	server.handleDurableContextRecall(rec, req)

	if rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "context_scope_denied") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleDurableContextRecallMapsDegradation(t *testing.T) {
	stub := &durableContextStub{err: errors.New("connection refused")}
	server := &Server{DurableContext: stub}
	req := httptest.NewRequest(http.MethodPost, "/v1/durable-context/recall",
		strings.NewReader(`{"contract":"durable-context.v1","principal":"alice"}`))
	req, _, _, _ = memoryHandlerContext(req, nil)
	rec := httptest.NewRecorder()

	server.handleDurableContextRecall(rec, req)

	if rec.Code != http.StatusBadGateway ||
		!strings.Contains(rec.Body.String(), "context_unavailable") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Fatalf("storage error details leaked: %s", rec.Body.String())
	}
}

func TestHandleDurableContextGetMemoryMapsContractStates(t *testing.T) {
	memoryID := uuid.New()
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"unapproved behaves as absent", durablecontext.ErrNotFound, http.StatusNotFound, "not_found"},
		{"forgotten surfaces explicitly", durablecontext.ErrForgotten, http.StatusGone, "memory_forgotten"},
		{"archived is stale", durablecontext.ErrStale, http.StatusConflict, "context_stale"},
		{"storage degradation", errors.New("boom"), http.StatusBadGateway, "context_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &durableContextStub{err: tc.err}
			server := &Server{DurableContext: stub}
			req := durableRouteRequest(http.MethodGet,
				"/v1/durable-context/memories/"+memoryID.String()+
					"?contract=durable-context.v1&principal=alice",
				map[string]string{"id": memoryID.String()}, nil)
			req, _, _, _ = memoryHandlerContext(req, nil)
			rec := httptest.NewRecorder()

			server.handleDurableContextGetMemory(rec, req)

			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantCode) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleDurableContextGetMemoryValidatesContractInputs(t *testing.T) {
	memoryID := uuid.New()
	t.Run("wrong contract", func(t *testing.T) {
		stub := &durableContextStub{}
		server := &Server{DurableContext: stub}
		req := durableRouteRequest(http.MethodGet,
			"/v1/durable-context/memories/"+memoryID.String()+
				"?contract=other.v9&principal=alice",
			map[string]string{"id": memoryID.String()}, nil)
		req, _, _, _ = memoryHandlerContext(req, nil)
		rec := httptest.NewRecorder()
		server.handleDurableContextGetMemory(rec, req)
		if rec.Code != http.StatusBadRequest ||
			!strings.Contains(rec.Body.String(), "contract_unsupported") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("missing principal", func(t *testing.T) {
		stub := &durableContextStub{}
		server := &Server{DurableContext: stub}
		req := durableRouteRequest(http.MethodGet,
			"/v1/durable-context/memories/"+memoryID.String()+
				"?contract=durable-context.v1",
			map[string]string{"id": memoryID.String()}, nil)
		req, _, _, _ = memoryHandlerContext(req, nil)
		rec := httptest.NewRecorder()
		server.handleDurableContextGetMemory(rec, req)
		if rec.Code != http.StatusBadRequest ||
			!strings.Contains(rec.Body.String(), "bad_principal") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestHandleDurableContextGrantLifecycle(t *testing.T) {
	memoryID := uuid.New()
	grantID := uuid.New()
	workspaceID := uuid.New()
	grant := &durablecontext.Grant{
		ID:          grantID,
		WorkspaceID: workspaceID,
		Principal:   "alice",
		MemoryID:    memoryID,
		Mode:        durablecontext.ModeRead,
	}

	t.Run("create", func(t *testing.T) {
		stub := &durableContextStub{grant: grant}
		server := &Server{DurableContext: stub}
		req := httptest.NewRequest(http.MethodPost, "/v1/durable-context/grants",
			strings.NewReader(`{"principal":"alice","memory_id":"`+memoryID.String()+`"}`))
		req, actorID, tokenID, requestWorkspace := memoryHandlerContext(req, nil)
		rec := httptest.NewRecorder()

		server.handleCreateDurableContextGrant(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Location"); got != "/v1/durable-context/grants/"+grantID.String() {
			t.Fatalf("Location = %q", got)
		}
		if stub.grantCommand.WorkspaceID != requestWorkspace ||
			stub.grantCommand.Principal != "alice" ||
			stub.grantCommand.MemoryID != memoryID {
			t.Fatalf("grant command = %+v", stub.grantCommand)
		}
		if stub.grantCommand.ActorUserID == nil || *stub.grantCommand.ActorUserID != actorID ||
			stub.grantCommand.ActorTokenID == nil || *stub.grantCommand.ActorTokenID != tokenID {
			t.Fatalf("grant actor = %+v", stub.grantCommand)
		}
		if strings.Contains(rec.Body.String(), "granted_by_token_id") {
			t.Fatalf("token identifier leaked: %s", rec.Body.String())
		}
	})

	t.Run("create target absent", func(t *testing.T) {
		stub := &durableContextStub{err: durablecontext.ErrNotFound}
		server := &Server{DurableContext: stub}
		req := httptest.NewRequest(http.MethodPost, "/v1/durable-context/grants",
			strings.NewReader(`{"principal":"alice","memory_id":"`+uuid.NewString()+`"}`))
		req, _, _, _ = memoryHandlerContext(req, nil)
		rec := httptest.NewRecorder()
		server.handleCreateDurableContextGrant(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("revoke", func(t *testing.T) {
		stub := &durableContextStub{grant: grant}
		server := &Server{DurableContext: stub}
		req := durableRouteRequest(http.MethodPost,
			"/v1/durable-context/grants/"+grantID.String()+"/revoke",
			map[string]string{"grantID": grantID.String()}, nil)
		req, _, _, requestWorkspace := memoryHandlerContext(req, nil)
		rec := httptest.NewRecorder()

		server.handleRevokeDurableContextGrant(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if stub.revokeCmd.GrantID != grantID || stub.revokeCmd.WorkspaceID != requestWorkspace {
			t.Fatalf("revoke command = %+v", stub.revokeCmd)
		}
	})

	t.Run("list passes principal filter", func(t *testing.T) {
		view := durablecontext.GrantView{
			Grant:        *grant,
			MemoryStatus: memory.StatusActive,
			Status:       durablecontext.GrantStatusActive,
		}
		stub := &durableContextStub{grantViews: []durablecontext.GrantView{view}}
		server := &Server{DurableContext: stub}
		req := httptest.NewRequest(http.MethodGet,
			"/v1/durable-context/grants?principal=alice&limit=10", nil)
		req, _, _, _ = memoryHandlerContext(req, nil)
		rec := httptest.NewRecorder()

		server.handleListDurableContextGrants(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if stub.listQuery.Principal != "alice" || stub.listQuery.Limit != 10 {
			t.Fatalf("list query = %+v", stub.listQuery)
		}
		var page struct {
			Grants []durablecontext.GrantView `json:"grants"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Grants) != 1 || page.Grants[0].ID != grantID {
			t.Fatalf("grants = %+v", page.Grants)
		}
		if page.Grants[0].Status != durablecontext.GrantStatusActive ||
			page.Grants[0].MemoryStatus != memory.StatusActive {
			t.Fatalf("grant view annotation = %+v", page.Grants[0])
		}
		if strings.Contains(rec.Body.String(), "granted_by_token_id") {
			t.Fatalf("token identifier leaked: %s", rec.Body.String())
		}
	})
}

func TestHandleDurableContextDisabled(t *testing.T) {
	server := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/v1/durable-context/recall",
		strings.NewReader(`{"contract":"durable-context.v1","principal":"alice"}`))
	req, _, _, _ = memoryHandlerContext(req, nil)
	rec := httptest.NewRecorder()

	server.handleDurableContextRecall(rec, req)

	if rec.Code != http.StatusServiceUnavailable ||
		!strings.Contains(rec.Body.String(), "durable_context_disabled") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

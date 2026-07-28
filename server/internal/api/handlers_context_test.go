package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/contextpack"
	"github.com/PeterGuy326/mem/server/internal/search"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

type contextSearchStub struct {
	query search.Query
}

type contextMemoryStub struct {
	query contextpack.MemoryQuery
}

func (s *contextMemoryStub) Recall(_ context.Context, q contextpack.MemoryQuery) ([]contextpack.MemoryHit, error) {
	s.query = q
	return []contextpack.MemoryHit{{
		MemoryID: uuid.New(), Kind: "decision", Content: "mem returns evidence",
		Path: "/Work", ContentSHA256: "memory-sha", Score: 1,
	}}, nil
}

func (s *contextSearchStub) Search(_ context.Context, q search.Query) ([]search.Hit, error) {
	s.query = q
	return []search.Hit{{
		EvidenceID:    "chunk-1",
		FileID:        uuid.New(),
		Name:          "decision.md",
		Path:          "/Work",
		MIME:          "text/markdown",
		ContentSHA256: "sha",
		ChunkIndex:    0,
		Snippet:       "Use PostgreSQL because the original files remain the source of truth.",
		Source:        search.RouteText,
	}}, nil
}

func TestHandleContextPassesTokenPathsAndReturnsEvidence(t *testing.T) {
	stub := &contextSearchStub{}
	s := &Server{Context: contextpack.New(stub)}
	req := httptest.NewRequest(http.MethodPost, "/v1/context",
		strings.NewReader(`{"query":"database decision","scope":"/Work","max_chars":1000}`))
	user := &auth.User{ID: uuid.New()}
	token := &auth.Token{Paths: []string{"/Work"}}
	ctx := context.WithValue(req.Context(), ctxUser, user)
	ctx = context.WithValue(ctx, ctxToken, token)
	rec := httptest.NewRecorder()

	s.handleContext(rec, req.WithContext(ctx))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(stub.query.AllowedPaths) != 1 || stub.query.AllowedPaths[0] != "/Work" {
		t.Fatalf("allowed paths = %#v", stub.query.AllowedPaths)
	}
	if !strings.Contains(rec.Body.String(), `"citation":"mem://files/`) {
		t.Fatalf("response lacks citation: %s", rec.Body.String())
	}
}

func TestHandleContextPassesWorkspaceAndMemoryFilters(t *testing.T) {
	stub := &contextMemoryStub{}
	s := &Server{Context: contextpack.New(nil, stub)}
	req := httptest.NewRequest(http.MethodPost, "/v1/context",
		strings.NewReader(`{"query":"database decision","scope":"/Work","source":"memory","memory_kind":"decision"}`))
	user := &auth.User{ID: uuid.New()}
	token := &auth.Token{Paths: []string{"/Work"}}
	workspaceID := uuid.New()
	ctx := context.WithValue(req.Context(), ctxUser, user)
	ctx = context.WithValue(ctx, ctxToken, token)
	ctx = context.WithValue(ctx, ctxWorkspace, &workspace.Workspace{ID: workspaceID})
	rec := httptest.NewRecorder()

	s.handleContext(rec, req.WithContext(ctx))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if stub.query.WorkspaceID != workspaceID || stub.query.Kind != "decision" {
		t.Fatalf("memory query = %+v", stub.query)
	}
	if !strings.Contains(rec.Body.String(), `"source_kind":"memory"`) {
		t.Fatalf("response lacks memory evidence: %s", rec.Body.String())
	}
}

func TestHandleContextRejectsIncompatibleSourceFilter(t *testing.T) {
	s := &Server{Context: contextpack.New(nil, &contextMemoryStub{})}
	req := httptest.NewRequest(http.MethodPost, "/v1/context",
		strings.NewReader(`{"query":"photo","source":"memory","type":"image"}`))
	ctx := context.WithValue(req.Context(), ctxUser, &auth.User{ID: uuid.New()})
	ctx = context.WithValue(ctx, ctxToken, &auth.Token{})
	rec := httptest.NewRecorder()

	s.handleContext(rec, req.WithContext(ctx))

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_filter") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

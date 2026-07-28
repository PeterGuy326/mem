package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/google/uuid"
)

func requestWithTokenPaths(paths ...string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := context.WithValue(req.Context(), ctxToken, &auth.Token{Paths: paths})
	return req.WithContext(ctx)
}

func TestRequestedWorkspaceEnforcesTokenBinding(t *testing.T) {
	bound := uuid.New()
	got, err := requestedWorkspace("", &bound)
	if err != nil || got == nil || *got != bound {
		t.Fatalf("empty header should select bound workspace: got=%v err=%v", got, err)
	}
	if _, err := requestedWorkspace(uuid.NewString(), &bound); !errors.Is(err, errTokenWorkspaceMismatch) {
		t.Fatalf("mismatched workspace error = %v", err)
	}
	got, err = requestedWorkspace(bound.String(), &bound)
	if err != nil || got == nil || *got != bound {
		t.Fatalf("matching workspace rejected: got=%v err=%v", got, err)
	}
}

func TestTokenAllowsPathUsesSegmentBoundaries(t *testing.T) {
	req := requestWithTokenPaths("/Work")
	for _, path := range []string{"/Work", "/Work/contracts", "/Work/100%_done"} {
		if !tokenAllowsPath(req, path) {
			t.Errorf("expected %q to be allowed", path)
		}
	}
	for _, path := range []string{"/", "/Worker", "/Private/Work"} {
		if tokenAllowsPath(req, path) {
			t.Errorf("expected %q to be denied", path)
		}
	}
}

func TestTokenAllowsPathTreatsWildcardCharactersLiterally(t *testing.T) {
	req := requestWithTokenPaths("/100%_done")
	if !tokenAllowsPath(req, "/100%_done/child") {
		t.Fatal("literal wildcard path should allow its own subtree")
	}
	if tokenAllowsPath(req, "/100XXdone/child") {
		t.Fatal("percent and underscore must not behave as SQL wildcards")
	}
}

func TestTokenAllowsPathEmptyOrRootIsUnrestricted(t *testing.T) {
	for _, paths := range [][]string{nil, {"/"}} {
		req := requestWithTokenPaths(paths...)
		if !tokenAllowsPath(req, "/anywhere") {
			t.Errorf("paths %#v should be unrestricted", paths)
		}
	}
}

func TestTokenAllowsPathEmptyEntryFailsClosed(t *testing.T) {
	req := requestWithTokenPaths("")
	if tokenAllowsPath(req, "/anywhere") {
		t.Fatal("an empty legacy path entry must not grant root access")
	}
}

func TestProviderControlRejectsPathRestrictedToken(t *testing.T) {
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPut, "/v1/providers/embedding"},
		{http.MethodPost, "/v1/providers/embedding/test"},
		{http.MethodPost, "/v1/providers/embedding/reindex"},
	} {
		req := requestWithTokenPaths("/Work")
		req.Method = tc.method
		req.URL.Path = tc.path
		rec := httptest.NewRecorder()
		called := false
		next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			called = true
		})

		(&Server{}).requireUnrestrictedPaths(next).ServeHTTP(rec, req)

		if called {
			t.Errorf("%s %s reached provider handler with a path-restricted token", tc.method, tc.path)
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s status=%d body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestDelegatedTokenPathsCannotBroadenAuthorization(t *testing.T) {
	got, err := delegatedTokenPaths([]string{"/Work"}, nil)
	if err != nil || len(got) != 1 || got[0] != "/Work" {
		t.Fatalf("omitted child paths should inherit: got=%#v err=%v", got, err)
	}
	got, err = delegatedTokenPaths(
		[]string{"/Work", "/Shared"},
		[]string{"/Work/contracts", "/Shared"},
	)
	if err != nil || len(got) != 2 {
		t.Fatalf("narrow delegation rejected: got=%#v err=%v", got, err)
	}
	for _, paths := range [][]string{{"/"}, {"/Private"}, {""}} {
		if _, err := delegatedTokenPaths([]string{"/Work"}, paths); err == nil {
			t.Fatalf("broader child paths %#v should be rejected", paths)
		}
	}
}

func TestDelegatedTokenPathsUnrestrictedParentMayChooseScope(t *testing.T) {
	got, err := delegatedTokenPaths(nil, []string{"/Work"})
	if err != nil || len(got) != 1 || got[0] != "/Work" {
		t.Fatalf("root delegation failed: got=%#v err=%v", got, err)
	}
}

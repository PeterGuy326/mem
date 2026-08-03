package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/indexgeneration"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

type fakeIndexGenerationService struct {
	workspaceID uuid.UUID
	buildID     uuid.UUID
}

func (f *fakeIndexGenerationService) List(
	_ context.Context,
	workspaceID uuid.UUID,
	limit int,
) ([]indexgeneration.Build, error) {
	f.workspaceID = workspaceID
	if limit != 25 {
		return nil, indexgeneration.ErrUnavailable
	}
	return []indexgeneration.Build{{ID: f.buildID, WorkspaceID: workspaceID, State: indexgeneration.StateBuilding}}, nil
}

func (f *fakeIndexGenerationService) Get(
	_ context.Context,
	workspaceID, buildID uuid.UUID,
) (*indexgeneration.Build, error) {
	f.workspaceID = workspaceID
	if buildID != f.buildID {
		return nil, indexgeneration.ErrNotFound
	}
	return &indexgeneration.Build{ID: buildID, WorkspaceID: workspaceID, State: indexgeneration.StateReady}, nil
}

func (f *fakeIndexGenerationService) Events(
	_ context.Context,
	workspaceID, buildID uuid.UUID,
) ([]indexgeneration.Event, error) {
	f.workspaceID = workspaceID
	if buildID != f.buildID {
		return nil, indexgeneration.ErrNotFound
	}
	return []indexgeneration.Event{{BuildID: buildID, WorkspaceID: workspaceID, EventType: "created"}}, nil
}

func TestIndexGenerationStatusHandlersStayWorkspaceScoped(t *testing.T) {
	workspaceID := uuid.New()
	buildID := uuid.New()
	service := &fakeIndexGenerationService{buildID: buildID}
	server := &Server{IndexGenerations: service}

	t.Run("list", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := indexGenerationRequest(http.MethodGet,
			"/v1/workspaces/current/index-generations?limit=25", workspaceID, "")
		server.handleListIndexGenerations(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Items          []indexgeneration.Build `json:"items"`
			ExecutionWired bool                    `json:"execution_wired"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Items) != 1 || response.Items[0].ID != buildID || response.ExecutionWired {
			t.Fatalf("response = %#v", response)
		}
	})

	t.Run("get", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := indexGenerationRequest(http.MethodGet,
			"/v1/workspaces/current/index-generations/"+buildID.String(), workspaceID, buildID.String())
		server.handleGetIndexGeneration(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("events", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := indexGenerationRequest(http.MethodGet,
			"/v1/workspaces/current/index-generations/"+buildID.String()+"/events", workspaceID, buildID.String())
		server.handleListIndexGenerationEvents(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
		}
	})

	if service.workspaceID != workspaceID {
		t.Fatalf("service workspace = %s, want %s", service.workspaceID, workspaceID)
	}
}

func TestIndexGenerationStatusRejectsInvalidID(t *testing.T) {
	server := &Server{IndexGenerations: &fakeIndexGenerationService{buildID: uuid.New()}}
	recorder := httptest.NewRecorder()
	request := indexGenerationRequest(http.MethodGet,
		"/v1/workspaces/current/index-generations/not-a-uuid", uuid.New(), "not-a-uuid")
	server.handleGetIndexGeneration(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestIndexGenerationPublicRoutesAreReadOnly(t *testing.T) {
	routes, ok := (&Server{}).Router().(chi.Routes)
	if !ok {
		t.Fatal("Server.Router did not return chi.Routes")
	}
	wanted := map[string]bool{
		"/v1/workspaces/current/index-generations":                  false,
		"/v1/workspaces/current/index-generations/{buildID}":        false,
		"/v1/workspaces/current/index-generations/{buildID}/events": false,
	}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/v1/workspaces/current/index-generations") {
			if method != http.MethodGet {
				t.Fatalf("index generation route %s unexpectedly allows %s", route, method)
			}
			if _, exists := wanted[route]; exists {
				wanted[route] = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for route, found := range wanted {
		if !found {
			t.Fatalf("read-only index generation route %s is not registered", route)
		}
	}
}

func indexGenerationRequest(method, target string, workspaceID uuid.UUID, routeID string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	ctx := context.WithValue(request.Context(), ctxWorkspace, &workspace.Workspace{ID: workspaceID})
	if routeID != "" {
		route := chi.NewRouteContext()
		route.URLParams.Add("buildID", routeID)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, route)
	}
	return request.WithContext(ctx)
}

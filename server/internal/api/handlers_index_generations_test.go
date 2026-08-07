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

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/indexgeneration"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

type fakeIndexGenerationService struct {
	workspaceID uuid.UUID
	buildID     uuid.UUID
	lastAction  string
	lastActor   uuid.UUID
	lastProfile string
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

func (f *fakeIndexGenerationService) Create(
	_ context.Context,
	workspaceID, actorID uuid.UUID,
	profileID string,
) (*indexgeneration.Build, error) {
	f.workspaceID = workspaceID
	f.lastActor = actorID
	f.lastProfile = profileID
	if profileID == "" {
		return nil, indexgeneration.ErrProfileUnavailable
	}
	return &indexgeneration.Build{ID: f.buildID, WorkspaceID: workspaceID, State: indexgeneration.StateBuilding}, nil
}

func (f *fakeIndexGenerationService) Cancel(
	_ context.Context,
	workspaceID, actorID, buildID uuid.UUID,
) (*indexgeneration.Build, error) {
	return f.doAction("cancel", workspaceID, actorID, buildID)
}

func (f *fakeIndexGenerationService) Resume(
	_ context.Context,
	workspaceID, actorID, buildID uuid.UUID,
) (*indexgeneration.Build, error) {
	return f.doAction("resume", workspaceID, actorID, buildID)
}

func (f *fakeIndexGenerationService) Activate(
	_ context.Context,
	workspaceID, actorID, buildID uuid.UUID,
) (*indexgeneration.Build, error) {
	return f.doAction("activate", workspaceID, actorID, buildID)
}

func (f *fakeIndexGenerationService) Rollback(
	_ context.Context,
	workspaceID, actorID, buildID uuid.UUID,
) (*indexgeneration.Build, error) {
	return f.doAction("rollback", workspaceID, actorID, buildID)
}

func (f *fakeIndexGenerationService) Discard(
	_ context.Context,
	workspaceID, actorID, buildID uuid.UUID,
) (*indexgeneration.Build, error) {
	return f.doAction("discard", workspaceID, actorID, buildID)
}

func (f *fakeIndexGenerationService) doAction(
	action string,
	workspaceID, actorID, buildID uuid.UUID,
) (*indexgeneration.Build, error) {
	f.workspaceID = workspaceID
	f.lastActor = actorID
	f.lastAction = action
	if buildID != f.buildID {
		return nil, indexgeneration.ErrNotFound
	}
	return &indexgeneration.Build{ID: buildID, WorkspaceID: workspaceID, State: indexgeneration.StateCancelled}, nil
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
		if len(response.Items) != 1 || response.Items[0].ID != buildID || !response.ExecutionWired {
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

func TestIndexGenerationMutationHandlers(t *testing.T) {
	workspaceID := uuid.New()
	actorID := uuid.New()
	buildID := uuid.New()
	service := &fakeIndexGenerationService{buildID: buildID}
	server := &Server{IndexGenerations: service}

	t.Run("create", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		body := strings.NewReader(`{"profile_id":"local-fast-v2"}`)
		request := indexGenerationMutationRequest(http.MethodPost,
			"/v1/workspaces/current/index-generations", workspaceID, actorID, "", body)
		server.handleCreateIndexGeneration(recorder, request)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
		}
		if service.lastProfile != "local-fast-v2" {
			t.Fatalf("profile = %q", service.lastProfile)
		}
		if service.lastActor != actorID {
			t.Fatalf("actor = %s, want %s", service.lastActor, actorID)
		}
	})

	t.Run("create_rejects_empty_profile", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		body := strings.NewReader(`{"profile_id":""}`)
		request := indexGenerationMutationRequest(http.MethodPost,
			"/v1/workspaces/current/index-generations", workspaceID, actorID, "", body)
		server.handleCreateIndexGeneration(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("cancel", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := indexGenerationMutationRequest(http.MethodPost,
			"/v1/workspaces/current/index-generations/"+buildID.String()+"/cancel",
			workspaceID, actorID, buildID.String(), nil)
		server.handleCancelIndexGeneration(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
		}
		if service.lastAction != "cancel" {
			t.Fatalf("action = %q", service.lastAction)
		}
	})

	t.Run("cancel_not_found", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		otherID := uuid.New()
		request := indexGenerationMutationRequest(http.MethodPost,
			"/v1/workspaces/current/index-generations/"+otherID.String()+"/cancel",
			workspaceID, actorID, otherID.String(), nil)
		server.handleCancelIndexGeneration(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("disabled_service", func(t *testing.T) {
		disabledServer := &Server{}
		recorder := httptest.NewRecorder()
		request := indexGenerationMutationRequest(http.MethodPost,
			"/v1/workspaces/current/index-generations/"+buildID.String()+"/cancel",
			workspaceID, actorID, buildID.String(), nil)
		disabledServer.handleCancelIndexGeneration(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestIndexGenerationPublicRoutesAreReadOnly(t *testing.T) {
	routes, ok := (&Server{}).Router().(chi.Routes)
	if !ok {
		t.Fatal("Server.Router did not return chi.Routes")
	}
	wantedGET := map[string]bool{
		"/v1/workspaces/current/index-generations":                  false,
		"/v1/workspaces/current/index-generations/{buildID}":        false,
		"/v1/workspaces/current/index-generations/{buildID}/events": false,
	}
	wantedPOST := map[string]bool{
		"/v1/workspaces/current/index-generations":                    false,
		"/v1/workspaces/current/index-generations/{buildID}/cancel":   false,
		"/v1/workspaces/current/index-generations/{buildID}/resume":   false,
		"/v1/workspaces/current/index-generations/{buildID}/activate": false,
		"/v1/workspaces/current/index-generations/{buildID}/rollback": false,
		"/v1/workspaces/current/index-generations/{buildID}/discard":  false,
	}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/v1/workspaces/current/index-generations") {
			switch method {
			case http.MethodGet:
				if _, exists := wantedGET[route]; exists {
					wantedGET[route] = true
				}
			case http.MethodPost:
				if _, exists := wantedPOST[route]; exists {
					wantedPOST[route] = true
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for route, found := range wantedGET {
		if !found {
			t.Fatalf("GET route %s is not registered", route)
		}
	}
	for route, found := range wantedPOST {
		if !found {
			t.Fatalf("POST route %s is not registered", route)
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

func indexGenerationMutationRequest(method, target string, workspaceID, actorID uuid.UUID, routeID string, body *strings.Reader) *http.Request {
	var request *http.Request
	if body != nil {
		request = httptest.NewRequest(method, target, body)
	} else {
		request = httptest.NewRequest(method, target, nil)
	}
	ctx := context.WithValue(request.Context(), ctxWorkspace, &workspace.Workspace{ID: workspaceID})
	ctx = context.WithValue(ctx, ctxActor, &auth.User{ID: actorID})
	if routeID != "" {
		route := chi.NewRouteContext()
		route.URLParams.Add("buildID", routeID)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, route)
	}
	return request.WithContext(ctx)
}

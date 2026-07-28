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
	"github.com/PeterGuy326/mem/server/internal/handoff"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

type handoffServiceStub struct {
	checkpointCommand    handoff.CheckpointCommand
	resumeQuery          handoff.ResumeQuery
	listTasksQuery       handoff.ListTasksQuery
	listCheckpointsQuery handoff.ListCheckpointsQuery
	checkpointResult     *handoff.CheckpointResult
	resumeResult         *handoff.ResumeSnapshot
	tasks                []handoff.Task
	checkpoints          []handoff.CheckpointSummary
	checkpoint           *handoff.CheckpointRecord
	err                  error
	calls                int
}

func (s *handoffServiceStub) Checkpoint(
	_ context.Context,
	command handoff.CheckpointCommand,
) (*handoff.CheckpointResult, error) {
	s.calls++
	s.checkpointCommand = command
	return s.checkpointResult, s.err
}

func (s *handoffServiceStub) Resume(
	_ context.Context,
	query handoff.ResumeQuery,
) (*handoff.ResumeSnapshot, error) {
	s.calls++
	s.resumeQuery = query
	return s.resumeResult, s.err
}

func (s *handoffServiceStub) GetCheckpoint(
	_ context.Context,
	_ handoff.GetCheckpointQuery,
) (*handoff.CheckpointRecord, error) {
	s.calls++
	return s.checkpoint, s.err
}

func (s *handoffServiceStub) ListTasks(
	_ context.Context,
	query handoff.ListTasksQuery,
) ([]handoff.Task, error) {
	s.calls++
	s.listTasksQuery = query
	return s.tasks, s.err
}

func (s *handoffServiceStub) ListCheckpoints(
	_ context.Context,
	query handoff.ListCheckpointsQuery,
) ([]handoff.CheckpointSummary, error) {
	s.calls++
	s.listCheckpointsQuery = query
	return s.checkpoints, s.err
}

func validHandoffDocument(taskKey string) handoff.HandoffV1 {
	return handoff.HandoffV1{
		Contract:       handoff.ContractName,
		SchemaVersion:  handoff.SchemaVersionV1,
		CheckpointKind: handoff.CheckpointKindHandoff,
		TaskKey:        taskKey,
		ScopePath:      "/Projects/mem",
		State: handoff.StateV1{
			Status: handoff.TaskStatusInProgress,
			Goal:   "Build a portable drive for Agents",
			Progress: handoff.ProgressV1{
				Summary:   "The memory plane exists.",
				Completed: []string{"Added provenance"},
			},
			Decisions:     []handoff.DecisionV1{},
			NextSteps:     []handoff.NextStepV1{{Summary: "Implement resume", References: []string{}}},
			Blockers:      []handoff.BlockerV1{},
			OpenQuestions: []string{},
			Artifacts:     []handoff.ArtifactV1{},
		},
		Producer: handoff.ProducerV1{AgentID: "claude-code", SessionID: "session-1"},
	}
}

func withHandoffContext(
	req *http.Request,
	taskKey string,
	paths []string,
) (*http.Request, uuid.UUID, uuid.UUID, uuid.UUID) {
	actorID := uuid.New()
	ownerID := uuid.New()
	tokenID := uuid.New()
	workspaceID := uuid.New()
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("taskKey", taskKey)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, ctxActor, &auth.User{ID: actorID})
	ctx = context.WithValue(ctx, ctxUser, &auth.User{ID: ownerID})
	ctx = context.WithValue(ctx, ctxToken, &auth.Token{
		ID: tokenID, Paths: paths, Scopes: append([]string(nil), auth.AllScopes...),
	})
	ctx = context.WithValue(ctx, ctxWorkspace, &workspace.Workspace{
		ID: workspaceID, ResourceOwnerUserID: ownerID, Role: workspace.RoleOwner,
	})
	return req.WithContext(ctx), actorID, tokenID, workspaceID
}

func TestHandleCheckpointPersistsAuthenticatedVersionedHandoff(t *testing.T) {
	taskKey := "portable-agent-drive"
	checkpointID := uuid.New()
	stub := &handoffServiceStub{checkpointResult: &handoff.CheckpointResult{
		Checkpoint: handoff.CheckpointRecord{ID: checkpointID, TaskKey: taskKey},
	}}
	server := &Server{Handoff: stub}
	body, _ := json.Marshal(validHandoffDocument(taskKey))
	req := httptest.NewRequest(http.MethodPost,
		"/v1/tasks/"+taskKey+"/checkpoints", strings.NewReader(string(body)))
	req.Header.Set("Idempotency-Key", "checkpoint-1")
	req, actorID, tokenID, workspaceID := withHandoffContext(req, taskKey, []string{"/Projects"})
	rec := httptest.NewRecorder()

	server.handleCheckpoint(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if stub.checkpointCommand.WorkspaceID != workspaceID ||
		stub.checkpointCommand.CreatedByUserID == nil ||
		*stub.checkpointCommand.CreatedByUserID != actorID ||
		stub.checkpointCommand.CreatedByTokenID == nil ||
		*stub.checkpointCommand.CreatedByTokenID != tokenID {
		t.Fatalf("authenticated command = %+v", stub.checkpointCommand)
	}
	if stub.checkpointCommand.TaskKey != taskKey ||
		stub.checkpointCommand.Handoff.Producer.AgentID != "claude-code" ||
		stub.checkpointCommand.IdempotencyKey != "checkpoint-1" {
		t.Fatalf("checkpoint command = %+v", stub.checkpointCommand)
	}
	if got := rec.Header().Get("Location"); !strings.Contains(got, checkpointID.String()) {
		t.Fatalf("Location = %q", got)
	}
}

func TestHandleCheckpointReplayAndContractErrors(t *testing.T) {
	t.Run("replay uses 200", func(t *testing.T) {
		taskKey := "task"
		stub := &handoffServiceStub{checkpointResult: &handoff.CheckpointResult{
			Checkpoint: handoff.CheckpointRecord{ID: uuid.New(), TaskKey: taskKey},
			Replayed:   true,
		}}
		server := &Server{Handoff: stub}
		body, _ := json.Marshal(validHandoffDocument(taskKey))
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
		req.Header.Set("Idempotency-Key", "same")
		req, _, _, _ = withHandoffContext(req, taskKey, nil)
		rec := httptest.NewRecorder()

		server.handleCheckpoint(rec, req)

		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"replayed":true`) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("base required is explicit", func(t *testing.T) {
		taskKey := "task"
		stub := &handoffServiceStub{err: handoff.ErrBaseRequired}
		server := &Server{Handoff: stub}
		body, _ := json.Marshal(validHandoffDocument(taskKey))
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
		req.Header.Set("Idempotency-Key", "next")
		req, _, _, _ = withHandoffContext(req, taskKey, nil)
		rec := httptest.NewRecorder()

		server.handleCheckpoint(rec, req)

		if rec.Code != http.StatusPreconditionRequired ||
			!strings.Contains(rec.Body.String(), "base_checkpoint_required") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown field fails before service", func(t *testing.T) {
		stub := &handoffServiceStub{}
		server := &Server{Handoff: stub}
		req := httptest.NewRequest(http.MethodPost, "/",
			strings.NewReader(`{"contract":"mem.handoff","unknown":true}`))
		req.Header.Set("Idempotency-Key", "bad")
		req, _, _, _ = withHandoffContext(req, "task", nil)
		rec := httptest.NewRecorder()

		server.handleCheckpoint(rec, req)

		if rec.Code != http.StatusBadRequest || stub.calls != 0 {
			t.Fatalf("status=%d calls=%d body=%s", rec.Code, stub.calls, rec.Body.String())
		}
	})

	t.Run("mem evidence requires read scope", func(t *testing.T) {
		taskKey := "task"
		required := false
		document := validHandoffDocument(taskKey)
		document.State.Artifacts = []handoff.ArtifactV1{{
			URI: "mem://files/" + uuid.NewString(), Required: &required,
		}}
		body, _ := json.Marshal(document)
		stub := &handoffServiceStub{}
		server := &Server{Handoff: stub}
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
		req.Header.Set("Idempotency-Key", "write-only")
		req, _, _, _ = withHandoffContext(req, taskKey, nil)
		req.Context().Value(ctxToken).(*auth.Token).Scopes = []string{auth.ScopeWrite}
		rec := httptest.NewRecorder()

		server.handleCheckpoint(rec, req)

		if rec.Code != http.StatusForbidden ||
			!strings.Contains(rec.Body.String(), "handoff_reference_forbidden") ||
			stub.calls != 0 {
			t.Fatalf("status=%d calls=%d body=%s", rec.Code, stub.calls, rec.Body.String())
		}
	})
}

func TestHandleResumeReportsRequiredMissingReference(t *testing.T) {
	taskKey := "portable-agent-drive"
	required := true
	now := time.Now().UTC()
	checkpoint := handoff.CheckpointRecord{
		ID: uuid.New(), TaskKey: taskKey,
		Handoff: validHandoffDocument(taskKey),
	}
	checkpoint.Handoff.State.Artifacts = []handoff.ArtifactV1{{
		URI: "mem://files/" + uuid.NewString(), Required: &required,
	}}
	ref := handoff.Reference{
		URI:      checkpoint.Handoff.State.Artifacts[0].URI,
		Relation: "artifact", Required: true,
	}
	stub := &handoffServiceStub{resumeResult: &handoff.ResumeSnapshot{
		Contract: handoff.ResumeContractName, SchemaVersion: 1,
		Task:       handoff.Task{TaskKey: taskKey, ScopePath: "/Projects/mem"},
		Checkpoint: checkpoint, References: []handoff.Reference{ref}, RetrievedAt: now,
	}}
	server := &Server{Handoff: stub}
	req := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"scope":"/Projects","limit":5,"max_chars":4000}`))
	req, _, _, workspaceID := withHandoffContext(req, taskKey, []string{"/Projects"})
	rec := httptest.NewRecorder()

	server.handleResume(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if stub.resumeQuery.WorkspaceID != workspaceID ||
		stub.resumeQuery.TaskKey != taskKey ||
		stub.resumeQuery.Scope != "/Projects" ||
		stub.resumeQuery.Limit != 5 {
		t.Fatalf("resume query = %+v", stub.resumeQuery)
	}
	if !strings.Contains(rec.Body.String(), `"complete":false`) ||
		!strings.Contains(rec.Body.String(), `"status":"unavailable"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestHandleListTasksPassesPathScopeAndPagination(t *testing.T) {
	stub := &handoffServiceStub{tasks: []handoff.Task{{TaskKey: "one"}}}
	server := &Server{Handoff: stub}
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks?scope=/Work&limit=25", nil)
	req, _, _, workspaceID := withHandoffContext(req, "", []string{"/Work"})
	rec := httptest.NewRecorder()

	server.handleListTasks(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if stub.listTasksQuery.WorkspaceID != workspaceID ||
		stub.listTasksQuery.Scope != "/Work" ||
		stub.listTasksQuery.Limit != 25 ||
		len(stub.listTasksQuery.AllowedPaths) != 1 {
		t.Fatalf("query = %+v", stub.listTasksQuery)
	}
}

func TestHandleListCheckpointsReturnsBoundedSummary(t *testing.T) {
	taskKey := "portable-agent-drive"
	checkpointID := uuid.New()
	stub := &handoffServiceStub{checkpoints: []handoff.CheckpointSummary{{
		ID:              checkpointID,
		TaskKey:         taskKey,
		Sequence:        3,
		CheckpointKind:  handoff.CheckpointKindHandoff,
		Status:          handoff.TaskStatusReady,
		ProgressExcerpt: "Bounded progress",
		ProgressLength:  900,
		CompletedCount:  2,
		ReferenceCount:  4,
		PayloadSHA256:   strings.Repeat("a", 64),
		ProducerAgent:   "codex",
		ProducerSession: "session-3",
		CreatedAt:       time.Now().UTC(),
	}}}
	server := &Server{Handoff: stub}
	req := httptest.NewRequest(
		http.MethodGet,
		"/v1/tasks/"+taskKey+"/checkpoints?scope=/Work&limit=25&before=4",
		nil,
	)
	req, _, _, workspaceID := withHandoffContext(req, taskKey, []string{"/Work"})
	rec := httptest.NewRecorder()

	server.handleListCheckpoints(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if stub.listCheckpointsQuery.WorkspaceID != workspaceID ||
		stub.listCheckpointsQuery.TaskKey != taskKey ||
		stub.listCheckpointsQuery.Scope != "/Work" ||
		stub.listCheckpointsQuery.Limit != 25 ||
		stub.listCheckpointsQuery.Before == nil ||
		*stub.listCheckpointsQuery.Before != 4 ||
		len(stub.listCheckpointsQuery.AllowedPaths) != 1 {
		t.Fatalf("query = %+v", stub.listCheckpointsQuery)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"progress_excerpt":"Bounded progress"`) ||
		!strings.Contains(body, `"reference_count":4`) ||
		strings.Contains(body, `"handoff":`) ||
		strings.Contains(body, `"references":`) {
		t.Fatalf("unbounded checkpoint list response: %s", body)
	}
}

func TestHandoffTaskKeyDecodesExactlyOnce(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want string
	}{
		{"team%2Frelease%20%CE%B1", "team/release α"},
		{"literal%252Fvalue", "literal%2Fvalue"},
		{"plain-task", "plain-task"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("taskKey", test.raw)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		got, err := handoffTaskKey(req)
		if err != nil {
			t.Fatalf("handoffTaskKey(%q): %v", test.raw, err)
		}
		if got != test.want {
			t.Fatalf("handoffTaskKey(%q) = %q, want %q", test.raw, got, test.want)
		}
	}
}

func TestWriteHandoffErrorDoesNotHideConflictKinds(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{errors.Join(handoff.ErrUnsupportedVersion, errors.New("v2")), 422, "unsupported_handoff_version"},
		{handoff.ErrHeadConflict, 409, "checkpoint_head_conflict"},
		{handoff.ErrNotFound, 404, "not_found"},
	} {
		rec := httptest.NewRecorder()
		writeHandoffError(rec, test.err)
		if rec.Code != test.status || !strings.Contains(rec.Body.String(), test.code) {
			t.Fatalf("err=%v status=%d body=%s", test.err, rec.Code, rec.Body.String())
		}
	}
}

package apiclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckpointPostsVersionedHandoffAndIdempotencyHeader(t *testing.T) {
	var (
		gotMethod    string
		gotPath      string
		gotKey       string
		gotWorkspace string
		gotBody      []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		gotKey = r.Header.Get("Idempotency-Key")
		gotWorkspace = r.Header.Get("X-Workspace-ID")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"checkpoint":{"id":"cp-1"},"replayed":false}`)
	}))
	defer srv.Close()

	taskKey := "task 42/phase-a"
	response, err := New(srv.URL, "token").WithWorkspace("workspace-1").Checkpoint(
		context.Background(),
		taskKey,
		validHandoff(taskKey),
		"task-42-phase-a-v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q", gotMethod)
	}
	if gotPath != "/v1/tasks/task%2042%2Fphase-a/checkpoints" {
		t.Fatalf("escaped path = %q", gotPath)
	}
	if gotKey != "task-42-phase-a-v1" {
		t.Fatalf("Idempotency-Key = %q", gotKey)
	}
	if gotWorkspace != "workspace-1" {
		t.Fatalf("X-Workspace-ID = %q", gotWorkspace)
	}

	var body HandoffV1
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatal(err)
	}
	if body.Contract != HandoffContract ||
		body.SchemaVersion != HandoffSchemaVersion ||
		body.TaskKey != taskKey ||
		body.State.Progress.Completed == nil {
		t.Fatalf("handoff body = %#v", body)
	}
	if !strings.Contains(string(response), `"id":"cp-1"`) {
		t.Fatalf("transparent response = %s", response)
	}
}

func TestCheckpointRejectsURLPayloadTaskKeyMismatchBeforeRequest(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer srv.Close()

	handoff := validHandoff("payload-task")
	_, err := New(srv.URL, "token").Checkpoint(
		context.Background(),
		"url-task",
		handoff,
		"retry-1",
	)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("unexpected HTTP requests = %d", requests)
	}
}

func TestResumePostsSelectionWithoutDuplicatingTaskKeyInBody(t *testing.T) {
	var (
		gotPath string
		gotBody map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"contract":"mem.resume","schema_version":1,"missing":[]}`)
	}))
	defer srv.Close()

	response, err := New(srv.URL, "token").Resume(
		context.Background(),
		"task 42",
		ResumeRequest{
			CheckpointID: "11111111-1111-1111-1111-111111111111",
			Scope:        "/Projects/mem",
			Focus:        "remaining tests",
			Limit:        6,
			MaxChars:     9000,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/tasks/task%2042/resume" {
		t.Fatalf("escaped path = %q", gotPath)
	}
	if _, duplicated := gotBody["task_key"]; duplicated {
		t.Fatalf("task_key must only appear in the URL: %#v", gotBody)
	}
	if gotBody["scope"] != "/Projects/mem" ||
		gotBody["focus"] != "remaining tests" ||
		gotBody["limit"] != float64(6) ||
		gotBody["max_chars"] != float64(9000) {
		t.Fatalf("resume body = %#v", gotBody)
	}
	if !strings.Contains(string(response), `"contract":"mem.resume"`) {
		t.Fatalf("transparent response = %s", response)
	}
}

func TestHandoffReadMethodsPreserveCanonicalPathsAndQueries(t *testing.T) {
	taskKey := "task 42/phase-a"
	taskID := "11111111-1111-1111-1111-111111111111"
	checkpointID := "22222222-2222-2222-2222-222222222222"
	after := "33333333-3333-3333-3333-333333333333"
	var requestIndex int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestIndex++
		w.Header().Set("Content-Type", "application/json")
		switch requestIndex {
		case 1:
			if r.Method != http.MethodGet || r.URL.EscapedPath() != "/v1/tasks" {
				t.Fatalf("task request = %s %s", r.Method, r.URL.EscapedPath())
			}
			if query := r.URL.Query(); query.Get("scope") != "/Projects/mem α" ||
				query.Get("limit") != "25" ||
				query.Get("after") != after {
				t.Fatalf("task query = %v", query)
			}
			_, _ = io.WriteString(w, `{"tasks":[{
				"id":"`+taskID+`",
				"task_key":"task 42/phase-a",
				"scope_path":"/Projects/mem",
				"head_checkpoint_id":"`+checkpointID+`",
				"head_sequence":4
			}]}`)
		case 2:
			want := "/v1/tasks/task%2042%2Fphase-a/checkpoints"
			if r.Method != http.MethodGet || r.URL.EscapedPath() != want {
				t.Fatalf("checkpoint list request = %s %s", r.Method, r.URL.EscapedPath())
			}
			if query := r.URL.Query(); query.Get("scope") != "/Projects/mem" ||
				query.Get("limit") != "10" ||
				query.Get("before") != "9" {
				t.Fatalf("checkpoint list query = %v", query)
			}
			_, _ = io.WriteString(w, `{"checkpoints":[{
				"id":"`+checkpointID+`",
				"task_id":"`+taskID+`",
				"task_key":"task 42/phase-a",
				"sequence":4,
				"checkpoint_kind":"handoff",
				"contract":"mem.handoff",
				"schema_version":1,
				"scope_path":"/Projects/mem",
				"status":"ready",
				"progress_excerpt":"Client contract implemented",
				"progress_length":27,
				"completed_count":1,
				"reference_count":0
			}]}`)
		case 3:
			want := "/v1/tasks/task%2042%2Fphase-a/checkpoints/" + checkpointID
			if r.Method != http.MethodGet || r.URL.EscapedPath() != want {
				t.Fatalf("checkpoint get request = %s %s", r.Method, r.URL.EscapedPath())
			}
			if got := r.URL.Query().Get("scope"); got != "/Projects/mem" {
				t.Fatalf("checkpoint get scope = %q", got)
			}
			_, _ = io.WriteString(w, `{
				"id":"`+checkpointID+`",
				"task_id":"`+taskID+`",
				"task_key":"task 42/phase-a",
				"sequence":4,
				"checkpoint_kind":"handoff",
				"contract":"mem.handoff",
				"schema_version":1,
				"scope_path":"/Projects/mem",
				"handoff":{"task_key":"task 42/phase-a"},
				"references":[]
			}`)
		default:
			t.Fatalf("unexpected request %d: %s %s", requestIndex, r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "token")
	tasks, err := client.ListTasks(context.Background(), TaskListOptions{
		Scope: "/Projects/mem α",
		Limit: 25,
		After: after,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks.Tasks) != 1 ||
		tasks.Tasks[0].ID != taskID ||
		tasks.Tasks[0].HeadCheckpointID == nil ||
		*tasks.Tasks[0].HeadCheckpointID != checkpointID {
		t.Fatalf("tasks = %+v", tasks)
	}

	checkpoints, err := client.ListCheckpoints(
		context.Background(),
		taskKey,
		CheckpointListOptions{Scope: "/Projects/mem", Limit: 10, Before: 9},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints.Checkpoints) != 1 ||
		checkpoints.Checkpoints[0].ID != checkpointID ||
		checkpoints.Checkpoints[0].TaskKey != taskKey ||
		checkpoints.Checkpoints[0].Status != "ready" ||
		checkpoints.Checkpoints[0].ProgressExcerpt != "Client contract implemented" {
		t.Fatalf("checkpoints = %+v", checkpoints)
	}

	checkpoint, err := client.GetCheckpoint(
		context.Background(),
		taskKey,
		checkpointID,
		CheckpointGetOptions{Scope: "/Projects/mem"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.ID != checkpointID || checkpoint.TaskKey != taskKey {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}
}

func TestHandoffReadMethodsRejectInvalidInputBeforeRequest(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer srv.Close()
	client := New(srv.URL, "token")

	if _, err := client.ListTasks(context.Background(), TaskListOptions{
		Scope: "relative",
	}); err == nil {
		t.Fatal("relative task scope accepted")
	}
	if _, err := client.ListTasks(context.Background(), TaskListOptions{
		Limit: 201,
	}); err == nil {
		t.Fatal("oversized task limit accepted")
	}
	if _, err := client.ListTasks(context.Background(), TaskListOptions{
		After: "not-a-uuid",
	}); err == nil {
		t.Fatal("invalid task after cursor accepted")
	}
	if _, err := client.ListCheckpoints(
		context.Background(),
		"task",
		CheckpointListOptions{Before: -1},
	); err == nil {
		t.Fatal("negative checkpoint sequence accepted")
	}
	if _, err := client.GetCheckpoint(
		context.Background(),
		"task",
		"not-a-uuid",
		CheckpointGetOptions{},
	); err == nil {
		t.Fatal("invalid checkpoint id accepted")
	}
	if requests != 0 {
		t.Fatalf("invalid inputs made %d request(s)", requests)
	}
}

func TestHandoffValidateRequiresRequiredArrays(t *testing.T) {
	handoff := validHandoff("task-42")
	handoff.State.OpenQuestions = nil
	if err := handoff.Validate(); err == nil ||
		!strings.Contains(err.Error(), "arrays") {
		t.Fatalf("error = %v", err)
	}
}

func validHandoff(taskKey string) HandoffV1 {
	dirty := true
	required := true
	return HandoffV1{
		Contract:       HandoffContract,
		SchemaVersion:  HandoffSchemaVersion,
		CheckpointKind: "handoff",
		TaskKey:        taskKey,
		ScopePath:      "/Projects/mem",
		State: HandoffState{
			Status: "ready",
			Goal:   "Finish the portable Agent drive",
			Progress: HandoffProgress{
				Summary:   "Client contract implemented",
				Completed: []string{"Defined typed handoff DTO"},
			},
			Decisions: []HandoffDecision{{
				Summary:    "Use one canonical API",
				Rationale:  "Prevent adapter drift",
				References: []string{"mem://memories/decision-1"},
			}},
			NextSteps: []HandoffNextStep{{
				Summary:    "Run integration tests",
				References: []string{},
			}},
			Blockers:      []HandoffBlocker{},
			OpenQuestions: []string{},
			Artifacts: []HandoffArtifact{{
				URI:      "mem://files/file-1",
				Role:     "implementation",
				Required: &required,
			}},
			WorkspaceState: &HandoffWorkspaceState{
				WorkingDirectory: "/workspace/mem",
				VCS: &HandoffVCS{
					Revision: "abc123",
					Branch:   "codex/handoff",
					Dirty:    &dirty,
				},
			},
		},
		Producer: HandoffProducer{
			AgentID:   "claude-code",
			SessionID: "session-7",
		},
	}
}

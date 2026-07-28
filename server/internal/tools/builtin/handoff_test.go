package builtin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/PeterGuy326/mem/server/internal/tools"
)

func TestMemCheckpointSchemaContainsStrongNestedHandoffV1(t *testing.T) {
	reg := tools.New()
	if err := registerCheckpoint(reg, apiclient.New("http://unused", "token")); err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Get("mem_checkpoint")
	if !ok {
		t.Fatal("mem_checkpoint not registered")
	}
	handoff := tool.InputSchema.Properties["handoff"]
	if handoff.Const != nil || handoff.Type != "object" {
		t.Fatalf("handoff schema = %#v", handoff)
	}
	if handoff.Properties["contract"].Const != apiclient.HandoffContract ||
		handoff.Properties["schema_version"].Const != apiclient.HandoffSchemaVersion {
		t.Fatalf("handoff contract/version constraints missing: %#v", handoff.Properties)
	}
	state := handoff.Properties["state"]
	progress := state.Properties["progress"]
	completed := progress.Properties["completed"]
	if completed.Items == nil || completed.Items.Type != "string" {
		t.Fatalf("nested progress.completed schema = %#v", completed)
	}
	decisions := state.Properties["decisions"]
	if decisions.Items == nil ||
		decisions.Items.Properties["references"].Items == nil {
		t.Fatalf("nested decisions schema = %#v", decisions)
	}
	if handoff.AdditionalProperties == nil || *handoff.AdditionalProperties {
		t.Fatal("handoff must reject additional properties")
	}
}

func TestMemCheckpointUsesTypedClientContract(t *testing.T) {
	fs := newFakeServer(
		`{"checkpoint":{"id":"11111111-1111-1111-1111-111111111111"},"replayed":false}`,
		http.StatusCreated,
		"application/json",
	)
	defer fs.Close()
	reg := tools.New()
	if err := registerCheckpoint(reg, apiclient.New(fs.URL, "token")); err != nil {
		t.Fatal(err)
	}

	handoff := builtinValidHandoff("task-42")
	handoffMap := asObject(t, handoff)
	out, err := reg.Call(context.Background(), "mem_checkpoint", map[string]any{
		"task_key":        "task-42",
		"idempotency_key": "task-42-v1",
		"handoff":         handoffMap,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fs.lastMethod != http.MethodPost ||
		fs.lastPath != "/v1/tasks/task-42/checkpoints" {
		t.Fatalf("request = %s %s", fs.lastMethod, fs.lastPath)
	}
	if got := fs.lastHeaders.Get("Idempotency-Key"); got != "task-42-v1" {
		t.Fatalf("Idempotency-Key = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(fs.lastBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["contract"] != apiclient.HandoffContract ||
		body["task_key"] != "task-42" {
		t.Fatalf("checkpoint body = %#v", body)
	}
	if _, nestedWrapper := body["handoff"]; nestedWrapper {
		t.Fatalf("canonical API body must be the handoff itself: %#v", body)
	}
	raw, ok := out.(json.RawMessage)
	if !ok || !strings.Contains(string(raw), `"replayed":false`) {
		t.Fatalf("transparent output = %T %v", out, out)
	}
}

func TestMemResumeUsesTypedClientAndDoesNotDuplicateTaskKey(t *testing.T) {
	fs := newFakeServer(
		`{"contract":"mem.resume","schema_version":1,"missing":[]}`,
		http.StatusOK,
		"application/json",
	)
	defer fs.Close()
	reg := tools.New()
	if err := registerResume(reg, apiclient.New(fs.URL, "token")); err != nil {
		t.Fatal(err)
	}

	out, err := reg.Call(context.Background(), "mem_resume", map[string]any{
		"task_key":      "task-42",
		"checkpoint_id": "11111111-1111-1111-1111-111111111111",
		"scope":         "/Projects/mem",
		"focus":         "remaining tests",
		"limit":         float64(5),
		"max_chars":     float64(8000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fs.lastMethod != http.MethodPost ||
		fs.lastPath != "/v1/tasks/task-42/resume" {
		t.Fatalf("request = %s %s", fs.lastMethod, fs.lastPath)
	}
	var body map[string]any
	if err := json.Unmarshal(fs.lastBody, &body); err != nil {
		t.Fatal(err)
	}
	if _, duplicated := body["task_key"]; duplicated {
		t.Fatalf("task_key must only appear in URL: %#v", body)
	}
	if body["scope"] != "/Projects/mem" ||
		body["limit"] != float64(5) ||
		body["max_chars"] != float64(8000) {
		t.Fatalf("resume body = %#v", body)
	}
	raw, ok := out.(json.RawMessage)
	if !ok || !strings.Contains(string(raw), `"contract":"mem.resume"`) {
		t.Fatalf("transparent output = %T %v", out, out)
	}
}

func TestMemHandoffReadToolsUseTypedClientContracts(t *testing.T) {
	t.Run("task list", func(t *testing.T) {
		fs := newFakeServer(
			`{"tasks":[{"id":"11111111-1111-1111-1111-111111111111","task_key":"task-42"}]}`,
			http.StatusOK,
			"application/json",
		)
		defer fs.Close()
		reg := tools.New()
		if err := registerTaskList(reg, apiclient.New(fs.URL, "token")); err != nil {
			t.Fatal(err)
		}
		out, err := reg.Call(context.Background(), "mem_task_list", map[string]any{
			"scope": "/Projects/mem α",
			"limit": float64(25),
			"after": "33333333-3333-3333-3333-333333333333",
		})
		if err != nil {
			t.Fatal(err)
		}
		query, err := url.ParseQuery(fs.lastQuery)
		if err != nil {
			t.Fatal(err)
		}
		if fs.lastMethod != http.MethodGet || fs.lastPath != "/v1/tasks" ||
			query.Get("scope") != "/Projects/mem α" ||
			query.Get("limit") != "25" ||
			query.Get("after") != "33333333-3333-3333-3333-333333333333" {
			t.Fatalf("request = %s %s?%s", fs.lastMethod, fs.lastPath, fs.lastQuery)
		}
		response, ok := out.(*apiclient.TaskListResponse)
		if !ok || len(response.Tasks) != 1 || response.Tasks[0].TaskKey != "task-42" {
			t.Fatalf("output = %#v", out)
		}
	})

	t.Run("checkpoint list", func(t *testing.T) {
		fs := newFakeServer(
			`{"checkpoints":[{
				"id":"22222222-2222-2222-2222-222222222222",
				"task_key":"task-42",
				"sequence":4,
				"status":"ready",
				"progress_excerpt":"bounded progress",
				"progress_length":900,
				"completed_count":2,
				"reference_count":3,
				"payload_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}]}`,
			http.StatusOK,
			"application/json",
		)
		defer fs.Close()
		reg := tools.New()
		if err := registerCheckpointList(reg, apiclient.New(fs.URL, "token")); err != nil {
			t.Fatal(err)
		}
		out, err := reg.Call(context.Background(), "mem_checkpoint_list", map[string]any{
			"task_key": "task-42",
			"scope":    "/Projects/mem",
			"limit":    float64(10),
			"before":   float64(9),
		})
		if err != nil {
			t.Fatal(err)
		}
		query, err := url.ParseQuery(fs.lastQuery)
		if err != nil {
			t.Fatal(err)
		}
		if fs.lastMethod != http.MethodGet ||
			fs.lastPath != "/v1/tasks/task-42/checkpoints" ||
			query.Get("scope") != "/Projects/mem" ||
			query.Get("limit") != "10" ||
			query.Get("before") != "9" {
			t.Fatalf("request = %s %s?%s", fs.lastMethod, fs.lastPath, fs.lastQuery)
		}
		response, ok := out.(*apiclient.CheckpointListResponse)
		if !ok || len(response.Checkpoints) != 1 ||
			response.Checkpoints[0].Sequence != 4 ||
			response.Checkpoints[0].Status != "ready" ||
			response.Checkpoints[0].ProgressExcerpt != "bounded progress" ||
			response.Checkpoints[0].ProgressLength != 900 ||
			response.Checkpoints[0].CompletedCount != 2 ||
			response.Checkpoints[0].ReferenceCount != 3 {
			t.Fatalf("output = %#v", out)
		}
	})

	t.Run("checkpoint get", func(t *testing.T) {
		checkpointID := "22222222-2222-2222-2222-222222222222"
		fs := newFakeServer(
			`{"id":"`+checkpointID+`","task_key":"task-42","sequence":4,"handoff":{}}`,
			http.StatusOK,
			"application/json",
		)
		defer fs.Close()
		reg := tools.New()
		if err := registerCheckpointGet(reg, apiclient.New(fs.URL, "token")); err != nil {
			t.Fatal(err)
		}
		out, err := reg.Call(context.Background(), "mem_checkpoint_get", map[string]any{
			"task_key":      "task-42",
			"checkpoint_id": checkpointID,
			"scope":         "/Projects/mem",
		})
		if err != nil {
			t.Fatal(err)
		}
		query, err := url.ParseQuery(fs.lastQuery)
		if err != nil {
			t.Fatal(err)
		}
		if fs.lastMethod != http.MethodGet ||
			fs.lastPath != "/v1/tasks/task-42/checkpoints/"+checkpointID ||
			query.Get("scope") != "/Projects/mem" {
			t.Fatalf("request = %s %s?%s", fs.lastMethod, fs.lastPath, fs.lastQuery)
		}
		checkpoint, ok := out.(*apiclient.CheckpointRecord)
		if !ok || checkpoint.ID != checkpointID || checkpoint.Sequence != 4 {
			t.Fatalf("output = %#v", out)
		}
	})
}

func TestMemHandoffReadSchemasExposeRuntimeBounds(t *testing.T) {
	taskLimit := taskListToolSchema().Properties["limit"]
	if taskLimit.Maximum == nil || *taskLimit.Maximum != 200 {
		t.Fatalf("mem_task_list limit schema = %#v", taskLimit)
	}

	checkpointProperties := checkpointListToolSchema().Properties
	checkpointLimit := checkpointProperties["limit"]
	if checkpointLimit.Maximum == nil || *checkpointLimit.Maximum != 200 {
		t.Fatalf("mem_checkpoint_list limit schema = %#v", checkpointLimit)
	}
	before := checkpointProperties["before"]
	if before.Minimum == nil || *before.Minimum != 1 {
		t.Fatalf("mem_checkpoint_list before schema = %#v", before)
	}

	encoded, err := json.Marshal(checkpointListToolSchema())
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	if properties["limit"].(map[string]any)["maximum"] != float64(200) ||
		properties["before"].(map[string]any)["minimum"] != float64(1) {
		t.Fatalf("serialized schema omitted bounds: %s", encoded)
	}
}

func TestMemCheckpointRejectsUnknownNestedFields(t *testing.T) {
	fs := newFakeServer(`{}`, http.StatusCreated, "application/json")
	defer fs.Close()
	reg := tools.New()
	if err := registerCheckpoint(reg, apiclient.New(fs.URL, "token")); err != nil {
		t.Fatal(err)
	}
	handoff := asObject(t, builtinValidHandoff("task-42"))
	state := handoff["state"].(map[string]any)
	state["vendor_private_state"] = "must not cross the contract boundary"

	_, err := reg.Call(context.Background(), "mem_checkpoint", map[string]any{
		"task_key":        "task-42",
		"idempotency_key": "task-42-v1",
		"handoff":         handoff,
	})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
	if fs.lastMethod != "" {
		t.Fatalf("invalid input unexpectedly reached API: %s %s", fs.lastMethod, fs.lastPath)
	}
}

func builtinValidHandoff(taskKey string) apiclient.HandoffV1 {
	required := true
	return apiclient.HandoffV1{
		Contract:       apiclient.HandoffContract,
		SchemaVersion:  apiclient.HandoffSchemaVersion,
		CheckpointKind: "handoff",
		TaskKey:        taskKey,
		ScopePath:      "/Projects/mem",
		State: apiclient.HandoffState{
			Status: "ready",
			Goal:   "Continue the migration implementation",
			Progress: apiclient.HandoffProgress{
				Summary:   "MCP client surface implemented",
				Completed: []string{"Added recursive JSON Schema"},
			},
			Decisions: []apiclient.HandoffDecision{{
				Summary:    "Use typed apiclient methods",
				References: []string{},
			}},
			NextSteps: []apiclient.HandoffNextStep{{
				Summary:    "Run API integration tests",
				References: []string{},
			}},
			Blockers:      []apiclient.HandoffBlocker{},
			OpenQuestions: []string{},
			Artifacts: []apiclient.HandoffArtifact{{
				URI:      "mem://files/file-1",
				Required: &required,
			}},
		},
		Producer: apiclient.HandoffProducer{AgentID: "codex"},
	}
}

func asObject(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

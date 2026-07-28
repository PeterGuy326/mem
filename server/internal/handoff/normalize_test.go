package handoff

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func boolPointer(v bool) *bool { return &v }

func validHandoffV1(taskKey string) HandoffV1 {
	return HandoffV1{
		Contract:       ContractName,
		SchemaVersion:  SchemaVersionV1,
		CheckpointKind: CheckpointKindHandoff,
		TaskKey:        taskKey,
		ScopePath:      "/Projects/mem",
		State: StateV1{
			Status: TaskStatusReady,
			Goal:   "Continue the portable Agent drive implementation.",
			Progress: ProgressV1{
				Summary:   "The versioned contract is ready for persistence.",
				Completed: []string{"Defined the v1 schema"},
			},
			Decisions: []DecisionV1{{
				Summary:    "Use immutable checkpoints.",
				Rationale:  "Retries and audit history must remain verifiable.",
				References: []string{"mem://memories/decision-1"},
			}},
			NextSteps: []NextStepV1{{
				Summary:    "Wire the HTTP adapter.",
				References: []string{"mem://files/api-go"},
			}},
			Blockers: []BlockerV1{{
				Summary:    "No active blocker.",
				References: []string{},
			}},
			OpenQuestions: []string{},
			Artifacts: []ArtifactV1{{
				URI:      "mem://files/schema",
				Role:     "contract",
				SHA256:   strings.Repeat("a", 64),
				Required: boolPointer(true),
			}},
			WorkspaceState: &WorkspaceState{
				WorkingDirectory: "/workspace/mem",
				VCS: &VCSState{
					Revision:      "abc123",
					Branch:        "codex/handoff",
					Dirty:         true,
					StatusSummary: "new handoff package",
				},
			},
		},
		Producer: ProducerV1{
			AgentID:   "claude-code",
			SessionID: "session-1",
		},
	}
}

func validCheckpointCommand() CheckpointCommand {
	taskKey := "portable-task"
	return CheckpointCommand{
		WorkspaceID:    uuid.New(),
		TaskKey:        taskKey,
		Handoff:        validHandoffV1(taskKey),
		IdempotencyKey: "portable-task-handoff-1",
	}
}

func TestDecodeV1StrictlyRejectsUnknownAndTrailingJSON(t *testing.T) {
	valid, err := json.Marshal(validHandoffV1("task-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeV1(valid); err != nil {
		t.Fatalf("valid payload: %v", err)
	}

	var document map[string]any
	if err := json.Unmarshal(valid, &document); err != nil {
		t.Fatal(err)
	}
	state := document["state"].(map[string]any)
	progress := state["progress"].(map[string]any)
	progress["unknown"] = true
	withUnknown, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeV1(withUnknown); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unknown nested field error = %v", err)
	}
	if _, err := DecodeV1(append(valid, []byte(` {}`)...)); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("trailing JSON error = %v", err)
	}
}

func TestNormalizeV1CanonicalizesTextAndPath(t *testing.T) {
	handoff := validHandoffV1("task-1")
	handoff.TaskKey = " task-1 "
	handoff.ScopePath = "/Projects//mem/"
	handoff.State.Goal = "  Continue the task.  "
	handoff.State.Decisions[0].References[0] = "  mem://memories/decision-1 "
	handoff.Producer.AgentID = " claude-code "

	got, err := NormalizeV1(handoff, " task-1 ")
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskKey != "task-1" || got.ScopePath != "/Projects/mem" {
		t.Fatalf("identity normalization = task %q path %q", got.TaskKey, got.ScopePath)
	}
	if got.State.Goal != "Continue the task." {
		t.Fatalf("goal = %q", got.State.Goal)
	}
	if got.State.Decisions[0].References[0] != "mem://memories/decision-1" {
		t.Fatalf("reference = %q", got.State.Decisions[0].References[0])
	}
	if got.Producer.AgentID != "claude-code" {
		t.Fatalf("producer = %q", got.Producer.AgentID)
	}
}

func TestNormalizeV1RejectsUnsupportedVersionBeforeGenericValidation(t *testing.T) {
	handoff := validHandoffV1("task-1")
	handoff.SchemaVersion = 2
	_, err := NormalizeV1(handoff, "task-1")
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestNormalizeV1RequiredFieldsAndSchemaBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*HandoffV1)
	}{
		{"contract", func(h *HandoffV1) { h.Contract = "vendor.handoff" }},
		{"kind", func(h *HandoffV1) { h.CheckpointKind = "snapshot" }},
		{"task key mismatch", func(h *HandoffV1) { h.TaskKey = "other-task" }},
		{"nil completed", func(h *HandoffV1) { h.State.Progress.Completed = nil }},
		{"nil decisions", func(h *HandoffV1) { h.State.Decisions = nil }},
		{"nil decision refs", func(h *HandoffV1) { h.State.Decisions[0].References = nil }},
		{"nil next steps", func(h *HandoffV1) { h.State.NextSteps = nil }},
		{"nil blockers", func(h *HandoffV1) { h.State.Blockers = nil }},
		{"nil open questions", func(h *HandoffV1) { h.State.OpenQuestions = nil }},
		{"nil artifacts", func(h *HandoffV1) { h.State.Artifacts = nil }},
		{"artifact required omitted", func(h *HandoffV1) { h.State.Artifacts[0].Required = nil }},
		{"uppercase sha", func(h *HandoffV1) { h.State.Artifacts[0].SHA256 = strings.Repeat("A", 64) }},
		{"empty producer", func(h *HandoffV1) { h.Producer.AgentID = " " }},
		{"too many questions", func(h *HandoffV1) {
			h.State.OpenQuestions = make([]string, maxReferenceItems+1)
			for i := range h.State.OpenQuestions {
				h.State.OpenQuestions[i] = "question"
			}
		}},
		{"bad path", func(h *HandoffV1) { h.ScopePath = "/Projects/../secret" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handoff := validHandoffV1("task-1")
			tc.mutate(&handoff)
			if _, err := NormalizeV1(handoff, "task-1"); !errors.Is(err, ErrInvalidCommand) {
				t.Fatalf("error = %v, want ErrInvalidCommand", err)
			}
		})
	}
}

func TestNormalizeCheckpointCommandStableHashesAndReferenceProjection(t *testing.T) {
	first := validCheckpointCommand()
	first.Handoff.ScopePath = "/Projects//mem/"
	first.Handoff.State.Goal = "  Continue the portable Agent drive implementation. "
	first.CreatedByUserID = uuidPointer(uuid.New())
	first.CreatedByTokenID = uuidPointer(uuid.New())

	second := first
	second.Handoff.ScopePath = "/Projects/mem"
	second.Handoff.State.Goal = "Continue the portable Agent drive implementation."
	second.CreatedByUserID = uuidPointer(uuid.New())
	second.CreatedByTokenID = uuidPointer(uuid.New())
	second.IdempotencyKey = "another-key"

	gotA, err := normalizeCheckpointCommand(first)
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := normalizeCheckpointCommand(second)
	if err != nil {
		t.Fatal(err)
	}
	if gotA.requestSHA256 != gotB.requestSHA256 {
		t.Fatalf("equivalent normalized request hashes differ:\n%s\n%s",
			gotA.requestSHA256, gotB.requestSHA256)
	}
	if gotA.payloadSHA256 != gotB.payloadSHA256 {
		t.Fatalf("equivalent payload hashes differ:\n%s\n%s",
			gotA.payloadSHA256, gotB.payloadSHA256)
	}
	if len(gotA.references) != 3 {
		t.Fatalf("references = %#v, want decision + next_step + artifact", gotA.references)
	}
	if gotA.references[0].Relation != "decision" ||
		gotA.references[1].Relation != "next_step" ||
		gotA.references[2].Relation != "artifact" ||
		!gotA.references[2].Required ||
		gotA.references[2].ExpectedSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("reference projection = %#v", gotA.references)
	}

	changed := second
	changed.Handoff.State.Progress.Summary = "Different progress"
	gotChanged, err := normalizeCheckpointCommand(changed)
	if err != nil {
		t.Fatal(err)
	}
	if gotChanged.requestSHA256 == gotA.requestSHA256 {
		t.Fatal("different state produced the same request hash")
	}
}

func TestNormalizeCheckpointCommandValidatesAuthenticatedEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CheckpointCommand)
		want   error
	}{
		{"workspace", func(c *CheckpointCommand) { c.WorkspaceID = uuid.Nil }, ErrInvalidCommand},
		{"task", func(c *CheckpointCommand) { c.TaskKey = "" }, ErrInvalidCommand},
		{"key", func(c *CheckpointCommand) { c.IdempotencyKey = "" }, ErrInvalidCommand},
		{"long key", func(c *CheckpointCommand) {
			c.IdempotencyKey = strings.Repeat("界", maxIdempotencyKeyRunes+1)
		}, ErrInvalidCommand},
		{"nil base uuid", func(c *CheckpointCommand) {
			zero := uuid.Nil
			c.Handoff.BaseCheckpointID = &zero
		}, ErrInvalidCommand},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			command := validCheckpointCommand()
			tc.mutate(&command)
			_, err := normalizeCheckpointCommand(command)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPathFiltersAreSegmentSafe(t *testing.T) {
	args, where := appendPathFilters(
		[]any{"workspace"},
		[]string{"workspace_id = $1"},
		"scope_path",
		"/Work",
		[]string{"/Allowed", "/100%_done"},
	)
	joined := strings.Join(where, " ")
	if strings.Contains(joined, "LIKE") {
		t.Fatalf("path SQL uses wildcard matching: %s", joined)
	}
	if !strings.Contains(joined, "left(scope_path") {
		t.Fatalf("path SQL lacks segment-safe comparison: %s", joined)
	}
	if args[1] != "/Work" || args[2] != "/Allowed" || args[3] != "/100%_done" {
		t.Fatalf("path bind args = %#v", args)
	}
}

func uuidPointer(id uuid.UUID) *uuid.UUID { return &id }

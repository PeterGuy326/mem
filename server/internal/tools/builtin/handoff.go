package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/PeterGuy326/mem/server/internal/tools"
)

func registerCheckpoint(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_checkpoint",
		Description: "Persist an immutable, versioned mem.handoff checkpoint for a task. " +
			"Use checkpoint_kind=handoff when another Agent or device should take over. " +
			"Safe retries require the same idempotency_key and identical handoff payload.",
		InputSchema: checkpointToolSchema(),
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			var input checkpointToolInput
			if err := decodeToolInput(args, &input); err != nil {
				return nil, fmt.Errorf("mem_checkpoint: invalid input: %w", err)
			}
			out, err := c.Checkpoint(
				ctx,
				input.TaskKey,
				input.Handoff,
				input.IdempotencyKey,
			)
			if err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

func registerResume(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_resume",
		Description: "Resume a task from its current handoff head or an explicit checkpoint. " +
			"Returns the canonical resume pack unchanged, including checkpoint state, " +
			"resolved evidence, and missing references.",
		InputSchema: resumeToolSchema(),
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			var input resumeToolInput
			if err := decodeToolInput(args, &input); err != nil {
				return nil, fmt.Errorf("mem_resume: invalid input: %w", err)
			}
			out, err := c.Resume(ctx, input.TaskKey, apiclient.ResumeRequest{
				CheckpointID: input.CheckpointID,
				Scope:        input.Scope,
				Focus:        input.Focus,
				Limit:        input.Limit,
				MaxChars:     input.MaxChars,
			})
			if err != nil {
				return nil, err
			}
			return out, nil
		},
	})
}

func registerTaskList(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_task_list",
		Description: "List bounded task summaries visible in the authenticated workspace " +
			"and optional virtual-path scope.",
		InputSchema: taskListToolSchema(),
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			var input taskListToolInput
			if err := decodeToolInput(args, &input); err != nil {
				return nil, fmt.Errorf("mem_task_list: invalid input: %w", err)
			}
			return c.ListTasks(ctx, apiclient.TaskListOptions{
				Scope: input.Scope,
				Limit: input.Limit,
				After: input.After,
			})
		},
	})
}

func registerCheckpointList(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_checkpoint_list",
		Description: "List newest-first bounded checkpoint summaries for one task, with " +
			"optional path scope and sequence pagination. Use mem_checkpoint_get for " +
			"the full handoff payload.",
		InputSchema: checkpointListToolSchema(),
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			var input checkpointListToolInput
			if err := decodeToolInput(args, &input); err != nil {
				return nil, fmt.Errorf("mem_checkpoint_list: invalid input: %w", err)
			}
			return c.ListCheckpoints(
				ctx,
				input.TaskKey,
				apiclient.CheckpointListOptions{
					Scope:  input.Scope,
					Limit:  input.Limit,
					Before: input.Before,
				},
			)
		},
	})
}

func registerCheckpointGet(reg *tools.Registry, c *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_checkpoint_get",
		Description: "Get one immutable checkpoint by task key and checkpoint UUID, " +
			"including its versioned handoff payload and evidence references.",
		InputSchema: checkpointGetToolSchema(),
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			var input checkpointGetToolInput
			if err := decodeToolInput(args, &input); err != nil {
				return nil, fmt.Errorf("mem_checkpoint_get: invalid input: %w", err)
			}
			return c.GetCheckpoint(
				ctx,
				input.TaskKey,
				input.CheckpointID,
				apiclient.CheckpointGetOptions{Scope: input.Scope},
			)
		},
	})
}

type checkpointToolInput struct {
	TaskKey        string              `json:"task_key"`
	IdempotencyKey string              `json:"idempotency_key"`
	Handoff        apiclient.HandoffV1 `json:"handoff"`
}

type resumeToolInput struct {
	TaskKey      string `json:"task_key"`
	CheckpointID string `json:"checkpoint_id,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Focus        string `json:"focus,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	MaxChars     int    `json:"max_chars,omitempty"`
}

type taskListToolInput struct {
	Scope string `json:"scope,omitempty"`
	Limit int    `json:"limit,omitempty"`
	After string `json:"after,omitempty"`
}

type checkpointListToolInput struct {
	TaskKey string `json:"task_key"`
	Scope   string `json:"scope,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Before  int64  `json:"before,omitempty"`
}

type checkpointGetToolInput struct {
	TaskKey      string `json:"task_key"`
	CheckpointID string `json:"checkpoint_id"`
	Scope        string `json:"scope,omitempty"`
}

func decodeToolInput(args map[string]any, out any) error {
	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func checkpointToolSchema() tools.Schema {
	return closedSchema(
		[]string{"task_key", "idempotency_key", "handoff"},
		map[string]tools.Property{
			"task_key": {
				Type:        "string",
				Description: "Stable task key used in the canonical API path; must equal handoff.task_key",
				MinLength:   1,
				MaxLength:   200,
			},
			"idempotency_key": {
				Type:        "string",
				Description: "Stable retry key unique within the workspace",
				MinLength:   1,
				MaxLength:   200,
			},
			"handoff": handoffV1Property(),
		},
	)
}

func resumeToolSchema() tools.Schema {
	return closedSchema(
		[]string{"task_key"},
		map[string]tools.Property{
			"task_key": {
				Type:        "string",
				Description: "Stable task key whose head or selected revision should be restored",
				MinLength:   1,
				MaxLength:   200,
			},
			"checkpoint_id": {
				Type:        "string",
				Format:      "uuid",
				Description: "Optional historical checkpoint; omit to resume the current head",
			},
			"scope": {
				Type:        "string",
				Pattern:     "^/",
				MaxLength:   2048,
				Description: "Optional virtual path that may only narrow the checkpoint scope",
			},
			"focus": {
				Type:        "string",
				MaxLength:   16384,
				Description: "Optional additional evidence-retrieval focus",
			},
			"limit": {
				Type:        "integer",
				Default:     8,
				Description: "Maximum related evidence items",
			},
			"max_chars": {
				Type:        "integer",
				Default:     12000,
				Description: "Maximum character budget for related evidence",
			},
		},
	)
}

func taskListToolSchema() tools.Schema {
	return closedSchema(
		nil,
		map[string]tools.Property{
			"scope": {
				Type:        "string",
				Pattern:     "^/",
				MaxLength:   2048,
				Description: "Optional virtual path that may only narrow token access",
			},
			"limit": {
				Type:        "integer",
				Default:     50,
				Maximum:     schemaInteger(200),
				Description: "Page size (max 200)",
			},
			"after": {
				Type:        "string",
				Format:      "uuid",
				Description: "Task UUID cursor from the previous page",
			},
		},
	)
}

func checkpointListToolSchema() tools.Schema {
	return closedSchema(
		[]string{"task_key"},
		map[string]tools.Property{
			"task_key": {
				Type:        "string",
				MinLength:   1,
				MaxLength:   200,
				Description: "Stable task key",
			},
			"scope": {
				Type:        "string",
				Pattern:     "^/",
				MaxLength:   2048,
				Description: "Optional virtual path that may only narrow token access",
			},
			"limit": {
				Type:        "integer",
				Default:     50,
				Maximum:     schemaInteger(200),
				Description: "Page size (max 200)",
			},
			"before": {
				Type:        "integer",
				Minimum:     schemaInteger(1),
				Description: "Return checkpoints before this positive sequence",
			},
		},
	)
}

func schemaInteger(value int) *int {
	return &value
}

func checkpointGetToolSchema() tools.Schema {
	return closedSchema(
		[]string{"task_key", "checkpoint_id"},
		map[string]tools.Property{
			"task_key": {
				Type:        "string",
				MinLength:   1,
				MaxLength:   200,
				Description: "Stable task key",
			},
			"checkpoint_id": {
				Type:        "string",
				Format:      "uuid",
				Description: "Immutable checkpoint UUID",
			},
			"scope": {
				Type:        "string",
				Pattern:     "^/",
				MaxLength:   2048,
				Description: "Optional virtual path that may only narrow token access",
			},
		},
	)
}

func handoffV1Property() tools.Property {
	return closedObject(
		[]string{
			"contract",
			"schema_version",
			"checkpoint_kind",
			"task_key",
			"scope_path",
			"state",
			"producer",
		},
		map[string]tools.Property{
			"contract": {
				Type:  "string",
				Const: apiclient.HandoffContract,
			},
			"schema_version": {
				Type:  "integer",
				Const: apiclient.HandoffSchemaVersion,
			},
			"checkpoint_kind": {
				Type: "string",
				Enum: []string{"checkpoint", "handoff"},
			},
			"task_key": {
				Type:      "string",
				MinLength: 1,
				MaxLength: 200,
			},
			"base_checkpoint_id": {
				Type:   []string{"string", "null"},
				Format: "uuid",
			},
			"scope_path": {
				Type:      "string",
				MinLength: 1,
				MaxLength: 2048,
				Pattern:   "^/",
			},
			"state":    handoffStateProperty(),
			"producer": handoffProducerProperty(),
		},
	)
}

func handoffStateProperty() tools.Property {
	return closedObject(
		[]string{
			"status",
			"goal",
			"progress",
			"decisions",
			"next_steps",
			"blockers",
			"open_questions",
			"artifacts",
		},
		map[string]tools.Property{
			"status": {
				Type: "string",
				Enum: []string{"in_progress", "ready", "blocked", "complete"},
			},
			"goal": {
				Type:      "string",
				MinLength: 1,
				MaxLength: 16384,
			},
			"progress": closedObject(
				[]string{"summary", "completed"},
				map[string]tools.Property{
					"summary": {
						Type:      "string",
						MinLength: 1,
						MaxLength: 16384,
					},
					"completed": stringArrayProperty(100, 4096),
				},
			),
			"decisions": objectArrayProperty(100, closedObject(
				[]string{"summary", "references"},
				map[string]tools.Property{
					"summary":    textItemProperty(),
					"rationale":  {Type: "string", MaxLength: 8192},
					"references": referenceListProperty(),
				},
			)),
			"next_steps": objectArrayProperty(100, closedObject(
				[]string{"summary", "references"},
				map[string]tools.Property{
					"summary":    textItemProperty(),
					"references": referenceListProperty(),
				},
			)),
			"blockers": objectArrayProperty(100, closedObject(
				[]string{"summary", "references"},
				map[string]tools.Property{
					"summary":    textItemProperty(),
					"needs":      {Type: "string", MaxLength: 4096},
					"references": referenceListProperty(),
				},
			)),
			"open_questions": stringArrayProperty(100, 4096),
			"artifacts": objectArrayProperty(100, closedObject(
				[]string{"uri", "required"},
				map[string]tools.Property{
					"uri": {
						Type:      "string",
						MinLength: 1,
						MaxLength: 2048,
					},
					"role": {
						Type:      "string",
						MaxLength: 200,
					},
					"sha256": {
						Type:    "string",
						Pattern: "^[0-9a-f]{64}$",
					},
					"required": {Type: "boolean"},
				},
			)),
			"workspace_state": closedObject(
				nil,
				map[string]tools.Property{
					"working_directory": {
						Type:      "string",
						MaxLength: 4096,
					},
					"vcs": closedObject(
						nil,
						map[string]tools.Property{
							"revision": {
								Type:      "string",
								MaxLength: 200,
							},
							"branch": {
								Type:      "string",
								MaxLength: 500,
							},
							"dirty": {Type: "boolean"},
							"status_summary": {
								Type:      "string",
								MaxLength: 8192,
							},
						},
					),
				},
			),
		},
	)
}

func handoffProducerProperty() tools.Property {
	return closedObject(
		[]string{"agent_id"},
		map[string]tools.Property{
			"agent_id": {
				Type:      "string",
				MinLength: 1,
				MaxLength: 200,
			},
			"session_id": {
				Type:      "string",
				MaxLength: 200,
			},
		},
	)
}

func textItemProperty() tools.Property {
	return tools.Property{Type: "string", MinLength: 1, MaxLength: 4096}
}

func referenceListProperty() tools.Property {
	return stringArrayProperty(100, 2048)
}

func stringArrayProperty(maxItems, maxLength int) tools.Property {
	return tools.Property{
		Type:     "array",
		MaxItems: maxItems,
		Items: &tools.Property{
			Type:      "string",
			MinLength: 1,
			MaxLength: maxLength,
		},
	}
}

func objectArrayProperty(maxItems int, item tools.Property) tools.Property {
	return tools.Property{
		Type:     "array",
		MaxItems: maxItems,
		Items:    &item,
	}
}

func closedObject(required []string, properties map[string]tools.Property) tools.Property {
	noAdditionalProperties := false
	return tools.Property{
		Type:                 "object",
		Required:             required,
		Properties:           properties,
		AdditionalProperties: &noAdditionalProperties,
	}
}

func closedSchema(required []string, properties map[string]tools.Property) tools.Schema {
	noAdditionalProperties := false
	return tools.Schema{
		Type:                 "object",
		Required:             required,
		Properties:           properties,
		AdditionalProperties: &noAdditionalProperties,
	}
}

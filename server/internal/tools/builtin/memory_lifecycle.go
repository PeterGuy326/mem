package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/PeterGuy326/mem/server/internal/tools"
)

var memoryKindEnum = []string{
	"observation",
	"decision",
	"preference",
	"task_state",
	"fact",
	"note",
	"artifact",
}

func registerMemoryGet(reg *tools.Registry, client *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_memory_get",
		Description: "Get one structured memory by UUID, including its full content and " +
			"provenance, within the authenticated workspace and path boundary.",
		InputSchema: closedSchema(
			[]string{"memory_id"},
			map[string]tools.Property{
				"memory_id": {
					Type:        "string",
					Format:      "uuid",
					Description: "Structured memory UUID",
				},
				"scope": {
					Type:        "string",
					Pattern:     "^/",
					MaxLength:   2048,
					Description: "Optional virtual path that may only narrow token access",
				},
			},
		),
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			memoryID, ok := memoryOptionalString(args, "memory_id")
			if !ok {
				return nil, fmt.Errorf("mem_memory_get: memory_id is required")
			}
			scope, _ := memoryOptionalString(args, "scope")
			return client.GetMemory(
				ctx,
				memoryID,
				apiclient.MemoryGetOptions{Scope: scope},
			)
		},
	})
}

func registerMemoryList(reg *tools.Registry, client *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_memory_list",
		Description: "List bounded structured-memory summaries for inspection and lifecycle operations. " +
			"`mem_list` remains the file-listing tool.",
		InputSchema: tools.Schema{
			Type: "object",
			Properties: map[string]tools.Property{
				"scope": {
					Type:        "string",
					Description: "Optional canonical virtual-folder scope",
				},
				"recursive": {
					Type:        "boolean",
					Default:     true,
					Description: "Include descendant paths (default true)",
				},
				"kind": {
					Type:        "array",
					Description: "Optional memory kind filters",
					Items:       &tools.Property{Type: "string", Enum: memoryKindEnum},
				},
				"lifecycle": {
					Type:        "string",
					Enum:        []string{"active", "archived", "all"},
					Default:     "active",
					Description: "Lifecycle filter",
				},
				"pinned": {
					Type:        "boolean",
					Description: "Optional pinned-state filter",
				},
				"limit": {
					Type:        "integer",
					Default:     50,
					Description: "Page size (max 100)",
				},
				"cursor": {
					Type:        "string",
					Description: "Opaque next_cursor from the previous page",
				},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			options := apiclient.MemoryListOptions{}
			if value, ok := memoryOptionalString(args, "scope"); ok {
				options.Scope = value
			}
			if value, ok, err := memoryOptionalBool(args, "recursive"); err != nil {
				return nil, err
			} else if ok {
				options.Recursive = &value
			}
			if value, ok, err := memoryStringArray(args, "kind"); err != nil {
				return nil, err
			} else if ok {
				options.Kinds = value
			}
			if value, ok := memoryOptionalString(args, "lifecycle"); ok {
				options.Lifecycle = value
			}
			if value, ok, err := memoryOptionalBool(args, "pinned"); err != nil {
				return nil, err
			} else if ok {
				options.Pinned = &value
			}
			if value, ok, err := memoryOptionalInteger(args, "limit"); err != nil {
				return nil, err
			} else if ok {
				if value < 1 || value > 100 {
					return nil, fmt.Errorf("mem_memory_list: limit must be between 1 and 100")
				}
				options.Limit = int(value)
			}
			if value, ok := memoryOptionalString(args, "cursor"); ok {
				options.Cursor = value
			}
			return client.ListMemories(ctx, options)
		},
	})
}

func registerMemoryFeedback(reg *tools.Registry, client *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_feedback",
		Description: "Record useful/not-useful feedback or pin/unpin a structured memory. " +
			"Uses optimistic state_version checks and an idempotency key.",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"memory_id", "action", "expected_version", "idempotency_key"},
			Properties: map[string]tools.Property{
				"memory_id": {
					Type:        "string",
					Format:      "uuid",
					Description: "Structured memory UUID",
				},
				"action": {
					Type:        "string",
					Enum:        []string{"useful", "not_useful", "pin", "unpin"},
					Description: "Feedback action",
				},
				"expected_version": {
					Type:        "integer",
					Description: "Current memory state_version",
				},
				"idempotency_key": {
					Type:        "string",
					Description: "Stable retry key",
				},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			memoryID, key, version, err := memoryMutationArgs("mem_feedback", args)
			if err != nil {
				return nil, err
			}
			action, ok := memoryOptionalString(args, "action")
			if !ok {
				return nil, fmt.Errorf("mem_feedback: action is required")
			}
			switch action {
			case "useful", "not_useful", "pin", "unpin":
			default:
				return nil, fmt.Errorf(
					"mem_feedback: action must be useful, not_useful, pin, or unpin",
				)
			}
			return client.FeedbackMemory(
				ctx,
				memoryID,
				key,
				apiclient.MemoryFeedbackRequest{
					Action:          action,
					ExpectedVersion: version,
				},
			)
		},
	})
}

func registerMemoryArchive(reg *tools.Registry, client *apiclient.Client) error {
	return registerMemoryStateMutation(
		reg,
		client,
		"mem_archive",
		"archive",
		"Archive a structured memory so normal recall excludes it while audit data remains.",
	)
}

func registerMemoryRestore(reg *tools.Registry, client *apiclient.Client) error {
	return registerMemoryStateMutation(
		reg,
		client,
		"mem_restore",
		"restore",
		"Restore an archived structured memory to normal recall.",
	)
}

func registerMemoryStateMutation(
	reg *tools.Registry,
	client *apiclient.Client,
	toolName string,
	action string,
	description string,
) error {
	return reg.Register(tools.Tool{
		Name:        toolName,
		Description: description + " Uses optimistic state_version checks and an idempotency key.",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"memory_id", "expected_version", "idempotency_key"},
			Properties: map[string]tools.Property{
				"memory_id": {
					Type:        "string",
					Format:      "uuid",
					Description: "Structured memory UUID",
				},
				"expected_version": {
					Type:        "integer",
					Description: "Current memory state_version",
				},
				"idempotency_key": {
					Type:        "string",
					Description: "Stable retry key",
				},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			memoryID, key, version, err := memoryMutationArgs(toolName, args)
			if err != nil {
				return nil, err
			}
			request := apiclient.MemoryVersionRequest{ExpectedVersion: version}
			if action == "archive" {
				return client.ArchiveMemory(ctx, memoryID, key, request)
			}
			return client.RestoreMemory(ctx, memoryID, key, request)
		},
	})
}

func registerMemoryForget(reg *tools.Registry, client *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_forget",
		Description: "Irreversibly redact one structured memory from the live service without " +
			"deleting an independent source file. Requires confirm=true, a current state_version, " +
			"a reason, and an idempotency key; backups follow deployment retention.",
		InputSchema: tools.Schema{
			Type:     "object",
			Required: []string{"memory_id", "expected_version", "reason", "idempotency_key", "confirm"},
			Properties: map[string]tools.Property{
				"memory_id": {
					Type:        "string",
					Format:      "uuid",
					Description: "Structured memory UUID",
				},
				"expected_version": {
					Type:        "integer",
					Description: "Current memory state_version",
				},
				"reason": {
					Type: "string",
					Enum: []string{
						"user_request",
						"incorrect",
						"sensitive",
						"expired",
						"other",
					},
					Description: "Why the memory must be forgotten",
				},
				"idempotency_key": {
					Type:        "string",
					Description: "Stable retry key",
				},
				"confirm": {
					Type:        "boolean",
					Const:       true,
					Description: "Must be true to confirm irreversible forgetting",
				},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			confirmed, ok, err := memoryOptionalBool(args, "confirm")
			if err != nil || !ok || !confirmed {
				return nil, fmt.Errorf("mem_forget: confirm must be true")
			}
			memoryID, key, version, err := memoryMutationArgs("mem_forget", args)
			if err != nil {
				return nil, err
			}
			reason, ok := memoryOptionalString(args, "reason")
			if !ok {
				return nil, fmt.Errorf("mem_forget: reason is required")
			}
			switch reason {
			case "user_request", "incorrect", "sensitive", "expired", "other":
			default:
				return nil, fmt.Errorf(
					"mem_forget: reason must be user_request, incorrect, sensitive, expired, or other",
				)
			}
			return client.ForgetMemory(
				ctx,
				memoryID,
				key,
				apiclient.MemoryForgetRequest{
					ExpectedVersion: version,
					Reason:          reason,
				},
			)
		},
	})
}

func memoryMutationArgs(
	toolName string,
	args map[string]any,
) (memoryID string, key string, version int64, err error) {
	memoryID, ok := memoryOptionalString(args, "memory_id")
	if !ok {
		return "", "", 0, fmt.Errorf("%s: memory_id is required", toolName)
	}
	key, ok = memoryOptionalString(args, "idempotency_key")
	if !ok {
		return "", "", 0, fmt.Errorf("%s: idempotency_key is required", toolName)
	}
	version, ok, err = memoryOptionalInteger(args, "expected_version")
	if err != nil {
		return "", "", 0, fmt.Errorf("%s: %w", toolName, err)
	}
	if !ok || version <= 0 {
		return "", "", 0, fmt.Errorf("%s: expected_version must be greater than zero", toolName)
	}
	return memoryID, key, version, nil
}

func memoryOptionalString(args map[string]any, key string) (string, bool) {
	value, ok := args[key].(string)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func memoryOptionalBool(args map[string]any, key string) (bool, bool, error) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return false, false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, false, fmt.Errorf("%s must be a boolean", key)
	}
	return value, true, nil
}

func memoryOptionalInteger(args map[string]any, key string) (int64, bool, error) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return 0, false, nil
	}
	switch value := raw.(type) {
	case int:
		return int64(value), true, nil
	case int64:
		return value, true, nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
			return 0, false, fmt.Errorf("%s must be an integer", key)
		}
		if value > math.MaxInt64 || value < math.MinInt64 {
			return 0, false, fmt.Errorf("%s is out of range", key)
		}
		return int64(value), true, nil
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false, fmt.Errorf("%s must be an integer", key)
		}
		return parsed, true, nil
	default:
		return 0, false, fmt.Errorf("%s must be an integer", key)
	}
}

func memoryStringArray(
	args map[string]any,
	key string,
) ([]string, bool, error) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return nil, false, nil
	}
	var values []string
	switch items := raw.(type) {
	case []string:
		values = append(values, items...)
	case []any:
		values = make([]string, 0, len(items))
		for _, item := range items {
			value, ok := item.(string)
			if !ok {
				return nil, false, fmt.Errorf("%s must contain only strings", key)
			}
			values = append(values, value)
		}
	default:
		return nil, false, fmt.Errorf("%s must be an array", key)
	}
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
		if values[index] == "" {
			return nil, false, fmt.Errorf("%s must not contain empty values", key)
		}
	}
	return values, true, nil
}

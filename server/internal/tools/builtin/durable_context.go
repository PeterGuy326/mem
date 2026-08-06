package builtin

import (
	"context"
	"fmt"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/PeterGuy326/mem/server/internal/tools"
)

// registerDurableContextRecall exposes the scoped durable-context contract:
// one principal resumes only explicitly approved, active memories with stable
// version-pinned locators. The contract version is pinned client-side.
func registerDurableContextRecall(reg *tools.Registry, client *apiclient.Client) error {
	return reg.Register(tools.Tool{
		Name: "mem_durable_context_recall",
		Description: "Resume explicitly approved durable context for one principal across " +
			"sessions and channels through the pinned " + apiclient.DurableContextContract +
			" contract. Returns only active, workspace-scoped memories with stable locators " +
			"and provenance; unapproved, archived, and forgotten items are absent or denied.",
		InputSchema: closedSchema(
			[]string{"principal"},
			map[string]tools.Property{
				"principal": {
					Type:        "string",
					MaxLength:   128,
					Description: "Employee/user principal identity approved by an operator grant",
				},
				"session_ref": {
					Type:        "string",
					MaxLength:   512,
					Description: "Optional session/channel marker; allowlists are keyed by principal",
				},
				"limit": {
					Type:        "integer",
					Default:     50,
					Description: "Page size (max 100)",
				},
			},
		),
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			principal, ok := memoryOptionalString(args, "principal")
			if !ok {
				return nil, fmt.Errorf("mem_durable_context_recall: principal is required")
			}
			options := apiclient.DurableContextRecallOptions{Principal: principal}
			if value, ok := memoryOptionalString(args, "session_ref"); ok {
				options.SessionRef = value
			}
			if value, ok, err := memoryOptionalInteger(args, "limit"); err != nil {
				return nil, err
			} else if ok {
				if value < 1 || value > 100 {
					return nil, fmt.Errorf(
						"mem_durable_context_recall: limit must be between 1 and 100",
					)
				}
				options.Limit = int(value)
			}
			return client.DurableContextRecall(ctx, options)
		},
	})
}

package builtin

import (
	"context"
	"fmt"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/PeterGuy326/mem/server/internal/tools"
)

func registerFileAnnotationDecision(
	registry *tools.Registry,
	client *apiclient.Client,
) error {
	minimumVersion := 1
	return registry.Register(tools.Tool{
		Name: "mem_file_annotation_decide",
		Description: "Accept or reject one pending model-generated file annotation. " +
			"Uses the current state_version and delegates authorization, path scope, " +
			"replay, and conflict semantics to memd.",
		InputSchema: tools.Schema{
			Type: "object",
			Required: []string{
				"file_id",
				"annotation_id",
				"decision",
				"expected_version",
			},
			Properties: map[string]tools.Property{
				"file_id": {
					Type:        "string",
					Format:      "uuid",
					Description: "File UUID containing the annotation",
				},
				"annotation_id": {
					Type:        "string",
					Format:      "uuid",
					Description: "Pending file annotation UUID",
				},
				"decision": {
					Type:        "string",
					Enum:        []string{"accepted", "rejected"},
					Description: "Terminal human review decision",
				},
				"expected_version": {
					Type:        "integer",
					Minimum:     &minimumVersion,
					Description: "Current annotation state_version",
				},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (any, error) {
			fileID, ok := memoryOptionalString(args, "file_id")
			if !ok {
				return nil, fmt.Errorf(
					"mem_file_annotation_decide: file_id is required",
				)
			}
			annotationID, ok := memoryOptionalString(args, "annotation_id")
			if !ok {
				return nil, fmt.Errorf(
					"mem_file_annotation_decide: annotation_id is required",
				)
			}
			rawDecision, ok := memoryOptionalString(args, "decision")
			if !ok {
				return nil, fmt.Errorf(
					"mem_file_annotation_decide: decision is required",
				)
			}
			decision := apiclient.FileAnnotationDecision(rawDecision)
			switch decision {
			case apiclient.FileAnnotationDecisionAccepted,
				apiclient.FileAnnotationDecisionRejected:
			default:
				return nil, fmt.Errorf(
					"mem_file_annotation_decide: decision must be accepted or rejected",
				)
			}
			expectedVersion, ok, err := memoryOptionalInteger(
				args,
				"expected_version",
			)
			if err != nil {
				return nil, fmt.Errorf(
					"mem_file_annotation_decide: %w",
					err,
				)
			}
			if !ok || expectedVersion <= 0 {
				return nil, fmt.Errorf(
					"mem_file_annotation_decide: expected_version must be greater than zero",
				)
			}
			return client.DecideFileAnnotation(
				ctx,
				fileID,
				annotationID,
				apiclient.FileAnnotationDecisionRequest{
					Decision:        decision,
					ExpectedVersion: expectedVersion,
				},
			)
		},
	})
}

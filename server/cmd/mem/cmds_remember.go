package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

func newRememberCmd() *cobra.Command {
	var (
		kind              string
		path              string
		idempotencyKey    string
		eventAt           string
		sourceType        string
		sourceRef         string
		sourceFileID      string
		sourceLocatorJSON string
		agentID           string
		sessionID         string
		taskID            string
		attributesJSON    string
	)

	cmd := &cobra.Command{
		Use:   "remember <content>",
		Short: "Write structured memory for an external Agent",
		Long: `Persist a model-independent observation, decision, preference, task state,
or artifact reference with source and producer provenance.

Examples:
  mem remember "Use PostgreSQL for lexical recall" \
    --kind decision --path /Projects/mem \
    --idempotency-key task-42-db-decision --agent-id codex

  mem remember "Clause 4 sets a 30-day notice period" \
    --kind observation --path /Legal/Lease \
    --idempotency-key lease-clause-4-v1 --source-type file \
    --source-file-id 7c9... --source-locator '{"kind":"page","page":4}'`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			content := strings.Join(args, " ")
			if strings.TrimSpace(content) == "" {
				return fmt.Errorf("content must not be empty")
			}
			if strings.TrimSpace(kind) == "" {
				return fmt.Errorf("--kind must not be empty")
			}
			if strings.TrimSpace(path) == "" {
				return fmt.Errorf("--path must not be empty")
			}
			idempotencyKey = strings.TrimSpace(idempotencyKey)
			if idempotencyKey == "" {
				return fmt.Errorf("--idempotency-key must not be empty")
			}
			sourceType = strings.TrimSpace(sourceType)
			if sourceType == "" {
				sourceType = "agent"
			}

			source := map[string]any{"type": sourceType}
			if sourceRef != "" {
				source["ref"] = sourceRef
			}
			if sourceFileID != "" {
				source["file_id"] = sourceFileID
			}
			if sourceLocatorJSON != "" {
				locator, err := rememberJSONObjectFlag("--source-locator", sourceLocatorJSON)
				if err != nil {
					return err
				}
				source["locator"] = locator
			}

			producer := map[string]any{}
			if agentID != "" {
				producer["agent_id"] = agentID
			}
			if sessionID != "" {
				producer["session_id"] = sessionID
			}
			if taskID != "" {
				producer["task_id"] = taskID
			}

			body := map[string]any{
				"kind":     kind,
				"content":  content,
				"path":     path,
				"source":   source,
				"producer": producer,
			}
			if eventAt != "" {
				body["event_at"] = eventAt
			}
			if attributesJSON != "" {
				attributes, err := rememberJSONObjectFlag("--attributes", attributesJSON)
				if err != nil {
					return err
				}
				body["attributes"] = attributes
			}

			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return errNotLoggedIn()
			}

			var resp map[string]any
			client := newHTTPClient(cfg)
			err = client.api.DoJSONWithHeaders(
				context.Background(),
				http.MethodPost,
				"/v1/memories",
				body,
				&resp,
				map[string]string{"Idempotency-Key": idempotencyKey},
			)
			if err != nil {
				return fromAPIError(err)
			}

			if format == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}
			printRememberText(cmd, resp)
			return nil
		},
	}

	cmd.Flags().StringVar(&kind, "kind", "", "memory kind, e.g. observation|decision|preference|task_state")
	cmd.Flags().StringVar(&path, "path", "", "canonical virtual path used to scope the memory")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "stable retry key, unique within the workspace")
	cmd.Flags().StringVar(&eventAt, "event-at", "", "event time in RFC 3339 format")
	cmd.Flags().StringVar(&sourceType, "source-type", "agent", "source type (default agent)")
	cmd.Flags().StringVar(&sourceRef, "source-ref", "", "external source reference")
	cmd.Flags().StringVar(&sourceFileID, "source-file-id", "", "mem source file UUID")
	cmd.Flags().StringVar(&sourceLocatorJSON, "source-locator", "", "source locator as a JSON object")
	cmd.Flags().StringVar(&agentID, "agent-id", "", "producer Agent identifier")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "producer session identifier")
	cmd.Flags().StringVar(&taskID, "task-id", "", "producer task identifier")
	cmd.Flags().StringVar(&attributesJSON, "attributes", "", "additional attributes as a JSON object")
	_ = cmd.MarkFlagRequired("kind")
	_ = cmd.MarkFlagRequired("path")
	_ = cmd.MarkFlagRequired("idempotency-key")
	return cmd
}

func rememberJSONObjectFlag(name, raw string) (map[string]any, error) {
	var value map[string]any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, fmt.Errorf("%s must be a valid JSON object: %w", name, err)
	}
	if value == nil {
		return nil, fmt.Errorf("%s must be a JSON object", name)
	}
	return value, nil
}

func rememberOutputFormat(cmd *cobra.Command) (string, error) {
	format := "text"
	if cmd.Flags().Lookup("format") != nil {
		var err error
		format, err = cmd.Flags().GetString("format")
		if err != nil {
			return "", err
		}
	}
	switch format {
	case "text", "json":
		return format, nil
	default:
		return "", fmt.Errorf("--format must be text or json, got %q", format)
	}
}

func printRememberText(cmd *cobra.Command, resp map[string]any) {
	memory := resp
	if nested, ok := resp["memory"].(map[string]any); ok {
		memory = nested
	}
	id, _ := memory["id"].(string)
	if id == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "remembered")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "remembered: %s\n", id)
	}
	if replayed, ok := resp["replayed"].(bool); ok {
		fmt.Fprintf(cmd.OutOrStdout(), "replayed: %t\n", replayed)
	}
}

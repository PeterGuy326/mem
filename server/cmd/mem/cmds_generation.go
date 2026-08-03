package main

import (
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/PeterGuy326/mem/server/internal/indexgeneration"
)

const workspaceIndexGenerationsPath = "/v1/workspaces/current/index-generations"

type indexGenerationListResponse struct {
	Items          []indexgeneration.Build `json:"items"`
	ExecutionWired bool                    `json:"execution_wired"`
}

type indexGenerationGetResponse struct {
	Generation     indexgeneration.Build `json:"generation"`
	ExecutionWired bool                  `json:"execution_wired"`
}

type indexGenerationEventsResponse struct {
	Items []indexgeneration.Event `json:"items"`
}

func newGenerationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generation",
		Short: "Inspect versioned index-generation state",
		Long: `Inspect safe generation identities, bounded rebuild progress and audit events.
Build/activate commands remain unavailable until Worker rebuild execution and
active-generation search routing are wired to the same server contract.`,
	}
	cmd.AddCommand(newGenerationListCmd())
	cmd.AddCommand(newGenerationStatusCmd())
	cmd.AddCommand(newGenerationEventsCmd())
	return cmd
}

func newGenerationListCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List index generations for the current workspace",
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			if limit < 1 || limit > 100 {
				return fmt.Errorf("--limit must be between 1 and 100")
			}
			client, err := configuredWorkspaceAIProfileClient()
			if err != nil {
				return err
			}
			var response indexGenerationListResponse
			path := fmt.Sprintf("%s?limit=%d", workspaceIndexGenerationsPath, limit)
			if err := client.doJSON("GET", path, nil, &response); err != nil {
				return err
			}
			if format == "json" {
				return writeCommandJSON(cmd, response)
			}
			return writeGenerationList(cmd, response.Items)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum generations to return (1-100)")
	cmd.Flags().String("format", "text", "text|json")
	return cmd
}

func newGenerationStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <build-id>",
		Short: "Show one index generation and route identity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			id, err := parseGenerationBuildID(args[0])
			if err != nil {
				return err
			}
			client, err := configuredWorkspaceAIProfileClient()
			if err != nil {
				return err
			}
			var response indexGenerationGetResponse
			if err := client.doJSON("GET", workspaceIndexGenerationsPath+"/"+url.PathEscape(id.String()), nil, &response); err != nil {
				return err
			}
			if format == "json" {
				return writeCommandJSON(cmd, response)
			}
			return writeGenerationStatus(cmd, response.Generation)
		},
	}
	cmd.Flags().String("format", "text", "text|json")
	return cmd
}

func newGenerationEventsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events <build-id>",
		Short: "Show the append-only generation audit trail",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			id, err := parseGenerationBuildID(args[0])
			if err != nil {
				return err
			}
			client, err := configuredWorkspaceAIProfileClient()
			if err != nil {
				return err
			}
			var response indexGenerationEventsResponse
			path := workspaceIndexGenerationsPath + "/" + url.PathEscape(id.String()) + "/events"
			if err := client.doJSON("GET", path, nil, &response); err != nil {
				return err
			}
			if format == "json" {
				return writeCommandJSON(cmd, response)
			}
			writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(writer, "TIME\tEVENT\tFROM\tTO")
			for _, event := range response.Items {
				fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n",
					event.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
					terminalSafe(event.EventType), pointerText(event.FromState),
					pointerText(event.ToState))
			}
			return writer.Flush()
		},
	}
	cmd.Flags().String("format", "text", "text|json")
	return cmd
}

func parseGenerationBuildID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, fmt.Errorf("generation build ID must be a UUID")
	}
	return id, nil
}

func writeGenerationList(cmd *cobra.Command, builds []indexgeneration.Build) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tPROFILE\tSTATE\tPROGRESS")
	for _, build := range builds {
		completed := build.SucceededTargets + build.SkippedTargets
		fmt.Fprintf(writer, "%s\t%s@%s\t%s\t%d/%d (%d failed)\n",
			build.ID, terminalSafe(build.ProfileID), terminalSafe(build.ProfileRevision),
			terminalSafe(build.State), completed, build.RequiredTargets,
			build.FailedTargets)
	}
	return writer.Flush()
}

func writeGenerationStatus(cmd *cobra.Command, build indexgeneration.Build) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintf(writer, "id\t%s\n", build.ID)
	fmt.Fprintf(writer, "profile\t%s@%s\n", terminalSafe(build.ProfileID), terminalSafe(build.ProfileRevision))
	fmt.Fprintf(writer, "pipeline\t%s\n", terminalSafe(build.PipelineRevision))
	fmt.Fprintf(writer, "state\t%s\n", terminalSafe(build.State))
	fmt.Fprintf(writer, "progress\t%d succeeded, %d skipped, %d failed / %d\n",
		build.SucceededTargets, build.SkippedTargets, build.FailedTargets,
		build.RequiredTargets)
	for _, generation := range build.Generations {
		fmt.Fprintf(writer, "%s route\t%s:%s (%d dimensions)\n",
			terminalSafe(generation.RouteKind), terminalSafe(generation.Provider),
			terminalSafe(generation.ModelRevision), generation.OutputDimension)
	}
	return writer.Flush()
}

func pointerText(value *string) string {
	if value == nil {
		return ""
	}
	return terminalSafe(*value)
}

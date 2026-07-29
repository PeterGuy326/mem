package main

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

const workspaceAIProfilePath = "/v1/workspaces/current/ai-profile"

// workspaceAIProfile intentionally keeps public profile fields open-ended.
// The server owns the allowlisted model, credential, and egress details; the
// CLI only needs a stable public ID to display and select a profile.
type workspaceAIProfile map[string]any

type workspaceAIProfileResponse struct {
	Active    workspaceAIProfile   `json:"active"`
	Available []workspaceAIProfile `json:"available"`
}

type workspaceAIProfileSelectResponse struct {
	Active workspaceAIProfile `json:"active"`
}

type workspaceAIProfileSelectRequest struct {
	ProfileID string `json:"profile_id"`
}

func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "View or select the current workspace AI profile",
		Long: `Workspace AI profiles are server-side allowlisted pipelines.
The CLI can select only a profile ID; it never accepts provider URLs, model
IDs, or platform credentials.`,
	}
	cmd.AddCommand(newProfileListCmd())
	cmd.AddCommand(newProfileStatusCmd())
	cmd.AddCommand(newProfileSelectCmd())
	return cmd
}

func newProfileListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List workspace AI profiles available for selection",
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			response, err := getWorkspaceAIProfiles()
			if err != nil {
				return err
			}
			if format == "json" {
				return writeCommandJSON(cmd, response)
			}
			return writeWorkspaceAIProfileList(cmd, response)
		},
	}
	cmd.Flags().String("format", "text", "text|json")
	return cmd
}

func newProfileStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the active workspace AI profile",
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			response, err := getWorkspaceAIProfiles()
			if err != nil {
				return err
			}
			if format == "json" {
				return writeCommandJSON(cmd, response)
			}
			return writeWorkspaceAIProfileStatus(cmd, response.Active)
		},
	}
	cmd.Flags().String("format", "text", "text|json")
	return cmd
}

func newProfileSelectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "select <profile-id>",
		Short: "Select an allowlisted workspace AI profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			profileID := strings.TrimSpace(args[0])
			if profileID == "" {
				return fmt.Errorf("profile ID must not be empty")
			}
			client, err := configuredWorkspaceAIProfileClient()
			if err != nil {
				return err
			}
			var response workspaceAIProfileSelectResponse
			if err := client.doJSON(
				"PUT",
				workspaceAIProfilePath,
				workspaceAIProfileSelectRequest{ProfileID: profileID},
				&response,
			); err != nil {
				return err
			}
			if format == "json" {
				return writeCommandJSON(cmd, response)
			}
			return writeWorkspaceAIProfileStatus(cmd, response.Active)
		},
	}
	cmd.Flags().String("format", "text", "text|json")
	return cmd
}

func getWorkspaceAIProfiles() (*workspaceAIProfileResponse, error) {
	client, err := configuredWorkspaceAIProfileClient()
	if err != nil {
		return nil, err
	}
	response := &workspaceAIProfileResponse{}
	if err := client.doJSON("GET", workspaceAIProfilePath, nil, response); err != nil {
		return nil, err
	}
	return response, nil
}

func configuredWorkspaceAIProfileClient() (*httpClient, error) {
	cfg, err := resolveConfig("")
	if err != nil {
		return nil, err
	}
	if cfg.Token == "" {
		return nil, newCliError(3, "not logged in", "run `mem auth login` first")
	}
	return newHTTPClient(cfg), nil
}

func writeWorkspaceAIProfileList(
	cmd *cobra.Command,
	response *workspaceAIProfileResponse,
) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	activeID := workspaceAIProfileField(response.Active, "id", "profile_id")
	fmt.Fprintln(writer, "PROFILE_ID\tREVISION\tACTIVE")
	for _, profile := range response.Available {
		profileID := workspaceAIProfileField(profile, "id", "profile_id")
		active := ""
		if profileID != "" && profileID == activeID {
			active = "yes"
		}
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\n",
			terminalSafe(profileID),
			terminalSafe(workspaceAIProfileField(profile, "revision", "profile_revision")),
			active,
		)
	}
	return writer.Flush()
}

func writeWorkspaceAIProfileStatus(
	cmd *cobra.Command,
	profile workspaceAIProfile,
) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	profileID := workspaceAIProfileField(profile, "id", "profile_id")
	if profileID == "" {
		fmt.Fprintln(writer, "active\t(none)")
		return writer.Flush()
	}
	fmt.Fprintf(writer, "active\t%s\n", terminalSafe(profileID))
	if revision := workspaceAIProfileField(profile, "revision", "profile_revision"); revision != "" {
		fmt.Fprintf(writer, "revision\t%s\n", terminalSafe(revision))
	}
	return writer.Flush()
}

func workspaceAIProfileField(profile workspaceAIProfile, names ...string) string {
	for _, name := range names {
		value, ok := profile[name]
		if !ok {
			continue
		}
		if text, ok := value.(string); ok {
			return text
		}
	}
	return ""
}

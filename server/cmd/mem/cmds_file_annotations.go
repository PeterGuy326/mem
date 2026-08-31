package main

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/spf13/cobra"
)

func newAnnotationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "annotation",
		Short: "Review model-generated file annotations",
	}
	cmd.AddCommand(
		newAnnotationDecisionCmd(
			"accept",
			"Accept a pending file annotation",
			apiclient.FileAnnotationDecisionAccepted,
		),
		newAnnotationDecisionCmd(
			"reject",
			"Reject a pending file annotation",
			apiclient.FileAnnotationDecisionRejected,
		),
	)
	return cmd
}

func newAnnotationDecisionCmd(
	name string,
	description string,
	decision apiclient.FileAnnotationDecision,
) *cobra.Command {
	var expectedVersion int64
	cmd := &cobra.Command{
		Use:   name + " <file_id> <annotation_id>",
		Short: description,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			if expectedVersion <= 0 {
				return fmt.Errorf("--expected-version must be greater than zero")
			}
			client, err := configuredFileAnnotationClient()
			if err != nil {
				return err
			}
			response, err := client.DecideFileAnnotation(
				cmd.Context(),
				strings.TrimSpace(args[0]),
				strings.TrimSpace(args[1]),
				apiclient.FileAnnotationDecisionRequest{
					Decision:        decision,
					ExpectedVersion: expectedVersion,
				},
			)
			if err != nil {
				return fromAPIError(err)
			}
			return printFileAnnotationDecision(cmd, format, response)
		},
	}
	cmd.Flags().Int64Var(
		&expectedVersion,
		"expected-version",
		0,
		"current annotation state_version",
	)
	_ = cmd.MarkFlagRequired("expected-version")
	return cmd
}

func configuredFileAnnotationClient() (*apiclient.Client, error) {
	cfg, err := resolveConfig("")
	if err != nil {
		return nil, err
	}
	if cfg.Token == "" {
		return nil, errNotLoggedIn()
	}
	return newHTTPClient(cfg).api, nil
}

func printFileAnnotationDecision(
	cmd *cobra.Command,
	format string,
	response *apiclient.FileAnnotationDecisionResponse,
) error {
	if format == "json" {
		return writeCommandJSON(cmd, response)
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintf(writer, "annotation_id\t%s\n", terminalSafe(response.Annotation.ID))
	fmt.Fprintf(writer, "file_id\t%s\n", terminalSafe(response.Annotation.FileID))
	fmt.Fprintf(writer, "kind\t%s\n", terminalSafe(response.Annotation.Kind))
	fmt.Fprintf(writer, "value\t%s\n", terminalSafe(response.Annotation.ValueText))
	fmt.Fprintf(writer, "status\t%s\n", terminalSafe(response.Annotation.Status))
	fmt.Fprintf(writer, "state_version\t%d\n", response.Annotation.StateVersion)
	fmt.Fprintf(writer, "replayed\t%t\n", response.Replayed)
	return writer.Flush()
}

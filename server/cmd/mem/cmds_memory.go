package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"unicode"
	"unicode/utf8"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/spf13/cobra"
)

var memoryKinds = map[string]struct{}{
	"observation": {},
	"decision":    {},
	"preference":  {},
	"task_state":  {},
	"fact":        {},
	"note":        {},
	"artifact":    {},
}

var feedbackActions = map[string]struct{}{
	"useful":     {},
	"not_useful": {},
	"pin":        {},
	"unpin":      {},
}

var forgetReasons = map[string]struct{}{
	"user_request": {},
	"incorrect":    {},
	"sensitive":    {},
	"expired":      {},
	"other":        {},
}

func newMemoryCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "memory <memory-id>",
		Short: "Get one structured Agent memory",
		Long: `Get the full content and provenance for one known structured-memory UUID.

Missing, cross-workspace, and out-of-token-path records share the same
not-found response. Use --scope only to narrow the authenticated path boundary.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			client, err := configuredMemoryClient()
			if err != nil {
				return err
			}
			record, err := client.GetMemory(
				cmd.Context(),
				strings.TrimSpace(args[0]),
				apiclient.MemoryGetOptions{Scope: strings.TrimSpace(scope)},
			)
			if err != nil {
				return fromAPIError(err)
			}
			if format == "json" {
				return writeCommandJSON(cmd, record)
			}
			return printMemoryDetail(cmd, record)
		},
	}
	cmd.Flags().StringVar(
		&scope,
		"scope",
		"",
		"optional absolute virtual path that narrows token access",
	)
	return cmd
}

func newMemoriesCmd() *cobra.Command {
	var (
		scope     string
		recursive bool
		kinds     []string
		lifecycle string
		pinned    bool
		limit     int
		cursor    string
	)
	cmd := &cobra.Command{
		Use:   "memories",
		Short: "List structured Agent memories",
		Long: `List inspectable structured memories without running retrieval.

The default text output is a compact table. Use --format json to preserve the
cursor and bounded memory summaries for scripts.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			lifecycle = strings.ToLower(strings.TrimSpace(lifecycle))
			switch lifecycle {
			case "active", "archived", "all":
			default:
				return fmt.Errorf("--lifecycle must be active, archived, or all")
			}
			for i, kind := range kinds {
				kinds[i] = strings.ToLower(strings.TrimSpace(kind))
				if _, ok := memoryKinds[kinds[i]]; !ok {
					return fmt.Errorf("invalid --kind %q", kind)
				}
			}
			if limit < 0 || limit > 100 {
				return fmt.Errorf("--limit must be between 0 and 100")
			}

			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return errNotLoggedIn()
			}

			options := apiclient.MemoryListOptions{
				Scope:     strings.TrimSpace(scope),
				Kinds:     kinds,
				Lifecycle: lifecycle,
				Limit:     limit,
				Cursor:    strings.TrimSpace(cursor),
			}
			if cmd.Flags().Changed("recursive") {
				options.Recursive = &recursive
			}
			if cmd.Flags().Changed("pinned") {
				options.Pinned = &pinned
			}
			response, err := newHTTPClient(cfg).api.ListMemories(cmd.Context(), options)
			if err != nil {
				return fromAPIError(err)
			}
			if format == "json" {
				return writeCommandJSON(cmd, response)
			}
			printMemoriesTable(cmd, response)
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "canonical virtual-folder scope")
	cmd.Flags().BoolVar(&recursive, "recursive", true, "include memories in descendant paths")
	cmd.Flags().StringArrayVar(&kinds, "kind", nil, "memory kind filter (repeatable)")
	cmd.Flags().StringVar(&lifecycle, "lifecycle", "active", "active|archived|all")
	cmd.Flags().BoolVar(&pinned, "pinned", false, "filter by pinned state; use --pinned=false for unpinned")
	cmd.Flags().IntVar(&limit, "limit", 50, "page size (max 100)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "opaque cursor returned by the previous page")
	return cmd
}

func newFeedbackCmd() *cobra.Command {
	var (
		action         string
		expected       int64
		idempotencyKey string
	)
	cmd := &cobra.Command{
		Use:   "feedback <memory-id>",
		Short: "Record useful/not-useful or pin feedback",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			action = strings.ToLower(strings.TrimSpace(action))
			if _, ok := feedbackActions[action]; !ok {
				return fmt.Errorf("--action must be useful, not_useful, pin, or unpin")
			}
			if expected <= 0 {
				return fmt.Errorf("--expected-version must be greater than zero")
			}
			idempotencyKey = strings.TrimSpace(idempotencyKey)
			if idempotencyKey == "" {
				return fmt.Errorf("--idempotency-key must not be empty")
			}
			client, err := configuredMemoryClient()
			if err != nil {
				return err
			}
			response, err := client.FeedbackMemory(
				cmd.Context(),
				strings.TrimSpace(args[0]),
				idempotencyKey,
				apiclient.MemoryFeedbackRequest{
					Action:          action,
					ExpectedVersion: expected,
				},
			)
			if err != nil {
				return fromAPIError(err)
			}
			return printMemoryMutation(cmd, format, response)
		},
	}
	cmd.Flags().StringVar(&action, "action", "", "useful|not_useful|pin|unpin")
	cmd.Flags().Int64Var(&expected, "expected-version", 0, "current memory state_version")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "stable retry key")
	_ = cmd.MarkFlagRequired("action")
	_ = cmd.MarkFlagRequired("expected-version")
	_ = cmd.MarkFlagRequired("idempotency-key")
	return cmd
}

func newArchiveCmd() *cobra.Command {
	return newMemoryStateCmd(
		"archive",
		"Exclude a memory from normal recall while preserving its audit record",
		func(
			client *apiclient.Client,
			cmd *cobra.Command,
			memoryID string,
			key string,
			request apiclient.MemoryVersionRequest,
		) (*apiclient.MemoryMutationResponse, error) {
			return client.ArchiveMemory(cmd.Context(), memoryID, key, request)
		},
	)
}

func newRestoreCmd() *cobra.Command {
	return newMemoryStateCmd(
		"restore",
		"Restore an archived memory to normal recall",
		func(
			client *apiclient.Client,
			cmd *cobra.Command,
			memoryID string,
			key string,
			request apiclient.MemoryVersionRequest,
		) (*apiclient.MemoryMutationResponse, error) {
			return client.RestoreMemory(cmd.Context(), memoryID, key, request)
		},
	)
}

type memoryStateCall func(
	client *apiclient.Client,
	cmd *cobra.Command,
	memoryID string,
	idempotencyKey string,
	request apiclient.MemoryVersionRequest,
) (*apiclient.MemoryMutationResponse, error)

func newMemoryStateCmd(name string, description string, call memoryStateCall) *cobra.Command {
	var (
		expected       int64
		idempotencyKey string
	)
	cmd := &cobra.Command{
		Use:   name + " <memory-id>",
		Short: description,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			if expected <= 0 {
				return fmt.Errorf("--expected-version must be greater than zero")
			}
			idempotencyKey = strings.TrimSpace(idempotencyKey)
			if idempotencyKey == "" {
				return fmt.Errorf("--idempotency-key must not be empty")
			}
			client, err := configuredMemoryClient()
			if err != nil {
				return err
			}
			response, err := call(
				client,
				cmd,
				strings.TrimSpace(args[0]),
				idempotencyKey,
				apiclient.MemoryVersionRequest{ExpectedVersion: expected},
			)
			if err != nil {
				return fromAPIError(err)
			}
			return printMemoryMutation(cmd, format, response)
		},
	}
	cmd.Flags().Int64Var(&expected, "expected-version", 0, "current memory state_version")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "stable retry key")
	_ = cmd.MarkFlagRequired("expected-version")
	_ = cmd.MarkFlagRequired("idempotency-key")
	return cmd
}

func newForgetCmd() *cobra.Command {
	var (
		expected       int64
		idempotencyKey string
		reason         string
		yes            bool
	)
	cmd := &cobra.Command{
		Use:   "forget <memory-id>",
		Short: "Irreversibly redact a structured memory from the live service",
		Long: `Forget one structured memory by irreversibly redacting its payload
from the live memory service. A separately stored source file is not deleted,
and database backups remain subject to the deployment's retention policy.

This operation is intentionally explicit: --yes, the current state version,
and a stable idempotency key are all required.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			if !yes {
				return fmt.Errorf("--yes is required to forget a memory")
			}
			if expected <= 0 {
				return fmt.Errorf("--expected-version must be greater than zero")
			}
			idempotencyKey = strings.TrimSpace(idempotencyKey)
			if idempotencyKey == "" {
				return fmt.Errorf("--idempotency-key must not be empty")
			}
			reason = strings.ToLower(strings.TrimSpace(reason))
			if _, ok := forgetReasons[reason]; !ok {
				return fmt.Errorf("--reason must be user_request, incorrect, sensitive, expired, or other")
			}
			client, err := configuredMemoryClient()
			if err != nil {
				return err
			}
			response, err := client.ForgetMemory(
				cmd.Context(),
				strings.TrimSpace(args[0]),
				idempotencyKey,
				apiclient.MemoryForgetRequest{
					ExpectedVersion: expected,
					Reason:          reason,
				},
			)
			if err != nil {
				return fromAPIError(err)
			}
			if format == "json" {
				return writeCommandJSON(cmd, response)
			}
			memoryID := response.MemoryID
			forgottenAt := response.ForgottenAt
			if response.Tombstone != nil {
				memoryID = response.Tombstone.ID
				forgottenAt = response.Tombstone.ForgottenAt
			}
			writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintf(writer, "memory_id\t%s\n", memoryID)
			if forgottenAt != nil {
				fmt.Fprintf(
					writer,
					"forgotten_at\t%s\n",
					forgottenAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
				)
			}
			fmt.Fprintf(writer, "replayed\t%t\n", response.Replayed)
			return writer.Flush()
		},
	}
	cmd.Flags().Int64Var(&expected, "expected-version", 0, "current memory state_version")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "stable retry key")
	cmd.Flags().StringVar(
		&reason,
		"reason",
		"user_request",
		"user_request|incorrect|sensitive|expired|other",
	)
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm permanent forgetting")
	_ = cmd.MarkFlagRequired("expected-version")
	_ = cmd.MarkFlagRequired("idempotency-key")
	return cmd
}

func configuredMemoryClient() (*apiclient.Client, error) {
	cfg, err := resolveConfig("")
	if err != nil {
		return nil, err
	}
	if cfg.Token == "" {
		return nil, errNotLoggedIn()
	}
	return newHTTPClient(cfg).api, nil
}

func writeCommandJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printMemoriesTable(cmd *cobra.Command, response *apiclient.MemoryListResponse) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tKIND\tLIFECYCLE\tPINNED\tVERSION\tPATH\tCONTENT")
	for _, memory := range response.Memories {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%t\t%d\t%s\t%s\n",
			memory.ID,
			memory.Kind,
			memory.LifecycleStatus,
			memory.Pinned,
			memory.StateVersion,
			terminalSafe(memory.Path),
			memoryTableContent(memory.Excerpt),
		)
	}
	if response.NextCursor != "" {
		fmt.Fprintf(writer, "\nnext_cursor\t%s\n", response.NextCursor)
	}
	_ = writer.Flush()
}

func printMemoryDetail(cmd *cobra.Command, memory *apiclient.MemoryDetail) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintf(writer, "memory_id\t%s\n", memory.ID)
	fmt.Fprintf(writer, "citation\t%s\n", terminalSafe(memory.Citation))
	fmt.Fprintf(writer, "kind\t%s\n", terminalSafe(memory.Kind))
	fmt.Fprintf(writer, "lifecycle\t%s\n", terminalSafe(memory.LifecycleStatus))
	fmt.Fprintf(writer, "pinned\t%t\n", memory.Pinned)
	fmt.Fprintf(writer, "state_version\t%d\n", memory.StateVersion)
	fmt.Fprintf(writer, "path\t%s\n", terminalSafe(memory.Path))
	fmt.Fprintf(
		writer,
		"workspace_id\t%s\n",
		terminalSafe(memory.Provenance.WorkspaceID),
	)
	if memory.Provenance.CreatedByUserID != nil {
		fmt.Fprintf(
			writer,
			"created_by_user_id\t%s\n",
			terminalSafe(*memory.Provenance.CreatedByUserID),
		)
	}
	if memory.Provenance.EventAt != nil {
		fmt.Fprintf(
			writer,
			"event_at\t%s\n",
			memory.Provenance.EventAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		)
	}
	fmt.Fprintf(
		writer,
		"source_type\t%s\n",
		terminalSafe(memory.Provenance.SourceType),
	)
	if memory.Provenance.SourceRef != "" {
		fmt.Fprintf(
			writer,
			"source_ref\t%s\n",
			terminalSafe(memory.Provenance.SourceRef),
		)
	}
	if memory.Provenance.SourceFileID != nil {
		fmt.Fprintf(
			writer,
			"source_file_id\t%s\n",
			terminalSafe(*memory.Provenance.SourceFileID),
		)
	}
	if memory.Provenance.SourceFileSHA256 != "" {
		fmt.Fprintf(
			writer,
			"source_file_sha256\t%s\n",
			terminalSafe(memory.Provenance.SourceFileSHA256),
		)
	}
	if len(memory.Provenance.SourceLocator) > 0 {
		fmt.Fprintf(
			writer,
			"source_locator\t%s\n",
			terminalSafe(string(memory.Provenance.SourceLocator)),
		)
	}
	if memory.Provenance.ProducerAgent != "" {
		fmt.Fprintf(
			writer,
			"producer_agent\t%s\n",
			terminalSafe(memory.Provenance.ProducerAgent),
		)
	}
	if memory.Provenance.ProducerSession != "" {
		fmt.Fprintf(
			writer,
			"producer_session\t%s\n",
			terminalSafe(memory.Provenance.ProducerSession),
		)
	}
	if memory.Provenance.ProducerTask != "" {
		fmt.Fprintf(
			writer,
			"producer_task\t%s\n",
			terminalSafe(memory.Provenance.ProducerTask),
		)
	}
	if len(memory.Attributes) > 0 {
		fmt.Fprintf(
			writer,
			"attributes\t%s\n",
			terminalSafe(string(memory.Attributes)),
		)
	}
	if memory.ContentSHA256 != "" {
		fmt.Fprintf(writer, "content_sha256\t%s\n", memory.ContentSHA256)
	}
	if !memory.CreatedAt.IsZero() {
		fmt.Fprintf(
			writer,
			"created_at\t%s\n",
			memory.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		)
	}
	if !memory.UpdatedAt.IsZero() {
		fmt.Fprintf(
			writer,
			"updated_at\t%s\n",
			memory.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		)
	}
	fmt.Fprintf(writer, "content\t%s\n", terminalSafe(memory.Content))
	return writer.Flush()
}

func printMemoryMutation(
	cmd *cobra.Command,
	format string,
	response *apiclient.MemoryMutationResponse,
) error {
	if format == "json" {
		return writeCommandJSON(cmd, response)
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintf(writer, "memory_id\t%s\n", response.Memory.ID)
	fmt.Fprintf(writer, "lifecycle\t%s\n", response.Memory.LifecycleStatus)
	fmt.Fprintf(writer, "pinned\t%t\n", response.Memory.Pinned)
	fmt.Fprintf(writer, "feedback_score\t%d\n", response.Memory.FeedbackScore)
	fmt.Fprintf(writer, "feedback_count\t%d\n", response.Memory.FeedbackCount)
	fmt.Fprintf(writer, "state_version\t%d\n", response.Memory.StateVersion)
	fmt.Fprintf(writer, "replayed\t%t\n", response.Replayed)
	return writer.Flush()
}

func memoryTableContent(content string) string {
	content = terminalSafe(strings.Join(strings.Fields(content), " "))
	const maxRunes = 72
	if utf8.RuneCountInString(content) <= maxRunes {
		return content
	}
	runes := []rune(content)
	return string(runes[:maxRunes-1]) + "…"
}

// terminalSafe preserves normal Unicode while rendering control and bidi
// formatting code points as visible escapes. JSON output intentionally keeps
// the original value; this guard is for human-facing terminal output.
func terminalSafe(value string) string {
	var out strings.Builder
	for _, r := range value {
		bidiControl := unicode.Properties["Bidi_Control"]
		if unicode.IsControl(r) ||
			r == '\u2028' ||
			r == '\u2029' ||
			(bidiControl != nil && unicode.Is(bidiControl, r)) {
			if r <= 0xff {
				fmt.Fprintf(&out, `\x%02x`, r)
			} else {
				fmt.Fprintf(&out, `\u%04x`, r)
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

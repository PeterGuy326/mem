package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/spf13/cobra"
)

const maxHandoffInputBytes = 512 << 10

func newCheckpointCmd() *cobra.Command {
	var (
		inputPath      string
		idempotencyKey string
	)
	cmd := &cobra.Command{
		Use:   "checkpoint",
		Short: "Save a portable Agent task checkpoint",
		Long: `Persist one strict mem.handoff v1 document so another Agent or
computer can continue the same task.

Examples:
  mem checkpoint --input handoff.json --idempotency-key task-42-checkpoint-1
  cat handoff.json | mem checkpoint --input - --idempotency-key task-42-checkpoint-1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(inputPath) == "" {
				return errors.New("--input must not be empty")
			}
			idempotencyKey = strings.TrimSpace(idempotencyKey)
			if idempotencyKey == "" {
				return errors.New("--idempotency-key must not be empty")
			}
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}

			document, err := readHandoffInput(cmd, inputPath)
			if err != nil {
				return err
			}
			if err := document.Validate(); err != nil {
				return fmt.Errorf("invalid handoff input: %w", err)
			}

			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return errNotLoggedIn()
			}
			raw, err := newHTTPClient(cfg).api.Checkpoint(
				commandContext(cmd),
				document.TaskKey,
				document,
				idempotencyKey,
			)
			if err != nil {
				return fromAPIError(err)
			}
			if format == "json" {
				return writeHandoffJSON(cmd.OutOrStdout(), raw)
			}
			return printCheckpointText(cmd.OutOrStdout(), raw)
		},
	}
	cmd.Flags().StringVar(&inputPath, "input", "", "handoff v1 JSON file (- for stdin)")
	cmd.Flags().StringVar(
		&idempotencyKey,
		"idempotency-key",
		"",
		"stable retry key, unique within the workspace",
	)
	_ = cmd.MarkFlagRequired("input")
	_ = cmd.MarkFlagRequired("idempotency-key")
	cmd.AddCommand(newCheckpointGetCmd())
	return cmd
}

func newTasksCmd() *cobra.Command {
	var (
		scope string
		limit int
		after string
	)
	cmd := &cobra.Command{
		Use:   "tasks",
		Short: "List resumable Agent tasks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			client, err := configuredMemoryClient()
			if err != nil {
				return err
			}
			response, err := client.ListTasks(
				commandContext(cmd),
				apiclient.TaskListOptions{
					Scope: strings.TrimSpace(scope),
					Limit: limit,
					After: strings.TrimSpace(after),
				},
			)
			if err != nil {
				return fromAPIError(err)
			}
			if format == "json" {
				return writeCommandJSON(cmd, response)
			}
			return printTasksText(cmd, response)
		},
	}
	cmd.Flags().StringVar(
		&scope,
		"scope",
		"",
		"optional absolute virtual path that narrows token access",
	)
	cmd.Flags().IntVar(&limit, "limit", 50, "page size (max 200)")
	cmd.Flags().StringVar(&after, "after", "", "task UUID cursor from the previous page")
	return cmd
}

func newCheckpointsCmd() *cobra.Command {
	var (
		scope  string
		limit  int
		before int64
	)
	cmd := &cobra.Command{
		Use:   "checkpoints <task-key>",
		Short: "List bounded checkpoint summaries for one task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			client, err := configuredMemoryClient()
			if err != nil {
				return err
			}
			response, err := client.ListCheckpoints(
				commandContext(cmd),
				args[0],
				apiclient.CheckpointListOptions{
					Scope:  strings.TrimSpace(scope),
					Limit:  limit,
					Before: before,
				},
			)
			if err != nil {
				return fromAPIError(err)
			}
			if format == "json" {
				return writeCommandJSON(cmd, response)
			}
			return printCheckpointsText(cmd, response)
		},
	}
	cmd.Flags().StringVar(
		&scope,
		"scope",
		"",
		"optional absolute virtual path that narrows token access",
	)
	cmd.Flags().IntVar(&limit, "limit", 50, "page size (max 200)")
	cmd.Flags().Int64Var(
		&before,
		"before",
		0,
		"return checkpoints before this positive sequence",
	)
	return cmd
}

func newCheckpointGetCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "get <task-key> <checkpoint-id>",
		Short: "Get one immutable task checkpoint",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			client, err := configuredMemoryClient()
			if err != nil {
				return err
			}
			checkpoint, err := client.GetCheckpoint(
				commandContext(cmd),
				args[0],
				args[1],
				apiclient.CheckpointGetOptions{Scope: strings.TrimSpace(scope)},
			)
			if err != nil {
				return fromAPIError(err)
			}
			if format == "json" {
				return writeCommandJSON(cmd, checkpoint)
			}
			return printCheckpointDetailText(cmd, checkpoint)
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

func newResumeCmd() *cobra.Command {
	var (
		checkpointID string
		scope        string
		focus        string
		limit        int
		maxChars     int
	)
	cmd := &cobra.Command{
		Use:   "resume <task-key>",
		Short: "Load a portable Agent task checkpoint",
		Long: `Resolve an explicit checkpoint, or the current task head, together
with its verified references and a bounded related context pack.

Examples:
  mem resume task-42
  mem resume task-42 --scope /Projects/mem --focus "remaining migration work"
  mem resume task-42 --checkpoint-id 4ca7... --format json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			taskKey := args[0]
			request := apiclient.ResumeRequest{
				CheckpointID: strings.TrimSpace(checkpointID),
				Scope:        strings.TrimSpace(scope),
				Focus:        strings.TrimSpace(focus),
				Limit:        limit,
				MaxChars:     maxChars,
			}

			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return errNotLoggedIn()
			}
			raw, err := newHTTPClient(cfg).api.Resume(
				commandContext(cmd),
				taskKey,
				request,
			)
			if err != nil {
				return fromAPIError(err)
			}
			if format == "json" {
				return writeHandoffJSON(cmd.OutOrStdout(), raw)
			}
			return printResumeText(cmd.OutOrStdout(), raw)
		},
	}
	cmd.Flags().StringVar(
		&checkpointID,
		"checkpoint-id",
		"",
		"resume an explicit immutable checkpoint UUID (default current head)",
	)
	cmd.Flags().StringVar(&scope, "scope", "", "narrow evidence to an absolute virtual path")
	cmd.Flags().StringVar(&focus, "focus", "", "semantic focus for related context")
	cmd.Flags().IntVar(&limit, "limit", 0, "max related evidence items")
	cmd.Flags().IntVar(&maxChars, "max-chars", 0, "related context character budget")
	return cmd
}

func readHandoffInput(cmd *cobra.Command, inputPath string) (apiclient.HandoffV1, error) {
	var (
		reader io.Reader
		close  func() error
	)
	if inputPath == "-" {
		reader = cmd.InOrStdin()
	} else {
		file, err := os.Open(inputPath)
		if err != nil {
			return apiclient.HandoffV1{}, fmt.Errorf("open handoff input: %w", err)
		}
		reader = file
		close = file.Close
	}
	if close != nil {
		defer close()
	}

	data, err := io.ReadAll(io.LimitReader(reader, maxHandoffInputBytes+1))
	if err != nil {
		return apiclient.HandoffV1{}, fmt.Errorf("read handoff input: %w", err)
	}
	if len(data) > maxHandoffInputBytes {
		return apiclient.HandoffV1{}, fmt.Errorf(
			"handoff input exceeds %d bytes",
			maxHandoffInputBytes,
		)
	}

	var document apiclient.HandoffV1
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return apiclient.HandoffV1{}, fmt.Errorf("decode handoff input: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return apiclient.HandoffV1{}, errors.New(
			"decode handoff input: expected exactly one JSON document",
		)
	} else if !errors.Is(err, io.EOF) {
		return apiclient.HandoffV1{}, fmt.Errorf("decode handoff input: %w", err)
	}
	return document, nil
}

func commandContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func writeHandoffJSON(w io.Writer, raw json.RawMessage) error {
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, raw, "", "  "); err != nil {
		return fmt.Errorf("decode server response: %w", err)
	}
	formatted.WriteByte('\n')
	_, err := w.Write(formatted.Bytes())
	return err
}

func printCheckpointText(w io.Writer, raw json.RawMessage) error {
	var response struct {
		Checkpoint struct {
			ID       string `json:"id"`
			TaskKey  string `json:"task_key"`
			Sequence int64  `json:"sequence"`
			Scope    string `json:"scope_path"`
		} `json:"checkpoint"`
		Replayed bool `json:"replayed"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("decode server response: %w", err)
	}
	if response.Checkpoint.ID == "" {
		return errors.New("decode server response: checkpoint.id is missing")
	}
	fmt.Fprintf(w, "checkpoint: %s\n", response.Checkpoint.ID)
	if response.Checkpoint.TaskKey != "" {
		fmt.Fprintf(w, "task: %s\n", response.Checkpoint.TaskKey)
	}
	if response.Checkpoint.Sequence > 0 {
		fmt.Fprintf(w, "sequence: %d\n", response.Checkpoint.Sequence)
	}
	if response.Checkpoint.Scope != "" {
		fmt.Fprintf(w, "scope: %s\n", response.Checkpoint.Scope)
	}
	fmt.Fprintf(w, "replayed: %t\n", response.Replayed)
	return nil
}

func printTasksText(cmd *cobra.Command, response *apiclient.TaskListResponse) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tTASK\tHEAD\tSEQUENCE\tSCOPE\tUPDATED")
	for _, task := range response.Tasks {
		head := ""
		if task.HeadCheckpointID != nil {
			head = *task.HeadCheckpointID
		}
		updated := ""
		if !task.UpdatedAt.IsZero() {
			updated = task.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%d\t%s\t%s\n",
			task.ID,
			terminalSafe(task.TaskKey),
			head,
			task.HeadSequence,
			terminalSafe(task.ScopePath),
			updated,
		)
	}
	return writer.Flush()
}

func printCheckpointsText(
	cmd *cobra.Command,
	response *apiclient.CheckpointListResponse,
) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tSEQUENCE\tKIND\tSTATUS\tSCOPE\tCREATED")
	for _, checkpoint := range response.Checkpoints {
		created := ""
		if !checkpoint.CreatedAt.IsZero() {
			created = checkpoint.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		fmt.Fprintf(
			writer,
			"%s\t%d\t%s\t%s\t%s\t%s\n",
			checkpoint.ID,
			checkpoint.Sequence,
			terminalSafe(checkpoint.CheckpointKind),
			terminalSafe(checkpoint.Status),
			terminalSafe(checkpoint.ScopePath),
			created,
		)
	}
	return writer.Flush()
}

func printCheckpointDetailText(
	cmd *cobra.Command,
	checkpoint *apiclient.CheckpointRecord,
) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintf(writer, "checkpoint\t%s\n", checkpoint.ID)
	fmt.Fprintf(writer, "task\t%s\n", terminalSafe(checkpoint.TaskKey))
	fmt.Fprintf(writer, "sequence\t%d\n", checkpoint.Sequence)
	fmt.Fprintf(writer, "kind\t%s\n", terminalSafe(checkpoint.CheckpointKind))
	fmt.Fprintf(writer, "contract\t%s\n", terminalSafe(checkpoint.Contract))
	fmt.Fprintf(writer, "schema_version\t%d\n", checkpoint.SchemaVersion)
	fmt.Fprintf(writer, "scope\t%s\n", terminalSafe(checkpoint.ScopePath))
	fmt.Fprintf(writer, "status\t%s\n", terminalSafe(checkpoint.Handoff.State.Status))
	fmt.Fprintf(writer, "goal\t%s\n", terminalSafe(checkpoint.Handoff.State.Goal))
	fmt.Fprintf(
		writer,
		"progress\t%s\n",
		terminalSafe(checkpoint.Handoff.State.Progress.Summary),
	)
	fmt.Fprintf(writer, "references\t%d\n", len(checkpoint.References))
	if !checkpoint.CreatedAt.IsZero() {
		fmt.Fprintf(
			writer,
			"created_at\t%s\n",
			checkpoint.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		)
	}
	return writer.Flush()
}

func printResumeText(w io.Writer, raw json.RawMessage) error {
	var response struct {
		Task struct {
			TaskKey string `json:"task_key"`
		} `json:"task"`
		Checkpoint struct {
			ID       string `json:"id"`
			Sequence int64  `json:"sequence"`
			Handoff  struct {
				State struct {
					Status   string `json:"status"`
					Goal     string `json:"goal"`
					Progress struct {
						Summary string `json:"summary"`
					} `json:"progress"`
				} `json:"state"`
			} `json:"handoff"`
		} `json:"checkpoint"`
		Resolved []json.RawMessage `json:"resolved"`
		Missing  []json.RawMessage `json:"missing"`
		Complete bool              `json:"complete"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("decode server response: %w", err)
	}
	if response.Checkpoint.ID == "" {
		return errors.New("decode server response: checkpoint.id is missing")
	}
	if response.Task.TaskKey != "" {
		fmt.Fprintf(w, "task: %s\n", response.Task.TaskKey)
	}
	fmt.Fprintf(w, "checkpoint: %s\n", response.Checkpoint.ID)
	if response.Checkpoint.Sequence > 0 {
		fmt.Fprintf(w, "sequence: %d\n", response.Checkpoint.Sequence)
	}
	if response.Checkpoint.Handoff.State.Status != "" {
		fmt.Fprintf(w, "status: %s\n", response.Checkpoint.Handoff.State.Status)
	}
	if response.Checkpoint.Handoff.State.Goal != "" {
		fmt.Fprintf(w, "goal: %s\n", response.Checkpoint.Handoff.State.Goal)
	}
	if response.Checkpoint.Handoff.State.Progress.Summary != "" {
		fmt.Fprintf(
			w,
			"progress: %s\n",
			response.Checkpoint.Handoff.State.Progress.Summary,
		)
	}
	fmt.Fprintf(w, "references: %d resolved, %d missing\n",
		len(response.Resolved), len(response.Missing))
	fmt.Fprintf(w, "complete: %t\n", response.Complete)
	return nil
}

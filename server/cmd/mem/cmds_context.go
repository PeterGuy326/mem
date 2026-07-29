package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

type contextEvidence struct {
	EvidenceID    string `json:"evidence_id"`
	SourceKind    string `json:"source_kind"`
	SourceID      string `json:"source_id"`
	Citation      string `json:"citation"`
	FileID        string `json:"file_id,omitempty"`
	MemoryID      string `json:"memory_id,omitempty"`
	MemoryKind    string `json:"memory_kind,omitempty"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	MIME          string `json:"mime"`
	ContentSHA256 string `json:"content_sha256"`
	ContentURL    string `json:"content_url"`
	Locator       struct {
		Kind       string `json:"kind"`
		ChunkIndex *int   `json:"chunk_index,omitempty"`
	} `json:"locator"`
	Excerpt    string  `json:"excerpt"`
	Score      float64 `json:"score"`
	Route      string  `json:"route"`
	Reason     string  `json:"reason,omitempty"`
	TimelineAt *string `json:"timeline_at,omitempty"`
}

type contextWarning struct {
	Source  string `json:"source"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type contextResponse struct {
	Query      string            `json:"query"`
	Scope      string            `json:"scope"`
	Source     string            `json:"source"`
	Evidence   []contextEvidence `json:"evidence"`
	TotalChars int               `json:"total_chars"`
	Partial    bool              `json:"partial"`
	Warnings   []contextWarning  `json:"warnings,omitempty"`
	Retrieved  string            `json:"retrieved_at"`
}

func newContextCmd() *cobra.Command {
	var (
		scope          string
		source         string
		typ            string
		memKind        string
		since          string
		until          string
		limit          int
		maxChars       int
		format         string
		idempotencyKey string
	)
	cmd := &cobra.Command{
		Use:   "context <query>",
		Short: "Build a bounded evidence pack for an external Agent",
		Long: `Recall source-verifiable memory without asking mem to generate an answer.
The returned citations and excerpts are designed for Agent context windows.

Examples:
  mem context "上次为什么决定使用 PostgreSQL" --scope /Projects/mem
  mem context "2012 年云南照片" --type image --format json`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			validatedFormat, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return newCliError(3, "not logged in", "run `mem auth login` first")
			}
			body := map[string]any{"query": strings.Join(args, " ")}
			if scope != "" {
				body["scope"] = scope
			}
			if source != "" {
				body["source"] = source
			}
			if typ != "" {
				body["type"] = typ
			}
			if memKind != "" {
				body["memory_kind"] = memKind
			}
			if since != "" {
				body["since"] = since
			}
			if until != "" {
				body["until"] = until
			}
			if limit > 0 {
				body["limit"] = limit
			}
			if maxChars > 0 {
				body["max_chars"] = maxChars
			}
			var resp contextResponse
			requestKey, err := managedRequestKey("context", idempotencyKey)
			if err != nil {
				return err
			}
			if err := newHTTPClient(cfg).doJSONWithHeaders(
				"POST",
				"/v1/context",
				body,
				&resp,
				map[string]string{"Idempotency-Key": requestKey},
			); err != nil {
				return err
			}
			output := cmd.OutOrStdout()
			if validatedFormat == "json" {
				enc := json.NewEncoder(output)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}
			if len(resp.Evidence) == 0 {
				fmt.Fprintln(output, "(no evidence)")
				return nil
			}
			for i, ev := range resp.Evidence {
				label := ev.Name
				if label == "" {
					label = ev.MemoryKind
				}
				if label == "" {
					label = ev.SourceID
				}
				fmt.Fprintf(
					output,
					"%2d. [%.3f / %s] %s\n",
					i+1,
					ev.Score,
					terminalSafe(ev.Route),
					terminalSafe(label),
				)
				fmt.Fprintf(output, "    %s\n", terminalSafe(ev.Citation))
				if ev.MIME == "" {
					fmt.Fprintf(output, "    %s\n", terminalSafe(ev.Path))
				} else {
					fmt.Fprintf(
						output,
						"    %s  (%s)\n",
						terminalSafe(ev.Path),
						terminalSafe(ev.MIME),
					)
				}
				if ev.Reason != "" {
					fmt.Fprintf(output, "    reason: %s\n", terminalSafe(ev.Reason))
				}
				if ev.Locator.ChunkIndex != nil {
					fmt.Fprintf(
						output,
						"    locator: %s %d\n",
						terminalSafe(ev.Locator.Kind),
						*ev.Locator.ChunkIndex,
					)
				} else if ev.Locator.Kind != "" {
					fmt.Fprintf(output, "    locator: %s\n", terminalSafe(ev.Locator.Kind))
				}
				if ev.Excerpt != "" {
					fmt.Fprintf(
						output,
						"    > %s\n",
						terminalSafe(strings.ReplaceAll(ev.Excerpt, "\n", " ")),
					)
				}
			}
			fmt.Fprintf(output, "\n%d evidence item(s), %d chars\n", len(resp.Evidence), resp.TotalChars)
			if resp.Partial {
				for _, warning := range resp.Warnings {
					fmt.Fprintf(
						output,
						"warning [%s]: %s\n",
						terminalSafe(warning.Source),
						terminalSafe(warning.Message),
					)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "virtual-folder scope, e.g. /Projects/mem")
	cmd.Flags().StringVar(&source, "source", "", "evidence source: all|file|memory (default all)")
	cmd.Flags().StringVar(&typ, "type", "", "mime prefix filter: image|text|application|audio|video")
	cmd.Flags().StringVar(&memKind, "memory-kind", "", "structured memory kind filter")
	cmd.Flags().StringVar(&since, "since", "", "YYYY-MM-DD inclusive lower bound")
	cmd.Flags().StringVar(&until, "until", "", "YYYY-MM-DD inclusive upper bound")
	cmd.Flags().IntVar(&limit, "limit", 0, "max evidence items (default 8, max 50)")
	cmd.Flags().IntVar(&maxChars, "max-chars", 0, "total context character budget (default 12000)")
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	cmd.Flags().StringVar(
		&idempotencyKey,
		"idempotency-key",
		"",
		"stable retry key for managed embedding usage (generated when omitted)",
	)
	return cmd
}

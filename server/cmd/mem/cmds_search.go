package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// SearchHit mirrors search.Hit on the server (kept here so the CLI does not
// import the server-internal package).
type SearchHit struct {
	FileID     string     `json:"file_id"`
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	MIME       string     `json:"mime"`
	Score      float64    `json:"score"`
	Snippet    string     `json:"snippet"`
	Source     string     `json:"source"`
	Summary    *string    `json:"summary,omitempty"`
	TimelineAt *time.Time `json:"timeline_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type searchResponse struct {
	Results []SearchHit    `json:"results"`
	Meta    map[string]any `json:"_meta"`
}

func newSearchCmd() *cobra.Command {
	var (
		typ            string
		route          string
		since          string
		until          string
		limit          int
		format         string
		idempotencyKey string
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Natural-language search across your AI drive (SPEC §F3)",
		Long: `Search your indexed files by meaning. Examples:

  mem search "草地上的金毛"
  mem search "rental contract" --type doc --since 2024-01-01
  mem search "vacation photos" --format json --limit 20`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return newCliError(3, "not logged in", "run `mem auth login` first")
			}
			c := newHTTPClient(cfg)

			body := map[string]any{"query": strings.Join(args, " ")}
			if typ != "" {
				body["type"] = typ
			}
			if route != "" {
				body["route"] = route
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

			var resp searchResponse
			requestKey, err := managedRequestKey("search", idempotencyKey)
			if err != nil {
				return err
			}
			if err := c.doJSONWithHeaders(
				"POST",
				"/v1/search",
				body,
				&resp,
				map[string]string{"Idempotency-Key": requestKey},
			); err != nil {
				return err
			}

			if format == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}
			return printSearchText(resp)
		},
	}
	cmd.Flags().StringVar(&typ, "type", "", "mime prefix filter: image|text|application|audio|video")
	cmd.Flags().StringVar(&route, "route", "", "search route: text|visual|auto (default auto)")
	cmd.Flags().StringVar(&since, "since", "", "YYYY-MM-DD inclusive lower bound on timeline_at")
	cmd.Flags().StringVar(&until, "until", "", "YYYY-MM-DD inclusive upper bound on timeline_at")
	cmd.Flags().IntVar(&limit, "limit", 0, "max results (default 10, max 100)")
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	cmd.Flags().StringVar(
		&idempotencyKey,
		"idempotency-key",
		"",
		"stable retry key for managed embedding usage (generated when omitted)",
	)
	return cmd
}

func printSearchText(resp searchResponse) error {
	if len(resp.Results) == 0 {
		fmt.Println("(no matches)")
		if ms, ok := resp.Meta["latency_ms"]; ok {
			fmt.Printf("\n%v ms\n", ms)
		}
		return nil
	}
	for i, h := range resp.Results {
		tag := h.Source
		if tag == "" {
			tag = "text"
		}
		fmt.Printf("%2d. [%.3f / %s] %s\n", i+1, h.Score, tag, h.Name)
		fmt.Printf("    %s  (%s, %s)\n", h.FileID, h.MIME, h.Path)
		if h.Snippet != "" {
			fmt.Printf("    > %s\n", strings.ReplaceAll(h.Snippet, "\n", " "))
		}
		if h.Summary != nil && *h.Summary != "" {
			fmt.Printf("    summary: %s\n", *h.Summary)
		}
	}
	if ms, ok := resp.Meta["latency_ms"]; ok {
		fmt.Printf("\n%d result(s), %v ms\n", len(resp.Results), ms)
	}
	return nil
}

// Compile-time check: cobra commands return *cobra.Command. errors import is
// retained only if needed elsewhere; remove unused import warning would surface.
var _ = errors.New

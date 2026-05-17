package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type askSource struct {
	FileID  string  `json:"file_id"`
	Name    string  `json:"name"`
	Path    string  `json:"path"`
	MIME    string  `json:"mime"`
	Excerpt string  `json:"excerpt"`
	Score   float64 `json:"score"`
}

type askResp struct {
	Answer    string      `json:"answer"`
	Sources   []askSource `json:"sources"`
	Provider  string      `json:"provider"`
	LatencyMS int64       `json:"latency_ms"`
}

func newAskCmd() *cobra.Command {
	var (
		scope  string
		topK   int
		format string
	)
	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Cross-file question answering (SPEC §F5 RAG)",
		Long: `Ask a question about your indexed files. mem retrieves the top-K
relevant snippets and asks your configured LLM to synthesize an answer,
returning the answer plus the source files it grounded in.

Examples:
  mem ask "what is in my rental contract?"
  mem ask "summarize my notes about python" --top-k 3
  mem ask "find vacation memories" --scope /Photos`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return newCliError(3, "not logged in", "run `mem login` first")
			}
			c := newHTTPClient(cfg)
			body := map[string]any{"question": strings.Join(args, " ")}
			if scope != "" {
				body["scope"] = scope
			}
			if topK > 0 {
				body["top_k"] = topK
			}
			var resp askResp
			if err := c.doJSON("POST", "/v1/ask", body, &resp); err != nil {
				return err
			}
			if format == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}
			fmt.Println(resp.Answer)
			if len(resp.Sources) > 0 {
				fmt.Println("\nSources:")
				for i, src := range resp.Sources {
					fmt.Printf("  [%d] %s  (%s)\n", i+1, src.Name, src.FileID[:8])
				}
			}
			fmt.Printf("\n%s · %d ms\n", resp.Provider, resp.LatencyMS)
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "restrict to a path prefix (not yet implemented)")
	cmd.Flags().IntVar(&topK, "top-k", 0, "number of context snippets (default 5, max 20)")
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

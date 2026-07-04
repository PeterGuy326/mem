package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

type relatedHit struct {
	FileID  string  `json:"file_id"`
	Name    string  `json:"name"`
	Path    string  `json:"path"`
	MIME    string  `json:"mime"`
	Type    string  `json:"type"`
	Score   float64 `json:"score"`
	Summary *string `json:"summary,omitempty"`
}

type relatedResp struct {
	FileID  string       `json:"file_id"`
	Related []relatedHit `json:"related"`
	Note    string       `json:"note,omitempty"`
}

func newRelatedCmd() *cobra.Command {
	var (
		typ    string
		limit  int
		format string
	)
	cmd := &cobra.Command{
		Use:   "related <file_id>",
		Short: "Find files related to <file_id> by embedding similarity (SPEC §F4)",
		Long: `Returns the top-K files most similar to the given one.

Relation types currently supported:
  same_topic  — text embedding similarity (any document)
  same_event  — visual embedding similarity (images only)
  same_person — shared person entities (face/text overlap)
  sequel      — narrative continuation (future)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return newCliError(3, "not logged in", "run `mem login` first")
			}
			c := newHTTPClient(cfg)
			path := "/v1/files/" + args[0] + "/related"
			q := ""
			if typ != "" {
				q = "?type=" + typ
			}
			if limit > 0 {
				if q == "" {
					q = "?"
				} else {
					q += "&"
				}
				q += fmt.Sprintf("limit=%d", limit)
			}
			var resp relatedResp
			if err := c.doJSON("GET", path+q, nil, &resp); err != nil {
				return err
			}
			if format == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}
			if len(resp.Related) == 0 {
				if resp.Note != "" {
					fmt.Printf("(no related: %s)\n", resp.Note)
				} else {
					fmt.Println("(no related files)")
				}
				return nil
			}
			for i, r := range resp.Related {
				fmt.Printf("%2d. [%.3f / %s] %s\n", i+1, r.Score, r.Type, r.Name)
				fmt.Printf("    %s  (%s, %s)\n", r.FileID, r.MIME, r.Path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&typ, "type", "", "filter: same_topic|same_event|same_person|sequel")
	cmd.Flags().IntVar(&limit, "limit", 0, "max results (default 10)")
	cmd.Flags().StringVar(&format, "format", "text", "text|json")

	cmd.AddCommand(newRelatedRebuildCmd())
	return cmd
}

type rebuildReq struct {
	FileID string `json:"file_id,omitempty"`
}

type rebuildResp struct {
	Files    int `json:"files"`
	Failures int `json:"failures"`
}

func newRelatedRebuildCmd() *cobra.Command {
	var (
		file   string
		format string
	)
	cmd := &cobra.Command{
		Use:   "rebuild",
		Short: "Recompute file_relations for every file (or one --file)",
		Long: `Backfills file_relations after a new relation type comes online
(e.g. same_person). Safe to run repeatedly — the relator wipes each file's
outgoing rows before recomputing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return newCliError(3, "not logged in", "run `mem login` first")
			}
			c := newHTTPClient(cfg)
			body := rebuildReq{FileID: file}
			var resp rebuildResp
			if err := c.doJSON("POST", "/v1/relations/rebuild", body, &resp); err != nil {
				return err
			}
			if format == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}
			fmt.Printf("rebuilt %d file(s), %d failure(s)\n", resp.Files, resp.Failures)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "scope to a single file_id (default: all files)")
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

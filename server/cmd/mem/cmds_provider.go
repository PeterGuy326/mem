package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// ProviderSetting mirrors provider.Setting on the server.
type ProviderSetting struct {
	UserID    string `json:"user_id"`
	Kind      string `json:"kind"`
	Spec      string `json:"spec"`
	Dim       *int   `json:"dim,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

type providerListResp struct {
	Settings []ProviderSetting `json:"settings"`
	Kinds    []string          `json:"kinds"`
}

type providerSetResp struct {
	Setting         ProviderSetting `json:"setting"`
	ReindexQueued   bool            `json:"reindex_queued"`
	ReindexFiles    int             `json:"reindex_files,omitempty"`
	ReindexRequired bool            `json:"reindex_required,omitempty"`
	PreviousDim     *int            `json:"previous_dim,omitempty"`
	DimMigrationOK  bool            `json:"dim_migration_ok"`
}

func newProviderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Manage indexing model providers (Embedding / VLM)",
		Long:  `View, set, and test the models used to index and retrieve memory. The calling Agent owns answer generation.`,
	}
	cmd.AddCommand(newProviderListCmd())
	cmd.AddCommand(newProviderSetCmd())
	cmd.AddCommand(newProviderTestCmd())
	cmd.AddCommand(newProviderReindexCmd())
	return cmd
}

func newProviderListCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your provider settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return newCliError(3, "not logged in", "run `mem auth login` first")
			}
			c := newHTTPClient(cfg)
			var resp providerListResp
			if err := c.doJSON("GET", "/v1/providers", nil, &resp); err != nil {
				return err
			}
			if format == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}
			fmt.Printf("%-12s  %-40s  %s\n", "KIND", "SPEC", "DIM")
			fmt.Println(strings.Repeat("-", 64))
			byKind := map[string]ProviderSetting{}
			for _, s := range resp.Settings {
				byKind[s.Kind] = s
			}
			for _, k := range resp.Kinds {
				if s, ok := byKind[k]; ok {
					dim := "-"
					if s.Dim != nil {
						dim = fmt.Sprintf("%d", *s.Dim)
					}
					fmt.Printf("%-12s  %-40s  %s\n", k, s.Spec, dim)
				} else {
					fmt.Printf("%-12s  %-40s  %s\n", k, "(default)", "-")
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

func newProviderSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <kind> <vendor:model>",
		Short: "Set an indexing provider",
		Long: `Examples:
  mem provider set embedding ollama:nomic-embed-text
  mem provider set embedding openai:text-embedding-3-small
  mem provider set vlm ollama:minicpm-v

Before setting the embedding provider, mem will:
  1. Probe the new provider to learn its output dim
  2. Reject a model whose dimension differs from the current vector schema
  3. Refuse vector-space changes once a corpus exists, until a staged index
     generation can be built and activated atomically

Visual embeddings are currently fixed to clip:ViT-B-32 so indexing and query
vectors cannot silently enter different spaces.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return newCliError(3, "not logged in", "run `mem auth login` first")
			}
			c := newHTTPClient(cfg)
			kind := args[0]
			spec := args[1]
			var resp providerSetResp
			if err := c.doJSON("PUT", "/v1/providers/"+kind,
				map[string]any{"spec": spec}, &resp); err != nil {
				return err
			}
			fmt.Printf("ok: %s -> %s\n", resp.Setting.Kind, resp.Setting.Spec)
			if resp.Setting.Dim != nil {
				fmt.Printf("dim: %d\n", *resp.Setting.Dim)
			}
			if resp.DimMigrationOK {
				prev := "(none)"
				if resp.PreviousDim != nil {
					prev = fmt.Sprintf("%d", *resp.PreviousDim)
				}
				fmt.Printf("schema dimension compatible: %s -> %d\n",
					prev, *resp.Setting.Dim)
			}
			if resp.ReindexQueued {
				fmt.Printf("re-index queued: %d files\n", resp.ReindexFiles)
			}
			if resp.ReindexRequired {
				fmt.Println("re-index required: run `mem provider reindex`")
			}
			return nil
		},
	}
	return cmd
}

func newProviderReindexCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Rebuild all file indexes with the selected embedding provider",
		Long: `Reprocess every file with the currently selected text embedding model.
Use this explicit recovery operation after upgrading a legacy corpus whose
historical provider identity was not recorded.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return newCliError(3, "not logged in", "run `mem auth login` first")
			}
			var resp struct {
				Provider string `json:"provider"`
				Files    int    `json:"files"`
				Queued   int    `json:"queued"`
				Failed   int    `json:"failed"`
			}
			if err := newHTTPClient(cfg).doJSON(
				"POST", "/v1/providers/embedding/reindex", map[string]any{}, &resp,
			); err != nil {
				return err
			}
			if format == "json" {
				return jsonOut(resp)
			}
			fmt.Printf("provider: %s\nfiles: %d\nqueued: %d\nfailed: %d\n",
				resp.Provider, resp.Files, resp.Queued, resp.Failed)
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

func newProviderTestCmd() *cobra.Command {
	var spec string
	cmd := &cobra.Command{
		Use:   "test <kind>",
		Short: "Probe an indexing provider (VLM makes one minimal real request)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return newCliError(3, "not logged in", "run `mem auth login` first")
			}
			c := newHTTPClient(cfg)
			kind := args[0]
			body := map[string]any{}
			if spec != "" {
				body["spec"] = spec
			}
			var resp map[string]any
			if err := c.doJSON("POST", "/v1/providers/"+kind+"/test", body, &resp); err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		},
	}
	cmd.Flags().StringVar(&spec, "spec", "", "test a specific spec without saving (e.g. openai:text-embedding-3-small)")
	return cmd
}

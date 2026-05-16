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
	Setting        ProviderSetting `json:"setting"`
	ReindexQueued  bool            `json:"reindex_queued"`
	ReindexFiles   int             `json:"reindex_files,omitempty"`
	PreviousDim    *int            `json:"previous_dim,omitempty"`
	DimMigrationOK bool            `json:"dim_migration_ok"`
}

func newProviderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Manage AI model providers (Embedding / LLM / VLM)",
		Long:  `View, set, and test the model providers used for indexing and search.`,
	}
	cmd.AddCommand(newProviderListCmd())
	cmd.AddCommand(newProviderSetCmd())
	cmd.AddCommand(newProviderTestCmd())
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
				return newCliError(3, "not logged in", "run `mem login` first")
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
		Short: "Set provider for a kind. Embedding switches trigger an automatic re-index.",
		Long: `Examples:
  mem provider set embedding ollama:nomic-embed-text
  mem provider set embedding openai:text-embedding-3-small
  mem provider set llm anthropic:claude-opus-4-7
  mem provider set vlm ollama:minicpm-v

When switching the embedding provider to one with a different output dimension,
mem will automatically:
  1. Probe the new provider to learn its output dim
  2. ALTER embeddings_text.embedding TO vector(N) (TRUNCATE first)
  3. Mark all your files as index_status='pending'
  4. Enqueue an indexing task per file (visible in worker logs)`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return newCliError(3, "not logged in", "run `mem login` first")
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
				fmt.Printf("dim migration: %s -> %d (embeddings_text re-typed, files marked pending)\n",
					prev, *resp.Setting.Dim)
			}
			if resp.ReindexQueued {
				fmt.Printf("re-index queued: %d files\n", resp.ReindexFiles)
			}
			return nil
		},
	}
	return cmd
}

func newProviderTestCmd() *cobra.Command {
	var spec string
	cmd := &cobra.Command{
		Use:   "test <kind>",
		Short: "Probe the provider for a kind (defaults to your saved spec)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return newCliError(3, "not logged in", "run `mem login` first")
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

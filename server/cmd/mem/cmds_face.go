package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

type faceCluster struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	FaceCount int    `json:"face_count"`
	FileCount int    `json:"file_count"`
}

type faceListResp struct {
	Clusters []faceCluster `json:"clusters"`
}

func newFaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "face",
		Short: "Manage person clusters detected in your images (SPEC §F6.1)",
	}
	cmd.AddCommand(newFaceListCmd())
	cmd.AddCommand(newFaceNameCmd())
	cmd.AddCommand(newFaceMergeCmd())
	return cmd
}

func newFaceListCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List person clusters with size",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return errNotLoggedIn()
			}
			c := newHTTPClient(cfg)
			var resp faceListResp
			if err := c.doJSON("GET", "/v1/faces", nil, &resp); err != nil {
				return err
			}
			if format == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}
			if len(resp.Clusters) == 0 {
				fmt.Println("(no faces detected yet — upload some photos with people first)")
				return nil
			}
			fmt.Printf("%-36s  %-20s  %5s  %5s\n", "ID", "NAME", "FACES", "FILES")
			for _, c := range resp.Clusters {
				name := c.Name
				if name == "" {
					name = "(unnamed)"
				}
				fmt.Printf("%-36s  %-20s  %5d  %5d\n", c.ID, name, c.FaceCount, c.FileCount)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

func newFaceNameCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "name <cluster_id> <name>",
		Short: "Name a person cluster",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)
			body := map[string]any{"name": args[1]}
			var out map[string]any
			if err := c.doJSON("POST", "/v1/faces/"+args[0]+"/name", body, &out); err != nil {
				return err
			}
			fmt.Println("ok")
			return nil
		},
	}
	return cmd
}

func newFaceMergeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "merge <keep_id> <merge_id>",
		Short: "Merge two clusters — keep_id survives, merge_id is removed",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)
			body := map[string]any{"into": args[1]}
			var out map[string]any
			if err := c.doJSON("POST", "/v1/faces/"+args[0]+"/merge", body, &out); err != nil {
				return err
			}
			fmt.Println("ok")
			return nil
		},
	}
	return cmd
}

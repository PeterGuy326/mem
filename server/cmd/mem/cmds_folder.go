package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

// newMkdirCmd — `mem mkdir /Photos/2012`. mkdir -p semantics on the server.
func newMkdirCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "mkdir <folder_path>",
		Short: "Create a folder (mkdir -p — auto-creates parents)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return errNotLoggedIn()
			}
			c := newHTTPClient(cfg)
			var resp map[string]any
			if err := c.doJSON(http.MethodPost, "/v1/folders",
				map[string]any{"path": args[0]}, &resp); err != nil {
				return err
			}
			if format == "json" {
				return jsonOut(resp)
			}
			fmt.Printf("created %v (id=%v)\n", resp["path"], resp["id"])
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

// newMvCmd — `mem mv <file_id> <new_path>`. Moves a file to a different
// folder (mkdir -p applied to destination).
func newMvCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "mv <file_id> <new_folder_path>",
		Short: "Move a file to a different folder",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)
			body := map[string]any{"path": args[1]}
			var resp map[string]any
			if err := c.doJSON(http.MethodPatch, "/v1/files/"+args[0], body, &resp); err != nil {
				return err
			}
			if format == "json" {
				return jsonOut(resp)
			}
			fmt.Printf("moved %v to %v\n", resp["id"], resp["path"])
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

// newRenameCmd — `mem rename <file_id> <new_name>`. Renames a file in place.
func newRenameCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "rename <file_id> <new_name>",
		Short: "Rename a file (basename only — path unchanged)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)
			body := map[string]any{"name": args[1]}
			var resp map[string]any
			if err := c.doJSON(http.MethodPatch, "/v1/files/"+args[0], body, &resp); err != nil {
				return err
			}
			if format == "json" {
				return jsonOut(resp)
			}
			fmt.Printf("renamed %v -> %v\n", resp["id"], resp["name"])
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

// newFolderCmd groups subcommands for folder management:
//
//	mem folder tree
//	mem folder rename <id> <new_name>
//	mem folder move   <id> <new_parent_path>
//	mem folder rm     <id> [--recursive]
func newFolderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "folder",
		Short: "Manage folders (tree / rename / move / rm)",
	}
	cmd.AddCommand(newFolderTreeCmd())
	cmd.AddCommand(newFolderRenameCmd())
	cmd.AddCommand(newFolderMoveCmd())
	cmd.AddCommand(newFolderRmCmd())
	return cmd
}

func newFolderTreeCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "tree",
		Short: "Print the full folder tree",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)
			var resp map[string]any
			if err := c.doJSON(http.MethodGet, "/v1/folders/tree", nil, &resp); err != nil {
				return err
			}
			if format == "json" {
				return jsonOut(resp)
			}
			printTree(resp, 0)
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

func printTree(n map[string]any, depth int) {
	if n == nil {
		return
	}
	indent := ""
	for i := 0; i < depth; i++ {
		indent += "  "
	}
	name := fmt.Sprint(n["path"])
	if d, ok := n["name"].(string); ok && d != "" {
		name = d
	}
	count := n["file_count"]
	fmt.Printf("%s📁 %s (%v files)\n", indent, name, count)
	children, _ := n["children"].([]any)
	for _, c := range children {
		cm, _ := c.(map[string]any)
		printTree(cm, depth+1)
	}
}

func newFolderRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <folder_id> <new_name>",
		Short: "Rename a folder (cascades to descendant paths)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)
			var resp map[string]any
			if err := c.doJSON(http.MethodPatch, "/v1/folders/"+args[0],
				map[string]any{"name": args[1]}, &resp); err != nil {
				return err
			}
			fmt.Printf("renamed to %v\n", resp["path"])
			return nil
		},
	}
}

func newFolderMoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "move <folder_id> <new_parent_path>",
		Short: "Move a folder to a different parent (cascades to descendants)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)
			var resp map[string]any
			if err := c.doJSON(http.MethodPatch, "/v1/folders/"+args[0],
				map[string]any{"parent_path": args[1]}, &resp); err != nil {
				return err
			}
			fmt.Printf("moved to %v\n", resp["path"])
			return nil
		},
	}
}

func newFolderRmCmd() *cobra.Command {
	var recursive bool
	cmd := &cobra.Command{
		Use:   "rm <folder_id>",
		Short: "Delete a folder (must be empty unless --recursive)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)
			q := url.Values{}
			if recursive {
				q.Set("recursive", "true")
			}
			path := "/v1/folders/" + args[0]
			if enc := q.Encode(); enc != "" {
				path += "?" + enc
			}
			if err := c.doJSON(http.MethodDelete, path, nil, nil); err != nil {
				var ce *cliError
				if errors.As(err, &ce) {
					return ce
				}
				return err
			}
			fmt.Println("deleted")
			return nil
		},
	}
	cmd.Flags().BoolVar(&recursive, "recursive", false, "recursively delete subfolders and files")
	return cmd
}

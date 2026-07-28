package main

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newPutCmd() *cobra.Command {
	var (
		recursive bool
		tag       []string
		name      string
		mimeFlag  string
		format    string
		toFolder  string
	)
	cmd := &cobra.Command{
		Use:   "put <path|-|--url=URL>",
		Short: "Upload a file, directory, stdin, or remote URL",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return newCliError(3, "not logged in", "run `mem auth login` first")
			}
			c := newHTTPClient(cfg)

			urlFlag, _ := cmd.Flags().GetString("url")
			if urlFlag != "" {
				return errors.New("--url ingestion lands in W2; W1 supports local files and stdin only")
			}
			if len(args) == 0 {
				return errors.New("usage: mem put <path> | mem put - | mem put <dir> --recursive")
			}

			target := args[0]
			if target == "-" {
				if name == "" {
					return errors.New("--name required when reading from stdin")
				}
				return uploadStream(c, name, mimeFlag, toFolder, -1, tag, os.Stdin, format)
			}

			st, err := os.Stat(target)
			if err != nil {
				return err
			}
			if st.IsDir() {
				if !recursive {
					return errors.New("path is a directory; pass --recursive to upload its contents")
				}
				return uploadDir(c, target, toFolder, tag, format)
			}
			return uploadFile(c, target, name, mimeFlag, toFolder, tag, format)
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "recurse into directories")
	cmd.Flags().StringArrayVar(&tag, "tag", nil, "tag(s) to attach (repeatable)")
	cmd.Flags().StringVar(&name, "name", "", "override file name (required for stdin)")
	cmd.Flags().StringVar(&mimeFlag, "mime", "", "override MIME type")
	cmd.Flags().StringVar(&toFolder, "to", "/", "destination folder (mkdir -p; e.g. /Photos/2012)")
	cmd.Flags().String("url", "", "remote URL to fetch (W2)")
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

func uploadFile(c *httpClient, path, nameOverride, mimeOverride, targetFolder string, tags []string, format string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	name := nameOverride
	if name == "" {
		name = filepath.Base(path)
	}
	mimeType := mimeOverride
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(name))
	}
	var resp map[string]any
	if err := c.doMultipartUpload("/v1/files", "file", name, mimeType, targetFolder, f, tags, &resp); err != nil {
		return err
	}
	return printPutResp(resp, format)
}

func uploadStream(c *httpClient, name, mimeType, targetFolder string, size int64, tags []string, r io.Reader, format string) error {
	var resp map[string]any
	if err := c.doStreamUpload(name, mimeType, targetFolder, size, tags, r, &resp); err != nil {
		return err
	}
	return printPutResp(resp, format)
}

// uploadDir walks `dir` recursively and uploads every regular file, mapping
// the on-disk hierarchy onto the mem virtual tree under targetFolder.
//
// Local: /home/me/Photos/2012/IMG.jpg, --to /Albums  →
// Remote: /Albums/2012/IMG.jpg (when called with dir=/home/me/Photos).
func uploadDir(c *httpClient, dir, targetFolder string, tags []string, format string) error {
	count := 0
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		abs, _ := filepath.Abs(p)
		rel, _ := filepath.Rel(root, abs)
		subFolder := targetFolder
		if d := filepath.Dir(rel); d != "" && d != "." {
			subFolder = joinFolder(targetFolder, filepath.ToSlash(d))
		}
		fmt.Fprintf(os.Stderr, "  put %s -> %s\n", p, subFolder)
		if e := uploadFile(c, p, "", "", subFolder, tags, "text"); e != nil {
			fmt.Fprintf(os.Stderr, "  ! %s: %v\n", p, e)
		} else {
			count++
		}
		return nil
	})
	fmt.Printf("uploaded %d files from %s\n", count, dir)
	return err
}

// joinFolder concatenates a base folder absolute path with a relative
// sub-path. Both inputs are already in slash form.
func joinFolder(base, sub string) string {
	if base == "" {
		base = "/"
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	base = strings.TrimRight(base, "/")
	sub = strings.Trim(sub, "/")
	if sub == "" {
		if base == "" {
			return "/"
		}
		return base
	}
	if base == "" {
		return "/" + sub
	}
	return base + "/" + sub
}

func printPutResp(resp map[string]any, format string) error {
	if format == "json" {
		return jsonOut(resp)
	}
	f, _ := resp["file"].(map[string]any)
	deduped, _ := resp["deduped"].(bool)
	fmt.Printf("id:     %v\n", f["id"])
	fmt.Printf("name:   %v\n", f["name"])
	fmt.Printf("size:   %v\n", f["size"])
	fmt.Printf("sha256: %v\n", f["sha256"])
	fmt.Printf("mime:   %v\n", f["mime"])
	fmt.Printf("status: %v\n", f["index_status"])
	if deduped {
		fmt.Println("note:   秒传 (content already existed, no re-upload)")
	}
	return nil
}

func newGetCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "get <file_id>",
		Short: "Download a file by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)
			rc, _, err := c.downloadStream(args[0])
			if err != nil {
				return err
			}
			defer rc.Close()
			var sink io.Writer = os.Stdout
			if output != "" && output != "-" {
				f, err := os.Create(output)
				if err != nil {
					return err
				}
				defer f.Close()
				sink = f
			}
			_, err = io.Copy(sink, rc)
			if err == nil && output != "" && output != "-" {
				fmt.Fprintf(os.Stderr, "wrote %s\n", output)
			}
			return err
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "-", "output path (- for stdout)")
	return cmd
}

func newCatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cat <file_id>",
		Short: "Print a file's text contents to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)
			rc, ctype, err := c.downloadStream(args[0])
			if err != nil {
				return err
			}
			defer rc.Close()
			if !isTextLike(ctype) {
				return newCliError(1, "file is not text/* — refusing to cat", "use `mem get "+args[0]+" -o <path>` to download binary")
			}
			_, err = io.Copy(os.Stdout, rc)
			return err
		},
	}
}

func newInfoCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "info <file_id>",
		Short: "Show file metadata + AI fields",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)
			var f map[string]any
			if err := c.doJSON(http.MethodGet, "/v1/files/"+args[0], nil, &f); err != nil {
				return err
			}
			if format == "json" {
				return jsonOut(f)
			}
			for _, k := range []string{"id", "name", "size", "mime", "sha256", "index_status", "summary", "caption", "tags", "timeline_at", "created_at"} {
				if v, ok := f[k]; ok && v != nil {
					fmt.Printf("%-13s %v\n", k+":", v)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

func newLsCmd() *cobra.Command {
	var (
		tag    string
		typ    string
		since  string
		until  string
		limit  int
		page   int
		format string
		prefix string
	)
	cmd := &cobra.Command{
		Use:   "ls [folder_path]",
		Short: "List subfolders + files under a folder (default: root)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveConfig("")
			if err != nil {
				return err
			}
			c := newHTTPClient(cfg)

			// If neither --tag nor positional folder path is given, default to
			// listing the root folder (folders + files mixed). If filters are
			// supplied (--tag, --type, --since, ...) we fall back to the flat
			// file list endpoint.
			folderPath := ""
			if len(args) == 1 {
				folderPath = args[0]
			}
			filterMode := tag != "" || typ != "" || since != "" || until != "" || prefix != ""

			if !filterMode {
				if folderPath == "" {
					folderPath = "/"
				}
				q := url.Values{}
				q.Set("parent", folderPath)
				var resp struct {
					Parent  string           `json:"parent"`
					Folders []map[string]any `json:"folders"`
					Files   []map[string]any `json:"files"`
				}
				if err := c.doJSON(http.MethodGet, "/v1/folders?"+q.Encode(), nil, &resp); err != nil {
					return err
				}
				if format == "json" {
					return jsonOut(resp)
				}
				fmt.Printf("%s\n", resp.Parent)
				for _, d := range resp.Folders {
					fmt.Printf("  📁 %v\n", d["name"])
				}
				fmt.Printf("%-36s  %8s  %-24s  %-10s  %s\n", "ID", "SIZE", "MIME", "STATUS", "NAME")
				for _, f := range resp.Files {
					fmt.Printf("%-36v  %8v  %-24v  %-10v  %v\n",
						f["id"], f["size"], f["mime"], f["index_status"], f["name"])
				}
				return nil
			}

			// Flat-file listing path (filters applied).
			q := url.Values{}
			if tag != "" {
				q.Set("tag", tag)
			}
			if typ != "" {
				q.Set("type", typ)
			}
			if since != "" {
				q.Set("since", since)
			}
			if until != "" {
				q.Set("until", until)
			}
			if prefix != "" {
				q.Set("prefix", prefix)
			} else if folderPath != "" {
				q.Set("path", folderPath)
			}
			if limit > 0 {
				q.Set("limit", fmt.Sprint(limit))
			}
			if page > 0 {
				q.Set("page", fmt.Sprint(page))
			}
			path := "/v1/files"
			if e := q.Encode(); e != "" {
				path += "?" + e
			}
			var resp struct {
				Files []map[string]any `json:"files"`
			}
			if err := c.doJSON(http.MethodGet, path, nil, &resp); err != nil {
				return err
			}
			if format == "json" {
				return jsonOut(resp.Files)
			}
			fmt.Printf("%-36s  %8s  %-24s  %-10s  %s\n", "ID", "SIZE", "MIME", "STATUS", "NAME")
			for _, f := range resp.Files {
				fmt.Printf("%-36v  %8v  %-24v  %-10v  %v\n",
					f["id"], f["size"], f["mime"], f["index_status"], f["name"])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&tag, "tag", "", "filter by tag")
	cmd.Flags().StringVar(&typ, "type", "", "filter by mime prefix (image|text|...)")
	cmd.Flags().StringVar(&since, "since", "", "RFC3339 timestamp")
	cmd.Flags().StringVar(&until, "until", "", "RFC3339 timestamp")
	cmd.Flags().StringVar(&prefix, "prefix", "", "subtree-prefix filter (e.g. /Photos)")
	cmd.Flags().IntVar(&limit, "limit", 50, "page size")
	cmd.Flags().IntVar(&page, "page", 1, "page number")
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print client + server version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("mem CLI dev")
			cfg, _ := resolveConfig("")
			if cfg != nil && cfg.Server != "" {
				c := newHTTPClient(cfg)
				var resp struct {
					Version string `json:"version"`
				}
				if err := c.doJSON(http.MethodGet, "/v1/version", nil, &resp); err == nil {
					fmt.Printf("server: %s (%s)\n", resp.Version, cfg.Server)
				}
			}
			return nil
		},
	}
}

func isTextLike(ctype string) bool {
	ctype = strings.ToLower(ctype)
	if strings.HasPrefix(ctype, "text/") {
		return true
	}
	switch {
	case strings.Contains(ctype, "json"),
		strings.Contains(ctype, "xml"),
		strings.Contains(ctype, "yaml"),
		strings.Contains(ctype, "javascript"):
		return true
	}
	return false
}

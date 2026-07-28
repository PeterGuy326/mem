package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

type timelineEntry struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	MIME    string    `json:"mime"`
	At      time.Time `json:"at"`
	Summary *string   `json:"summary,omitempty"`
	Caption *string   `json:"caption,omitempty"`
}

type timelineBucket struct {
	Month string          `json:"month"`
	Count int             `json:"count"`
	Files []timelineEntry `json:"files"`
}

type timelineResp struct {
	From   time.Time        `json:"from"`
	Until  time.Time        `json:"until"`
	Months []timelineBucket `json:"months"`
}

func newTimelineCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "timeline <year|YYYY-YYYY>",
		Short: "Show files grouped by month for a given year or range (SPEC §F6.3)",
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
			var resp timelineResp
			if err := c.doJSON("GET", "/v1/timeline?year="+args[0], nil, &resp); err != nil {
				return err
			}
			if format == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}
			if len(resp.Months) == 0 {
				fmt.Println("(no files in this range)")
				return nil
			}
			for _, m := range resp.Months {
				fmt.Printf("\n%s  (%d file%s)\n", m.Month, m.Count, pluralS(m.Count))
				for _, e := range m.Files {
					fmt.Printf("  %s  %s  %s\n", e.At.Format("01-02"), e.ID[:8], e.Name)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text|json")
	return cmd
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Command mem is the user-facing CLI. SPEC §7.
//
// Exit codes (SPEC §7.1):
//
//	0 ok · 2 not_found · 3 auth · 4 quota · 5 provider_error
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		var ce *cliError
		if errors.As(err, &ce) {
			fmt.Fprintln(os.Stderr, "error:", ce.msg)
			if ce.hint != "" {
				fmt.Fprintln(os.Stderr, "hint: ", ce.hint)
			}
			os.Exit(ce.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var (
		server string
		format string
	)
	root := &cobra.Command{
		Use:   "mem",
		Short: "mem — Agent-Native AI 网盘 CLI",
		Long:  `mem is the command-line interface for the mem AI drive.`,
	}
	root.PersistentFlags().StringVar(&server, "server", "", "memd base URL (overrides config; e.g. http://localhost:8787)")
	root.PersistentFlags().StringVar(&format, "format", "text", "output format: text|json")

	root.AddCommand(newLoginCmd())
	root.AddCommand(newLogoutCmd())
	root.AddCommand(newTokenCmd())
	root.AddCommand(newPutCmd())
	root.AddCommand(newGetCmd())
	root.AddCommand(newCatCmd())
	root.AddCommand(newInfoCmd())
	root.AddCommand(newLsCmd())
	root.AddCommand(newMkdirCmd())
	root.AddCommand(newMvCmd())
	root.AddCommand(newRenameCmd())
	root.AddCommand(newFolderCmd())
	root.AddCommand(newSearchCmd())
	root.AddCommand(newAskCmd())
	root.AddCommand(newRelatedCmd())
	root.AddCommand(newFaceCmd())
	root.AddCommand(newProviderCmd())
	root.AddCommand(newTimelineCmd())
	root.AddCommand(newVersionCmd())
	return root
}

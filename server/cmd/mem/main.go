// Command mem is the user-facing CLI. SPEC §7.
//
// Exit codes (SPEC §7.1):
//
//	0 ok · 2 not_found · 3 auth · 4 plan/quota · 5 provider/timeout
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
)

var (
	cliServerOverride    string
	cliWorkspaceOverride string
)

func main() {
	root := newRootCmd()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
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
	var format string
	root := &cobra.Command{
		Use:   "mem",
		Short: "mem — Agent-Native AI 网盘 CLI",
		Long:  `mem is the command-line interface for the mem AI drive.`,
	}
	root.PersistentFlags().StringVar(&cliServerOverride, "server", "", "memd base URL (overrides config; e.g. http://localhost:8787)")
	root.PersistentFlags().StringVar(&cliWorkspaceOverride, "workspace", "", "memory workspace UUID (overrides config)")
	root.PersistentFlags().StringVar(&format, "format", "text", "output format: text|json")

	root.AddCommand(newAuthCmd())
	root.AddCommand(newLegacyAuthCommand(newLoginCmd(), "mem auth login"))
	root.AddCommand(newLegacyAuthCommand(newLogoutCmd(), "mem auth logout"))
	root.AddCommand(newLegacyTokenCmd())
	root.AddCommand(newRememberCmd())
	root.AddCommand(newMemoryCmd())
	root.AddCommand(newMemoriesCmd())
	root.AddCommand(newFeedbackCmd())
	root.AddCommand(newArchiveCmd())
	root.AddCommand(newRestoreCmd())
	root.AddCommand(newForgetCmd())
	root.AddCommand(newCheckpointCmd())
	root.AddCommand(newTasksCmd())
	root.AddCommand(newCheckpointsCmd())
	root.AddCommand(newResumeCmd())
	root.AddCommand(newPutCmd())
	root.AddCommand(newGetCmd())
	root.AddCommand(newCatCmd())
	root.AddCommand(newInfoCmd())
	root.AddCommand(newAnnotationCmd())
	root.AddCommand(newLsCmd())
	root.AddCommand(newMkdirCmd())
	root.AddCommand(newMvCmd())
	root.AddCommand(newRenameCmd())
	root.AddCommand(newFolderCmd())
	root.AddCommand(newSearchCmd())
	root.AddCommand(newContextCmd())
	root.AddCommand(newRelatedCmd())
	root.AddCommand(newFaceCmd())
	root.AddCommand(newProviderCmd())
	root.AddCommand(newModelCmd())
	root.AddCommand(newTimelineCmd())
	root.AddCommand(newWorkspaceCmd())
	root.AddCommand(newVersionCmd())
	return root
}

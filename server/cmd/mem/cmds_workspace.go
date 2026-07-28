package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newWorkspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Export or import a portable Agent workspace",
	}
	cmd.AddCommand(newWorkspaceExportCmd())
	cmd.AddCommand(newWorkspaceImportCmd())
	return cmd
}

func newWorkspaceExportCmd() *cobra.Command {
	var (
		output string
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the current workspace as a .membundle",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			outputPath, err := prepareWorkspaceExportTarget(output, force)
			if err != nil {
				return err
			}
			client, err := configuredMemoryClient()
			if err != nil {
				return err
			}
			download, err := client.ExportWorkspace(cmd.Context())
			if err != nil {
				return fromAPIError(err)
			}
			defer download.Body.Close()

			written, err := writeWorkspaceExportAtomically(
				outputPath,
				force,
				download.ContentLength,
				download.Body,
			)
			if err != nil {
				return err
			}
			result := struct {
				Output      string `json:"output"`
				Bytes       int64  `json:"bytes"`
				ContentType string `json:"content_type"`
			}{
				Output:      outputPath,
				Bytes:       written,
				ContentType: download.ContentType,
			}
			if format == "json" {
				return writeCommandJSON(cmd, result)
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"exported workspace bundle: %s (%d bytes)\n",
				outputPath,
				written,
			)
			return nil
		},
	}
	cmd.Flags().StringVarP(
		&output,
		"output",
		"o",
		"",
		"destination .membundle file",
	)
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing output file")
	_ = cmd.MarkFlagRequired("output")
	return cmd
}

func newWorkspaceImportCmd() *cobra.Command {
	var (
		input string
		mode  string
		yes   bool
	)
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a .membundle into the current workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			if mode != apiclient.WorkspaceRestoreModeFresh {
				return fmt.Errorf(
					"--mode must be %q; merge restore is not implemented",
					apiclient.WorkspaceRestoreModeFresh,
				)
			}
			if input == "-" {
				return fmt.Errorf("--input must name a regular .membundle file")
			}
			archive, err := os.Open(filepath.Clean(input))
			if err != nil {
				return fmt.Errorf("open workspace bundle: %w", err)
			}
			defer archive.Close()
			info, err := archive.Stat()
			if err != nil {
				return fmt.Errorf("stat workspace bundle: %w", err)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("--input must name a regular .membundle file")
			}
			if info.Size() == 0 {
				return fmt.Errorf("workspace bundle is empty")
			}
			proceed, err := confirmWorkspaceImport(cmd, input, yes)
			if err != nil {
				return err
			}
			if !proceed {
				fmt.Fprintln(cmd.ErrOrStderr(), "workspace import cancelled")
				return nil
			}

			client, err := configuredMemoryClient()
			if err != nil {
				return err
			}
			result, err := client.ImportWorkspace(
				cmd.Context(),
				mode,
				info.Size(),
				archive,
			)
			if err != nil {
				return writeWorkspaceImportError(cmd, format, err)
			}
			if format == "json" {
				return writeCommandJSON(cmd, result)
			}
			return printWorkspaceImportResult(cmd, result)
		},
	}
	cmd.Flags().StringVarP(&input, "input", "i", "", "source .membundle file")
	cmd.Flags().StringVar(
		&mode,
		"mode",
		apiclient.WorkspaceRestoreModeFresh,
		"restore mode (fresh only)",
	)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm workspace import")
	_ = cmd.MarkFlagRequired("input")
	return cmd
}

func prepareWorkspaceExportTarget(raw string, force bool) (string, error) {
	output, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		return "", fmt.Errorf("resolve output path: %w", err)
	}
	info, err := os.Lstat(output)
	switch {
	case err == nil && info.IsDir():
		return "", fmt.Errorf("output path is a directory: %s", output)
	case err == nil && !force:
		return "", fmt.Errorf("output file already exists; use --force to replace it: %s", output)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("inspect output path: %w", err)
	}
	parent, err := os.Stat(filepath.Dir(output))
	if err != nil {
		return "", fmt.Errorf("inspect output directory: %w", err)
	}
	if !parent.IsDir() {
		return "", fmt.Errorf("output parent is not a directory: %s", filepath.Dir(output))
	}
	return output, nil
}

func writeWorkspaceExportAtomically(
	output string,
	force bool,
	expectedSize int64,
	body io.Reader,
) (written int64, resultErr error) {
	directory := filepath.Dir(output)
	base := filepath.Base(output)
	temporary, err := os.CreateTemp(directory, "."+base+".*.tmp")
	if err != nil {
		return 0, fmt.Errorf("create export temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	published := false
	defer func() {
		_ = temporary.Close()
		if !published {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return 0, fmt.Errorf("secure export temporary file: %w", err)
	}
	written, err = io.Copy(temporary, body)
	if err != nil {
		return written, fmt.Errorf("download workspace bundle: %w", err)
	}
	if expectedSize >= 0 && written != expectedSize {
		return written, fmt.Errorf(
			"workspace bundle download size mismatch: got %d bytes, expected %d",
			written,
			expectedSize,
		)
	}
	if err := temporary.Sync(); err != nil {
		return written, fmt.Errorf("sync workspace bundle: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return written, fmt.Errorf("close workspace bundle: %w", err)
	}

	if force {
		if err := os.Rename(temporaryPath, output); err != nil {
			return written, fmt.Errorf("publish workspace bundle: %w", err)
		}
		published = true
		return written, nil
	}
	// A hard link atomically publishes without the overwrite race inherent in
	// a separate existence check followed by os.Rename.
	if err := os.Link(temporaryPath, output); err != nil {
		if errors.Is(err, os.ErrExist) {
			return written, fmt.Errorf(
				"output file already exists; use --force to replace it: %s",
				output,
			)
		}
		return written, fmt.Errorf("publish workspace bundle: %w", err)
	}
	published = true
	if err := os.Remove(temporaryPath); err != nil {
		return written, fmt.Errorf(
			"workspace bundle was published but temporary file cleanup failed: %w",
			err,
		)
	}
	return written, nil
}

func confirmWorkspaceImport(
	cmd *cobra.Command,
	input string,
	yes bool,
) (bool, error) {
	if yes {
		return true, nil
	}
	in := cmd.InOrStdin()
	terminalInput, ok := in.(interface{ Fd() uintptr })
	if !ok || !term.IsTerminal(int(terminalInput.Fd())) {
		return false, fmt.Errorf(
			"--yes is required when stdin is not a TTY",
		)
	}
	fmt.Fprintf(
		cmd.ErrOrStderr(),
		"Import %s into the current workspace in fresh mode? [y/N] ",
		input,
	)
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func printWorkspaceImportResult(
	cmd *cobra.Command,
	result *apiclient.WorkspaceImportResult,
) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintf(writer, "bundle_id\t%s\n", result.BundleID)
	fmt.Fprintf(writer, "source_workspace_id\t%s\n", result.SourceWorkspaceID)
	fmt.Fprintf(writer, "archive_sha256\t%s\n", result.ArchiveSHA256)
	fmt.Fprintf(writer, "replayed\t%t\n", result.Replayed)
	fmt.Fprintf(writer, "folders\t%d\n", result.Counts.Folders)
	fmt.Fprintf(writer, "files\t%d\n", result.Counts.Files)
	fmt.Fprintf(writer, "memories\t%d\n", result.Counts.Memories)
	fmt.Fprintf(writer, "memory_events\t%d\n", result.Counts.MemoryEvents)
	fmt.Fprintf(writer, "tasks\t%d\n", result.Counts.Tasks)
	fmt.Fprintf(writer, "checkpoints\t%d\n", result.Counts.Checkpoints)
	fmt.Fprintf(writer, "checkpoint_refs\t%d\n", result.Counts.CheckpointRefs)
	fmt.Fprintf(writer, "checkpoint_payloads\t%d\n", result.Counts.CheckpointPayloads)
	fmt.Fprintf(writer, "blobs\t%d\n", result.Counts.Blobs)
	fmt.Fprintf(writer, "blob_bytes\t%d\n", result.Counts.BlobBytes)
	return writer.Flush()
}

func writeWorkspaceImportError(
	cmd *cobra.Command,
	format string,
	err error,
) error {
	var apiError *apiclient.APIError
	if !errors.As(err, &apiError) ||
		apiError.Code != "workspace_import_conflict" ||
		len(apiError.Conflicts) == 0 {
		return fromAPIError(err)
	}
	// A structured conflict response must remain valid JSON/text without Cobra
	// appending command usage to the same output stream.
	cmd.Root().SilenceUsage = true
	if format == "json" {
		if encodeErr := writeCommandJSON(cmd, struct {
			Error     string                              `json:"error"`
			Hint      string                              `json:"hint,omitempty"`
			Conflicts []apiclient.WorkspaceImportConflict `json:"conflicts"`
			Total     int                                 `json:"total,omitempty"`
			Truncated bool                                `json:"truncated,omitempty"`
		}{
			Error:     apiError.Code,
			Hint:      apiError.Hint,
			Conflicts: apiError.Conflicts,
			Total:     apiError.ConflictTotal,
			Truncated: apiError.ConflictsTruncated,
		}); encodeErr != nil {
			return encodeErr
		}
	} else {
		writer := tabwriter.NewWriter(cmd.ErrOrStderr(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "workspace import conflicts:")
		fmt.Fprintln(writer, "KIND\tRESOURCE\tVALUE")
		for _, conflict := range apiError.Conflicts {
			fmt.Fprintf(
				writer,
				"%s\t%s\t%s\n",
				conflict.Kind,
				conflict.Resource,
				conflict.Value,
			)
		}
		if flushErr := writer.Flush(); flushErr != nil {
			return flushErr
		}
		if apiError.ConflictsTruncated {
			total := apiError.ConflictTotal
			if total < len(apiError.Conflicts) {
				total = len(apiError.Conflicts)
			}
			fmt.Fprintf(
				cmd.ErrOrStderr(),
				"more conflicts omitted (at least %d total)\n",
				total,
			)
		}
	}
	return fromAPIError(err)
}

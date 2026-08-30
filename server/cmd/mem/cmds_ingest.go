package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/spf13/cobra"
)

// newIngestCmd plumbs local CLI-session artifacts (Qoder transcripts) into mem
// as first-class input. It reads ~/.qoder/projects/**/*.jsonl, normalizes each
// conversation turn to a memory observation, and writes through the same
// /v1/memories API the `mem remember` command uses — so ingested records are
// immediately recallable across the API/MCP/CLI/UI surfaces.
func newIngestCmd() *cobra.Command {
	var (
		root     string
		pathRoot string
		stateDir string
		dryRun   bool
		limit    int
	)

	cmd := &cobra.Command{
		Use:     "ingest",
		Short:   "Ingest local AI-agent transcript stores into mem",
		Aliases: []string{"transcript"},
		Args:    cobra.NoArgs,
	}

	qoder := &cobra.Command{
		Use:   "qoder",
		Short: "Ingest Qoder CLI session transcripts (*.jsonl) into mem",
		Long: `Read AI-agent conversation transcripts from a Qoder/CLI session store
(by default ~/.qoder/projects/**/*.jsonl) and write each message into mem as a
memory observation with Qoder source flags and per-file incremental checkpoints.

Messages are written through the standard /v1/memories API, so ingested records
are recallable from the API, MCP, CLI, and Web UI. Re-runs are idempotent: only
lines appended since the last run are posted, and each line carries a stable
Idempotency-Key derived from the file path and line number.

Example:
  mem ingest qoder                              # default ~/.qoder/projects
  mem ingest qoder --root ~/sessions --dry-run  # preview, no writes
  mem ingest qoder --limit 200                  # stop after 200 memories`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIngestQoder(cmd, ingestOptions{
				root:     root,
				pathRoot: pathRoot,
				stateDir: stateDir,
				dryRun:   dryRun,
				limit:    limit,
			})
		},
	}
	qoder.Flags().StringVar(&root, "root", "", "glob base of session transcripts (default ~/.qoder/projects)")
	qoder.Flags().StringVar(&pathRoot, "path-root", "/AgentTranscripts", "virtual path prefix for ingested memories")
	qoder.Flags().StringVar(&stateDir, "state-dir", "", "ingest checkpoint directory (default ~/.mem/ingest/qoder)")
	qoder.Flags().BoolVar(&dryRun, "dry-run", false, "parse and plan only; do not write any memories")
	qoder.Flags().IntVar(&limit, "limit", 0, "stop after this many memories (0 = no limit)")

	cmd.AddCommand(qoder)
	return cmd
}

type ingestOptions struct {
	root     string
	pathRoot string
	stateDir string
	dryRun   bool
	limit    int
}

func (o ingestOptions) baseDir() string {
	if o.root != "" {
		return o.root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".qoder", "projects")
}

func (o ingestOptions) checkpointDir() string {
	if o.stateDir != "" {
		return o.stateDir
	}
	return filepath.Join(cliStateRoot(), "ingest", "qoder")
}

// cliStateRoot returns $HOME/.mem (overridable via MEM_STATE_DIR for tests) —
// the same place the CLI persists its config and cursors.
func cliStateRoot() string {
	if v := os.Getenv("MEM_STATE_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".mem"
	}
	return filepath.Join(home, ".mem")
}

// expandTranscriptGlob recursively collects *.jsonl transcripts under base and
// sorts the results, so ingestion order is deterministic across runs and
// checkpoints are stable. (Go's filepath.Glob does not treat ** as recursive, so
// we walk the tree explicitly.)
func expandTranscriptGlob(base string) ([]string, error) {
	// A bare base that names a single existing file (not a directory) is
	// accepted as a one-off transcript.
	if fi, e := os.Stat(base); e == nil && !fi.IsDir() {
		return []string{base}, nil
	}
	var paths []string
	err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries rather than failing the walk
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".jsonl") {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, newCliError(1, fmt.Sprintf("walk %s: %v", base, err), "")
	}
	sort.Strings(paths)
	return paths, nil
}

func runIngestQoder(cmd *cobra.Command, o ingestOptions) error {
	base := o.baseDir()
	if base == "" {
		return newCliError(1, "cannot determine session store root", "set --root or $HOME")
	}
	paths, err := expandTranscriptGlob(base)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "no transcripts matched %s/**/*.jsonl\n", base)
		return nil
	}

	cfg, err := resolveConfig("")
	if err != nil {
		return err
	}
	if !o.dryRun && cfg.Token == "" {
		return newCliError(3, "not logged in", "run `mem auth login` first")
	}
	client := newHTTPClient(cfg)
	stateDir := o.checkpointDir()

	var (
		files       = 0
		memories    int // written this run
		replayed    int // server-reported idempotent replays
		unparseable int // parsed lines yielded no ingestible text
		remaining   = o.limit
	)

	for _, abs := range paths {
		files++
		cp := loadQoderCheckpoint(stateDir, abs)
		turns, skipped, perr := parseQoderTranscript(abs, cp.LastLine)
		if perr != nil {
			return perr
		}
		unparseable += skipped
		project, session := splitTranscriptPath(base, abs)

		newLast := cp.LastLine
		for _, turn := range turns {
			if o.limit > 0 && remaining <= 0 {
				break
			}
			// parseQoderTranscript already skips <= cp.LastLine, so this
			// guard is a belt-and-suspenders check against anomalies.
			if turn.Line <= newLast {
				continue
			}

			key := ingestIdempotencyKey(abs, turn.Line)
			body := ingestMemoryBody(o, abs, project, session, turn)

			if o.dryRun {
				memories++
				if remaining > 0 {
					remaining--
				}
				continue
			}

			var resp map[string]any
			// Use the raw client to detect 409 (Idempotency-Key conflict)
			// without wrapping into cliError, which would lose the kind.
			err := client.api.DoJSONWithHeaders(
				context.Background(),
				http.MethodPost,
				"/v1/memories",
				body,
				&resp,
				map[string]string{"Idempotency-Key": key},
			)
			if err != nil {
				// 409 Idempotency-Key conflict: the file was rewritten with
				// different content at the same line — skip the remainder of
				// this file and continue with others rather than aborting the
				// whole run. The checkpoint is NOT advanced for this file, so
				// the operator can investigate and retry.
				var ae *apiclient.APIError
				if errors.As(err, &ae) && ae.Kind() == apiclient.KindConflict {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"warn: %s line %d: idempotency conflict (file rewritten?); skipping remaining lines in %s\n",
						abs, turn.Line, filepath.Base(abs))
					break
				}
				return fromAPIError(err)
			}
			if r, _ := resp["replayed"].(bool); r {
				replayed++
			} else {
				memories++
			}
			if remaining > 0 {
				remaining--
			}
			newLast = turn.Line
		}

		if !o.dryRun {
			if size, mtime, serr := fileState(abs); serr == nil {
				if err := saveQoderCheckpoint(stateDir, qoderCheckpoint{
					Abs:      abs,
					Size:     size,
					ModTime:  mtime,
					LastLine: newLast,
				}); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warn: save checkpoint for %s: %v\n", abs, err)
				}
			}
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"qoder ingest: %d file(s), %d memory written, %d server-replay, %d unparseable line%s\n",
		files, memories, replayed, unparseable,
		ingestModeNote(o.dryRun))
	return nil
}

func ingestModeNote(dryRun bool) string {
	if dryRun {
		return " (dry-run: no writes)"
	}
	return ""
}

// ingestMemoryBody builds the /v1/memories request body for one turn. The shape
// mirrors `mem remember` (see cmds_remember.go): the Idempotency-Key travels in
// the header, not the body; provenance, paths, and source flags line up with
// the existing record model.
func ingestMemoryBody(o ingestOptions, abs, project, session string, turn qoderTurn) map[string]any {
	source := map[string]any{
		"type": "qoder",
		"ref":  abs,
		"locator": map[string]any{
			"line": turn.Line,
		},
	}
	producer := map[string]any{"session_id": session}
	if turn.AgentID != "" {
		producer["agent_id"] = turn.AgentID
	}

	body := map[string]any{
		"kind":     "observation",
		"content":  turn.Content,
		"path":     virtualMemoryPath(o.pathRoot, project, session),
		"source":   source,
		"producer": producer,
	}
	if turn.EventAt != nil {
		body["event_at"] = turn.EventAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return body
}

// virtualMemoryPath scopes ingested memories under a stable virtual path so
// they are grouped per session and collision-safe across projects:
//
//	<pathRoot>/<project>/<session>
func virtualMemoryPath(pathRoot, project, session string) string {
	if pathRoot == "" {
		pathRoot = "/AgentTranscripts"
	}
	parts := []string{strings.Trim(pathRoot, "/"), cleanPathPart(project)}
	if session != "" {
		parts = append(parts, cleanPathPart(session))
	}
	return "/" + strings.Join(parts, "/")
}

// cleanPathPart sanitizes a path component so it cannot escape or collide with
// other memory paths, replacing unsafe characters with dashes and collapsing
// empty results to a readable slug.
func cleanPathPart(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "-")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-./")
	if out == "" {
		out = "session"
	}
	return out
}

// ingestIdempotencyKey is a stable, content-independent retry key per line:
// re-posting the same (file, line) is an idempotent replay, so re-runs never
// duplicate a memory even if the checkpoint is lost.
func ingestIdempotencyKey(abs string, line int) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s:%d", abs, line)))
	return "qoder:" + hex.EncodeToString(sum[:])[:24]
}

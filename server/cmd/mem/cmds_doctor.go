package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/spf13/cobra"
)

// mem doctor is a read-only diagnosis surface (issue #112). It exists because a
// first-time user's most common failure is a missing prerequisite they cannot
// name: nothing listening at the configured URL, no credential, no workspace.
// Before this command the only signal was a per-command error.
//
// Contract, in the order the checks run:
//
//	server_reachability  GET /healthz with no credential
//	credential           is a token configured at all (no request)
//	workspace            GET /v1/capabilities
//	version_skew         GET /v1/version
//
// REQ-003 keeps this strictly diagnostic: every request is a GET, nothing is
// created, no dependency is installed, no Docker or compose command is issued,
// and no secret value or DSN is ever printed (the configured URL is reported
// with any userinfo removed).

// doctorContract and doctorSchemaVersion follow the repo convention of naming a
// machine-readable surface and versioning it, mirroring docs/schemas.
const (
	doctorContract      = "mem.doctor"
	doctorSchemaVersion = 1
)

// Statuses are a closed set. "skipped" is explicit: a check that could not run
// because an earlier one failed says so, instead of reporting an OK it did not
// earn or a failure it did not observe.
const (
	doctorOK      = "ok"
	doctorWarn    = "warn"
	doctorFail    = "fail"
	doctorSkipped = "skipped"
)

// exit codes, per SPEC §7.1: 0 ok · 2 not_found · 3 auth · 4 plan/quota ·
// 5 provider/timeout.
const (
	exitOK        = 0
	exitNotFound  = 2
	exitAuth      = 3
	exitPlanQuota = 4
	exitProvider  = 5
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// ExitCode is this finding's contribution to the process exit status.
	// Advisory findings contribute 0.
	ExitCode int    `json:"exit_code"`
	Detail   string `json:"detail"`
	Hint     string `json:"hint,omitempty"`
}

type doctorReport struct {
	Contract      string        `json:"contract"`
	SchemaVersion int           `json:"schema_version"`
	Server        string        `json:"server"`
	CLIVersion    string        `json:"cli_version"`
	ServerVersion string        `json:"server_version,omitempty"`
	ExitCode      int           `json:"exit_code"`
	Checks        []doctorCheck `json:"checks"`
}

func newDoctorCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose local configuration and server connectivity (read-only)",
		Long: `Report why the CLI cannot talk to a working mem server.

doctor issues only GET requests and writes nothing: no token, no file, no
container and no configuration. It checks, in order, reachability of the
configured server URL, whether a credential exists, which workspace the server
resolved for that credential, and CLI/server version skew. Each finding carries
the SPEC §7.1 exit code it contributes (0 ok · 2 not_found · 3 auth ·
4 plan/quota · 5 provider/timeout); the process exits with the first failing
check's code.

A check that an earlier failure made impossible is reported as "skipped" rather
than guessed.

Example:
  mem doctor
  mem doctor --format json
  mem doctor --server http://localhost:8787 --timeout 2s`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := rememberOutputFormat(cmd)
			if err != nil {
				return err
			}
			report, err := runDoctor(cmd, timeout)
			if err != nil {
				return err
			}
			if format == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(report); err != nil {
					return err
				}
			} else {
				printDoctorReport(cmd, report)
			}
			if report.ExitCode != exitOK {
				f := report.firstFailed()
				return newCliError(report.ExitCode, "doctor: "+f.Name+" failed", f.Detail)
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "per-request budget for the read-only probes")
	return cmd
}

func (r doctorReport) firstFailed() doctorCheck {
	for _, c := range r.Checks {
		if c.Status == doctorFail {
			return c
		}
	}
	return doctorCheck{Name: "doctor", Detail: "a check failed"}
}

func runDoctor(cmd *cobra.Command, timeout time.Duration) (doctorReport, error) {
	if timeout <= 0 {
		return doctorReport{}, newCliError(1, "--timeout must be positive", "")
	}
	cfg, err := resolveConfig("")
	if err != nil {
		return doctorReport{}, err
	}
	report := doctorReport{
		Contract:      doctorContract,
		SchemaVersion: doctorSchemaVersion,
		Server:        redactURL(cfg.Server),
		CLIVersion:    cliVersion,
	}

	reachCtx, cancel := context.WithTimeout(cmd.Context(), timeout)
	reach := probeReachability(reachCtx, cfg.Server)
	cancel()
	report.Checks = append(report.Checks, reach)

	cred := probeCredential(cfg)
	report.Checks = append(report.Checks, cred)

	// The remaining checks need a live, authenticated connection. Reporting a
	// fabricated result for them would be the exact failure mode this command
	// exists to remove.
	var ws, ver doctorCheck
	switch {
	case reach.Status == doctorFail:
		ws, ver = skippedCheck("workspace", reach.Name), skippedCheck("version_skew", reach.Name)
	case cred.Status == doctorFail:
		ws, ver = skippedCheck("workspace", cred.Name), skippedCheck("version_skew", cred.Name)
	default:
		wsCtx, wsCancel := context.WithTimeout(cmd.Context(), timeout)
		ws = probeWorkspace(wsCtx, cfg)
		wsCancel()

		verCtx, verCancel := context.WithTimeout(cmd.Context(), timeout)
		ver = probeVersion(verCtx, cfg.Server, &report)
		verCancel()
	}
	report.Checks = append(report.Checks, ws, ver)

	for _, c := range report.Checks {
		if c.Status == doctorFail {
			report.ExitCode = c.ExitCode
			break
		}
	}
	return report, nil
}

// skippedCheck records a check that an earlier failure made impossible, and
// names the blocker so the text report stays actionable without the JSON.
func skippedCheck(name, blockedBy string) doctorCheck {
	return doctorCheck{
		Name:   name,
		Status: doctorSkipped,
		Detail: "not evaluated: " + blockedBy + " is failing",
	}
}

func probeReachability(ctx context.Context, server string) doctorCheck {
	check := doctorCheck{Name: "server_reachability"}
	// An unauthenticated probe: a 401 here would otherwise be read as "the
	// server is down" by a user whose only problem is a bad token.
	c := apiclient.New(server, "")
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/healthz", nil, &resp); err != nil {
		check.Status, check.ExitCode, check.Detail, check.Hint = classifyProbe(err)
		return check
	}
	if !resp.OK {
		check.Status = doctorFail
		check.ExitCode = exitProvider
		check.Detail = "healthz answered without ok:true"
		check.Hint = deployPathHint()
		return check
	}
	check.Status = doctorOK
	check.Detail = "healthz ok at " + redactURL(server)
	return check
}

func probeCredential(cfg *cliConfig) doctorCheck {
	check := doctorCheck{Name: "credential"}
	if cfg.Token == "" {
		check.Status = doctorFail
		check.ExitCode = exitAuth
		check.Detail = "no token configured"
		check.Hint = notLoggedInHint
		if !configFileExists() {
			check.Hint += "; " + firstRunDeployHint
		}
		return check
	}
	// The value never leaves this function: only its origin is reported.
	check.Status = doctorOK
	check.Detail = "token present (from " + credentialSource() + ")"
	return check
}

// credentialSource names where the token came from without printing it.
func credentialSource() string {
	if strings.TrimSpace(os.Getenv("MEM_TOKEN")) != "" {
		return "$MEM_TOKEN"
	}
	return "config file"
}

func probeWorkspace(ctx context.Context, cfg *cliConfig) doctorCheck {
	check := doctorCheck{Name: "workspace"}
	c := apiclient.New(cfg.Server, cfg.Token).WithWorkspace(cfg.Workspace)
	var resp struct {
		Workspace struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"workspace"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/v1/capabilities", nil, &resp); err != nil {
		check.Status, check.ExitCode, check.Detail, check.Hint = classifyProbe(err)
		return check
	}
	if resp.Workspace.ID == "" {
		check.Status = doctorFail
		check.ExitCode = exitNotFound
		check.Detail = "server resolved no workspace for this credential"
		check.Hint = "select one with `mem auth login` or --workspace <uuid>"
		return check
	}
	check.Status = doctorOK
	if cfg.Workspace == "" {
		check.Detail = fmt.Sprintf(
			"server-resolved workspace %s (%s), role %s; none configured locally, using the server default",
			resp.Workspace.Name, resp.Workspace.ID, resp.Workspace.Role,
		)
		return check
	}
	check.Detail = fmt.Sprintf("workspace %s (%s), role %s", resp.Workspace.Name, resp.Workspace.ID, resp.Workspace.Role)
	return check
}

func probeVersion(ctx context.Context, server string, report *doctorReport) doctorCheck {
	check := doctorCheck{Name: "version_skew"}
	c := apiclient.New(server, "")
	var resp struct {
		Version string `json:"version"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/v1/version", nil, &resp); err != nil {
		check.Status, check.ExitCode, check.Detail, check.Hint = classifyProbe(err)
		return check
	}
	report.ServerVersion = resp.Version
	switch {
	case resp.Version == "":
		check.Status = doctorWarn
		check.Detail = "server reported no version"
	case cliVersion == "" || cliVersion == devCLIVersion:
		// Honest limit, not a pass: release builds do not inject a CLI version
		// yet, so there is nothing to compare against.
		check.Status = doctorWarn
		check.Detail = fmt.Sprintf(
			"skew not computable: this CLI build reports %q (no version injected at build time); server reports %s",
			cliVersion, resp.Version,
		)
		check.Hint = "compare `mem version` against the release notes for the images you deployed"
	case resp.Version == cliVersion:
		check.Status = doctorOK
		check.Detail = "CLI and server both report " + cliVersion
	default:
		check.Status = doctorWarn
		check.Detail = fmt.Sprintf("CLI reports %s, server reports %s", cliVersion, resp.Version)
		check.Hint = "upgrade the CLI or redeploy the server images so the two agree"
	}
	return check
}

// classifyProbe turns a probe failure into the finding fields. The classification
// is shared with no other surface on purpose: ingest has a failure-code
// vocabulary for cycles, while this one maps to SPEC §7.1 process exit codes.
func classifyProbe(err error) (status string, code int, detail, hint string) {
	var ae *apiclient.APIError
	if errors.As(err, &ae) {
		switch ae.Kind() {
		case apiclient.KindAuth:
			return doctorFail, exitAuth, fmt.Sprintf("server rejected the request (HTTP %d)", ae.StatusCode), notLoggedInHint
		case apiclient.KindNotFound:
			return doctorFail, exitNotFound, fmt.Sprintf("no mem server at this URL (HTTP %d)", ae.StatusCode), deployPathHint()
		case apiclient.KindPlan, apiclient.KindQuota:
			return doctorFail, exitPlanQuota, fmt.Sprintf("server refused for plan or quota (HTTP %d)", ae.StatusCode), ""
		}
		return doctorFail, exitProvider, fmt.Sprintf("server error (HTTP %d): %s", ae.StatusCode, ae.Message), deployPathHint()
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return doctorFail, exitProvider, "probe timed out", "raise --timeout, or check that the server is not behind a stalled proxy"
	}
	return doctorFail, exitProvider, "cannot reach the configured server: " + sanitizeProbeError(err), deployPathHint()
}

// deployPathHint points at the container path the docs recommend, instead of a
// host-specific dependency recipe.
func deployPathHint() string {
	return "start the documented container path: deploy/compose, see docs/DEPLOYMENT.md"
}

// redactURL strips userinfo so a URL that carries credentials cannot be echoed
// into a report that an operator will paste into an issue. The marker uses only
// unreserved characters, because url.User("***") would percent-encode it.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err == nil && u.User != nil {
		u.User = url.User("REDACTED")
		return u.String()
	}

	// If url.Parse fails, malformed credential URLs can still slip through
	// unchanged unless we fall back to a simple schema/userinfo splitter.
	if err != nil {
		if redacted := redactMalformedURL(raw); redacted != raw {
			return redacted
		}
		return raw
	}
	return raw
}

func redactMalformedURL(raw string) string {
	sep := "://"
	if i := strings.Index(raw, sep); i >= 0 {
		prefix := raw[:i+len(sep)]
		rest := raw[i+len(sep):]
		at := strings.Index(rest, "@")
		if at > 0 {
			return prefix + "REDACTED@" + rest[at+1:]
		}
	}
	return raw
}

func sanitizeProbeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	const quoted = "\""
	for start := 0; ; {
		left := strings.Index(msg[start:], quoted)
		if left < 0 {
			return msg
		}
		left += start
		right := strings.Index(msg[left+1:], quoted)
		if right < 0 {
			return msg
		}
		right += left + 1
		token := msg[left+1 : right]
		if strings.Contains(token, "://") {
			if redacted := redactURL(token); redacted != token {
				msg = msg[:left+1] + redacted + msg[right:]
			}
		}
		start = right + 1
	}
}

func printDoctorReport(cmd *cobra.Command, r doctorReport) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "mem doctor (%s v%d)\n", r.Contract, r.SchemaVersion)
	fmt.Fprintf(out, "server: %s\n", r.Server)
	for _, c := range r.Checks {
		fmt.Fprintf(out, "%-20s %-8s %s\n", c.Name, c.Status, c.Detail)
		if c.Hint != "" {
			fmt.Fprintf(out, "%-20s          hint: %s\n", "", c.Hint)
		}
	}
}

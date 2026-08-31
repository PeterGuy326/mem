package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/google/uuid"
)

// cliError carries the exit-code mapping from SPEC §7.1.
type cliError struct {
	code int
	msg  string
	hint string
}

func (e *cliError) Error() string { return e.msg }

func newCliError(code int, msg, hint string) *cliError {
	return &cliError{code: code, msg: msg, hint: hint}
}

// notLoggedInHint is the credential guidance that always applies.
const notLoggedInHint = "run `mem auth login` first"

// firstRunDeployHint names the documented deployment path rather than a
// host-specific install recipe, so a machine that has never been configured is
// not sent off to build the bare-metal stack by hand.
const firstRunDeployHint = "no server configured yet — the documented path is deploy/compose, see docs/DEPLOYMENT.md"

// errNotLoggedIn is the one fail-closed auth error for commands that need a
// credential. When no config file exists at all, the run is a first run: the
// hint additionally names the documented deployment path, because telling
// somebody to log in against a server that does not exist yet is not guidance.
func errNotLoggedIn() error {
	if configFileExists() {
		return newCliError(3, "not logged in", notLoggedInHint)
	}
	return newCliError(3, "not logged in", notLoggedInHint+"; "+firstRunDeployHint)
}

// fromAPIError maps an *apiclient.APIError to a *cliError with the SPEC §7.1
// exit code. Any other error is returned unchanged.
func fromAPIError(err error) error {
	var ae *apiclient.APIError
	if !errors.As(err, &ae) {
		return err
	}
	code := 1
	switch ae.Kind() {
	case apiclient.KindAuth:
		code = 3
	case apiclient.KindNotFound:
		code = 2
	case apiclient.KindPlan, apiclient.KindQuota:
		code = 4
	case apiclient.KindProvider, apiclient.KindTimeout:
		code = 5
	}
	return newCliError(code, fmt.Sprintf("%s (HTTP %d)", ae.Message, ae.StatusCode), ae.Hint)
}

// httpClient is the CLI's thin facade over apiclient.Client. It exists to
// keep the per-command code path the same shape it had before the extraction
// (callers say `c.doJSON(...)` not `c.api.DoJSON(ctx, ...)`).
type httpClient struct {
	api *apiclient.Client
}

func newHTTPClient(cfg *cliConfig) *httpClient {
	return &httpClient{api: apiclient.New(cfg.Server, cfg.Token).WithWorkspace(cfg.Workspace)}
}

func managedRequestKey(operation, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if len(configured) > 200 {
		return "", fmt.Errorf("--idempotency-key must be at most 200 characters")
	}
	if configured != "" {
		return configured, nil
	}
	return "cli-" + operation + "-" + uuid.NewString(), nil
}

func (c *httpClient) doJSON(method, path string, body, out any) error {
	return fromAPIError(c.api.DoJSON(context.Background(), method, path, body, out))
}

func (c *httpClient) doJSONWithHeaders(
	method, path string,
	body, out any,
	headers map[string]string,
) error {
	return fromAPIError(c.api.DoJSONWithHeaders(
		context.Background(),
		method,
		path,
		body,
		out,
		headers,
	))
}

func (c *httpClient) doMultipartUpload(_, _ /*fieldName,fileName—now derived*/, name string, mimeType, targetFolder string, file io.Reader, tags []string, out any) error {
	return c.doMultipartUploadWithSourceMetadata(name, mimeType, targetFolder, file, tags, nil, out)
}

func (c *httpClient) doMultipartUploadWithSourceMetadata(name, mimeType, targetFolder string, file io.Reader, tags []string, sourceMetadata *apiclient.FileSourceMetadata, out any) error {
	// Legacy 4-positional callers passed ("/v1/files","file",<name>,<mime>,...).
	// apiclient handles the path + field name internally; we only need name/mime.
	return fromAPIError(c.api.UploadMultipartWithSourceMetadata(context.Background(), name, mimeType, targetFolder, file, tags, sourceMetadata, out))
}

func (c *httpClient) doStreamUpload(name, mimeType, targetFolder string, size int64, tags []string, body io.Reader, out any) error {
	return c.doStreamUploadWithSourceMetadata(name, mimeType, targetFolder, size, tags, body, nil, out)
}

func (c *httpClient) doStreamUploadWithSourceMetadata(name, mimeType, targetFolder string, size int64, tags []string, body io.Reader, sourceMetadata *apiclient.FileSourceMetadata, out any) error {
	return fromAPIError(c.api.UploadStreamWithSourceMetadata(context.Background(), name, mimeType, targetFolder, size, tags, body, sourceMetadata, out))
}

func (c *httpClient) downloadStream(fileID string) (io.ReadCloser, string, error) {
	rc, ctype, err := c.api.DownloadStream(context.Background(), fileID)
	return rc, ctype, fromAPIError(err)
}

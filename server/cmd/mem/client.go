package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
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
	case apiclient.KindQuota:
		code = 4
	case apiclient.KindProvider:
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
	return &httpClient{api: apiclient.New(cfg.Server, cfg.Token)}
}

func (c *httpClient) doJSON(method, path string, body, out any) error {
	return fromAPIError(c.api.DoJSON(context.Background(), method, path, body, out))
}

func (c *httpClient) doMultipartUpload(_, _ /*fieldName,fileName—now derived*/, name string, mimeType, targetFolder string, file io.Reader, tags []string, out any) error {
	// Legacy 4-positional callers passed ("/v1/files","file",<name>,<mime>,...).
	// apiclient handles the path + field name internally; we only need name/mime.
	return fromAPIError(c.api.UploadMultipart(context.Background(), name, mimeType, targetFolder, file, tags, out))
}

func (c *httpClient) doStreamUpload(name, mimeType, targetFolder string, size int64, tags []string, body io.Reader, out any) error {
	return fromAPIError(c.api.UploadStream(context.Background(), name, mimeType, targetFolder, size, tags, body, out))
}

func (c *httpClient) downloadStream(fileID string) (io.ReadCloser, string, error) {
	rc, ctype, err := c.api.DownloadStream(context.Background(), fileID)
	return rc, ctype, fromAPIError(err)
}

package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const sourceMetadataHeader = "X-Mem-Source-Metadata"

// Client is a thin HTTP wrapper over the memd REST surface. It injects the
// bearer token, parses {error, hint} bodies into *APIError, and exposes the
// few non-JSON shapes (multipart upload, streaming upload, streaming
// download) that the file endpoints need.
type Client struct {
	baseURL   string
	token     string
	workspace string
	hc        *http.Client
}

// FileSourceLocation is caller-supplied capture location metadata. Lat/Lon
// intentionally use the API-facing order; PostgreSQL point storage details
// remain an implementation concern of memd.
type FileSourceLocation struct {
	Lat       float64  `json:"lat"`
	Lon       float64  `json:"lon"`
	AccuracyM *float64 `json:"accuracy_m,omitempty"`
	Label     string   `json:"label,omitempty"`
}

// FileSourceMetadata carries provenance supplied by an upload adapter or
// capture device. It is distinct from processor/model metadata so model
// output can never silently become a user-provided fact.
type FileSourceMetadata struct {
	CapturedAt string              `json:"captured_at,omitempty"`
	Location   *FileSourceLocation `json:"location,omitempty"`
	SourceKind string              `json:"source_kind,omitempty"`
	SourceName string              `json:"source_name,omitempty"`
}

// New constructs a Client. The baseURL is normalized (trailing slash
// stripped). The token may be empty for unauthenticated calls (e.g.
// /healthz, /v1/version, /v1/auth/login).
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		hc:      &http.Client{Timeout: 5 * time.Minute},
	}
}

// BaseURL returns the configured server URL (without trailing slash).
func (c *Client) BaseURL() string { return c.baseURL }

// WithToken returns a shallow copy of c with the given bearer token. Useful
// when a single process serves multiple auth contexts (e.g. an MCP server
// reading its token from the environment after construction).
func (c *Client) WithToken(token string) *Client {
	cp := *c
	cp.token = token
	return &cp
}

// WithWorkspace returns a shallow copy that sends X-Workspace-ID on every
// request. This lets one Agent token address an explicitly selected memory
// namespace instead of silently falling back to its personal workspace.
func (c *Client) WithWorkspace(workspace string) *Client {
	cp := *c
	cp.workspace = strings.TrimSpace(workspace)
	return &cp
}

// DoJSON issues a JSON request and decodes the response body into out (if
// non-nil). Returns *APIError for non-2xx responses.
func (c *Client) DoJSON(ctx context.Context, method, path string, body, out any) error {
	return c.DoJSONWithHeaders(ctx, method, path, body, out, nil)
}

// DoJSONWithHeaders is DoJSON with additional request headers. Client-owned
// authentication and workspace headers are attached last so callers cannot
// accidentally replace the configured request scope.
func (c *Client) DoJSONWithHeaders(ctx context.Context, method, path string, body, out any, headers map[string]string) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	c.attachAuth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decode(resp, out)
}

// UploadMultipart posts a file via multipart/form-data (POST /v1/files).
// The body is streamed; size need not be known up-front.
func (c *Client) UploadMultipart(ctx context.Context, name, mimeType, targetFolder string, body io.Reader, tags []string, out any) error {
	return c.UploadMultipartWithSourceMetadata(ctx, name, mimeType, targetFolder, body, tags, nil, out)
}

// UploadMultipartWithSourceMetadata is UploadMultipart with provenance and
// capture facts supplied by the caller. sourceMetadata is serialized into the
// upload contract's source_metadata form field.
func (c *Client) UploadMultipartWithSourceMetadata(ctx context.Context, name, mimeType, targetFolder string, body io.Reader, tags []string, sourceMetadata *FileSourceMetadata, out any) error {
	sourceJSON, err := marshalSourceMetadata(sourceMetadata)
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	errCh := make(chan error, 1)

	go func() {
		defer pw.Close()
		defer mw.Close()
		if len(tags) > 0 {
			if err := mw.WriteField("tags", strings.Join(tags, ",")); err != nil {
				errCh <- err
				return
			}
		}
		if targetFolder != "" {
			if err := mw.WriteField("path", targetFolder); err != nil {
				errCh <- err
				return
			}
		}
		if sourceJSON != "" {
			if err := mw.WriteField("source_metadata", sourceJSON); err != nil {
				errCh <- err
				return
			}
		}
		part, err := mw.CreatePart(multipartHeader("file", name, mimeType))
		if err != nil {
			errCh <- err
			return
		}
		if _, err := io.Copy(part, body); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/files", pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	c.attachAuth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if werr := <-errCh; werr != nil {
		return werr
	}
	return decode(resp, out)
}

// UploadStream posts a raw body to POST /v1/files?stream=1. Used when name
// + mime are known but the body is a pipe (stdin / remote fetch).
func (c *Client) UploadStream(ctx context.Context, name, mimeType, targetFolder string, size int64, tags []string, body io.Reader, out any) error {
	return c.UploadStreamWithSourceMetadata(ctx, name, mimeType, targetFolder, size, tags, body, nil, out)
}

// UploadStreamWithSourceMetadata is UploadStream with caller-supplied
// provenance and capture facts. The metadata travels in a bounded private
// request header rather than the URL so precise location data is not copied
// into ordinary proxy and access logs.
func (c *Client) UploadStreamWithSourceMetadata(ctx context.Context, name, mimeType, targetFolder string, size int64, tags []string, body io.Reader, sourceMetadata *FileSourceMetadata, out any) error {
	q := url.Values{}
	q.Set("stream", "1")
	q.Set("name", name)
	if mimeType != "" {
		q.Set("mime", mimeType)
	}
	if size >= 0 {
		q.Set("size", strconv.FormatInt(size, 10))
	}
	if len(tags) > 0 {
		q.Set("tags", strings.Join(tags, ","))
	}
	if targetFolder != "" {
		q.Set("path", targetFolder)
	}
	sourceJSON, err := marshalSourceMetadata(sourceMetadata)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/files?"+q.Encode(), body)
	if err != nil {
		return err
	}
	if sourceJSON != "" {
		req.Header.Set(sourceMetadataHeader, sourceJSON)
	}
	if mimeType != "" {
		req.Header.Set("Content-Type", mimeType)
	}
	if size >= 0 {
		req.ContentLength = size
	}
	c.attachAuth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decode(resp, out)
}

// DownloadStream returns a streaming reader for GET /v1/files/{id}/content.
// Callers MUST close the returned ReadCloser.
func (c *Client) DownloadStream(ctx context.Context, fileID string) (io.ReadCloser, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/files/"+fileID+"/content", nil)
	if err != nil {
		return nil, "", err
	}
	c.attachAuth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, "", errorFromResponse(resp)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

// --- internals ---

func marshalSourceMetadata(sourceMetadata *FileSourceMetadata) (string, error) {
	if sourceMetadata == nil {
		return "", nil
	}
	raw, err := json.Marshal(sourceMetadata)
	if err != nil {
		return "", fmt.Errorf("encode source metadata: %w", err)
	}
	return string(raw), nil
}

func (c *Client) attachAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.workspace != "" {
		req.Header.Set("X-Workspace-ID", c.workspace)
	}
}

func decode(resp *http.Response, out any) error {
	if resp.StatusCode >= 400 {
		return errorFromResponse(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func errorFromResponse(resp *http.Response) error {
	var er struct {
		Error     string                    `json:"error"`
		Hint      string                    `json:"hint"`
		Conflicts []WorkspaceImportConflict `json:"conflicts"`
		Total     int                       `json:"total"`
		Truncated bool                      `json:"truncated"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&er)
	msg := er.Error
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &APIError{
		StatusCode:         resp.StatusCode,
		Code:               er.Error, // memd uses the same string for code+message; can split later
		Message:            msg,
		Hint:               er.Hint,
		Conflicts:          er.Conflicts,
		ConflictTotal:      er.Total,
		ConflictsTruncated: er.Truncated,
	}
}

func multipartHeader(field, filename, ctype string) map[string][]string {
	disp := fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, escapeQuotes(filename))
	h := map[string][]string{"Content-Disposition": {disp}}
	if ctype != "" {
		h["Content-Type"] = []string{ctype}
	}
	return h
}

func escapeQuotes(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return r.Replace(s)
}

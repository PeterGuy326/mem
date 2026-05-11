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

// Client is a thin HTTP wrapper over the memd REST surface. It injects the
// bearer token, parses {error, hint} bodies into *APIError, and exposes the
// few non-JSON shapes (multipart upload, streaming upload, streaming
// download) that the file endpoints need.
type Client struct {
	baseURL string
	token   string
	hc      *http.Client
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

// DoJSON issues a JSON request and decodes the response body into out (if
// non-nil). Returns *APIError for non-2xx responses.
func (c *Client) DoJSON(ctx context.Context, method, path string, body, out any) error {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/files?"+q.Encode(), body)
	if err != nil {
		return err
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

func (c *Client) attachAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
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
		Error string `json:"error"`
		Hint  string `json:"hint"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&er)
	msg := er.Error
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &APIError{
		StatusCode: resp.StatusCode,
		Code:       er.Error, // memd uses the same string for code+message; can split later
		Message:    msg,
		Hint:       er.Hint,
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

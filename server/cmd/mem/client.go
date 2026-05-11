package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
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

// httpClient is a thin wrapper that injects Authorization + decodes
// {error,hint} into cliError with appropriate exit codes.
type httpClient struct {
	server string
	token  string
	http   *http.Client
}

func newHTTPClient(cfg *cliConfig) *httpClient {
	return &httpClient{
		server: strings.TrimRight(cfg.Server, "/"),
		token:  cfg.Token,
		http:   &http.Client{Timeout: 5 * time.Minute},
	}
}

func (c *httpClient) doJSON(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.server+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.attachAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.decode(resp, out)
}

func (c *httpClient) doMultipartUpload(path, fieldName, fileName, mimeType, targetFolder string, file io.Reader, tags []string, out any) error {
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
		h := make(map[string][]string)
		if mimeType != "" {
			h["Content-Type"] = []string{mimeType}
		}
		part, err := mw.CreatePart(textproto(fieldName, fileName, mimeType))
		if err != nil {
			errCh <- err
			return
		}
		if _, err := io.Copy(part, file); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	req, err := http.NewRequest(http.MethodPost, c.server+path, pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	c.attachAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if werr := <-errCh; werr != nil {
		return werr
	}
	return c.decode(resp, out)
}

// doStreamUpload uses POST /v1/files?stream=1 with the raw body.
func (c *httpClient) doStreamUpload(name, mimeType, targetFolder string, size int64, tags []string, body io.Reader, out any) error {
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
	req, err := http.NewRequest(http.MethodPost, c.server+"/v1/files?"+q.Encode(), body)
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

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.decode(resp, out)
}

// downloadStream returns a streaming reader for GET /v1/files/{id}/content.
func (c *httpClient) downloadStream(fileID string) (io.ReadCloser, string, error) {
	req, err := http.NewRequest(http.MethodGet, c.server+"/v1/files/"+fileID+"/content", nil)
	if err != nil {
		return nil, "", err
	}
	c.attachAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, "", c.errorFromResponse(resp)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

func (c *httpClient) attachAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func (c *httpClient) decode(resp *http.Response, out any) error {
	if resp.StatusCode >= 400 {
		return c.errorFromResponse(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *httpClient) errorFromResponse(resp *http.Response) error {
	var er struct {
		Error string `json:"error"`
		Hint  string `json:"hint"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&er)
	msg := er.Error
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	code := 1
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		code = 3 // auth
	case http.StatusNotFound:
		code = 2 // not_found
	case http.StatusTooManyRequests:
		code = 4 // quota
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		code = 5 // provider_error
	}
	return newCliError(code, fmt.Sprintf("%s (HTTP %d)", msg, resp.StatusCode), er.Hint)
}

// textproto returns the MIME header for a multipart file part.
func textproto(field, filename, ctype string) map[string][]string {
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

// fileSize returns the size of the file at path, or -1 if it can't be stat'd.
func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return st.Size()
}

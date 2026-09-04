package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
)

const (
	// WorkspaceBundleMediaType is the canonical media type for portable
	// workspace archives.
	WorkspaceBundleMediaType = workspacebundle.BundleMediaType
	// WorkspaceRestoreModeFresh is the only restore mode currently implemented
	// by memd. Additional modes must not be advertised before they are real.
	WorkspaceRestoreModeFresh = workspacebundle.RestoreModeFresh
)

// WorkspaceBundleDownload is a streaming workspace export. The caller must
// close Body.
type WorkspaceBundleDownload struct {
	Body          io.ReadCloser
	Filename      string
	ContentType   string
	ContentLength int64
}

// WorkspaceObjectCounts describes the portable objects represented by a
// workspace bundle.
type WorkspaceObjectCounts struct {
	Folders            int64 `json:"folders"`
	Files              int64 `json:"files"`
	Memories           int64 `json:"memories"`
	MemoryEvents       int64 `json:"memory_events"`
	Tasks              int64 `json:"tasks"`
	Checkpoints        int64 `json:"checkpoints"`
	CheckpointRefs     int64 `json:"checkpoint_refs"`
	CheckpointPayloads int64 `json:"checkpoint_payloads"`
	Blobs              int64 `json:"blobs"`
	BlobBytes          int64 `json:"blob_bytes"`
}

// WorkspaceImportResult is returned after a fully validated and committed
// workspace import.
type WorkspaceImportResult struct {
	BundleID          string                `json:"bundle_id"`
	ArchiveSHA256     string                `json:"archive_sha256"`
	SourceWorkspaceID string                `json:"source_workspace_id"`
	ImportedAt        time.Time             `json:"imported_at"`
	Counts            WorkspaceObjectCounts `json:"counts"`
	Replayed          bool                  `json:"replayed"`
}

// WorkspaceImportConflict safely identifies a colliding portable resource.
// Server-internal details and storage keys are deliberately not represented.
type WorkspaceImportConflict struct {
	Kind     string `json:"kind"`
	Resource string `json:"resource,omitempty"`
	Value    string `json:"value,omitempty"`
}

// ExportWorkspace downloads a portable workspace bundle without buffering it
// in memory. The server does not publish response headers until its complete
// archive has been built and validated.
func (c *Client) ExportWorkspace(ctx context.Context) (*WorkspaceBundleDownload, error) {
	req, err := c.newRequest(
		ctx,
		http.MethodGet,
		c.baseURL+"/v1/workspaces/current/export",
		nil,
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", WorkspaceBundleMediaType)
	c.attachAuth(req)

	resp, err := c.workspaceTransferHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()
		return nil, errorFromResponse(resp)
	}
	contentType, parameters, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil ||
		!strings.EqualFold(contentType, WorkspaceBundleMediaType) ||
		len(parameters) != 0 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf(
			"workspace export returned unexpected Content-Type %q",
			resp.Header.Get("Content-Type"),
		)
	}
	filename := ""
	if disposition := resp.Header.Get("Content-Disposition"); disposition != "" {
		_, parameters, parseErr := mime.ParseMediaType(disposition)
		if parseErr == nil {
			filename = parameters["filename"]
		}
	}
	return &WorkspaceBundleDownload{
		Body:          resp.Body,
		Filename:      filename,
		ContentType:   contentType,
		ContentLength: resp.ContentLength,
	}, nil
}

// ImportWorkspace uploads a portable workspace bundle as a streaming request.
// size may be -1 when unknown; a non-negative value is sent as Content-Length.
func (c *Client) ImportWorkspace(
	ctx context.Context,
	mode string,
	size int64,
	body io.Reader,
) (*WorkspaceImportResult, error) {
	if body == nil {
		return nil, fmt.Errorf("workspace bundle body is required")
	}
	if mode != WorkspaceRestoreModeFresh {
		return nil, fmt.Errorf("unsupported workspace restore mode %q", mode)
	}
	if size < -1 {
		return nil, fmt.Errorf("workspace bundle size must be -1 or non-negative")
	}
	query := url.Values{"mode": []string{mode}}
	req, err := c.newRequest(
		ctx,
		http.MethodPost,
		c.baseURL+"/v1/workspaces/current/import?"+query.Encode(),
		body,
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", WorkspaceBundleMediaType)
	if size >= 0 {
		req.ContentLength = size
	}
	c.attachAuth(req)

	resp, err := c.workspaceTransferHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, errorFromResponse(resp)
	}
	var result WorkspaceImportResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode workspace import response: %w", err)
	}
	return &result, nil
}

// Workspace transfers can legitimately take longer than the default API
// timeout while the server builds or validates a large archive. Cancellation
// remains controlled by the request context.
func (c *Client) workspaceTransferHTTPClient() *http.Client {
	client := *c.hc
	client.Timeout = 0
	return &client
}

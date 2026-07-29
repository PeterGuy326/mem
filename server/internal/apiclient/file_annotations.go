package apiclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

type FileAnnotationDecision string

const (
	FileAnnotationDecisionAccepted FileAnnotationDecision = "accepted"
	FileAnnotationDecisionRejected FileAnnotationDecision = "rejected"
)

// FileAnnotationDecisionRequest carries the canonical optimistic-concurrency
// contract for accepting or rejecting one model-generated file annotation.
type FileAnnotationDecisionRequest struct {
	Decision        FileAnnotationDecision `json:"decision"`
	ExpectedVersion int64                  `json:"expected_version"`
}

// FileAnnotation is the public annotation projection returned by memd.
type FileAnnotation struct {
	ID              string     `json:"id"`
	FileID          string     `json:"file_id"`
	StableKey       string     `json:"stable_key"`
	Kind            string     `json:"kind"`
	ValueText       string     `json:"value_text"`
	Confidence      float32    `json:"confidence"`
	Source          string     `json:"source"`
	Provider        string     `json:"provider"`
	Processor       string     `json:"processor"`
	AnalysisVersion string     `json:"analysis_version"`
	Status          string     `json:"status"`
	StateVersion    int64      `json:"state_version"`
	DecidedByUserID *string    `json:"decided_by_user_id,omitempty"`
	DecidedAt       *time.Time `json:"decided_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// FileAnnotationDecisionResponse is returned for both a new terminal decision
// and an idempotent replay of the same terminal decision.
type FileAnnotationDecisionResponse struct {
	Annotation FileAnnotation `json:"annotation"`
	Replayed   bool           `json:"replayed"`
}

// DecideFileAnnotation delegates to the canonical memd decision endpoint.
// Authorization, path scope, terminal-state replay, and conflict semantics
// remain server-owned.
func (c *Client) DecideFileAnnotation(
	ctx context.Context,
	fileID string,
	annotationID string,
	request FileAnnotationDecisionRequest,
) (*FileAnnotationDecisionResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("apiclient: nil client")
	}
	switch request.Decision {
	case FileAnnotationDecisionAccepted, FileAnnotationDecisionRejected:
	default:
		return nil, fmt.Errorf(
			"apiclient: file annotation decision must be accepted or rejected",
		)
	}
	if request.ExpectedVersion <= 0 {
		return nil, fmt.Errorf("apiclient: expected_version must be greater than zero")
	}

	parsedFileID, err := uuid.Parse(strings.TrimSpace(fileID))
	if err != nil || parsedFileID == uuid.Nil {
		return nil, fmt.Errorf("apiclient: file id must be a UUID")
	}
	parsedAnnotationID, err := uuid.Parse(strings.TrimSpace(annotationID))
	if err != nil || parsedAnnotationID == uuid.Nil {
		return nil, fmt.Errorf("apiclient: annotation id must be a UUID")
	}

	path := "/v1/files/" + url.PathEscape(parsedFileID.String()) +
		"/annotations/" + url.PathEscape(parsedAnnotationID.String())
	var response FileAnnotationDecisionResponse
	if err := c.DoJSON(ctx, http.MethodPut, path, request, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

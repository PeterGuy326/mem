package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/file"
)

const (
	// MaxSourceMetadataBytes bounds the complete transport JSON before parsing.
	MaxSourceMetadataBytes         = 4 << 10
	maxAnnotationDecisionBodyBytes = 4 << 10
	sourceMetadataHeader           = "X-Mem-Source-Metadata"
)

type sourceMetadataInput struct {
	CapturedAt *string              `json:"captured_at"`
	Location   *sourceLocationInput `json:"location"`
	SourceKind string               `json:"source_kind"`
	SourceName string               `json:"source_name"`
}

type sourceLocationInput struct {
	Lat            *float64 `json:"lat"`
	Lon            *float64 `json:"lon"`
	AccuracyMeters *float64 `json:"accuracy_m"`
	Label          string   `json:"label"`
}

// ParseSourceMetadataJSON strictly decodes the optional upload metadata field.
// It is exported so HTTP client adapters can share the contract in tests
// without duplicating validation rules.
func ParseSourceMetadataJSON(raw string) (file.SourceMetadata, error) {
	if len(raw) > MaxSourceMetadataBytes {
		return file.SourceMetadata{}, fmt.Errorf(
			"source_metadata exceeds %d bytes",
			MaxSourceMetadataBytes,
		)
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return file.SourceMetadata{}, nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return file.SourceMetadata{}, errors.New("source_metadata must be a JSON object")
	}
	if err := rejectDuplicateJSONKeys(trimmed); err != nil {
		return file.SourceMetadata{}, fmt.Errorf("decode source_metadata: %w", err)
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &rawFields); err != nil {
		return file.SourceMetadata{}, fmt.Errorf("decode source_metadata: %w", err)
	}
	for key, value := range rawFields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return file.SourceMetadata{}, fmt.Errorf(
				"source_metadata.%s must not be null",
				key,
			)
		}
	}
	if rawLocation, exists := rawFields["location"]; exists {
		var locationFields map[string]json.RawMessage
		if err := json.Unmarshal(rawLocation, &locationFields); err == nil {
			for key, value := range locationFields {
				if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					return file.SourceMetadata{}, fmt.Errorf(
						"source_metadata.location.%s must not be null",
						key,
					)
				}
			}
		}
	}

	var input sourceMetadataInput
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return file.SourceMetadata{}, fmt.Errorf("decode source_metadata: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return file.SourceMetadata{}, fmt.Errorf("decode source_metadata: %w", err)
	}

	metadata := file.SourceMetadata{
		SourceKind: input.SourceKind,
		SourceName: input.SourceName,
	}
	if input.CapturedAt != nil {
		capturedAt, err := time.Parse(time.RFC3339, *input.CapturedAt)
		if err != nil {
			return file.SourceMetadata{}, errors.New(
				"captured_at must be RFC3339 with an explicit timezone",
			)
		}
		metadata.CapturedAt = &capturedAt
	}
	if input.Location != nil {
		if input.Location.Lat == nil || input.Location.Lon == nil {
			return file.SourceMetadata{}, errors.New(
				"location.lat and location.lon are required together",
			)
		}
		metadata.Location = &file.SourceLocation{
			Lat:            *input.Location.Lat,
			Lon:            *input.Location.Lon,
			AccuracyMeters: input.Location.AccuracyMeters,
			Label:          input.Location.Label,
		}
	}
	if err := metadata.Validate(); err != nil {
		return file.SourceMetadata{}, err
	}
	return metadata, nil
}

func rejectDuplicateJSONKeys(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	var walkValue func() error
	walkValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key must be a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				seen[key] = struct{}{}
				if err := walkValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim('}') {
				return errors.New("malformed JSON object")
			}
		case '[':
			for decoder.More() {
				if err := walkValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim(']') {
				return errors.New("malformed JSON array")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		return nil
	}
	return walkValue()
}

type annotationDecisionRequest struct {
	Decision        string `json:"decision"`
	ExpectedVersion *int64 `json:"expected_version"`
}

// FileAnnotationService is the narrow mutation seam used by the annotation
// handler. The concrete file.Service enforces ownership and path scope while
// holding row locks; tests can supply a deterministic stub.
type FileAnnotationService interface {
	DecideAnnotation(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		uuid.UUID,
		uuid.UUID,
		file.AnnotationDecisionCommand,
	) (*file.AnnotationDecisionResult, error)
}

func (s *Server) handleDecideFileAnnotation(w http.ResponseWriter, r *http.Request) {
	fileID, err := uuid.Parse(chi.URLParam(r, "fileID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_file_id", "fileID must be a UUID")
		return
	}
	annotationID, err := uuid.Parse(chi.URLParam(r, "annotationID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_annotation_id", "annotationID must be a UUID")
		return
	}

	var request annotationDecisionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAnnotationDecisionBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if err := requireJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if request.Decision != file.AnnotationStatusAccepted &&
		request.Decision != file.AnnotationStatusRejected {
		writeError(
			w,
			http.StatusBadRequest,
			"bad_decision",
			"decision must be accepted or rejected",
		)
		return
	}
	if request.ExpectedVersion == nil || *request.ExpectedVersion <= 0 {
		writeError(
			w,
			http.StatusBadRequest,
			"bad_expected_version",
			"expected_version must be a positive integer",
		)
		return
	}

	owner, ownerOK := r.Context().Value(ctxUser).(*auth.User)
	actor, actorOK := r.Context().Value(ctxActor).(*auth.User)
	token, tokenOK := r.Context().Value(ctxToken).(*auth.Token)
	service := s.fileAnnotationService()
	if !ownerOK || !actorOK || !tokenOK || service == nil {
		writeError(
			w,
			http.StatusServiceUnavailable,
			"annotation_service_unavailable",
			"file annotation service is not configured",
		)
		return
	}
	result, err := service.DecideAnnotation(
		r.Context(),
		owner.ID,
		actor.ID,
		fileID,
		annotationID,
		file.AnnotationDecisionCommand{
			Decision:        request.Decision,
			ExpectedVersion: *request.ExpectedVersion,
			AllowedPaths:    append([]string(nil), token.Paths...),
		},
	)
	if err != nil {
		writeAnnotationDecisionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) fileAnnotationService() FileAnnotationService {
	if s.FileAnnotations != nil {
		return s.FileAnnotations
	}
	if s.File == nil {
		return nil
	}
	return s.File
}

func writeAnnotationDecisionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, file.ErrAnnotationNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such file annotation")
	case errors.Is(err, file.ErrAnnotationDecisionConflict):
		writeError(
			w,
			http.StatusConflict,
			"annotation_decision_conflict",
			"annotation already has a different terminal decision",
		)
	case errors.Is(err, file.ErrAnnotationVersionConflict):
		writeError(
			w,
			http.StatusConflict,
			"annotation_version_conflict",
			"annotation changed; reload it and retry with the current state_version",
		)
	case errors.Is(err, file.ErrInvalidAnnotationDecision):
		writeError(
			w,
			http.StatusBadRequest,
			"bad_decision",
			"decision must be accepted or rejected",
		)
	default:
		writeError(
			w,
			http.StatusInternalServerError,
			"annotation_update_failed",
			"file annotation could not be updated",
		)
	}
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values are not allowed")
	}
	return err
}

package file

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/PeterGuy326/mem/server/internal/pathx"
)

const (
	AnnotationKindDescription = "description"
	AnnotationKindTag         = "tag"

	AnnotationStatusPending    = "pending"
	AnnotationStatusAccepted   = "accepted"
	AnnotationStatusRejected   = "rejected"
	AnnotationStatusSuperseded = "superseded"
)

var (
	ErrAnnotationNotFound         = errors.New("file annotation not found")
	ErrAnnotationDecisionConflict = errors.New("annotation decision conflicts with terminal state")
	ErrAnnotationVersionConflict  = errors.New("annotation state version conflict")
	ErrInvalidAnnotationDecision  = errors.New("annotation decision must be accepted or rejected")
)

// Annotation is one provenance-bearing description or tag suggestion.
type Annotation struct {
	ID              uuid.UUID  `json:"id"`
	FileID          uuid.UUID  `json:"file_id"`
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
	DecidedByUserID *uuid.UUID `json:"decided_by_user_id,omitempty"`
	DecidedAt       *time.Time `json:"decided_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// AnnotationDecisionCommand carries the optimistic version and current token
// path boundary into the transaction that locks the annotation and file.
type AnnotationDecisionCommand struct {
	Decision        string
	ExpectedVersion int64
	AllowedPaths    []string
}

type AnnotationDecisionResult struct {
	Annotation Annotation `json:"annotation"`
	Replayed   bool       `json:"replayed"`
}

const maxFileDetailAnnotations = 256

func (s *Service) loadAnnotations(
	ctx context.Context,
	fileID uuid.UUID,
) ([]Annotation, bool, error) {
	rows, err := s.pool.Query(ctx, `
		WITH effective_summary AS (
			SELECT id
			  FROM file_annotations
			 WHERE file_id = $1
			   AND kind = 'description'
			   AND status = 'accepted'
			 ORDER BY COALESCE(decided_at, updated_at) DESC,
			          confidence DESC,
			          created_at DESC,
			          id DESC
			 LIMIT 1
		)
		SELECT annotation.id, annotation.file_id, annotation.stable_key,
		       annotation.kind, annotation.value_text, annotation.confidence,
		       annotation.source, annotation.provider, annotation.processor,
		       annotation.analysis_version, annotation.status,
		       annotation.state_version, annotation.decided_by_user_id,
		       annotation.decided_at, annotation.created_at,
		       annotation.updated_at
		  FROM file_annotations AS annotation
		  LEFT JOIN effective_summary
		    ON effective_summary.id = annotation.id
		 WHERE annotation.file_id = $1
		 ORDER BY
		       CASE WHEN effective_summary.id IS NOT NULL THEN 0 ELSE 1 END,
		       CASE WHEN annotation.status = 'pending' THEN 0 ELSE 1 END,
		       CASE WHEN annotation.status = 'pending'
		            THEN annotation.confidence END DESC NULLS LAST,
		       CASE WHEN annotation.status = 'pending'
		            THEN annotation.created_at END ASC NULLS LAST,
		       annotation.updated_at DESC,
		       annotation.id
		 LIMIT $2
	`, fileID, maxFileDetailAnnotations+1)
	if err != nil {
		return nil, false, fmt.Errorf("list file annotations: %w", err)
	}
	defer rows.Close()

	annotations := make([]Annotation, 0)
	for rows.Next() {
		var annotation Annotation
		if err := rows.Scan(annotationScanDestinations(&annotation)...); err != nil {
			return nil, false, fmt.Errorf("scan file annotation: %w", err)
		}
		annotations = append(annotations, annotation)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate file annotations: %w", err)
	}
	truncated := len(annotations) > maxFileDetailAnnotations
	if truncated {
		annotations = annotations[:maxFileDetailAnnotations]
	}
	return annotations, truncated, nil
}

// DecideAnnotation makes a pending suggestion terminal and recomputes the
// backwards-compatible files.tags / files.summary projections. A replay of the
// same terminal decision succeeds without incrementing state_version.
func (s *Service) DecideAnnotation(
	ctx context.Context,
	ownerID, actorID, fileID, annotationID uuid.UUID,
	command AnnotationDecisionCommand,
) (*AnnotationDecisionResult, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("file annotation service is not configured")
	}
	if ownerID == uuid.Nil || actorID == uuid.Nil || fileID == uuid.Nil || annotationID == uuid.Nil {
		return nil, errors.New("owner, actor, file, and annotation ids are required")
	}
	if command.ExpectedVersion <= 0 {
		return nil, errors.New("expected_version must be positive")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin annotation decision: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		filePath string
		userTags []string
	)
	err = tx.QueryRow(ctx, `
		SELECT path, user_tags
		  FROM files
		 WHERE id = $1
		   AND user_id = $2
		   FOR UPDATE
	`, fileID, ownerID).Scan(&filePath, &userTags)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAnnotationNotFound
		}
		return nil, fmt.Errorf("load annotation decision: %w", err)
	}
	if !annotationPathAllowed(filePath, command.AllowedPaths) {
		return nil, ErrAnnotationNotFound
	}

	// Keep the file -> annotation lock order aligned with the indexer, which
	// updates the file projection before upserting suggestions. Besides
	// serializing projection recomputes, this avoids a decision/indexing
	// deadlock caused by taking the two row types in opposite orders.
	var annotation Annotation
	err = tx.QueryRow(ctx, `
		SELECT id, file_id, stable_key, kind, value_text,
		       confidence, source, provider, processor, analysis_version,
		       status, state_version, decided_by_user_id, decided_at,
		       created_at, updated_at
		  FROM file_annotations
		 WHERE id = $1
		   AND file_id = $2
		   FOR UPDATE
	`, annotationID, fileID).Scan(annotationScanDestinations(&annotation)...)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAnnotationNotFound
		}
		return nil, fmt.Errorf("load annotation decision: %w", err)
	}

	replayed, err := annotationTransition(
		annotation.Status,
		annotation.StateVersion,
		command.Decision,
		command.ExpectedVersion,
	)
	if err != nil {
		return nil, err
	}
	if replayed {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit annotation replay: %w", err)
		}
		return &AnnotationDecisionResult{Annotation: annotation, Replayed: true}, nil
	}

	err = tx.QueryRow(ctx, `
		UPDATE file_annotations
		   SET status = $1,
		       state_version = state_version + 1,
		       decided_by_user_id = $2,
		       decided_at = now(),
		       updated_at = now()
		 WHERE id = $3
		 RETURNING id, file_id, stable_key, kind, value_text,
		           confidence, source, provider, processor, analysis_version,
		           status, state_version, decided_by_user_id, decided_at,
		           created_at, updated_at
	`, command.Decision, actorID, annotationID).Scan(
		annotationScanDestinations(&annotation)...,
	)
	if err != nil {
		return nil, fmt.Errorf("update annotation decision: %w", err)
	}
	if err := recomputeFileEnrichmentProjections(ctx, tx, fileID, userTags); err != nil {
		return nil, err
	}
	if annotation.Kind == AnnotationKindDescription {
		if err := RefreshReviewableDescriptionProjections(ctx, tx, fileID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit annotation decision: %w", err)
	}
	return &AnnotationDecisionResult{Annotation: annotation}, nil
}

func annotationTransition(status string, stateVersion int64, decision string, expectedVersion int64) (bool, error) {
	if decision != AnnotationStatusAccepted && decision != AnnotationStatusRejected {
		return false, ErrInvalidAnnotationDecision
	}
	if status == decision {
		return true, nil
	}
	switch status {
	case AnnotationStatusAccepted, AnnotationStatusRejected, AnnotationStatusSuperseded:
		return false, ErrAnnotationDecisionConflict
	case AnnotationStatusPending:
		if stateVersion != expectedVersion {
			return false, ErrAnnotationVersionConflict
		}
		return false, nil
	default:
		return false, fmt.Errorf("unsupported annotation status %q", status)
	}
}

func recomputeFileEnrichmentProjections(
	ctx context.Context,
	tx pgx.Tx,
	fileID uuid.UUID,
	userTags []string,
) error {
	rows, err := tx.Query(ctx, `
		SELECT value_text
		  FROM file_annotations
		 WHERE file_id = $1
		   AND kind = 'tag'
		   AND status = 'accepted'
		 ORDER BY COALESCE(decided_at, updated_at), created_at, id
	`, fileID)
	if err != nil {
		return fmt.Errorf("list accepted annotation tags: %w", err)
	}
	acceptedTags := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return fmt.Errorf("scan accepted annotation tag: %w", err)
		}
		acceptedTags = append(acceptedTags, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate accepted annotation tags: %w", err)
	}
	rows.Close()

	tags := mergeUniqueTags(userTags, acceptedTags)
	var description string
	descriptionErr := tx.QueryRow(ctx, `
		SELECT value_text
		  FROM file_annotations
		 WHERE file_id = $1
		   AND kind = 'description'
		   AND status = 'accepted'
		 ORDER BY COALESCE(decided_at, updated_at) DESC,
		          confidence DESC,
		          created_at DESC,
		          id DESC
		 LIMIT 1
	`, fileID).Scan(&description)

	switch {
	case descriptionErr == nil:
		if _, err := tx.Exec(ctx, `
			UPDATE files
			   SET tags = $1,
			       summary = $2,
			       updated_at = now()
			 WHERE id = $3
		`, tags, description, fileID); err != nil {
			return fmt.Errorf("update file enrichment projections: %w", err)
		}
	case errors.Is(descriptionErr, pgx.ErrNoRows):
		// Preserve a legacy processor summary until an accepted description
		// exists. This migration deliberately does not reinterpret old derived
		// summaries as user-approved annotations.
		if _, err := tx.Exec(ctx, `
			UPDATE files
			   SET tags = $1,
			       updated_at = now()
			 WHERE id = $2
		`, tags, fileID); err != nil {
			return fmt.Errorf("update file tag projection: %w", err)
		}
	default:
		return fmt.Errorf("load accepted description: %w", descriptionErr)
	}
	return nil
}

// RefreshReviewableDescriptionProjections keeps legacy description columns
// aligned with review state. Accepted descriptions take precedence over
// pending captions; rejected and superseded values are removed from both
// projections when they match the legacy summary.
func RefreshReviewableDescriptionProjections(
	ctx context.Context,
	tx pgx.Tx,
	fileID uuid.UUID,
) error {
	if _, err := tx.Exec(ctx, `
		UPDATE files AS target
		   SET summary = COALESCE(
				(
					SELECT value_text
					  FROM file_annotations
					 WHERE file_id = $1
					   AND kind = 'description'
					   AND status = 'accepted'
					 ORDER BY COALESCE(decided_at, updated_at) DESC,
					          confidence DESC,
					          created_at DESC,
					          id DESC
					 LIMIT 1
				),
				CASE
					WHEN target.summary IS NOT NULL
					 AND EXISTS (
						SELECT 1
						  FROM file_annotations
						 WHERE file_id = $1
						   AND kind = 'description'
						   AND status IN ('rejected', 'superseded')
						   AND value_text = target.summary
					 )
					THEN NULL
					ELSE target.summary
				END
		   ),
		       caption = (
				SELECT value_text
				  FROM file_annotations
				 WHERE file_id = $1
				   AND kind = 'description'
				   AND status IN ('accepted', 'pending')
				 ORDER BY
				       CASE WHEN status = 'accepted' THEN 0 ELSE 1 END,
				       CASE WHEN status = 'accepted'
				            THEN COALESCE(decided_at, updated_at) END DESC NULLS LAST,
				       CASE WHEN status = 'accepted'
				            THEN confidence END DESC NULLS LAST,
				       CASE WHEN status = 'pending'
				            THEN updated_at END DESC NULLS LAST,
				       CASE WHEN status = 'pending'
				            THEN confidence END DESC NULLS LAST,
				       created_at DESC,
				       id DESC
				 LIMIT 1
		   )
		 WHERE id = $1
	`, fileID); err != nil {
		return fmt.Errorf("update reviewable description projections: %w", err)
	}
	return nil
}

func mergeUniqueTags(groups ...[]string) []string {
	out := make([]string, 0)
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, tag := range group {
			if _, exists := seen[tag]; exists {
				continue
			}
			seen[tag] = struct{}{}
			out = append(out, tag)
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}

func annotationPathAllowed(candidate string, allowedPaths []string) bool {
	if len(allowedPaths) == 0 {
		return true
	}
	normalizedCandidate, err := pathx.Normalize(candidate)
	if err != nil {
		return false
	}
	for _, raw := range allowedPaths {
		if raw == "" {
			continue
		}
		allowed, err := pathx.Normalize(raw)
		if err != nil {
			continue
		}
		if allowed == pathx.Root ||
			pathx.IsDescendantOrSelf(normalizedCandidate, allowed) {
			return true
		}
	}
	return false
}

func annotationScanDestinations(annotation *Annotation) []any {
	return []any{
		&annotation.ID,
		&annotation.FileID,
		&annotation.StableKey,
		&annotation.Kind,
		&annotation.ValueText,
		&annotation.Confidence,
		&annotation.Source,
		&annotation.Provider,
		&annotation.Processor,
		&annotation.AnalysisVersion,
		&annotation.Status,
		&annotation.StateVersion,
		&annotation.DecidedByUserID,
		&annotation.DecidedAt,
		&annotation.CreatedAt,
		&annotation.UpdatedAt,
	}
}

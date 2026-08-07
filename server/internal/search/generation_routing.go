package search

import (
	"context"

	"github.com/google/uuid"
)

// ActiveGeneration identifies one comparable vector space that ANN queries
// should use instead of the legacy embeddings tables. The SearchResolver in
// the indexgeneration package produces these.
type ActiveGeneration struct {
	GenerationID    uuid.UUID
	Provider        string
	ModelRevision   string
	OutputDimension int
	RouteKind       string
}

// GenerationResolver returns the active generation for a given owner and
// route. It returns (nil, nil) when no generation is active — falling back to
// legacy tables. The search package defines this interface; the concrete
// implementation lives in the indexgeneration package to avoid circular imports.
type GenerationResolver interface {
	ActiveForOwner(ctx context.Context, ownerID uuid.UUID, routeKind string) (*ActiveGeneration, error)
}

// SetGenerationResolver wires the generation-aware ANN routing. It is safe to
// call on a nil service (memd startup order is not guaranteed).
func (s *Service) SetGenerationResolver(resolver GenerationResolver) {
	if s == nil {
		return
	}
	s.generations = resolver
}

// resolveActiveGeneration returns the active generation for the current query
// owner and route. It silently returns nil on error or when no generation is
// active so the caller falls back to legacy tables.
func (s *Service) resolveActiveGeneration(ctx context.Context, ownerID uuid.UUID, routeKind string) *ActiveGeneration {
	if s.generations == nil {
		return nil
	}
	gen, err := s.generations.ActiveForOwner(ctx, ownerID, routeKind)
	if err != nil || gen == nil {
		return nil
	}
	return gen
}

// runTextANNGeneration queries index_generation_vectors for the active text
// generation. This is the generation-aware equivalent of runTextANN. It uses
// the same scoring/dedup logic but reads from the versioned vector table.
func (s *Service) runTextANNGeneration(
	ctx context.Context,
	q Query,
	vec []float32,
	gen *ActiveGeneration,
) ([]Hit, error) {
	// The generation vectors table is populated by Worker execution (a later
	// slice). Until that is wired, fall back to legacy ANN which remains
	// correct when no generation vectors have been written yet.
	_ = gen
	return s.runTextANN(ctx, q, vec)
}

// runVisualANNGeneration is the generation-aware equivalent of runVisualANN.
func (s *Service) runVisualANNGeneration(
	ctx context.Context,
	q Query,
	vec []float32,
	gen *ActiveGeneration,
) ([]Hit, error) {
	_ = gen
	return s.runVisualANN(ctx, q, vec)
}

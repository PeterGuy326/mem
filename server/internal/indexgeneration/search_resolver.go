package indexgeneration

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/search"
)

// SearchResolver implements search.GenerationResolver using the concrete
// index_generations tables. It lives in this package to avoid a circular import
// between search and indexgeneration.
type SearchResolver struct {
	pool *pgxpool.Pool
}

// NewSearchResolver constructs a SearchResolver backed by the given pool.
func NewSearchResolver(pool *pgxpool.Pool) *SearchResolver {
	return &SearchResolver{pool: pool}
}

// ActiveForOwner returns the single active generation for the given resource
// owner and route kind. It returns (nil, nil) when no generation is active.
func (r *SearchResolver) ActiveForOwner(
	ctx context.Context,
	ownerID uuid.UUID,
	routeKind string,
) (*search.ActiveGeneration, error) {
	const q = `
		SELECT g.id, g.provider, g.model_revision, g.output_dimension, g.route_kind
		FROM index_generations g
		JOIN index_generation_builds b ON b.id = g.build_id AND b.workspace_id = g.workspace_id
		JOIN workspaces w ON w.id = b.workspace_id
		WHERE w.resource_owner_user_id = $1
		  AND g.route_kind = $2
		  AND g.state = 'active'
		LIMIT 1
	`
	var gen search.ActiveGeneration
	err := r.pool.QueryRow(ctx, q, ownerID, routeKind).Scan(
		&gen.GenerationID,
		&gen.Provider,
		&gen.ModelRevision,
		&gen.OutputDimension,
		&gen.RouteKind,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &gen, nil
}

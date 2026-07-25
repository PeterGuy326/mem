// Package workspace resolves workspace membership to the existing per-user resource owner model.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

var (
	ErrNotFound  = errors.New("workspace not found")
	ErrForbidden = errors.New("workspace membership required")
)

type Workspace struct {
	ID                  uuid.UUID `json:"id"`
	Name                string    `json:"name"`
	ResourceOwnerUserID uuid.UUID `json:"resource_owner_user_id"`
	Role                string    `json:"role"`
	CreatedAt           time.Time `json:"created_at"`
}

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Workspace, error) {
	rows, err := s.pool.Query(ctx, `SELECT w.id, w.name, w.resource_owner_user_id, m.role, w.created_at
		FROM workspace_memberships m JOIN workspaces w ON w.id = m.workspace_id
		WHERE m.user_id = $1 ORDER BY w.created_at, w.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.ResourceOwnerUserID, &w.Role, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Service) Resolve(ctx context.Context, userID uuid.UUID, requested *uuid.UUID) (*Workspace, error) {
	var row pgx.Row
	if requested == nil {
		row = s.pool.QueryRow(ctx, `SELECT w.id, w.name, w.resource_owner_user_id, m.role, w.created_at
			FROM workspace_memberships m JOIN workspaces w ON w.id = m.workspace_id
			WHERE m.user_id = $1
			ORDER BY (w.resource_owner_user_id = $1) DESC, w.created_at, w.id LIMIT 1`, userID)
	} else {
		row = s.pool.QueryRow(ctx, `SELECT w.id, w.name, w.resource_owner_user_id, m.role, w.created_at
			FROM workspace_memberships m JOIN workspaces w ON w.id = m.workspace_id
			WHERE m.user_id = $1 AND w.id = $2`, userID, *requested)
	}
	var w Workspace
	if err := row.Scan(&w.ID, &w.Name, &w.ResourceOwnerUserID, &w.Role, &w.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if requested != nil {
				return nil, ErrForbidden
			}
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	return &w, nil
}

func CanDelete(role string) bool         { return role == RoleOwner || role == RoleAdmin }
func CanModifyProvider(role string) bool { return role == RoleOwner || role == RoleAdmin }

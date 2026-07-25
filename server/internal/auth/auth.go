// Package auth implements the mem Token model (SPEC §3 F7, §6.1).
//
// Tokens are random 32-byte secrets, surfaced once at creation in the form
// `mem_<base64url>`. The database only stores a SHA-256 hash; tokens cannot be
// recovered after creation. Sessions (for dev login) reuse the same model.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// ScopeSearch / ScopeRead / ... are the canonical scope strings (SPEC §3 F7.3).
const (
	ScopeSearch = "search"
	ScopeRead   = "read"
	ScopeWrite  = "write"
	ScopeDelete = "delete"
	ScopeAdmin  = "admin"
)

// AllScopes lists the supported scopes in their canonical order.
var AllScopes = []string{ScopeSearch, ScopeRead, ScopeWrite, ScopeDelete, ScopeAdmin}

// User represents a row in the users table.
type User struct {
	ID        uuid.UUID
	Email     string
	CreatedAt time.Time
}

// Token represents a row in the tokens table (without the plaintext secret).
type Token struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Name       string
	Scopes     []string
	Paths      []string
	RedactPII  bool
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

// Service wires user + token persistence.
type Service struct {
	pool *pgxpool.Pool
}

// New constructs a Service.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// CreateUser provisions a user and personal workspace. It preserves the
// historical open-registration behavior for existing callers.
func (s *Service) CreateUser(ctx context.Context, email, password string) (*User, error) {
	return s.CreateUserWithRegistration(ctx, email, password, "open")
}

// CreateUserWithRegistration atomically creates the user, their personal
// workspace, and owner membership under the requested registration policy.
func (s *Service) CreateUserWithRegistration(ctx context.Context, email, password, mode string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return nil, errors.New("email and password are required")
	}
	if mode == "disabled" {
		return nil, ErrRegistrationDisabled
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("bcrypt: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin registration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if mode == "first_user" {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(72466301)`); err != nil {
			return nil, fmt.Errorf("lock registration: %w", err)
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check registration: %w", err)
		}
		if exists {
			return nil, ErrRegistrationDisabled
		}
	} else if mode != "open" {
		return nil, fmt.Errorf("invalid registration mode %q", mode)
	}

	id := uuid.New()
	created := time.Now().UTC()
	if _, err = tx.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, created_at) VALUES ($1, $2, $3, $4)`,
		id, email, string(hash), created); err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	workspaceID := uuid.New()
	name := strings.SplitN(email, "@", 2)[0] + "'s workspace"
	if _, err = tx.Exec(ctx,
		`INSERT INTO workspaces (id, name, resource_owner_user_id, created_at) VALUES ($1, $2, $3, $4)`,
		workspaceID, name, id, created); err != nil {
		return nil, fmt.Errorf("insert workspace: %w", err)
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO workspace_memberships (workspace_id, user_id, role, created_at) VALUES ($1, $2, 'owner', $3)`,
		workspaceID, id, created); err != nil {
		return nil, fmt.Errorf("insert workspace membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit registration: %w", err)
	}
	return &User{ID: id, Email: email, CreatedAt: created}, nil
}

// VerifyPassword returns the user if email/password match.
func (s *Service) VerifyPassword(ctx context.Context, email, password string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var u User
	var hash string
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, created_at FROM users WHERE email = $1`,
		email,
	).Scan(&u.ID, &u.Email, &hash, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("select user: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}
	return &u, nil
}

// CreateToken issues a new API token. Returns plaintext (one-time only) +
// the stored Token row.
//
// `scopes` is validated against AllScopes; empty defaults to [read].
// `expires` may be nil for no expiry.
func (s *Service) CreateToken(ctx context.Context, userID uuid.UUID, name string, scopes []string, paths []string, expires *time.Time, redactPII bool) (plaintext string, t *Token, err error) {
	if name == "" {
		return "", nil, errors.New("token name is required")
	}
	if len(scopes) == 0 {
		scopes = []string{ScopeRead}
	}
	for _, sc := range scopes {
		if !validScope(sc) {
			return "", nil, fmt.Errorf("invalid scope: %q", sc)
		}
	}
	// `paths` column is text[] NOT NULL DEFAULT '{}'. pgx encodes a Go nil
	// slice as SQL NULL — which violates the NOT NULL constraint. Normalize
	// to an empty slice so the SQL value is '{}' instead of NULL.
	if paths == nil {
		paths = []string{}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("rand: %w", err)
	}
	plaintext = "mem_" + base64.RawURLEncoding.EncodeToString(raw)
	hash := HashToken(plaintext)

	id := uuid.New()
	created := time.Now().UTC()
	_, err = s.pool.Exec(ctx,
		`INSERT INTO tokens (id, user_id, name, hash, scopes, paths, expires_at, redact_pii, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, userID, name, hash, scopes, paths, expires, redactPII, created,
	)
	if err != nil {
		return "", nil, fmt.Errorf("insert token: %w", err)
	}
	t = &Token{
		ID: id, UserID: userID, Name: name,
		Scopes: scopes, Paths: paths,
		ExpiresAt: expires, RedactPII: redactPII, CreatedAt: created,
	}
	return plaintext, t, nil
}

// ListTokens returns all tokens for a user.
func (s *Service) ListTokens(ctx context.Context, userID uuid.UUID) ([]Token, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, name, scopes, paths, expires_at, redact_pii, last_used_at, created_at
		 FROM tokens WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("query tokens: %w", err)
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Scopes, &t.Paths, &t.ExpiresAt, &t.RedactPII, &t.LastUsedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken deletes a token by id (must belong to userID).
func (s *Service) RevokeToken(ctx context.Context, userID, tokenID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM tokens WHERE id = $1 AND user_id = $2`, tokenID, userID)
	if err != nil {
		return fmt.Errorf("delete token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTokenNotFound
	}
	return nil
}

// ResolveToken looks up a plaintext token, returns the user + token info, and
// validates expiry. Updates last_used_at best-effort.
func (s *Service) ResolveToken(ctx context.Context, plaintext string) (*User, *Token, error) {
	if plaintext == "" {
		return nil, nil, ErrInvalidToken
	}
	hash := HashToken(plaintext)
	var t Token
	var u User
	err := s.pool.QueryRow(ctx,
		`SELECT t.id, t.user_id, t.name, t.scopes, t.paths, t.expires_at, t.redact_pii, t.last_used_at, t.created_at,
		        u.email, u.created_at
		 FROM tokens t JOIN users u ON u.id = t.user_id
		 WHERE t.hash = $1`, hash,
	).Scan(&t.ID, &t.UserID, &t.Name, &t.Scopes, &t.Paths, &t.ExpiresAt, &t.RedactPII, &t.LastUsedAt, &t.CreatedAt,
		&u.Email, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrInvalidToken
		}
		return nil, nil, fmt.Errorf("query token: %w", err)
	}
	u.ID = t.UserID
	if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
		return nil, nil, ErrTokenExpired
	}
	// best-effort last_used_at update; ignore errors
	_, _ = s.pool.Exec(ctx, `UPDATE tokens SET last_used_at = now() WHERE id = $1`, t.ID)
	return &u, &t, nil
}

// HasScope returns true if the token grants scope (or is admin).
func HasScope(t *Token, scope string) bool {
	if t == nil {
		return false
	}
	for _, s := range t.Scopes {
		if s == ScopeAdmin || s == scope {
			return true
		}
	}
	return false
}

// HashToken returns the hex SHA-256 of the plaintext token.
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func validScope(s string) bool {
	for _, x := range AllScopes {
		if x == s {
			return true
		}
	}
	return false
}

// Sentinel errors for the auth layer.
var (
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrInvalidToken         = errors.New("invalid token")
	ErrTokenExpired         = errors.New("token expired")
	ErrTokenNotFound        = errors.New("token not found")
	ErrForbidden            = errors.New("forbidden")
	ErrRegistrationDisabled = errors.New("registration is disabled")
)

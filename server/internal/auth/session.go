package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Session represents a server-managed browser session row.
type Session struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	CSRFToken   string
	CreatedAt   time.Time
	LastActive  time.Time
	ExpiresAt   time.Time
	RotatedFrom *uuid.UUID
	RevokedAt   *time.Time
}

// SessionConfig holds tunable session lifecycle parameters.
type SessionConfig struct {
	AbsoluteExpiry time.Duration // max session lifetime (default 24h)
	IdleTimeout    time.Duration // inactivity timeout (default 2h)
	RotateAfter    time.Duration // rotate token after this duration (default 1h)
	CookieName     string        // session cookie name
	CookieDomain   string        // cookie domain; empty = request host
	Secure         bool          // Secure flag (should be true in prod)
}

// DefaultSessionConfig returns safe production defaults.
func DefaultSessionConfig() SessionConfig {
	return SessionConfig{
		AbsoluteExpiry: 24 * time.Hour,
		IdleTimeout:    2 * time.Hour,
		RotateAfter:    1 * time.Hour,
		CookieName:     "__mem_session",
		Secure:         true,
	}
}

// Sentinel errors for the session layer.
var (
	ErrSessionExpired  = errors.New("session expired")
	ErrSessionRevoked  = errors.New("session revoked")
	ErrSessionIdle     = errors.New("session idle timeout")
	ErrSessionNotFound = errors.New("session not found")
	ErrCSRFMismatch    = errors.New("CSRF token mismatch")
)

// CreateSession provisions a new browser session for the given user.
// Returns the plaintext session token (to be set as cookie) and session metadata.
func (s *Service) CreateSession(ctx context.Context, userID uuid.UUID, cfg SessionConfig) (plaintext string, sess *Session, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("session rand: %w", err)
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	hash := hashSession(plaintext)

	csrfRaw := make([]byte, 32)
	if _, err := rand.Read(csrfRaw); err != nil {
		return "", nil, fmt.Errorf("csrf rand: %w", err)
	}
	csrfToken := base64.RawURLEncoding.EncodeToString(csrfRaw)

	now := time.Now().UTC()
	expiresAt := now.Add(cfg.AbsoluteExpiry)

	id := uuid.New()
	_, err = s.pool.Exec(ctx,
		`INSERT INTO sessions (id, user_id, token_hash, csrf_token, created_at, last_active, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $5, $6)`,
		id, userID, hash, csrfToken, now, expiresAt,
	)
	if err != nil {
		return "", nil, fmt.Errorf("insert session: %w", err)
	}

	sess = &Session{
		ID:         id,
		UserID:     userID,
		CSRFToken:  csrfToken,
		CreatedAt:  now,
		LastActive: now,
		ExpiresAt:  expiresAt,
	}
	return plaintext, sess, nil
}

// ResolveSession validates a session token and returns the session if active.
// It also updates last_active timestamp. Returns rotation hint if needed.
func (s *Service) ResolveSession(ctx context.Context, plaintext string, cfg SessionConfig) (*Session, *User, bool, error) {
	if plaintext == "" {
		return nil, nil, false, ErrSessionNotFound
	}
	hash := hashSession(plaintext)

	var sess Session
	var u User
	err := s.pool.QueryRow(ctx,
		`SELECT s.id, s.user_id, s.csrf_token, s.created_at, s.last_active, s.expires_at, s.rotated_from, s.revoked_at,
		        u.email, u.created_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = $1`, hash,
	).Scan(&sess.ID, &sess.UserID, &sess.CSRFToken, &sess.CreatedAt, &sess.LastActive, &sess.ExpiresAt, &sess.RotatedFrom, &sess.RevokedAt,
		&u.Email, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, false, ErrSessionNotFound
		}
		return nil, nil, false, fmt.Errorf("query session: %w", err)
	}
	u.ID = sess.UserID

	if sess.RevokedAt != nil {
		return nil, nil, false, ErrSessionRevoked
	}

	now := time.Now().UTC()
	if now.After(sess.ExpiresAt) {
		return nil, nil, false, ErrSessionExpired
	}
	if now.Sub(sess.LastActive) > cfg.IdleTimeout {
		return nil, nil, false, ErrSessionIdle
	}

	// Update last_active (best-effort)
	_, _ = s.pool.Exec(ctx, `UPDATE sessions SET last_active = $1 WHERE id = $2`, now, sess.ID)
	sess.LastActive = now

	// Signal rotation needed if token is old enough
	needsRotation := now.Sub(sess.CreatedAt) > cfg.RotateAfter

	return &sess, &u, needsRotation, nil
}

// RotateSession creates a new session and revokes the old one atomically.
func (s *Service) RotateSession(ctx context.Context, oldSessionID, userID uuid.UUID, cfg SessionConfig) (plaintext string, sess *Session, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("session rand: %w", err)
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	hash := hashSession(plaintext)

	csrfRaw := make([]byte, 32)
	if _, err := rand.Read(csrfRaw); err != nil {
		return "", nil, fmt.Errorf("csrf rand: %w", err)
	}
	csrfToken := base64.RawURLEncoding.EncodeToString(csrfRaw)

	now := time.Now().UTC()
	expiresAt := now.Add(cfg.AbsoluteExpiry)
	id := uuid.New()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("begin rotate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Revoke old session
	_, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at = $1 WHERE id = $2`, now, oldSessionID)
	if err != nil {
		return "", nil, fmt.Errorf("revoke old session: %w", err)
	}

	// Create new session
	_, err = tx.Exec(ctx,
		`INSERT INTO sessions (id, user_id, token_hash, csrf_token, created_at, last_active, expires_at, rotated_from)
		 VALUES ($1, $2, $3, $4, $5, $5, $6, $7)`,
		id, userID, hash, csrfToken, now, expiresAt, oldSessionID,
	)
	if err != nil {
		return "", nil, fmt.Errorf("insert rotated session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", nil, fmt.Errorf("commit rotate: %w", err)
	}

	sess = &Session{
		ID:          id,
		UserID:      userID,
		CSRFToken:   csrfToken,
		CreatedAt:   now,
		LastActive:  now,
		ExpiresAt:   expiresAt,
		RotatedFrom: &oldSessionID,
	}
	return plaintext, sess, nil
}

// RevokeSession marks a single session as revoked (logout).
func (s *Service) RevokeSession(ctx context.Context, sessionID uuid.UUID) error {
	now := time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = $1 WHERE id = $2 AND revoked_at IS NULL`,
		now, sessionID)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// RevokeAllSessions revokes all active sessions for a user (password change, admin action).
func (s *Service) RevokeAllSessions(ctx context.Context, userID uuid.UUID) (int64, error) {
	now := time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = $1 WHERE user_id = $2 AND revoked_at IS NULL`,
		now, userID)
	if err != nil {
		return 0, fmt.Errorf("revoke all sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListSessions returns all active (non-revoked, non-expired) sessions for a user.
func (s *Service) ListSessions(ctx context.Context, userID uuid.UUID) ([]Session, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, csrf_token, created_at, last_active, expires_at, rotated_from, revoked_at
		 FROM sessions
		 WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
		 ORDER BY last_active DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.CSRFToken, &sess.CreatedAt, &sess.LastActive, &sess.ExpiresAt, &sess.RotatedFrom, &sess.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// SessionCookie builds an http.Cookie for the session.
func SessionCookie(token string, cfg SessionConfig, maxAge time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     cfg.CookieName,
		Value:    token,
		Path:     "/",
		Domain:   cfg.CookieDomain,
		MaxAge:   int(maxAge.Seconds()),
		Secure:   cfg.Secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// ClearSessionCookie returns a cookie that expires the session cookie.
func ClearSessionCookie(cfg SessionConfig) *http.Cookie {
	return &http.Cookie{
		Name:     cfg.CookieName,
		Value:    "",
		Path:     "/",
		Domain:   cfg.CookieDomain,
		MaxAge:   -1,
		Secure:   cfg.Secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func hashSession(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

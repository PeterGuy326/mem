package auth

import (
	"net/http"
	"testing"
	"time"
)

func TestDefaultSessionConfig(t *testing.T) {
	cfg := DefaultSessionConfig()
	if cfg.AbsoluteExpiry != 24*time.Hour {
		t.Errorf("expected 24h absolute expiry, got %v", cfg.AbsoluteExpiry)
	}
	if cfg.IdleTimeout != 2*time.Hour {
		t.Errorf("expected 2h idle timeout, got %v", cfg.IdleTimeout)
	}
	if cfg.RotateAfter != 1*time.Hour {
		t.Errorf("expected 1h rotate, got %v", cfg.RotateAfter)
	}
	if cfg.CookieName != "__mem_session" {
		t.Errorf("unexpected cookie name: %s", cfg.CookieName)
	}
	if !cfg.Secure {
		t.Error("expected Secure=true by default")
	}
}

func TestSessionCookie(t *testing.T) {
	cfg := DefaultSessionConfig()
	c := SessionCookie("test-token", cfg, 24*time.Hour)

	if c.Name != "__mem_session" {
		t.Errorf("unexpected cookie name: %s", c.Name)
	}
	if c.Value != "test-token" {
		t.Errorf("unexpected value: %s", c.Value)
	}
	if !c.HttpOnly {
		t.Error("expected HttpOnly")
	}
	if !c.Secure {
		t.Error("expected Secure")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSiteLax, got %v", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("unexpected path: %s", c.Path)
	}
}

func TestClearSessionCookie(t *testing.T) {
	cfg := DefaultSessionConfig()
	c := ClearSessionCookie(cfg)

	if c.Value != "" {
		t.Errorf("expected empty value, got %s", c.Value)
	}
	if c.MaxAge != -1 {
		t.Errorf("expected MaxAge -1, got %d", c.MaxAge)
	}
}

func TestHashSession(t *testing.T) {
	h1 := hashSession("token-a")
	h2 := hashSession("token-a")
	h3 := hashSession("token-b")

	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("different inputs should produce different hashes")
	}
	if len(h1) != 64 {
		t.Errorf("expected 64 hex chars, got %d", len(h1))
	}
}

package config

import (
	"testing"
	"time"
)

func TestLoadPolicyDefaultsAndSessionTTL(t *testing.T) {
	t.Setenv("MEM_DEPLOYMENT_MODE", "")
	t.Setenv("MEM_REGISTRATION_MODE", "")
	t.Setenv("MEM_SESSION_TTL", "90m")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeploymentMode != "private" || cfg.RegistrationMode != "open" {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if cfg.SessionTTL != 90*time.Minute {
		t.Fatalf("session TTL = %s", cfg.SessionTTL)
	}
}

func TestLoadRejectsInvalidPolicies(t *testing.T) {
	for key, value := range map[string]string{
		"MEM_DEPLOYMENT_MODE":   "public",
		"MEM_REGISTRATION_MODE": "invite",
	} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := Load(); err == nil {
				t.Fatalf("expected %s validation error", key)
			}
		})
	}
}

func TestLoadCORSOrigins(t *testing.T) {
	t.Setenv("MEM_CORS_ORIGINS", " https://app.example.com , http://localhost:5174 ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://app.example.com", "http://localhost:5174"}
	if len(cfg.CORSOrigins) != len(want) || cfg.CORSOrigins[0] != want[0] || cfg.CORSOrigins[1] != want[1] {
		t.Fatalf("CORSOrigins = %#v", cfg.CORSOrigins)
	}

	t.Setenv("MEM_CORS_ORIGINS", "")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CORSOrigins) != 0 {
		t.Fatalf("empty env should disable CORS, got %#v", cfg.CORSOrigins)
	}

	for _, bad := range []string{"app.example.com", "https://app.example.com/"} {
		t.Setenv("MEM_CORS_ORIGINS", bad)
		if _, err := Load(); err == nil {
			t.Fatalf("expected validation error for %q", bad)
		}
	}
}

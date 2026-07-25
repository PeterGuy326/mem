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

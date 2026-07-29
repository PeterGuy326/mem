package config

import (
	"reflect"
	"testing"
	"time"
)

func TestLoadPolicyDefaultsAndSessionTTL(t *testing.T) {
	t.Setenv("MEM_DEPLOYMENT_MODE", "")
	t.Setenv("MEM_REGISTRATION_MODE", "")
	t.Setenv("MEM_SESSION_TTL", "90m")
	t.Setenv("MEM_WORKSPACE_TRANSFER_TIMEOUT", "")
	t.Setenv("MEM_WORKSPACE_BUNDLE_MAX_BYTES", "")
	t.Setenv("MEM_WORKSPACE_TRANSFER_MAX_CONCURRENT", "")
	t.Setenv("MEM_WORKSPACE_TRANSFER_TMP_DIR", "")
	t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "")
	t.Setenv("MEM_MANAGED_EMBEDDING_RESERVATION_TTL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeploymentMode != "private" || cfg.RegistrationMode != "open" {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if !reflect.DeepEqual(cfg.AIProfiles, []string{"local-fast-v1"}) {
		t.Fatalf("AIProfiles = %#v", cfg.AIProfiles)
	}
	if cfg.SessionTTL != 90*time.Minute {
		t.Fatalf("session TTL = %s", cfg.SessionTTL)
	}
	if cfg.WorkspaceTransferTimeout != DefaultWorkspaceTransferTimeout ||
		cfg.WorkspaceBundleMaxBytes != DefaultWorkspaceBundleMaxBytes ||
		cfg.WorkspaceTransferMaxConcurrent != DefaultWorkspaceTransferMaxConcurrent ||
		cfg.ManagedEmbeddingReservationTTL != DefaultManagedEmbeddingReservationTTL ||
		cfg.WorkspaceTransferTmpDir != "" {
		t.Fatalf("unexpected workspace transfer defaults: %#v", cfg)
	}
}

func TestLoadAIProfilesAreAnOperatorAllowlist(t *testing.T) {
	t.Setenv("MEM_DEPLOYMENT_MODE", "saas")
	t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "openai:text-embedding-3-large")
	t.Setenv("MEM_AI_PROFILES", " local-fast-v1, idealab-quality-v1 ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"local-fast-v1", "idealab-quality-v1"}
	if !reflect.DeepEqual(cfg.AIProfiles, want) {
		t.Fatalf("AIProfiles = %#v, want %#v", cfg.AIProfiles, want)
	}

	for _, raw := range []string{"unknown-v1", "local-fast-v1,local-fast-v1", ","} {
		t.Setenv("MEM_AI_PROFILES", raw)
		if _, err := Load(); err == nil {
			t.Fatalf("MEM_AI_PROFILES=%q unexpectedly loaded", raw)
		}
	}
}

func TestLoadPrivateRejectsManagedQualityProfile(t *testing.T) {
	t.Setenv("MEM_DEPLOYMENT_MODE", "private")
	t.Setenv("MEM_AI_PROFILES", "idealab-quality-v1")
	if _, err := Load(); err == nil {
		t.Fatal("private deployment accepted a platform-managed quality profile")
	}
}

func TestLoadSaaSIdealabQualityRequiresExactManagedEmbeddingSpec(t *testing.T) {
	t.Setenv("MEM_DEPLOYMENT_MODE", "saas")
	t.Setenv("MEM_AI_PROFILES", "local-fast-v1,idealab-quality-v1")
	t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "openai:text-embedding-3-small")
	if _, err := Load(); err == nil {
		t.Fatal("quality profile accepted a different managed embedding spec")
	}
	t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "openai:text-embedding-3-large")
	if _, err := Load(); err != nil {
		t.Fatalf("quality profile with exact embedding spec: %v", err)
	}
}

func TestLoadSaaSRequiresExactManagedEmbeddingProvider(t *testing.T) {
	t.Setenv("MEM_DEPLOYMENT_MODE", "saas")
	t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing managed embedding provider error")
	}

	t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "openai:text-embedding-3-small")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ManagedEmbeddingProvider != "openai:text-embedding-3-small" {
		t.Fatalf("managed provider = %q", cfg.ManagedEmbeddingProvider)
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

func TestLoadWorkspaceTransferOverrides(t *testing.T) {
	t.Setenv("MEM_WORKSPACE_TRANSFER_TIMEOUT", "45m")
	t.Setenv("MEM_WORKSPACE_BUNDLE_MAX_BYTES", "1073741824")
	t.Setenv("MEM_WORKSPACE_TRANSFER_MAX_CONCURRENT", "4")
	t.Setenv("MEM_WORKSPACE_TRANSFER_TMP_DIR", " /var/tmp/mem-transfer ")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkspaceTransferTimeout != 45*time.Minute {
		t.Fatalf("transfer timeout = %s", cfg.WorkspaceTransferTimeout)
	}
	if cfg.WorkspaceBundleMaxBytes != 1<<30 {
		t.Fatalf("bundle max bytes = %d", cfg.WorkspaceBundleMaxBytes)
	}
	if cfg.WorkspaceTransferMaxConcurrent != 4 {
		t.Fatalf(
			"transfer max concurrent = %d",
			cfg.WorkspaceTransferMaxConcurrent,
		)
	}
	if cfg.WorkspaceTransferTmpDir != "/var/tmp/mem-transfer" {
		t.Fatalf("transfer tmp dir = %q", cfg.WorkspaceTransferTmpDir)
	}
}

func TestLoadRejectsInvalidWorkspaceTransferResources(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{"MEM_WORKSPACE_TRANSFER_TIMEOUT", "not-a-duration"},
		{"MEM_WORKSPACE_TRANSFER_TIMEOUT", "0s"},
		{"MEM_WORKSPACE_TRANSFER_TIMEOUT", "-1s"},
		{"MEM_WORKSPACE_BUNDLE_MAX_BYTES", "nope"},
		{"MEM_WORKSPACE_BUNDLE_MAX_BYTES", "0"},
		{"MEM_WORKSPACE_BUNDLE_MAX_BYTES", "-1"},
		{"MEM_WORKSPACE_TRANSFER_MAX_CONCURRENT", "nope"},
		{"MEM_WORKSPACE_TRANSFER_MAX_CONCURRENT", "0"},
		{"MEM_WORKSPACE_TRANSFER_MAX_CONCURRENT", "-1"},
		{"MEM_MANAGED_EMBEDDING_RESERVATION_TTL", "not-a-duration"},
		{"MEM_MANAGED_EMBEDDING_RESERVATION_TTL", "0s"},
		{"MEM_MANAGED_EMBEDDING_RESERVATION_TTL", "-1s"},
	}
	for _, test := range tests {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			t.Setenv("MEM_WORKSPACE_TRANSFER_TIMEOUT", "")
			t.Setenv("MEM_WORKSPACE_BUNDLE_MAX_BYTES", "")
			t.Setenv("MEM_WORKSPACE_TRANSFER_MAX_CONCURRENT", "")
			t.Setenv("MEM_MANAGED_EMBEDDING_RESERVATION_TTL", "")
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("expected validation error for %s=%q", test.key, test.value)
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

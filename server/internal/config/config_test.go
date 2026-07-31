package config

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"
)

func setSaaSWorkerAuth(t *testing.T) {
	t.Helper()
	t.Setenv("MEM_WORKER_GRPC", "worker.internal:50051")
	t.Setenv("MEM_WORKER_AUTH_KEY_ID", "memd-primary")
	t.Setenv(
		"MEM_WORKER_AUTH_KEY_B64",
		base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))),
	)
}

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
	if !reflect.DeepEqual(cfg.AIProfiles, []string{"local-fast-v1", "local-fast-v2"}) {
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
	setSaaSWorkerAuth(t)
	t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "idealab:text-embedding-3-large")
	t.Setenv("MEM_AI_PROFILES", " local-fast-v2, idealab-quality-v2 ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"local-fast-v2", "idealab-quality-v2"}
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

func TestLoadPrivateRejectsManagedQualityProfiles(t *testing.T) {
	for _, profileID := range []string{"idealab-quality-v1", "idealab-quality-v2"} {
		t.Run(profileID, func(t *testing.T) {
			t.Setenv("MEM_DEPLOYMENT_MODE", "private")
			t.Setenv("MEM_AI_PROFILES", profileID)
			if _, err := Load(); err == nil {
				t.Fatal("private deployment accepted a platform-managed quality profile")
			}
		})
	}
}

func TestLoadSaaSIdealabQualityRequiresExactManagedEmbeddingSpec(t *testing.T) {
	tests := []struct {
		name     string
		profile  string
		exact    string
		mismatch string
	}{
		{
			name:     "legacy V1 compatibility",
			profile:  "idealab-quality-v1",
			exact:    "openai:text-embedding-3-large",
			mismatch: "idealab:text-embedding-3-large",
		},
		{
			name:     "current V2",
			profile:  "idealab-quality-v2",
			exact:    "idealab:text-embedding-3-large",
			mismatch: "openai:text-embedding-3-large",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("MEM_DEPLOYMENT_MODE", "saas")
			setSaaSWorkerAuth(t)
			t.Setenv("MEM_AI_PROFILES", "local-fast-v2,"+test.profile)
			if test.profile == "idealab-quality-v1" {
				t.Setenv("MEM_OPENAI_MANAGED_BINDING", "true")
			} else {
				t.Setenv("MEM_OPENAI_MANAGED_BINDING", "")
			}
			t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", test.mismatch)
			if _, err := Load(); err == nil {
				t.Fatal("quality profile accepted a different managed embedding spec")
			}
			t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", test.exact)
			if _, err := Load(); err != nil {
				t.Fatalf("quality profile with exact embedding spec: %v", err)
			}
		})
	}
}

func TestLoadLegacyManagedProfileRequiresExplicitBindingClassification(t *testing.T) {
	t.Setenv("MEM_DEPLOYMENT_MODE", "saas")
	setSaaSWorkerAuth(t)
	t.Setenv("MEM_AI_PROFILES", "idealab-quality-v1")
	t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "openai:text-embedding-3-large")
	t.Setenv("MEM_OPENAI_MANAGED_BINDING", "")
	if _, err := Load(); err == nil {
		t.Fatal("legacy V1 loaded without an explicit managed OpenAI binding")
	}
	t.Setenv("MEM_OPENAI_MANAGED_BINDING", "true")
	if _, err := Load(); err != nil {
		t.Fatalf("legacy V1 with explicit managed binding: %v", err)
	}
}

func TestLoadSupportsBothManagedProfileGenerationsDuringMigration(t *testing.T) {
	t.Setenv("MEM_DEPLOYMENT_MODE", "saas")
	setSaaSWorkerAuth(t)
	t.Setenv(
		"MEM_AI_PROFILES",
		"local-fast-v1,local-fast-v2,idealab-quality-v1,idealab-quality-v2",
	)
	t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "idealab:text-embedding-3-large")
	t.Setenv("MEM_OPENAI_MANAGED_BINDING", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"idealab:text-embedding-3-large",
		"openai:text-embedding-3-large",
	}
	if !reflect.DeepEqual(cfg.ManagedEmbeddingProviders, want) {
		t.Fatalf(
			"ManagedEmbeddingProviders = %#v, want %#v",
			cfg.ManagedEmbeddingProviders,
			want,
		)
	}
}

func TestLoadSaaSRequiresACompiledManagedEmbeddingProvider(t *testing.T) {
	t.Setenv("MEM_DEPLOYMENT_MODE", "saas")
	setSaaSWorkerAuth(t)
	t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing managed embedding provider error")
	}

	t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "openai:text-embedding-3-small")
	if _, err := Load(); err == nil {
		t.Fatal("SaaS accepted an arbitrary managed provider outside the catalog")
	}

	t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "idealab:text-embedding-3-large")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ManagedEmbeddingProvider != "idealab:text-embedding-3-large" {
		t.Fatalf("managed provider = %q", cfg.ManagedEmbeddingProvider)
	}
	if !reflect.DeepEqual(
		cfg.ManagedEmbeddingProviders,
		[]string{"idealab:text-embedding-3-large"},
	) {
		t.Fatalf("managed providers = %#v", cfg.ManagedEmbeddingProviders)
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

func TestLoadManagedReservationMinimumAppliesOnlyToSaaS(t *testing.T) {
	t.Setenv("MEM_MANAGED_EMBEDDING_RESERVATION_TTL", "5m")
	t.Setenv("MEM_DEPLOYMENT_MODE", "private")
	if _, err := Load(); err != nil {
		t.Fatalf("private deployment rejected a positive legacy TTL: %v", err)
	}

	t.Setenv("MEM_DEPLOYMENT_MODE", "saas")
	setSaaSWorkerAuth(t)
	t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "idealab:text-embedding-3-large")
	if _, err := Load(); err == nil {
		t.Fatal("SaaS deployment accepted a reservation TTL below the Worker timeout")
	}
}

func TestLoadWorkerAuthenticationContract(t *testing.T) {
	t.Run("saas requires Worker address and authentication", func(t *testing.T) {
		t.Setenv("MEM_DEPLOYMENT_MODE", "saas")
		t.Setenv("MEM_MANAGED_EMBEDDING_PROVIDER", "idealab:text-embedding-3-large")
		t.Setenv("MEM_WORKER_GRPC", "")
		t.Setenv("MEM_WORKER_AUTH_KEY_ID", "")
		t.Setenv("MEM_WORKER_AUTH_KEY_B64", "")
		if _, err := Load(); err == nil {
			t.Fatal("SaaS loaded without a Worker trust boundary")
		}
	})

	t.Run("invalid key material is rejected without echoing it", func(t *testing.T) {
		secret := "not-valid-base64-secret"
		t.Setenv("MEM_WORKER_AUTH_KEY_ID", "memd-primary")
		t.Setenv("MEM_WORKER_AUTH_KEY_B64", secret)
		_, err := Load()
		if err == nil {
			t.Fatal("invalid Worker authentication key loaded")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("configuration error leaked key material: %v", err)
		}
	})

	t.Run("private mode remains compatible without authentication", func(t *testing.T) {
		t.Setenv("MEM_DEPLOYMENT_MODE", "private")
		t.Setenv("MEM_WORKER_AUTH_KEY_ID", "")
		t.Setenv("MEM_WORKER_AUTH_KEY_B64", "")
		if _, err := Load(); err != nil {
			t.Fatalf("private configuration unexpectedly requires Worker auth: %v", err)
		}
	})
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

func setProductionConfig(t *testing.T) {
	t.Helper()
	t.Setenv("MEM_RUNTIME_PROFILE", "production")
	t.Setenv("MEM_AUTO_MIGRATE", "false")
	t.Setenv(
		"MEM_DB_URL",
		"postgres://mem_prod:database-secret@postgres:5432/mem?sslmode=disable",
	)
	t.Setenv("MEM_REDIS_URL", "redis://:redis-secret@redis:6379/0")
	t.Setenv("MEM_S3_ENDPOINT", "http://minio:9000")
	t.Setenv("MEM_S3_ACCESS_KEY", "mem-production")
	t.Setenv("MEM_S3_SECRET_KEY", "object-storage-secret")
	t.Setenv("MEM_REGISTRATION_MODE", "first_user")
	t.Setenv("MEM_CORS_ORIGINS", "")
	t.Setenv("MEM_WORKER_GRPC", "")
	t.Setenv("MEM_WORKER_AUTH_KEY_ID", "")
	t.Setenv("MEM_WORKER_AUTH_KEY_B64", "")
}

func TestLoadProductionRejectsDevelopmentDefaults(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"database", "MEM_DB_URL", "postgres://mem:mem@localhost:5432/mem?sslmode=disable"},
		{"redis default", "MEM_REDIS_URL", "redis://localhost:6479"},
		{"redis disabled", "MEM_REDIS_URL", ""},
		{"object endpoint", "MEM_S3_ENDPOINT", "http://localhost:9100"},
		{"object access key", "MEM_S3_ACCESS_KEY", "mem"},
		{"object secret", "MEM_S3_SECRET_KEY", "mem-minio-password"},
		{"open registration", "MEM_REGISTRATION_MODE", "open"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setProductionConfig(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf(
					"production runtime accepted development setting %s=%q",
					test.key,
					test.value,
				)
			}
		})
	}
}

func TestLoadProductionAcceptsExplicitModelFreeConfiguration(t *testing.T) {
	setProductionConfig(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RuntimeProfile != "production" {
		t.Fatalf("runtime profile = %q", cfg.RuntimeProfile)
	}
	if cfg.AutoMigrate {
		t.Fatal("MEM_AUTO_MIGRATE=false was not honored")
	}
	if cfg.WorkerGRPC != "" {
		t.Fatalf("model-free worker address = %q", cfg.WorkerGRPC)
	}
}

func TestLoadProductionRejectsAutomaticMigration(t *testing.T) {
	setProductionConfig(t)
	t.Setenv("MEM_AUTO_MIGRATE", "true")
	if _, err := Load(); err == nil {
		t.Fatal("production runtime accepted automatic migrations")
	}
}

func TestLoadProductionWorkerRequiresAuthentication(t *testing.T) {
	setProductionConfig(t)
	t.Setenv("MEM_WORKER_GRPC", "worker:50051")
	if _, err := Load(); err == nil {
		t.Fatal("production runtime accepted an unauthenticated Worker")
	}

	t.Setenv("MEM_WORKER_AUTH_KEY_ID", "memd-primary")
	t.Setenv(
		"MEM_WORKER_AUTH_KEY_B64",
		base64.StdEncoding.EncodeToString([]byte(strings.Repeat("p", 32))),
	)
	if _, err := Load(); err != nil {
		t.Fatalf("authenticated production Worker configuration: %v", err)
	}
}

func TestLoadProductionRejectsWildcardCORS(t *testing.T) {
	setProductionConfig(t)
	t.Setenv("MEM_CORS_ORIGINS", "*")
	if _, err := Load(); err == nil {
		t.Fatal("production runtime accepted wildcard CORS")
	}
}

func TestLoadRejectsInvalidRuntimeControls(t *testing.T) {
	t.Run("runtime profile", func(t *testing.T) {
		t.Setenv("MEM_RUNTIME_PROFILE", "staging")
		if _, err := Load(); err == nil {
			t.Fatal("invalid runtime profile loaded")
		}
	})
	t.Run("auto migrate", func(t *testing.T) {
		t.Setenv("MEM_AUTO_MIGRATE", "sometimes")
		if _, err := Load(); err == nil {
			t.Fatal("invalid auto-migrate value loaded")
		}
	})
}

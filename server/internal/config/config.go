// Package config loads memd configuration from environment variables.
//
// All keys are namespaced under MEM_ to avoid collisions. Defaults are tuned
// for the docker-compose local stack (see /docker-compose.yml).
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultWorkspaceTransferTimeout             = 30 * time.Minute
	DefaultWorkspaceBundleMaxBytes        int64 = 8 << 30
	DefaultWorkspaceTransferMaxConcurrent       = 2
	DefaultManagedEmbeddingReservationTTL       = 10 * time.Minute
	// Indexing permits one Worker RPC to run for five minutes. Reservations
	// must outlive that window so the reconciler cannot reclaim active paid
	// stages; the extra minute is a bounded settlement margin.
	MinimumManagedEmbeddingReservationTTL = 6 * time.Minute
)

var workerAuthKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Config is the resolved memd runtime configuration.
type Config struct {
	// HTTP
	HTTPAddr string // e.g. ":8787"

	// Runtime profile controls deployment-only safety checks. Development
	// preserves the local defaults; production rejects those defaults and
	// requires an explicit operator configuration.
	RuntimeProfile string // development|production
	AutoMigrate    bool

	// PostgreSQL (with pgvector)
	DBURL string

	// Redis (Phase 1 W2+ for queue; W1 keeps the URL but does not yet connect)
	RedisURL string

	// S3 / MinIO
	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3UseSSL    bool
	S3Region    string

	// Worker gRPC (Phase 1 W2+)
	WorkerGRPC      string
	WorkerAuthKeyID string
	WorkerAuthKey   []byte

	// Deployment / Auth
	DeploymentMode   string // private|saas
	RegistrationMode string // open|first_user|disabled
	SessionTTL       time.Duration
	CORSOrigins      []string // allowed browser origins; empty disables CORS (same-origin only)
	// ManagedEmbeddingProvider is the operator-selected primary exact Worker
	// provider spec. ManagedEmbeddingProviders is the complete exact allow-set
	// derived from that primary and the enabled immutable profile generations.
	// Keeping both fields lets one process serve persisted V1 workspaces while
	// advertising V2 for new selections; there is intentionally no arbitrary
	// provider or prefix fallback.
	ManagedEmbeddingProvider       string
	ManagedEmbeddingProviders      []string
	ManagedEmbeddingReservationTTL time.Duration
	LegacyOpenAIManagedBinding     bool
	// AIProfiles is the operator-enabled allowlist of fixed workspace AI
	// profiles. It contains profile IDs only—never models, URLs, or secrets.
	// Selection remains a workspace-scoped API action.
	AIProfiles []string

	// Portable workspace transfer resource controls.
	WorkspaceTransferTimeout       time.Duration
	WorkspaceBundleMaxBytes        int64
	WorkspaceTransferMaxConcurrent int
	WorkspaceTransferTmpDir        string

	// Dev knobs
	LogLevel string // debug|info|warn|error
}

// Load reads all MEM_* env vars and returns a populated Config or an error if
// required values are missing or malformed.
func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:       getenv("MEM_HTTP_ADDR", ":8787"),
		RuntimeProfile: getenv("MEM_RUNTIME_PROFILE", "development"),
		AutoMigrate:    true,
		DBURL:          getenv("MEM_DB_URL", "postgres://mem:mem@localhost:5432/mem?sslmode=disable"),
		// Redis/MinIO defaults match the host ports shipped in docker-compose.yml,
		// which are shifted off the upstream defaults (6379, 9000) so the stack
		// coexists with other local Redis/MinIO instances. Override with
		// MEM_REDIS_URL / MEM_S3_ENDPOINT when running against a non-compose stack.
		//
		// MEM_REDIS_URL has three states:
		//   - unset           -> default redis://localhost:6479 (compose stack)
		//   - set & non-empty -> that URL
		//   - set & EMPTY     -> "" => queue disabled, inline goroutine fallback
		// The empty case is the bare-metal/no-redis dev path (scripts/dev_up.sh).
		RedisURL:         getenvRedis(),
		S3Endpoint:       getenv("MEM_S3_ENDPOINT", "http://localhost:9100"),
		S3Bucket:         getenv("MEM_S3_BUCKET", "mem"),
		S3AccessKey:      getenv("MEM_S3_ACCESS_KEY", "mem"),
		S3SecretKey:      getenv("MEM_S3_SECRET_KEY", "mem-minio-password"),
		S3Region:         getenv("MEM_S3_REGION", "us-east-1"),
		WorkerGRPC:       getenvAllowEmpty("MEM_WORKER_GRPC", "localhost:50051"),
		DeploymentMode:   getenv("MEM_DEPLOYMENT_MODE", "private"),
		RegistrationMode: getenv("MEM_REGISTRATION_MODE", "open"),
		ManagedEmbeddingProvider: strings.TrimSpace(
			os.Getenv("MEM_MANAGED_EMBEDDING_PROVIDER"),
		),
		LogLevel: getenv("MEM_LOG_LEVEL", "info"),
	}

	if v := os.Getenv("MEM_AUTO_MIGRATE"); v != "" {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("MEM_AUTO_MIGRATE: %w", err)
		}
		cfg.AutoMigrate = enabled
	}

	workerAuthKeyID := os.Getenv("MEM_WORKER_AUTH_KEY_ID")
	workerAuthKeyB64 := os.Getenv("MEM_WORKER_AUTH_KEY_B64")
	if workerAuthKeyID != "" || workerAuthKeyB64 != "" {
		if !workerAuthKeyIDPattern.MatchString(workerAuthKeyID) ||
			workerAuthKeyB64 == "" ||
			strings.TrimSpace(workerAuthKeyB64) != workerAuthKeyB64 {
			return nil, errors.New("MEM_WORKER_AUTH_KEY_ID and MEM_WORKER_AUTH_KEY_B64 must configure a valid Worker authentication key")
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(workerAuthKeyB64)
		if err != nil || len(decoded) != 32 {
			return nil, errors.New("MEM_WORKER_AUTH_KEY_B64 must encode exactly 32 bytes")
		}
		cfg.WorkerAuthKeyID = workerAuthKeyID
		cfg.WorkerAuthKey = decoded
	}

	if v := os.Getenv("MEM_S3_USE_SSL"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("MEM_S3_USE_SSL: %w", err)
		}
		cfg.S3UseSSL = b
	} else {
		cfg.S3UseSSL = strings.HasPrefix(cfg.S3Endpoint, "https://")
	}
	if v := os.Getenv("MEM_OPENAI_MANAGED_BINDING"); v != "" {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("MEM_OPENAI_MANAGED_BINDING: %w", err)
		}
		cfg.LegacyOpenAIManagedBinding = enabled
	}

	if v := os.Getenv("MEM_SESSION_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("MEM_SESSION_TTL: %w", err)
		}
		cfg.SessionTTL = d
	} else {
		cfg.SessionTTL = 24 * time.Hour
	}
	if v := os.Getenv("MEM_MANAGED_EMBEDDING_RESERVATION_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("MEM_MANAGED_EMBEDDING_RESERVATION_TTL: %w", err)
		}
		cfg.ManagedEmbeddingReservationTTL = d
	} else {
		cfg.ManagedEmbeddingReservationTTL = DefaultManagedEmbeddingReservationTTL
	}
	if v := os.Getenv("MEM_WORKSPACE_TRANSFER_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("MEM_WORKSPACE_TRANSFER_TIMEOUT: %w", err)
		}
		cfg.WorkspaceTransferTimeout = d
	} else {
		cfg.WorkspaceTransferTimeout = DefaultWorkspaceTransferTimeout
	}
	if v := os.Getenv("MEM_WORKSPACE_BUNDLE_MAX_BYTES"); v != "" {
		value, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("MEM_WORKSPACE_BUNDLE_MAX_BYTES: %w", err)
		}
		cfg.WorkspaceBundleMaxBytes = value
	} else {
		cfg.WorkspaceBundleMaxBytes = DefaultWorkspaceBundleMaxBytes
	}
	if v := os.Getenv("MEM_WORKSPACE_TRANSFER_MAX_CONCURRENT"); v != "" {
		value, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("MEM_WORKSPACE_TRANSFER_MAX_CONCURRENT: %w", err)
		}
		cfg.WorkspaceTransferMaxConcurrent = value
	} else {
		cfg.WorkspaceTransferMaxConcurrent = DefaultWorkspaceTransferMaxConcurrent
	}
	cfg.WorkspaceTransferTmpDir = strings.TrimSpace(
		os.Getenv("MEM_WORKSPACE_TRANSFER_TMP_DIR"),
	)

	if cfg.DBURL == "" {
		return nil, errors.New("MEM_DB_URL is required")
	}
	if cfg.RuntimeProfile != "development" && cfg.RuntimeProfile != "production" {
		return nil, fmt.Errorf(
			"MEM_RUNTIME_PROFILE must be development or production, got %q",
			cfg.RuntimeProfile,
		)
	}
	if cfg.DeploymentMode != "private" && cfg.DeploymentMode != "saas" {
		return nil, fmt.Errorf("MEM_DEPLOYMENT_MODE must be private or saas, got %q", cfg.DeploymentMode)
	}
	if cfg.DeploymentMode == "saas" {
		if strings.TrimSpace(cfg.WorkerGRPC) == "" {
			return nil, errors.New("MEM_WORKER_GRPC is required in saas mode")
		}
		if cfg.WorkerAuthKeyID == "" || len(cfg.WorkerAuthKey) != 32 {
			return nil, errors.New("Worker request authentication is required in saas mode")
		}
	}
	profiles, err := parseAIProfiles(os.Getenv("MEM_AI_PROFILES"))
	if err != nil {
		return nil, err
	}
	cfg.AIProfiles = profiles
	legacyQuality := containsProfile(cfg.AIProfiles, "idealab-quality-v1")
	currentQuality := containsProfile(cfg.AIProfiles, "idealab-quality-v2")
	if (legacyQuality || currentQuality) && cfg.DeploymentMode != "saas" {
		// This profile consumes a platform-managed Idealab credential and must
		// therefore run behind the entitlement/usage boundary. Private installs
		// keep the explicit local profile or their existing BYOM provider path.
		return nil, errors.New("Idealab quality profiles require MEM_DEPLOYMENT_MODE=saas")
	}
	if cfg.DeploymentMode == "saas" {
		provider, model, ok := strings.Cut(cfg.ManagedEmbeddingProvider, ":")
		if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
			return nil, errors.New(
				"MEM_MANAGED_EMBEDDING_PROVIDER is required in saas mode and must be '<provider>:<model>'",
			)
		}
		if cfg.ManagedEmbeddingProvider != "openai:text-embedding-3-large" &&
			cfg.ManagedEmbeddingProvider != "idealab:text-embedding-3-large" {
			return nil, errors.New(
				"MEM_MANAGED_EMBEDDING_PROVIDER must be an exact compiled managed embedding provider",
			)
		}
		if legacyQuality && !currentQuality &&
			cfg.ManagedEmbeddingProvider != "openai:text-embedding-3-large" {
			return nil, errors.New(
				"MEM_MANAGED_EMBEDDING_PROVIDER must be openai:text-embedding-3-large when idealab-quality-v1 is enabled",
			)
		}
		if legacyQuality && !cfg.LegacyOpenAIManagedBinding {
			return nil, errors.New(
				"MEM_OPENAI_MANAGED_BINDING=true is required for idealab-quality-v1 compatibility",
			)
		}
		if currentQuality && !legacyQuality &&
			cfg.ManagedEmbeddingProvider != "idealab:text-embedding-3-large" {
			return nil, errors.New(
				"MEM_MANAGED_EMBEDDING_PROVIDER must be idealab:text-embedding-3-large when idealab-quality-v2 is enabled",
			)
		}
		cfg.ManagedEmbeddingProviders = append(
			cfg.ManagedEmbeddingProviders,
			cfg.ManagedEmbeddingProvider,
		)
		if legacyQuality {
			cfg.ManagedEmbeddingProviders = appendUnique(
				cfg.ManagedEmbeddingProviders,
				"openai:text-embedding-3-large",
			)
		}
		if currentQuality {
			cfg.ManagedEmbeddingProviders = appendUnique(
				cfg.ManagedEmbeddingProviders,
				"idealab:text-embedding-3-large",
			)
		}
	}
	if cfg.RegistrationMode != "open" && cfg.RegistrationMode != "first_user" && cfg.RegistrationMode != "disabled" {
		return nil, fmt.Errorf("MEM_REGISTRATION_MODE must be open, first_user, or disabled, got %q", cfg.RegistrationMode)
	}
	if cfg.SessionTTL <= 0 {
		return nil, errors.New("MEM_SESSION_TTL must be positive")
	}
	if cfg.ManagedEmbeddingReservationTTL <= 0 {
		return nil, errors.New("MEM_MANAGED_EMBEDDING_RESERVATION_TTL must be positive")
	}
	if cfg.DeploymentMode == "saas" &&
		cfg.ManagedEmbeddingReservationTTL < MinimumManagedEmbeddingReservationTTL {
		return nil, fmt.Errorf(
			"MEM_MANAGED_EMBEDDING_RESERVATION_TTL must be at least %s",
			MinimumManagedEmbeddingReservationTTL,
		)
	}
	if cfg.WorkspaceTransferTimeout <= 0 {
		return nil, errors.New("MEM_WORKSPACE_TRANSFER_TIMEOUT must be positive")
	}
	if cfg.WorkspaceBundleMaxBytes <= 0 {
		return nil, errors.New("MEM_WORKSPACE_BUNDLE_MAX_BYTES must be a positive integer")
	}
	if cfg.WorkspaceTransferMaxConcurrent <= 0 {
		return nil, errors.New("MEM_WORKSPACE_TRANSFER_MAX_CONCURRENT must be a positive integer")
	}
	if v := os.Getenv("MEM_CORS_ORIGINS"); v != "" {
		for _, o := range strings.Split(v, ",") {
			o = strings.TrimSpace(o)
			if o == "" {
				continue
			}
			if o != "*" && !strings.HasPrefix(o, "http://") && !strings.HasPrefix(o, "https://") {
				return nil, fmt.Errorf("MEM_CORS_ORIGINS: origin %q must be * or start with http:// or https://", o)
			}
			if o != "*" && strings.HasSuffix(o, "/") {
				return nil, fmt.Errorf("MEM_CORS_ORIGINS: origin %q must not have a trailing slash", o)
			}
			cfg.CORSOrigins = append(cfg.CORSOrigins, o)
		}
	}
	if cfg.RuntimeProfile == "production" {
		if err := validateProduction(cfg); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func validateProduction(cfg *Config) error {
	const (
		developmentDBURL       = "postgres://mem:mem@localhost:5432/mem?sslmode=disable"
		developmentRedisURL    = "redis://localhost:6479"
		developmentS3Endpoint  = "http://localhost:9100"
		developmentS3AccessKey = "mem"
		developmentS3SecretKey = "mem-minio-password"
	)

	if cfg.DBURL == developmentDBURL {
		return errors.New("MEM_DB_URL must be explicitly configured in production")
	}
	if cfg.AutoMigrate {
		return errors.New("MEM_AUTO_MIGRATE=false is required in production; run mem-migrate once before rollout")
	}
	if cfg.RedisURL == "" || cfg.RedisURL == developmentRedisURL {
		return errors.New("MEM_REDIS_URL must be explicitly configured in production")
	}
	if cfg.S3Endpoint == "" || cfg.S3Endpoint == developmentS3Endpoint {
		return errors.New("MEM_S3_ENDPOINT must be explicitly configured in production")
	}
	if cfg.S3AccessKey == "" || cfg.S3AccessKey == developmentS3AccessKey {
		return errors.New("MEM_S3_ACCESS_KEY must be explicitly configured in production")
	}
	if cfg.S3SecretKey == "" || cfg.S3SecretKey == developmentS3SecretKey {
		return errors.New("MEM_S3_SECRET_KEY must be explicitly configured in production")
	}
	if cfg.RegistrationMode == "open" {
		return errors.New("MEM_REGISTRATION_MODE=open is not permitted in production")
	}
	if cfg.WorkerGRPC != "" &&
		(cfg.WorkerAuthKeyID == "" || len(cfg.WorkerAuthKey) != 32) {
		return errors.New("Worker request authentication is required when MEM_WORKER_GRPC is enabled in production")
	}
	for _, origin := range cfg.CORSOrigins {
		if origin == "*" {
			return errors.New("MEM_CORS_ORIGINS=* is not permitted in production")
		}
	}
	return nil
}

func parseAIProfiles(raw string) ([]string, error) {
	// Local-only remains the default: enabling a paid cloud profile is an
	// operator decision, never an accidental consequence of a global key.
	if strings.TrimSpace(raw) == "" {
		// Keep the already-published V1 available for persisted selections,
		// while presenting V2 as the current no-implicit-download choice.
		return []string{"local-fast-v1", "local-fast-v2"}, nil
	}
	profiles := make([]string, 0, 4)
	seen := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		id := strings.TrimSpace(item)
		if id != "local-fast-v1" && id != "idealab-quality-v1" &&
			id != "local-fast-v2" && id != "idealab-quality-v2" {
			return nil, fmt.Errorf("MEM_AI_PROFILES contains unknown profile %q", id)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("MEM_AI_PROFILES contains duplicate profile %q", id)
		}
		seen[id] = struct{}{}
		profiles = append(profiles, id)
	}
	if len(profiles) == 0 {
		return nil, errors.New("MEM_AI_PROFILES must enable at least one profile")
	}
	return profiles, nil
}

func containsProfile(profiles []string, wanted string) bool {
	for _, profile := range profiles {
		if profile == wanted {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getenvAllowEmpty(k, def string) string {
	if value, ok := os.LookupEnv(k); ok {
		return value
	}
	return def
}

// getenvRedis resolves MEM_REDIS_URL with empty-aware semantics: an explicitly
// set but empty value means "no redis — use the inline goroutine fallback",
// which is the bare-metal dev path. Unset falls back to the compose default.
func getenvRedis() string {
	if v, ok := os.LookupEnv("MEM_REDIS_URL"); ok {
		return v // honor explicit value, including ""
	}
	return "redis://localhost:6479"
}

// S3EndpointHost strips the scheme prefix from S3Endpoint, returning host:port
// — minio-go expects no scheme in its endpoint argument.
func (c *Config) S3EndpointHost() string {
	e := c.S3Endpoint
	e = strings.TrimPrefix(e, "http://")
	e = strings.TrimPrefix(e, "https://")
	return e
}

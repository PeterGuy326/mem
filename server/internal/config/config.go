// Package config loads memd configuration from environment variables.
//
// All keys are namespaced under MEM_ to avoid collisions. Defaults are tuned
// for the docker-compose local stack (see /docker-compose.yml).
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultWorkspaceTransferTimeout             = 30 * time.Minute
	DefaultWorkspaceBundleMaxBytes        int64 = 8 << 30
	DefaultWorkspaceTransferMaxConcurrent       = 2
)

// Config is the resolved memd runtime configuration.
type Config struct {
	// HTTP
	HTTPAddr string // e.g. ":8787"

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
	WorkerGRPC string

	// Deployment / Auth
	DeploymentMode   string // private|saas
	RegistrationMode string // open|first_user|disabled
	SessionTTL       time.Duration
	CORSOrigins      []string // allowed browser origins; empty disables CORS (same-origin only)

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
		HTTPAddr: getenv("MEM_HTTP_ADDR", ":8787"),
		DBURL:    getenv("MEM_DB_URL", "postgres://mem:mem@localhost:5432/mem?sslmode=disable"),
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
		WorkerGRPC:       getenv("MEM_WORKER_GRPC", "localhost:50051"),
		DeploymentMode:   getenv("MEM_DEPLOYMENT_MODE", "private"),
		RegistrationMode: getenv("MEM_REGISTRATION_MODE", "open"),
		LogLevel:         getenv("MEM_LOG_LEVEL", "info"),
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

	if v := os.Getenv("MEM_SESSION_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("MEM_SESSION_TTL: %w", err)
		}
		cfg.SessionTTL = d
	} else {
		cfg.SessionTTL = 24 * time.Hour
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
	if cfg.DeploymentMode != "private" && cfg.DeploymentMode != "saas" {
		return nil, fmt.Errorf("MEM_DEPLOYMENT_MODE must be private or saas, got %q", cfg.DeploymentMode)
	}
	if cfg.RegistrationMode != "open" && cfg.RegistrationMode != "first_user" && cfg.RegistrationMode != "disabled" {
		return nil, fmt.Errorf("MEM_REGISTRATION_MODE must be open, first_user, or disabled, got %q", cfg.RegistrationMode)
	}
	if cfg.SessionTTL <= 0 {
		return nil, errors.New("MEM_SESSION_TTL must be positive")
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
	return cfg, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
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

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

	// Auth
	SessionTTL time.Duration

	// Dev knobs
	LogLevel string // debug|info|warn|error
}

// Load reads all MEM_* env vars and returns a populated Config or an error if
// required values are missing or malformed.
func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:    getenv("MEM_HTTP_ADDR", ":8787"),
		DBURL:       getenv("MEM_DB_URL", "postgres://mem:mem@localhost:5432/mem?sslmode=disable"),
		RedisURL:    getenv("MEM_REDIS_URL", "redis://localhost:6379"),
		S3Endpoint:  getenv("MEM_S3_ENDPOINT", "http://localhost:9000"),
		S3Bucket:    getenv("MEM_S3_BUCKET", "mem"),
		S3AccessKey: getenv("MEM_S3_ACCESS_KEY", "mem"),
		S3SecretKey: getenv("MEM_S3_SECRET_KEY", "mem-minio-password"),
		S3Region:    getenv("MEM_S3_REGION", "us-east-1"),
		WorkerGRPC:  getenv("MEM_WORKER_GRPC", ""),
		LogLevel:    getenv("MEM_LOG_LEVEL", "info"),
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

	if cfg.DBURL == "" {
		return nil, errors.New("MEM_DB_URL is required")
	}
	return cfg, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// S3EndpointHost strips the scheme prefix from S3Endpoint, returning host:port
// — minio-go expects no scheme in its endpoint argument.
func (c *Config) S3EndpointHost() string {
	e := c.S3Endpoint
	e = strings.TrimPrefix(e, "http://")
	e = strings.TrimPrefix(e, "https://")
	return e
}

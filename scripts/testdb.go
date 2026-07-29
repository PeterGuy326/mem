//go:build ignore

// Command testdb owns disposable PostgreSQL databases used by scripts/verify.sh.
// Run it from the server module so it uses the repository's pinned pgx version.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	baseDSNEnv                = "MEM_TEST_DB"
	targetDSNEnv              = "MEM_TEST_TARGET_DB"
	databaseSuffixBytes       = 8
	ownershipNonceBytes       = 32
	ownershipFragmentPrefix   = "mem-testdb-owner="
	maxPostgresIdentifierSize = 63
)

var (
	labelPattern         = regexp.MustCompile(`^[a-z0-9_]{1,24}$`)
	testDatabasePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*_test$`)
	ownedDatabasePattern = regexp.MustCompile(
		`^mem_verify_[a-z0-9_]{1,24}_[0-9a-f]{16}_test$`,
	)
	ownershipNoncePattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "testdb: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: testdb <check|create|drop|version|assert-state>")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	switch os.Args[1] {
	case "check":
		if len(os.Args) != 2 {
			return errors.New("check takes no arguments")
		}
		cfg, conn, database, err := connectDisposable(ctx, os.Getenv(baseDSNEnv))
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		if cfg.Database != database {
			return fmt.Errorf(
				"effective database mismatch: parsed %q, server selected %q",
				cfg.Database,
				database,
			)
		}
		fmt.Printf("validated disposable PostgreSQL control database: %s\n", database)
		return nil
	case "create":
		if len(os.Args) != 3 {
			return errors.New("create requires exactly one label")
		}
		if !labelPattern.MatchString(os.Args[2]) {
			return errors.New("create requires a lowercase label of 1-24 letters, digits or underscores")
		}
		return createDatabase(ctx, os.Args[2])
	case "drop":
		if len(os.Args) != 2 {
			return errors.New("drop takes no arguments")
		}
		return dropDatabase(ctx)
	case "version":
		if len(os.Args) != 2 {
			return errors.New("version takes no arguments")
		}
		return printVersion(ctx)
	case "assert-state":
		if len(os.Args) != 3 {
			return errors.New("assert-state requires exactly one state")
		}
		if os.Args[2] != "down" && os.Args[2] != "up" {
			return errors.New("assert-state requires down or up")
		}
		return assertMigrationState(ctx, os.Args[2])
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}

func connectDisposable(
	ctx context.Context,
	dsn string,
) (*pgx.ConnConfig, *pgx.Conn, string, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, nil, "", fmt.Errorf("%s is required", baseDSNEnv)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, nil, "", fmt.Errorf("parse PostgreSQL DSN: %w", err)
	}
	if err := validateTestDatabaseName(cfg.Database); err != nil {
		return nil, nil, "", err
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, nil, "", fmt.Errorf("connect PostgreSQL: %w", err)
	}
	var database string
	if err := conn.QueryRow(ctx, "SELECT current_database()").Scan(&database); err != nil {
		conn.Close(ctx)
		return nil, nil, "", fmt.Errorf("read current_database(): %w", err)
	}
	if err := validateTestDatabaseName(database); err != nil {
		conn.Close(ctx)
		return nil, nil, "", err
	}
	if cfg.Database != database {
		conn.Close(ctx)
		return nil, nil, "", fmt.Errorf(
			"effective database mismatch: parsed %q, server selected %q",
			cfg.Database,
			database,
		)
	}
	return cfg, conn, database, nil
}

func validateTestDatabaseName(database string) error {
	if len(database) == 0 || len(database) > maxPostgresIdentifierSize {
		return fmt.Errorf(
			"refusing database name of %d bytes; expected 1-%d bytes",
			len(database),
			maxPostgresIdentifierSize,
		)
	}
	if !testDatabasePattern.MatchString(database) {
		return fmt.Errorf(
			"refusing database %q; expected a lowercase ASCII name ending in _test",
			database,
		)
	}
	return nil
}

func validateOwnedDatabaseName(database string) error {
	if err := validateTestDatabaseName(database); err != nil {
		return err
	}
	if !ownedDatabasePattern.MatchString(database) {
		return fmt.Errorf("refusing to manage unowned database %q", database)
	}
	return nil
}

func randomHex(size int) (string, error) {
	random := make([]byte, size)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return hex.EncodeToString(random), nil
}

func createDatabase(ctx context.Context, label string) error {
	_, conn, _, err := connectDisposable(ctx, os.Getenv(baseDSNEnv))
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	suffix, err := randomHex(databaseSuffixBytes)
	if err != nil {
		return fmt.Errorf("generate database suffix: %w", err)
	}
	nonce, err := randomHex(ownershipNonceBytes)
	if err != nil {
		return fmt.Errorf("generate ownership nonce: %w", err)
	}
	database := "mem_verify_" + label + "_" + suffix + "_test"
	if err := validateOwnedDatabaseName(database); err != nil {
		return err
	}
	targetDSN, err := databaseDSN(os.Getenv(baseDSNEnv), database, nonce)
	if err != nil {
		return err
	}
	if _, err := conn.Exec(
		ctx,
		"CREATE DATABASE "+pgx.Identifier{database}.Sanitize(),
	); err != nil {
		return fmt.Errorf("create disposable database %q: %w", database, err)
	}
	if err := installOwnershipMarker(ctx, targetDSN, database, nonce); err != nil {
		return reclaimCreatedDatabase(
			conn,
			database,
			fmt.Errorf("install ownership marker in %q: %w", database, err),
		)
	}
	if _, err := fmt.Fprintln(os.Stdout, targetDSN); err != nil {
		return reclaimCreatedDatabase(
			conn,
			database,
			fmt.Errorf("write target database DSN: %w", err),
		)
	}
	return nil
}

func databaseDSN(dsn string, database string, nonce string) (string, error) {
	if err := validateOwnedDatabaseName(database); err != nil {
		return "", err
	}
	if !ownershipNoncePattern.MatchString(nonce) {
		return "", errors.New("ownership nonce must be 64 lowercase hexadecimal characters")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse control DSN URL: %w", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", errors.New(
			"MEM_TEST_DB must be a postgres:// or postgresql:// URL",
		)
	}
	if parsed.Host == "" {
		return "", errors.New("MEM_TEST_DB URL must include a host")
	}
	parsed.Path = "/" + database
	parsed.RawPath = ""
	query := parsed.Query()
	query.Del("database")
	query.Del("dbname")
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ownershipFragmentPrefix + nonce
	parsed.RawFragment = ""
	return parsed.String(), nil
}

func installOwnershipMarker(
	ctx context.Context,
	targetDSN string,
	database string,
	nonce string,
) error {
	if err := validateOwnedDatabaseName(database); err != nil {
		return err
	}
	if !ownershipNoncePattern.MatchString(nonce) {
		return errors.New("ownership nonce must be 64 lowercase hexadecimal characters")
	}
	cfg, conn, effectiveDatabase, err := connectDisposable(ctx, targetDSN)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if cfg.Database != database || effectiveDatabase != database {
		return fmt.Errorf(
			"ownership marker target mismatch: expected %q, parsed %q, selected %q",
			database,
			cfg.Database,
			effectiveDatabase,
		)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin ownership marker transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
CREATE TABLE public.mem_testdb_ownership (
  singleton boolean PRIMARY KEY,
  database_name text NOT NULL,
  nonce text NOT NULL,
  CONSTRAINT mem_testdb_ownership_singleton_check CHECK (singleton),
  CONSTRAINT mem_testdb_ownership_database_check CHECK (
    octet_length(database_name) BETWEEN 1 AND 63
  ),
  CONSTRAINT mem_testdb_ownership_nonce_check CHECK (
    nonce ~ '^[0-9a-f]{64}$'
  )
)`); err != nil {
		return fmt.Errorf("create ownership marker table: %w", err)
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO public.mem_testdb_ownership
		     (singleton, database_name, nonce)
		 VALUES (true, $1, $2)`,
		database,
		nonce,
	); err != nil {
		return fmt.Errorf("insert ownership marker: %w", err)
	}
	if _, err := tx.Exec(
		ctx,
		"REVOKE ALL ON TABLE public.mem_testdb_ownership FROM PUBLIC",
	); err != nil {
		return fmt.Errorf("protect ownership marker table: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ownership marker: %w", err)
	}
	return nil
}

func reclaimCreatedDatabase(
	controlConn *pgx.Conn,
	database string,
	cause error,
) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := dropDatabaseByName(cleanupCtx, controlConn, database); err != nil {
		return errors.Join(
			cause,
			fmt.Errorf("reclaim database after create failure: %w", err),
		)
	}
	return cause
}

func parseOwnershipTarget(
	targetDSN string,
) (*pgx.ConnConfig, string, error) {
	parsed, err := url.Parse(targetDSN)
	if err != nil {
		return nil, "", fmt.Errorf("parse target PostgreSQL URL: %w", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return nil, "", errors.New(
			"MEM_TEST_TARGET_DB must be a postgres:// or postgresql:// URL",
		)
	}
	if parsed.Host == "" {
		return nil, "", errors.New("MEM_TEST_TARGET_DB URL must include a host")
	}
	pathDatabase := strings.TrimPrefix(parsed.Path, "/")
	if pathDatabase == parsed.Path || strings.Contains(pathDatabase, "/") {
		return nil, "", errors.New(
			"MEM_TEST_TARGET_DB URL must contain exactly one database path segment",
		)
	}
	query := parsed.Query()
	if query.Has("database") || query.Has("dbname") {
		return nil, "", errors.New(
			"MEM_TEST_TARGET_DB must not override its database in query parameters",
		)
	}
	if !strings.HasPrefix(parsed.Fragment, ownershipFragmentPrefix) {
		return nil, "", errors.New("MEM_TEST_TARGET_DB is missing its ownership nonce")
	}
	nonce := strings.TrimPrefix(parsed.Fragment, ownershipFragmentPrefix)
	if !ownershipNoncePattern.MatchString(nonce) {
		return nil, "", errors.New("MEM_TEST_TARGET_DB has an invalid ownership nonce")
	}

	cfg, err := pgx.ParseConfig(targetDSN)
	if err != nil {
		return nil, "", fmt.Errorf("parse target PostgreSQL DSN: %w", err)
	}
	if cfg.Database != pathDatabase {
		return nil, "", fmt.Errorf(
			"target database mismatch: URL path %q, parsed database %q",
			pathDatabase,
			cfg.Database,
		)
	}
	if err := validateOwnedDatabaseName(cfg.Database); err != nil {
		return nil, "", err
	}
	return cfg, nonce, nil
}

func sameServerAndRole(base *pgx.ConnConfig, target *pgx.ConnConfig) bool {
	if target.Host != base.Host ||
		target.Port != base.Port ||
		target.User != base.User ||
		target.Password != base.Password ||
		len(target.Fallbacks) != len(base.Fallbacks) {
		return false
	}
	for i, targetFallback := range target.Fallbacks {
		baseFallback := base.Fallbacks[i]
		if targetFallback.Host != baseFallback.Host ||
			targetFallback.Port != baseFallback.Port {
			return false
		}
	}
	return true
}

func verifyOwnershipMarker(
	ctx context.Context,
	conn *pgx.Conn,
	database string,
	nonce string,
) error {
	var (
		markerDatabase string
		markerNonce    string
		markerRows     int64
	)
	if err := conn.QueryRow(ctx, `
SELECT database_name,
       nonce,
       (SELECT count(*) FROM public.mem_testdb_ownership)
  FROM public.mem_testdb_ownership
 WHERE singleton`).Scan(
		&markerDatabase,
		&markerNonce,
		&markerRows,
	); err != nil {
		return fmt.Errorf("read ownership marker: %w", err)
	}
	if markerRows != 1 {
		return fmt.Errorf("ownership marker has %d rows; expected exactly one", markerRows)
	}
	if markerDatabase != database {
		return fmt.Errorf(
			"ownership marker database mismatch: expected %q, got %q",
			database,
			markerDatabase,
		)
	}
	if subtle.ConstantTimeCompare([]byte(markerNonce), []byte(nonce)) != 1 {
		return errors.New("ownership marker nonce mismatch")
	}
	return nil
}

func dropDatabase(ctx context.Context) error {
	baseCfg, conn, controlDatabase, err := connectDisposable(
		ctx,
		os.Getenv(baseDSNEnv),
	)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	targetDSN := strings.TrimSpace(os.Getenv(targetDSNEnv))
	if targetDSN == "" {
		return fmt.Errorf("%s is required", targetDSNEnv)
	}
	target, nonce, err := parseOwnershipTarget(targetDSN)
	if err != nil {
		return err
	}
	if !sameServerAndRole(baseCfg, target) {
		return errors.New("target database server or user differs from the validated control DSN")
	}
	if target.Database == controlDatabase {
		return errors.New("refusing to drop the validated control database")
	}

	connectedTarget, targetConn, effectiveDatabase, err := connectDisposable(
		ctx,
		targetDSN,
	)
	if err != nil {
		return fmt.Errorf("connect target database for ownership verification: %w", err)
	}
	defer targetConn.Close(ctx)
	if connectedTarget.Database != target.Database ||
		effectiveDatabase != target.Database {
		return fmt.Errorf(
			"target database changed while connecting: expected %q, parsed %q, selected %q",
			target.Database,
			connectedTarget.Database,
			effectiveDatabase,
		)
	}
	if err := verifyOwnershipMarker(
		ctx,
		targetConn,
		target.Database,
		nonce,
	); err != nil {
		return fmt.Errorf(
			"refusing to drop database %q: %w",
			target.Database,
			err,
		)
	}
	_ = targetConn.Close(ctx)

	return dropDatabaseByName(ctx, conn, target.Database)
}

func dropDatabaseByName(
	ctx context.Context,
	controlConn *pgx.Conn,
	database string,
) error {
	if err := validateOwnedDatabaseName(database); err != nil {
		return err
	}
	if _, err := controlConn.Exec(
		ctx,
		`SELECT pg_terminate_backend(pid)
		   FROM pg_stat_activity
		  WHERE datname = $1
		    AND pid <> pg_backend_pid()`,
		database,
	); err != nil {
		return fmt.Errorf("terminate target database sessions: %w", err)
	}
	if _, err := controlConn.Exec(
		ctx,
		"DROP DATABASE "+pgx.Identifier{database}.Sanitize(),
	); err != nil {
		return fmt.Errorf("drop disposable database %q: %w", database, err)
	}
	return nil
}

func targetConnection(ctx context.Context) (*pgx.Conn, error) {
	dsn := strings.TrimSpace(os.Getenv(targetDSNEnv))
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv(baseDSNEnv))
	}
	_, conn, _, err := connectDisposable(ctx, dsn)
	return conn, err
}

func printVersion(ctx context.Context) error {
	conn, err := targetConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	var version int64
	if err := conn.QueryRow(
		ctx,
		`SELECT COALESCE(max(version_id) FILTER (WHERE is_applied), 0)
		   FROM goose_db_version`,
	).Scan(&version); err != nil {
		return fmt.Errorf("read goose version: %w", err)
	}
	fmt.Println(version)
	return nil
}

func assertMigrationState(ctx context.Context, state string) error {
	conn, err := targetConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	var (
		rawKey              bool
		hashedKey           bool
		replayPrincipal     bool
		oldFileIndex        bool
		hashConstraint      bool
		redactConstraint    bool
		receiptConstraint   bool
		replayConstraint    bool
		managedEntitlements bool
	)
	err = conn.QueryRow(ctx, `
SELECT
  EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name = 'memories'
       AND column_name = 'idempotency_key'
  ),
  EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name = 'memories'
       AND column_name = 'idempotency_key_sha256'
  ),
  EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name = 'memory_events'
       AND column_name = 'replay_principal_sha256'
  ),
  to_regclass('public.uniq_files_user_sha') IS NOT NULL,
  EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conname = 'memories_workspace_id_idempotency_key_sha256_key'
  ),
  EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conname = 'memories_forgotten_payload_redacted_check'
  ),
  EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conname = 'memory_events_forget_receipt_check'
  ),
  EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conname = 'memory_events_replay_principal_sha256_check'
  ),
  (
    to_regclass('public.workspace_entitlements') IS NOT NULL
    AND to_regclass('public.managed_embedding_usage') IS NOT NULL
    AND to_regclass('public.managed_embedding_usage_events') IS NOT NULL
    AND to_regclass('public.managed_embedding_replay_results') IS NOT NULL
  )
`).Scan(
		&rawKey,
		&hashedKey,
		&replayPrincipal,
		&oldFileIndex,
		&hashConstraint,
		&redactConstraint,
		&receiptConstraint,
		&replayConstraint,
		&managedEntitlements,
	)
	if err != nil {
		return fmt.Errorf("inspect migration state: %w", err)
	}

	valid := false
	switch state {
	case "down":
		valid = rawKey &&
			!hashedKey &&
			!replayPrincipal &&
			oldFileIndex &&
			!hashConstraint &&
			redactConstraint &&
			!receiptConstraint &&
			!replayConstraint &&
			!managedEntitlements
	case "up":
		valid = !rawKey &&
			hashedKey &&
			replayPrincipal &&
			!oldFileIndex &&
			hashConstraint &&
			redactConstraint &&
			receiptConstraint &&
			replayConstraint &&
			managedEntitlements
	}
	if !valid {
		return fmt.Errorf(
			"unexpected %s schema state: raw_key=%t hashed_key=%t replay_principal=%t old_file_index=%t hash_constraint=%t redact_constraint=%t receipt_constraint=%t replay_constraint=%t managed_entitlements=%t",
			state,
			rawKey,
			hashedKey,
			replayPrincipal,
			oldFileIndex,
			hashConstraint,
			redactConstraint,
			receiptConstraint,
			replayConstraint,
			managedEntitlements,
		)
	}
	return nil
}

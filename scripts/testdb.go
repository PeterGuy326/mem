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
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	baseDSNEnv                = "MEM_TEST_DB"
	targetDSNEnv              = "MEM_TEST_TARGET_DB"
	databaseSuffixBytes       = 8
	ownershipNonceBytes       = 32
	ownershipFragmentPrefix   = "mem-testdb-owner="
	maxPostgresIdentifierSize = 63
	migrationFileUserID       = "7f000000-0000-0000-0000-000000000001"
	migrationFileID           = "7f000000-0000-0000-0000-000000000002"
	migrationUnsafeFileID     = "7f000000-0000-0000-0000-000000000003"
	migrationReasoningFileID  = "7f000000-0000-0000-0000-000000000004"
	migrationUnicodeFileID    = "7f000000-0000-0000-0000-000000000005"
	migrationFormatFileID     = "7f000000-0000-0000-0000-000000000006"
	migrationFormatRangeID    = "7f000000-0000-0000-0000-000000000007"
	migrationIgnorableFileID  = "7f000000-0000-0000-0000-000000000008"
	migrationTrimmedLimitID   = "7f000000-0000-0000-0000-000000000009"
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
		return errors.New(
			"usage: testdb <check|create|drop|version|assert-state|" +
				"seed-file-enrichment|assert-file-preserved <down|up>|" +
				"seed-v15-noncanonical-text|assert-v15-noncanonical-text|" +
				"assert-canonical-model-text-values|" +
				"assert-canonical-model-text|seed-unsafe-derived-text|" +
				"assert-unsafe-derived-text-scrubbed>",
		)
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
	case "seed-file-enrichment":
		if len(os.Args) != 2 {
			return errors.New("seed-file-enrichment takes no arguments")
		}
		return seedFileEnrichment(ctx)
	case "seed-v15-noncanonical-text":
		if len(os.Args) != 2 {
			return errors.New("seed-v15-noncanonical-text takes no arguments")
		}
		return seedV15NonCanonicalText(ctx)
	case "assert-v15-noncanonical-text":
		if len(os.Args) != 2 {
			return errors.New("assert-v15-noncanonical-text takes no arguments")
		}
		return assertV15NonCanonicalText(ctx)
	case "assert-canonical-model-text-values":
		if len(os.Args) != 2 {
			return errors.New(
				"assert-canonical-model-text-values takes no arguments",
			)
		}
		return assertCanonicalModelTextValues(ctx)
	case "assert-canonical-model-text":
		if len(os.Args) != 2 {
			return errors.New("assert-canonical-model-text takes no arguments")
		}
		return assertCanonicalModelText(ctx)
	case "assert-file-preserved":
		if len(os.Args) != 3 ||
			(os.Args[2] != "down" && os.Args[2] != "up") {
			return errors.New("assert-file-preserved requires down or up")
		}
		return assertFilePreserved(ctx, os.Args[2])
	case "seed-unsafe-derived-text":
		if len(os.Args) != 2 {
			return errors.New("seed-unsafe-derived-text takes no arguments")
		}
		return seedUnsafeDerivedText(ctx)
	case "assert-unsafe-derived-text-scrubbed":
		if len(os.Args) != 2 {
			return errors.New("assert-unsafe-derived-text-scrubbed takes no arguments")
		}
		return assertUnsafeDerivedTextScrubbed(ctx)
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}

func seedV15NonCanonicalText(ctx context.Context) error {
	conn, err := targetConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin v15 non-canonical text seed: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
INSERT INTO users (id, email, password_hash)
VALUES ($1, 'migration-file-preservation@example.invalid', 'test')
ON CONFLICT (id) DO NOTHING
`, migrationFileUserID); err != nil {
		return fmt.Errorf("seed v15 non-canonical text owner: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO files (
  id, user_id, name, path, size, sha256, mime, storage_key,
  summary, caption, tags, index_status
)
VALUES (
  $1, $2, 'v15-noncanonical.txt', '/', 1, repeat('1', 64), 'text/plain',
  'migration/v15-noncanonical',
  $3, $4, ARRAY[]::text[], 'done'
)
ON CONFLICT (id) DO UPDATE
   SET summary = EXCLUDED.summary,
       caption = EXCLUDED.caption
`,
		migrationTrimmedLimitID,
		migrationFileUserID,
		" "+strings.Repeat("s", 2000)+" ",
		"\u3000"+strings.Repeat("c", 2000)+"\u00a0",
	); err != nil {
		return fmt.Errorf("seed v15 non-canonical model text: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit v15 non-canonical text seed: %w", err)
	}
	return nil
}

func assertV15NonCanonicalText(ctx context.Context) error {
	conn, err := targetConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	var summary, caption string
	if err := conn.QueryRow(ctx, `
SELECT summary, caption
  FROM files
 WHERE id = $1 AND user_id = $2
`, migrationTrimmedLimitID, migrationFileUserID).Scan(
		&summary,
		&caption,
	); err != nil {
		return fmt.Errorf("load v15 non-canonical model text: %w", err)
	}
	wantSummary := " " + strings.Repeat("s", 2000) + " "
	wantCaption := "\u3000" + strings.Repeat("c", 2000) + "\u00a0"
	if summary != wantSummary || caption != wantCaption {
		return fmt.Errorf(
			"v15 did not preserve non-canonical model text: summary=%d caption=%d",
			len([]rune(summary)),
			len([]rune(caption)),
		)
	}
	return nil
}

func assertCanonicalModelText(ctx context.Context) error {
	conn, err := targetConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	return assertCanonicalModelTextWithConnection(ctx, conn)
}

func assertCanonicalModelTextValues(ctx context.Context) error {
	conn, err := targetConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	return assertCanonicalModelTextValuesWithConnection(ctx, conn)
}

func assertCanonicalModelTextValuesWithConnection(ctx context.Context, conn *pgx.Conn) error {
	var summary, caption string
	if err := conn.QueryRow(ctx, `
SELECT summary, caption
  FROM files
 WHERE id = $1 AND user_id = $2
`, migrationTrimmedLimitID, migrationFileUserID).Scan(
		&summary,
		&caption,
	); err != nil {
		return fmt.Errorf("load canonical model text: %w", err)
	}
	wantSummary := strings.Repeat("s", 2000)
	wantCaption := strings.Repeat("c", 2000)
	if summary != wantSummary || caption != wantCaption {
		return fmt.Errorf(
			"model text was not canonicalized: summary=%d caption=%d",
			len([]rune(summary)),
			len([]rune(caption)),
		)
	}
	return nil
}

func assertCanonicalModelTextWithConnection(ctx context.Context, conn *pgx.Conn) error {
	if err := assertCanonicalModelTextValuesWithConnection(ctx, conn); err != nil {
		return err
	}
	constraintTests := []struct {
		name  string
		query string
		value string
	}{
		{
			name:  "caption non-canonical outer whitespace",
			query: `UPDATE files SET caption = $1 WHERE id = $2`,
			value: " caption ",
		},
		{
			name:  "summary non-canonical outer Unicode whitespace",
			query: `UPDATE files SET summary = $1 WHERE id = $2`,
			value: "\u3000summary\u00a0",
		},
	}
	for _, test := range constraintTests {
		if _, err := conn.Exec(
			ctx,
			test.query,
			test.value,
			migrationTrimmedLimitID,
		); err == nil {
			return fmt.Errorf(
				"%s unexpectedly passed its database check",
				test.name,
			)
		} else {
			var postgresError *pgconn.PgError
			if !errors.As(err, &postgresError) ||
				postgresError.Code != "23514" {
				return fmt.Errorf(
					"%s returned %v, want PostgreSQL check violation",
					test.name,
					err,
				)
			}
		}
	}
	return nil
}

func seedUnsafeDerivedText(ctx context.Context) error {
	conn, err := targetConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `
INSERT INTO files (
  id, user_id, name, path, size, sha256, mime, storage_key,
  summary, caption, tags, index_status
)
VALUES (
  $1, $2, 'unsafe-legacy.txt', '/', 1, repeat('b', 64), 'text/plain',
  'migration/unsafe-derived-text', '{"analysis":"private"}',
  E'visible\nprivate', ARRAY[]::text[], 'done'
), (
  $3, $2, 'reasoning-legacy.txt', '/', 1, repeat('c', 64), 'text/plain',
  'migration/reasoning-derived-text',
  '<reasoning visibility="hidden">private</reasoning>visible',
  'visible</reasoning>', ARRAY[]::text[], 'done'
), (
  $4, $2, 'unicode-whitespace-legacy.txt', '/', 1, repeat('d', 64), 'text/plain',
  'migration/unicode-whitespace-derived-text',
  U&'\00A0{"analysis":"private"}',
  U&'\3000Reasoning: private', ARRAY[]::text[], 'done'
), (
  $5, $2, 'format-character-legacy.txt', '/', 1, repeat('e', 64), 'text/plain',
  'migration/format-character-derived-text',
  U&'\FEFF{"analysis":"private","answer":"public"}',
  U&'\200B["private"]', ARRAY[]::text[], 'done'
), (
  $6, $2, 'format-range-legacy.txt', '/', 1, repeat('f', 64), 'text/plain',
  'migration/format-range-derived-text',
  U&'visible\2060private',
  U&'visible\+013439private', ARRAY[]::text[], 'done'
), (
  $7, $2, 'default-ignorable-legacy.txt', '/', 1, repeat('0', 64), 'text/plain',
  'migration/default-ignorable-derived-text',
  U&'visible\FE0Fprivate',
  U&'visible\034Fprivate', ARRAY[]::text[], 'done'
), (
  $8, $2, 'trimmed-limit-legacy.txt', '/', 1, repeat('1', 64), 'text/plain',
  'migration/trimmed-limit-derived-text',
  ' ' || repeat('s', 2000) || ' ',
  ' ' || repeat('c', 2000) || ' ', ARRAY[]::text[], 'done'
)
ON CONFLICT (id) DO UPDATE
   SET summary = EXCLUDED.summary,
       caption = EXCLUDED.caption
`,
		migrationUnsafeFileID,
		migrationFileUserID,
		migrationReasoningFileID,
		migrationUnicodeFileID,
		migrationFormatFileID,
		migrationFormatRangeID,
		migrationIgnorableFileID,
		migrationTrimmedLimitID,
	); err != nil {
		return fmt.Errorf("seed unsafe legacy derived text: %w", err)
	}
	return nil
}

func assertUnsafeDerivedTextScrubbed(ctx context.Context) error {
	conn, err := targetConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	for _, fileID := range []string{
		migrationUnsafeFileID,
		migrationReasoningFileID,
		migrationUnicodeFileID,
		migrationFormatFileID,
		migrationFormatRangeID,
		migrationIgnorableFileID,
	} {
		var summary, caption *string
		if err := conn.QueryRow(ctx, `
SELECT summary, caption
  FROM files
 WHERE id = $1 AND user_id = $2
`, fileID, migrationFileUserID).Scan(&summary, &caption); err != nil {
			return fmt.Errorf("load scrubbed legacy derived text %s: %w", fileID, err)
		}
		if summary != nil || caption != nil {
			return fmt.Errorf(
				"unsafe legacy derived text survived migration for %s: summary=%v caption=%v",
				fileID,
				summary,
				caption,
			)
		}
	}

	// Search and timeline project these columns directly. Re-run their read
	// shapes against the legacy Cf row so migration scrub is the regression
	// boundary, not an assumption that every reader sanitizes independently.
	var searchSummary *string
	if err := conn.QueryRow(ctx, `
SELECT f.summary
  FROM files AS f
 WHERE f.id = $1 AND f.user_id = $2
`, migrationFormatRangeID, migrationFileUserID).Scan(&searchSummary); err != nil {
		return fmt.Errorf("load legacy Cf search projection: %w", err)
	}
	var timelineSummary, timelineCaption *string
	if err := conn.QueryRow(ctx, `
SELECT summary, caption
  FROM files
 WHERE id = $1 AND user_id = $2
 ORDER BY COALESCE(timeline_at, created_at)
`, migrationFormatRangeID, migrationFileUserID).Scan(
		&timelineSummary,
		&timelineCaption,
	); err != nil {
		return fmt.Errorf("load legacy Cf timeline projection: %w", err)
	}
	if searchSummary != nil || timelineSummary != nil || timelineCaption != nil {
		return fmt.Errorf(
			"legacy Cf leaked through search/timeline projections: search=%v timeline_summary=%v timeline_caption=%v",
			searchSummary,
			timelineSummary,
			timelineCaption,
		)
	}

	if err := assertCanonicalModelTextWithConnection(ctx, conn); err != nil {
		return err
	}

	nonDisplayValues := make([]string, 0, 4206)
	for value := rune(0); value <= unicode.MaxRune; value++ {
		if unicode.Is(unicode.Cf, value) ||
			unicode.Is(unicode.Variation_Selector, value) ||
			unicode.Is(unicode.Other_Default_Ignorable_Code_Point, value) {
			nonDisplayValues = append(nonDisplayValues, string(value))
		}
	}
	if len(nonDisplayValues) != 4206 {
		return fmt.Errorf(
			"Go Unicode non-display set has %d values, want pinned Unicode 15 count 4206",
			len(nonDisplayValues),
		)
	}
	var allNonDisplayValuesDetected bool
	if err := conn.QueryRow(ctx, `
SELECT COALESCE(bool_and(mem_model_text_has_non_display_character(value)), false)
  FROM unnest($1::text[]) AS values(value)
`, nonDisplayValues).Scan(&allNonDisplayValuesDetected); err != nil {
		return fmt.Errorf("verify database non-display character set: %w", err)
	}
	if !allNonDisplayValuesDetected {
		return errors.New("database non-display character set does not cover Go Unicode 15")
	}
	var safeValueMisclassified bool
	if err := conn.QueryRow(ctx, `
SELECT COALESCE(bool_or(mem_model_text_has_non_display_character(value)), false)
  FROM unnest($1::text[]) AS values(value)
`, []string{"plain text", "café", "中文", "visible emoji 😀"}).Scan(
		&safeValueMisclassified,
	); err != nil {
		return fmt.Errorf("verify safe database display values: %w", err)
	}
	if safeValueMisclassified {
		return errors.New("database non-display character set rejects ordinary display text")
	}

	constraintTests := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "caption reasoning opener",
			query: `UPDATE files SET caption = $1 WHERE id = $2`,
			args: []any{
				`<ReAsOnInG visibility="hidden">private`,
				migrationReasoningFileID,
			},
		},
		{
			name:  "summary reasoning closer",
			query: `UPDATE files SET summary = $1 WHERE id = $2`,
			args:  []any{"visible</ReAsOnInG>", migrationReasoningFileID},
		},
		{
			name: "annotation reasoning opener",
			query: `
INSERT INTO file_annotations (
  file_id, stable_key, kind, value_text, confidence, source, analysis_version
)
VALUES ($1, $2, 'tag', $3, 0.5, 'model', 'migration-guard-v1')
`,
			args: []any{
				migrationReasoningFileID,
				"migration-reasoning-opener",
				`<reasoning visibility="hidden">private`,
			},
		},
		{
			name: "annotation reasoning closer",
			query: `
INSERT INTO file_annotations (
  file_id, stable_key, kind, value_text, confidence, source, analysis_version
)
VALUES ($1, $2, 'tag', $3, 0.5, 'model', 'migration-guard-v1')
`,
			args: []any{
				migrationReasoningFileID,
				"migration-reasoning-closer",
				"visible</reasoning>",
			},
		},
		{
			name: "annotation JSON-like value",
			query: `
INSERT INTO file_annotations (
  file_id, stable_key, kind, value_text, confidence, source, analysis_version
)
VALUES ($1, $2, 'tag', $3, 0.5, 'model', 'migration-guard-v1')
`,
			args: []any{
				migrationReasoningFileID,
				"migration-json-like-value",
				`{"analysis":"private"}`,
			},
		},
		{
			name:  "caption Unicode-whitespace JSON-like value",
			query: `UPDATE files SET caption = $1 WHERE id = $2`,
			args: []any{
				"\u00a0{\"analysis\":\"private\"}",
				migrationUnicodeFileID,
			},
		},
		{
			name:  "summary Unicode-whitespace reasoning prefix",
			query: `UPDATE files SET summary = $1 WHERE id = $2`,
			args: []any{
				"\u3000Reasoning: private",
				migrationUnicodeFileID,
			},
		},
		{
			name: "description Unicode-whitespace JSON-like value",
			query: `
INSERT INTO file_annotations (
  file_id, stable_key, kind, value_text, confidence, source, analysis_version
)
VALUES ($1, $2, 'description', $3, 0.5, 'model', 'migration-guard-v1')
`,
			args: []any{
				migrationUnicodeFileID,
				"migration-unicode-description",
				"\u00a0{\"analysis\":\"private\",\"answer\":\"public\"}",
			},
		},
		{
			name: "tag Unicode-whitespace array value",
			query: `
INSERT INTO file_annotations (
  file_id, stable_key, kind, value_text, confidence, source, analysis_version
)
VALUES ($1, $2, 'tag', $3, 0.5, 'model', 'migration-guard-v1')
`,
			args: []any{
				migrationUnicodeFileID,
				"migration-unicode-tag",
				"\u3000[\"private\"]",
			},
		},
		{
			name:  "caption BOM-prefixed JSON-like value",
			query: `UPDATE files SET caption = $1 WHERE id = $2`,
			args: []any{
				"\ufeff{\"analysis\":\"private\",\"answer\":\"public\"}",
				migrationFormatFileID,
			},
		},
		{
			name:  "summary embedded zero-width value",
			query: `UPDATE files SET summary = $1 WHERE id = $2`,
			args: []any{
				"visible\u200bprivate",
				migrationFormatFileID,
			},
		},
		{
			name: "description BOM-prefixed JSON-like value",
			query: `
INSERT INTO file_annotations (
  file_id, stable_key, kind, value_text, confidence, source, analysis_version
)
VALUES ($1, $2, 'description', $3, 0.5, 'model', 'migration-guard-v1')
`,
			args: []any{
				migrationFormatFileID,
				"migration-format-description",
				"\ufeff{\"analysis\":\"private\",\"answer\":\"public\"}",
			},
		},
		{
			name: "tag zero-width-prefixed array value",
			query: `
INSERT INTO file_annotations (
  file_id, stable_key, kind, value_text, confidence, source, analysis_version
)
VALUES ($1, $2, 'tag', $3, 0.5, 'model', 'migration-guard-v1')
`,
			args: []any{
				migrationFormatFileID,
				"migration-format-tag",
				"\u200b[\"private\"]",
			},
		},
		{
			name:  "summary embedded word-joiner value",
			query: `UPDATE files SET summary = $1 WHERE id = $2`,
			args: []any{
				"visible\u2060private",
				migrationFormatRangeID,
			},
		},
		{
			name:  "caption Unicode-15 format value",
			query: `UPDATE files SET caption = $1 WHERE id = $2`,
			args: []any{
				"visible\U00013439private",
				migrationFormatRangeID,
			},
		},
		{
			name: "annotation embedded word-joiner value",
			query: `
INSERT INTO file_annotations (
  file_id, stable_key, kind, value_text, confidence, source, analysis_version
)
VALUES ($1, $2, 'tag', $3, 0.5, 'model', 'migration-guard-v1')
`,
			args: []any{
				migrationFormatRangeID,
				"migration-format-word-joiner",
				"visible\u2060private",
			},
		},
		{
			name:  "summary variation-selector value",
			query: `UPDATE files SET summary = $1 WHERE id = $2`,
			args: []any{
				"visible\ufe0fprivate",
				migrationIgnorableFileID,
			},
		},
		{
			name:  "caption combining-grapheme-joiner value",
			query: `UPDATE files SET caption = $1 WHERE id = $2`,
			args: []any{
				"visible\u034fprivate",
				migrationIgnorableFileID,
			},
		},
		{
			name: "annotation variation-selector value",
			query: `
INSERT INTO file_annotations (
  file_id, stable_key, kind, value_text, confidence, source, analysis_version
)
VALUES ($1, $2, 'tag', $3, 0.5, 'model', 'migration-guard-v1')
`,
			args: []any{
				migrationIgnorableFileID,
				"migration-default-ignorable",
				"visible\ufe0fprivate",
			},
		},
		{
			name: "annotation provider word-joiner value",
			query: `
INSERT INTO file_annotations (
  file_id, stable_key, kind, value_text, confidence, source,
  provider, processor, analysis_version
)
VALUES ($1, $2, 'tag', 'safe-provider-provenance', 0.5, 'model',
        $3, 'text', 'migration-guard-v1')
`,
			args: []any{
				migrationIgnorableFileID,
				"migration-provider-word-joiner",
				"test\u2060private-provider",
			},
		},
		{
			name: "annotation processor variation-selector value",
			query: `
INSERT INTO file_annotations (
  file_id, stable_key, kind, value_text, confidence, source,
  provider, processor, analysis_version
)
VALUES ($1, $2, 'tag', 'safe-processor-provenance', 0.5, 'model',
        'migration:test', $3, 'migration-guard-v1')
`,
			args: []any{
				migrationIgnorableFileID,
				"migration-processor-variation-selector",
				"text\ufe0fprivate-processor",
			},
		},
		{
			name: "annotation analysis-version grapheme-joiner value",
			query: `
INSERT INTO file_annotations (
  file_id, stable_key, kind, value_text, confidence, source,
  provider, processor, analysis_version
)
VALUES ($1, $2, 'tag', 'safe-version-provenance', 0.5, 'model',
        'migration:test', 'text', $3)
`,
			args: []any{
				migrationIgnorableFileID,
				"migration-version-grapheme-joiner",
				"migration-\u034fprivate-version",
			},
		},
	}
	for _, test := range constraintTests {
		if _, err := conn.Exec(ctx, test.query, test.args...); err == nil {
			return fmt.Errorf("%s unexpectedly passed its database check", test.name)
		} else {
			var postgresError *pgconn.PgError
			if !errors.As(err, &postgresError) || postgresError.Code != "23514" {
				return fmt.Errorf(
					"%s returned %v, want PostgreSQL check violation",
					test.name,
					err,
				)
			}
		}
	}
	return nil
}

func seedFileEnrichment(ctx context.Context) error {
	conn, err := targetConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration file seed: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
INSERT INTO users (id, email, password_hash)
VALUES ($1, 'migration-file-preservation@example.invalid', 'test')
ON CONFLICT (id) DO NOTHING
`, migrationFileUserID); err != nil {
		return fmt.Errorf("seed migration file owner: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO files (
  id, user_id, name, path, size, sha256, mime, storage_key,
  summary, tags, user_tags, source_metadata, processor_metadata, index_status
)
VALUES (
  $2, $1, 'preserved.txt', '/', 17, repeat('a', 64), 'text/plain',
  'migration/preserved-object', 'accepted description',
  ARRAY['manual','model-reviewed'], ARRAY['manual'],
  '{"source_kind":"import"}'::jsonb, '{"processor":"text"}'::jsonb, 'done'
)
ON CONFLICT (id) DO NOTHING
`, migrationFileUserID, migrationFileID); err != nil {
		return fmt.Errorf("seed migration file: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO file_annotations (
  file_id, stable_key, kind, value_text, confidence, source,
  provider, processor, analysis_version, status, state_version,
  decided_by_user_id, decided_at
)
VALUES (
  $2, 'migration-review', 'tag', 'model-reviewed', 0.9, 'model',
  'migration:test', 'text', 'migration-v1', 'accepted', 2, $1, now()
)
ON CONFLICT (file_id, stable_key) DO NOTHING
`, migrationFileUserID, migrationFileID); err != nil {
		return fmt.Errorf("seed migration annotation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration file seed: %w", err)
	}
	return nil
}

func assertFilePreserved(ctx context.Context, state string) error {
	conn, err := targetConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	var (
		name       string
		size       int64
		sha256     string
		mime       string
		storageKey string
		summary    string
		tags       []string
	)
	if err := conn.QueryRow(ctx, `
SELECT name, size, sha256, mime, storage_key, summary, tags
  FROM files
 WHERE id = $1 AND user_id = $2
`, migrationFileID, migrationFileUserID).Scan(
		&name,
		&size,
		&sha256,
		&mime,
		&storageKey,
		&summary,
		&tags,
	); err != nil {
		return fmt.Errorf("load preserved migration file: %w", err)
	}
	if name != "preserved.txt" ||
		size != 17 ||
		sha256 != strings.Repeat("a", 64) ||
		mime != "text/plain" ||
		storageKey != "migration/preserved-object" ||
		summary != "accepted description" ||
		len(tags) != 1 ||
		tags[0] != "manual" {
		return fmt.Errorf(
			"migration file or trust projection changed: name=%q size=%d sha=%q mime=%q key=%q summary=%q tags=%v",
			name,
			size,
			sha256,
			mime,
			storageKey,
			summary,
			tags,
		)
	}
	var enrichmentSchema bool
	if err := conn.QueryRow(ctx, `
SELECT
  EXISTS (
    SELECT 1
      FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name = 'files'
       AND column_name = 'user_tags'
  )
  AND to_regclass('public.file_annotations') IS NOT NULL
`).Scan(&enrichmentSchema); err != nil {
		return fmt.Errorf("inspect %s enrichment schema: %w", state, err)
	}
	if enrichmentSchema != (state == "up") {
		return fmt.Errorf(
			"unexpected %s enrichment schema while checking trust projection: present=%t",
			state,
			enrichmentSchema,
		)
	}
	if state == "up" {
		var userTags []string
		if err := conn.QueryRow(ctx, `
SELECT user_tags
  FROM files
 WHERE id = $1 AND user_id = $2
`, migrationFileID, migrationFileUserID).Scan(&userTags); err != nil {
			return fmt.Errorf("load re-up user tags: %w", err)
		}
		if len(userTags) != 1 || userTags[0] != "manual" {
			return fmt.Errorf(
				"accepted model tag was laundered into user_tags on re-up: %v",
				userTags,
			)
		}
		var annotationCount int
		if err := conn.QueryRow(ctx, `
SELECT count(*)
  FROM file_annotations
 WHERE file_id = $1
`, migrationFileID).Scan(&annotationCount); err != nil {
			return fmt.Errorf("count re-up annotations: %w", err)
		}
		if annotationCount != 0 {
			return fmt.Errorf(
				"discarded downgrade annotations reappeared on re-up: %d",
				annotationCount,
			)
		}
	}
	return nil
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
		rawKey                bool
		hashedKey             bool
		replayPrincipal       bool
		oldFileIndex          bool
		hashConstraint        bool
		redactConstraint      bool
		receiptConstraint     bool
		replayConstraint      bool
		managedEntitlements   bool
		fileUserTags          bool
		fileSourceMetadata    bool
		fileProcessorMetadata bool
		fileAnnotations       bool
		fileCaptionConstraint bool
		fileSummaryConstraint bool
		canonicalCaption      bool
		canonicalSummary      bool
		nonDisplayFunction    bool
		workspaceAIProfiles   bool
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
  ),
  EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name = 'files'
       AND column_name = 'user_tags'
  ),
  EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name = 'files'
       AND column_name = 'source_metadata'
  ),
  EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'public'
       AND table_name = 'files'
       AND column_name = 'processor_metadata'
  ),
  to_regclass('public.file_annotations') IS NOT NULL,
  EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conname = 'files_caption_safe_model_text'
  ),
  EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conname = 'files_summary_safe_model_text'
  ),
  EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conname = 'files_caption_canonical_model_text'
       AND conrelid = 'public.files'::regclass
  ),
  EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conname = 'files_summary_canonical_model_text'
       AND conrelid = 'public.files'::regclass
  ),
  to_regprocedure('public.mem_model_text_has_non_display_character(text)') IS NOT NULL,
  to_regclass('public.workspace_ai_profiles') IS NOT NULL
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
		&fileUserTags,
		&fileSourceMetadata,
		&fileProcessorMetadata,
		&fileAnnotations,
		&fileCaptionConstraint,
		&fileSummaryConstraint,
		&canonicalCaption,
		&canonicalSummary,
		&nonDisplayFunction,
		&workspaceAIProfiles,
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
			!managedEntitlements &&
			!fileUserTags &&
			!fileSourceMetadata &&
			!fileProcessorMetadata &&
			!fileAnnotations &&
			!fileCaptionConstraint &&
			!fileSummaryConstraint &&
			!canonicalCaption &&
			!canonicalSummary &&
			!nonDisplayFunction &&
			!workspaceAIProfiles
	case "up":
		valid = !rawKey &&
			hashedKey &&
			replayPrincipal &&
			!oldFileIndex &&
			hashConstraint &&
			redactConstraint &&
			receiptConstraint &&
			replayConstraint &&
			managedEntitlements &&
			fileUserTags &&
			fileSourceMetadata &&
			fileProcessorMetadata &&
			fileAnnotations &&
			fileCaptionConstraint &&
			fileSummaryConstraint &&
			canonicalCaption &&
			canonicalSummary &&
			nonDisplayFunction &&
			workspaceAIProfiles
	}
	if !valid {
		return fmt.Errorf(
			"unexpected %s schema state: raw_key=%t hashed_key=%t replay_principal=%t old_file_index=%t hash_constraint=%t redact_constraint=%t receipt_constraint=%t replay_constraint=%t managed_entitlements=%t file_user_tags=%t file_source_metadata=%t file_processor_metadata=%t file_annotations=%t file_caption_constraint=%t file_summary_constraint=%t canonical_caption=%t canonical_summary=%t non_display_function=%t workspace_ai_profiles=%t",
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
			fileUserTags,
			fileSourceMetadata,
			fileProcessorMetadata,
			fileAnnotations,
			fileCaptionConstraint,
			fileSummaryConstraint,
			canonicalCaption,
			canonicalSummary,
			nonDisplayFunction,
			workspaceAIProfiles,
		)
	}
	return nil
}

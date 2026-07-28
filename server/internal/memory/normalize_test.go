package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validCommand() Command {
	return Command{
		WorkspaceID:    uuid.New(),
		Kind:           KindDecision,
		Content:        "Use PostgreSQL for memory recall.",
		Attributes:     json.RawMessage(`{"confidence":"confirmed"}`),
		Path:           "/Projects/mem",
		SourceType:     "agent",
		SourceLocator:  json.RawMessage(`{"kind":"message","index":3}`),
		ProducerAgent:  "codex",
		ProducerTask:   "task-42",
		IdempotencyKey: "task-42-decision",
	}
}

func TestNormalizeCommandCanonicalReplayHash(t *testing.T) {
	eventA := time.Date(2026, 7, 28, 12, 34, 56, 123, time.FixedZone("CST", 8*60*60))
	eventB := eventA.UTC()
	userA, userB := uuid.New(), uuid.New()
	tokenA, tokenB := uuid.New(), uuid.New()

	first := validCommand()
	first.CreatedByUserID = &userA
	first.CreatedByTokenID = &tokenA
	first.Kind = " DECISION "
	first.Content = "  Use PostgreSQL for memory recall.  "
	first.Attributes = json.RawMessage(`{"b":2, "a":{"z":1,"y":2}}`)
	first.Path = "/Projects//mem/"
	first.EventAt = &eventA
	first.SourceType = " AGENT "
	first.SourceLocator = json.RawMessage(`{"page":4,"span":{"end":9,"start":2}}`)
	first.IdempotencyKey = " key-a "

	second := first
	second.CreatedByUserID = &userB
	second.CreatedByTokenID = &tokenB
	second.Attributes = json.RawMessage(`{"a":{"y":2,"z":1},"b":2}`)
	second.Path = "/Projects/mem"
	second.EventAt = &eventB
	second.SourceType = "agent"
	second.SourceLocator = json.RawMessage(`{"span":{"start":2,"end":9},"page":4}`)
	second.IdempotencyKey = "key-b"

	gotA, err := normalizeCommand(first)
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := normalizeCommand(second)
	if err != nil {
		t.Fatal(err)
	}
	if gotA.requestSHA256 != gotB.requestSHA256 {
		t.Fatalf("equivalent normalized payload hashes differ:\n%s\n%s",
			gotA.requestSHA256, gotB.requestSHA256)
	}
	if gotA.Path != "/Projects/mem" {
		t.Fatalf("normalized path = %q", gotA.Path)
	}
	if gotA.Content != "Use PostgreSQL for memory recall." {
		t.Fatalf("normalized content = %q", gotA.Content)
	}
	if string(gotA.Attributes) != `{"a":{"y":2,"z":1},"b":2}` {
		t.Fatalf("canonical attributes = %s", gotA.Attributes)
	}
	if gotA.contentSHA256 == "" || len(gotA.contentSHA256) != 64 {
		t.Fatalf("content hash = %q", gotA.contentSHA256)
	}
	if len(gotA.idempotencyKeySHA256) != 64 ||
		gotA.idempotencyKeySHA256 == gotA.IdempotencyKey {
		t.Fatalf("idempotency key digest = %q", gotA.idempotencyKeySHA256)
	}
	if gotA.idempotencyKeySHA256 == gotB.idempotencyKeySHA256 {
		t.Fatal("different idempotency keys produced the same digest")
	}

	changed := second
	changed.Content = "Use SQLite for memory recall."
	gotChanged, err := normalizeCommand(changed)
	if err != nil {
		t.Fatal(err)
	}
	if gotA.requestSHA256 == gotChanged.requestSHA256 {
		t.Fatal("different content produced the same request hash")
	}
}

func TestNormalizeCommandValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Command)
	}{
		{"workspace", func(c *Command) { c.WorkspaceID = uuid.Nil }},
		{"kind", func(c *Command) { c.Kind = "prediction" }},
		{"content empty", func(c *Command) { c.Content = " \n\t " }},
		{"content too large", func(c *Command) { c.Content = strings.Repeat("a", maxContentBytes+1) }},
		{"path required", func(c *Command) { c.Path = "" }},
		{"path canonical rules", func(c *Command) { c.Path = "/a/../b" }},
		{"source required", func(c *Command) { c.SourceType = "" }},
		{"source type format", func(c *Command) { c.SourceType = "agent source" }},
		{"source ref too large", func(c *Command) { c.SourceRef = strings.Repeat("a", maxSourceRefBytes+1) }},
		{"source file sha", func(c *Command) { c.SourceFileSHA256 = "not-a-sha" }},
		{"producer too long", func(c *Command) { c.ProducerAgent = strings.Repeat("界", maxProducerIDRunes+1) }},
		{"attributes array", func(c *Command) { c.Attributes = json.RawMessage(`[1]`) }},
		{"attributes null", func(c *Command) { c.Attributes = json.RawMessage(`null`) }},
		{"attributes too large", func(c *Command) {
			c.Attributes = json.RawMessage(`{"value":"` + strings.Repeat("a", maxJSONObjectBytes) + `"}`)
		}},
		{"locator scalar", func(c *Command) { c.SourceLocator = json.RawMessage(`"page 4"`) }},
		{"key empty", func(c *Command) { c.IdempotencyKey = "" }},
		{"key too long", func(c *Command) { c.IdempotencyKey = strings.Repeat("界", 201) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			command := validCommand()
			tc.mutate(&command)
			_, err := normalizeCommand(command)
			if !errors.Is(err, ErrInvalidCommand) {
				t.Fatalf("error = %v, want ErrInvalidCommand", err)
			}
		})
	}
}

func TestNormalizeCommandBoundariesAndKinds(t *testing.T) {
	for kind := range validKinds {
		command := validCommand()
		command.Kind = kind
		if _, err := normalizeCommand(command); err != nil {
			t.Errorf("kind %q: %v", kind, err)
		}
	}

	command := validCommand()
	command.Content = strings.Repeat("a", maxContentBytes)
	command.IdempotencyKey = strings.Repeat("界", maxIdempotencyKeyRunes)
	if _, err := normalizeCommand(command); err != nil {
		t.Fatalf("valid size boundaries rejected: %v", err)
	}
}

func TestNormalizeAllowedPathsCompactsWithoutPrefixConfusion(t *testing.T) {
	got, err := normalizeAllowedPaths([]string{
		"/Work/contracts/",
		"/Work",
		"/Workflows",
		"/Work/contracts",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/Work", "/Workflows"}
	if len(got) != len(want) {
		t.Fatalf("allowed paths = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allowed paths = %#v, want %#v", got, want)
		}
	}
	if _, err := normalizeAllowedPaths([]string{""}); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("empty legacy entry error = %v", err)
	}
}

func TestMemoryJSONHidesAuthenticationAndIdempotencyInternals(t *testing.T) {
	token := uuid.New()
	encoded, err := json.Marshal(Memory{
		ID:                   uuid.New(),
		CreatedByTokenID:     &token,
		IdempotencyKeySHA256: strings.Repeat("c", 64),
		RequestSHA256:        strings.Repeat("a", 64),
		ContentSHA256:        strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	for _, forbidden := range []string{
		"created_by_token_id",
		"idempotency_key",
		"request_sha256",
		"private-key",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("public JSON leaked %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(raw, "content_sha256") {
		t.Fatalf("public JSON omitted content hash: %s", raw)
	}
}

func TestPathFilterSQLDoesNotInterpretWildcardCharacters(t *testing.T) {
	args, where := appendPathFilters(
		[]any{"workspace", "active"},
		[]string{"m.workspace_id = $1"},
		"m.path",
		"/100%_done",
		[]string{"/Allowed_Path"},
	)
	joined := strings.Join(where, " ")
	if strings.Contains(joined, "LIKE") {
		t.Fatalf("path SQL uses wildcard matching: %s", joined)
	}
	if !strings.Contains(joined, "left(m.path") {
		t.Fatalf("path SQL lacks a segment-safe comparison: %s", joined)
	}
	if args[2] != "/100%_done" || args[3] != "/Allowed_Path" {
		t.Fatalf("path bind args = %#v", args)
	}
}

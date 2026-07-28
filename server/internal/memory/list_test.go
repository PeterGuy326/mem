package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

func TestNormalizeListQueryDefaultsAndCanonicalFingerprint(t *testing.T) {
	workspaceID := uuid.New()
	first, err := normalizeListQuery(ListQuery{
		WorkspaceID:  workspaceID,
		AllowedPaths: []string{"/Work/contracts", "/Work", "/Shared"},
		Kinds:        []string{KindNote, KindDecision, KindNote},
		Limit:        maxListLimit + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.scope != "/" || !first.recursive {
		t.Fatalf("default scope/recursive = %q/%t", first.scope, first.recursive)
	}
	if first.limit != maxListLimit {
		t.Fatalf("limit = %d, want %d", first.limit, maxListLimit)
	}
	if len(first.statuses) != 1 || first.statuses[0] != StatusActive {
		t.Fatalf("statuses = %#v", first.statuses)
	}

	second, err := normalizeListQuery(ListQuery{
		WorkspaceID:       workspaceID,
		Scope:             "/",
		AllowedPaths:      []string{"/Shared", "/Work"},
		Kinds:             []string{KindDecision, KindNote},
		LifecycleStatuses: []string{StatusActive},
		Limit:             maxListLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.filterHash != second.filterHash {
		t.Fatalf("equivalent filters produced different hashes:\n%s\n%s",
			first.filterHash, second.filterHash)
	}
}

func TestListCursorRoundTripAndFilterBinding(t *testing.T) {
	workspaceID := uuid.New()
	query, err := normalizeListQuery(ListQuery{
		WorkspaceID:  workspaceID,
		Scope:        "/Work",
		AllowedPaths: []string{"/Work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 7, 28, 12, 34, 56, 789, time.UTC)
	id := uuid.New()
	cursor, err := encodeListCursor(createdAt, id, query.filterHash)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeListCursor(cursor, query.filterHash)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.createdAt.Equal(createdAt) || decoded.id != id {
		t.Fatalf("decoded cursor = %+v", decoded)
	}
	if _, err := decodeListCursor(cursor, strings.Repeat("0", 64)); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("filter mismatch error = %v", err)
	}
	for _, malformed := range []string{"", "not-base64", strings.Repeat("a", maxCursorBytes+1)} {
		if _, err := decodeListCursor(malformed, query.filterHash); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("decode %q error = %v", malformed, err)
		}
	}
}

func TestListRejectsForgottenAndCursorReuseAcrossAuthorization(t *testing.T) {
	workspaceID := uuid.New()
	if _, err := normalizeListQuery(ListQuery{
		WorkspaceID:       workspaceID,
		LifecycleStatuses: []string{StatusForgotten},
	}); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("forgotten list error = %v", err)
	}

	first, err := normalizeListQuery(ListQuery{
		WorkspaceID:  workspaceID,
		AllowedPaths: []string{"/Work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := encodeListCursor(time.Now().UTC(), uuid.New(), first.filterHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeListQuery(ListQuery{
		WorkspaceID:  workspaceID,
		AllowedPaths: []string{"/Private"},
		Cursor:       cursor,
	}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cross-authorization cursor error = %v", err)
	}
}

func TestMemorySummaryJSONIsBoundedProjection(t *testing.T) {
	excerpt := strings.Repeat("界", 500)
	if utf8.RuneCountInString(excerpt) != 500 {
		t.Fatal("bad test fixture")
	}
	encoded, err := json.Marshal(MemorySummary{
		ID:            uuid.New(),
		WorkspaceID:   uuid.New(),
		Kind:          KindNote,
		Excerpt:       excerpt,
		ContentLength: 70000,
		Citation:      "mem://memories/example",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	for _, forbidden := range []string{`"content":`, `"attributes":`, `"source_locator":`} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("summary leaked %s: %s", forbidden, raw)
		}
	}
	if !strings.Contains(raw, `"excerpt"`) || !strings.Contains(raw, `"content_length":70000`) {
		t.Fatalf("summary omitted bounded fields: %s", raw)
	}
}

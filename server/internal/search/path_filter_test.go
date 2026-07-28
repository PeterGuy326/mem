package search

import (
	"strings"
	"testing"
)

func TestAppendPathFiltersTreatsWildcardCharactersLiterally(t *testing.T) {
	args, where := appendPathFilters(
		[]any{"vector", "user"},
		[]string{"f.user_id = $2"},
		"/100%_done",
		[]string{"/Allowed_Path"},
	)
	joined := strings.Join(where, " ")
	if strings.Contains(joined, "LIKE") {
		t.Fatalf("path filter must not use wildcard LIKE matching: %s", joined)
	}
	if !strings.Contains(joined, "left(f.path") {
		t.Fatalf("path filter lacks segment-safe prefix comparison: %s", joined)
	}
	if args[2] != "/100%_done" || args[3] != "/Allowed_Path" {
		t.Fatalf("unexpected bind args: %#v", args)
	}
}

func TestAppendPathFiltersEmptyLegacyEntryFailsClosed(t *testing.T) {
	_, where := appendPathFilters(
		[]any{"vector", "user"},
		[]string{"f.user_id = $2"},
		"/",
		[]string{""},
	)
	if !strings.Contains(strings.Join(where, " "), "FALSE") {
		t.Fatalf("empty legacy allow-list must fail closed: %#v", where)
	}
}

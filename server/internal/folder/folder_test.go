package folder

import (
	"errors"
	"strings"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/pathx"
)

// These tests exercise the pure logic paths of the folder package that don't
// require a live PostgreSQL instance. The transactional cascade and SQL paths
// are covered by integration tests against testcontainers in W2+ (see README
// "Open questions / TODO"). Here we verify path normalization, cycle
// detection, prefix rewrites, and like-pattern generation — the three areas
// where bugs would silently corrupt user data.

// TestLikePrefix locks in the SQL pattern we use everywhere for subtree
// matches: must be unambiguous, must include the trailing slash so "/a"
// does NOT match "/abc".
func TestLikePrefix(t *testing.T) {
	t.Parallel()
	if got := LikePrefix("/Photos"); got != "/Photos/%" {
		t.Fatalf("LikePrefix(/Photos) = %q, want /Photos/%%", got)
	}
	if got := LikePrefix("/a/b"); got != "/a/b/%" {
		t.Fatalf("LikePrefix(/a/b) = %q, want /a/b/%%", got)
	}
}

// TestCycleDetection verifies that Move refuses any destination that would
// place a folder inside itself or below itself.
func TestCycleDetection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src, dstParent string
		wantCycle      bool
	}{
		// classic: move /a under /a -> /a/a
		{"/a", "/a", true},
		// deeper: move /a under /a/b -> /a/b/a
		{"/a", "/a/b", true},
		// move /Photos to /Photos itself (basename collision is still a cycle case via path math)
		{"/Photos/2012", "/Photos/2012", true},
		// legal: move /a under /b
		{"/a", "/b", false},
		// legal: move /Photos/2012 to root
		{"/Photos/2012", "/", false},
		// sibling collision: /ab is NOT a descendant of /a
		{"/a", "/ab", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.src+"->"+tc.dstParent, func(t *testing.T) {
			t.Parallel()
			src, err := pathx.Normalize(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			dst, err := pathx.Normalize(tc.dstParent)
			if err != nil {
				t.Fatal(err)
			}
			got := pathx.IsDescendantOrSelf(dst, src)
			if got != tc.wantCycle {
				t.Fatalf("IsDescendantOrSelf(%q, %q) = %v, want %v", dst, src, got, tc.wantCycle)
			}
		})
	}
}

// TestMkdirPAncestors locks the mkdir -p invariant: creating /a/b/c must
// produce exactly the three rows ["/a", "/a/b", "/a/b/c"] in top-down order.
func TestMkdirPAncestors(t *testing.T) {
	t.Parallel()
	got := pathx.Ancestors("/Photos/2012/Yunnan")
	want := []string{"/Photos", "/Photos/2012", "/Photos/2012/Yunnan"}
	if len(got) != len(want) {
		t.Fatalf("ancestors len mismatch: got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("ancestor[%d]: got %q want %q", i, got[i], want[i])
		}
	}
	// Idempotent property: re-running Ancestors yields the same list, so a
	// second mkdir -p on the same path is a noop (every row exists).
	got2 := pathx.Ancestors("/Photos/2012/Yunnan")
	for i := range got2 {
		if got[i] != got2[i] {
			t.Fatalf("ancestors not deterministic at [%d]: %q vs %q", i, got[i], got2[i])
		}
	}
}

// TestRenameMovePathMath exercises the path arithmetic used by rewritePrefixTx
// (the SQL-side `substring(path FROM oldLen+1)` trick).
func TestRenameMovePathMath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc, old, oldNew, child, want string
	}{
		{"rename /a -> /A", "/a", "/A", "/a", "/A"},
		{"rename /a -> /A cascades /a/b -> /A/b", "/a", "/A", "/a/b", "/A/b"},
		{"rename /a -> /A cascades /a/b/c.jpg path", "/a", "/A", "/a/b/c", "/A/b/c"},
		{"move /Photos -> /Albums cascades /Photos/2012", "/Photos", "/Albums", "/Photos/2012", "/Albums/2012"},
		{"non-descendant /abc is untouched", "/a", "/A", "/abc", "/abc"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			got := pathx.ReplacePrefix(tc.child, tc.old, tc.oldNew)
			if got != tc.want {
				t.Fatalf("ReplacePrefix(%q,%q,%q) = %q, want %q", tc.child, tc.old, tc.oldNew, got, tc.want)
			}
		})
	}
}

// TestNormalizationGuardsBadNames is a smoke test that nasty user input fails
// fast at the normalize gate — this is the only thing between a malicious
// payload and a corrupt folders.path row.
func TestNormalizationGuardsBadNames(t *testing.T) {
	t.Parallel()
	bad := []string{"/a/./b", "/a/../b", "/a//b/\x00", "relative/path"}
	for _, s := range bad {
		if _, err := pathx.Normalize(s); err == nil {
			t.Fatalf("expected normalize error for %q", s)
		}
	}
}

// TestErrCycleSentinel makes sure the sentinel is exported under the expected
// name (it shows up in API error responses, breaking a rename would be silent).
func TestErrCycleSentinel(t *testing.T) {
	t.Parallel()
	if !errors.Is(ErrCycle, ErrCycle) {
		t.Fatal("ErrCycle sentinel broken")
	}
	if !strings.Contains(ErrCycle.Error(), "cannot move") {
		t.Fatalf("ErrCycle message changed unexpectedly: %q", ErrCycle.Error())
	}
}

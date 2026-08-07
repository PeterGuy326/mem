package folder

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/pathx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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
		{"percent and underscore are literal", "/100%_done", "/renamed_%", "/100%_done/child", "/renamed_%/child"},
		{"percent is not a wildcard", "/100%_done", "/renamed_%", "/100xx_done/child", "/100xx_done/child"},
		{"underscore is not a wildcard", "/100%_done", "/renamed_%", "/100%Xdone/child", "/100%Xdone/child"},
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

func TestErrContainsMemoriesSentinel(t *testing.T) {
	t.Parallel()
	if !errors.Is(ErrContainsMemories, ErrContainsMemories) {
		t.Fatal("ErrContainsMemories sentinel broken")
	}
	if !strings.Contains(ErrContainsMemories.Error(), "forget") {
		t.Fatalf("ErrContainsMemories must direct callers to explicit forget: %q", ErrContainsMemories.Error())
	}
}

func TestErrContainsTaskStateSentinel(t *testing.T) {
	t.Parallel()
	if !errors.Is(ErrContainsTaskState, ErrContainsTaskState) {
		t.Fatal("ErrContainsTaskState sentinel broken")
	}
	if !strings.Contains(ErrContainsTaskState.Error(), "re-scope") {
		t.Fatalf("ErrContainsTaskState must require explicit re-scope: %q",
			ErrContainsTaskState.Error())
	}
}

// TestMemoryPathLifecycleIntegration validates the SQL-side contract against a
// real PostgreSQL schema. It is opt-in so ordinary unit-test runs stay
// hermetic:
//
//	MEM_TEST_DB=postgres://mem:mem@localhost:5432/mem_test?sslmode=disable \
//	  go test ./internal/folder -run TestMemoryPathLifecycleIntegration
//
// A session-local memories table shadows the evolving public table. This keeps
// the test pinned to the folder package's required columns without coupling it
// to unrelated memory payload columns.
func TestMemoryPathLifecycleIntegration(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping DB integration test")
	}

	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse MEM_TEST_DB: %v", err)
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatalf("refusing to modify non-test database %q; MEM_TEST_DB must end in _test",
			cfg.ConnConfig.Database)
	}
	// PostgreSQL temporary tables are session-local. One connection guarantees
	// every service transaction sees the shadow table created below.
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `
		CREATE TEMP TABLE memories (
			id               uuid PRIMARY KEY,
			workspace_id     uuid NOT NULL,
			path             text NOT NULL,
			lifecycle_status text NOT NULL,
			updated_at       timestamptz NOT NULL DEFAULT now()
		) ON COMMIT PRESERVE ROWS
	`); err != nil {
		t.Fatalf("create temporary memories table: %v", err)
	}

	userID := folderTestUUID(t, ctx, pool,
		`INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`,
		"folder-memory-"+uuid.NewString()+"@example.com")
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	}()
	workspaceID := folderTestUUID(t, ctx, pool,
		`INSERT INTO workspaces (name, resource_owner_user_id)
		 VALUES ('folder memory test', $1) RETURNING id`,
		userID)

	svc := New(pool)
	for _, p := range []string{
		"/A%_/Child",
		"/AtomicSource/original/Child",
		"/AtomicDestination/taken",
		"/AtomicDestination/original",
		"/Destination_100%",
		"/Memory_Only%",
		"/Task_Only%/Child",
	} {
		if _, err := svc.Create(ctx, userID, p); err != nil {
			t.Fatalf("Create(%q): %v", p, err)
		}
	}

	type memoryRow struct {
		id     uuid.UUID
		path   string
		status string
	}
	atomicMemoryID := uuid.New()
	rows := []memoryRow{
		{id: uuid.New(), path: "/A%_", status: "active"},
		{id: uuid.New(), path: "/A%_/Child", status: "archived"},
		{id: uuid.New(), path: "/Axx/Child", status: "active"},
		{id: uuid.New(), path: "/A%_suffix", status: "active"},
		{id: uuid.New(), path: "/Memory_Only%", status: "active"},
		{id: atomicMemoryID, path: "/AtomicSource/original/Child", status: "active"},
	}
	for _, row := range rows {
		if _, err := pool.Exec(ctx,
			`INSERT INTO memories (id, workspace_id, path, lifecycle_status)
			 VALUES ($1, $2, $3, $4)`,
			row.id, workspaceID, row.path, row.status); err != nil {
			t.Fatalf("insert memory %q: %v", row.path, err)
		}
	}

	atomicRoot, err := svc.Get(ctx, userID, "/AtomicSource/original")
	if err != nil {
		t.Fatalf("load atomic relocation source: %v", err)
	}
	if err := svc.Relocate(
		ctx,
		userID,
		atomicRoot.ID,
		"/AtomicSource/original",
		"/AtomicDestination",
		"taken",
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting Relocate = %v, want ErrConflict", err)
	}
	if _, err := svc.Get(ctx, userID, "/AtomicSource/original/Child"); err != nil {
		t.Fatalf("conflicting Relocate changed source: %v", err)
	}
	if got := folderTestMemoryPath(t, ctx, pool, atomicMemoryID); got != "/AtomicSource/original/Child" {
		t.Fatalf("conflicting Relocate changed memory path: %q", got)
	}

	if err := svc.Relocate(
		ctx,
		userID,
		atomicRoot.ID,
		"/AtomicSource/original",
		"/AtomicDestination",
		"renamed_%",
	); err != nil {
		t.Fatalf("Relocate around intermediate-name conflict: %v", err)
	}
	if _, err := svc.Get(ctx, userID, "/AtomicDestination/renamed_%/Child"); err != nil {
		t.Fatalf("relocated literal-segment descendant missing: %v", err)
	}
	if _, err := svc.Get(ctx, userID, "/AtomicDestination/original"); err != nil {
		t.Fatalf("intermediate-name conflict changed: %v", err)
	}
	if got := folderTestMemoryPath(t, ctx, pool, atomicMemoryID); got != "/AtomicDestination/renamed_%/Child" {
		t.Fatalf("relocated memory path = %q", got)
	}
	if err := svc.Relocate(
		ctx,
		userID,
		atomicRoot.ID,
		"/AtomicSource/original",
		"/CreatedByStaleFolderRelocate/Nested",
		"stale_%",
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale-source folder Relocate = %v, want ErrNotFound", err)
	}
	if _, err := svc.Get(ctx, userID, "/AtomicDestination/renamed_%/Child"); err != nil {
		t.Fatalf("stale-source Relocate changed relocated folder: %v", err)
	}
	if _, err := svc.Get(ctx, userID, "/CreatedByStaleFolderRelocate"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale-source folder Relocate created destination: err=%v", err)
	}

	if err := svc.Rename(ctx, userID, "/A%_", "Renamed_%"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got := folderTestMemoryPath(t, ctx, pool, rows[0].id); got != "/Renamed_%" {
		t.Fatalf("renamed exact memory path = %q", got)
	}
	if got := folderTestMemoryPath(t, ctx, pool, rows[1].id); got != "/Renamed_%/Child" {
		t.Fatalf("renamed descendant memory path = %q", got)
	}
	if got := folderTestMemoryPath(t, ctx, pool, rows[2].id); got != "/Axx/Child" {
		t.Fatalf("percent wildcard rewrote unrelated memory: %q", got)
	}
	if got := folderTestMemoryPath(t, ctx, pool, rows[3].id); got != "/A%_suffix" {
		t.Fatalf("segment-prefix rewrote unrelated memory: %q", got)
	}

	if err := svc.Move(ctx, userID, "/Renamed_%", "/Destination_100%"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	movedPath := "/Destination_100%/Renamed_%"
	if got := folderTestMemoryPath(t, ctx, pool, rows[0].id); got != movedPath {
		t.Fatalf("moved exact memory path = %q, want %q", got, movedPath)
	}
	if got := folderTestMemoryPath(t, ctx, pool, rows[1].id); got != movedPath+"/Child" {
		t.Fatalf("moved descendant memory path = %q", got)
	}

	// A memory directly at an otherwise empty directory makes non-recursive
	// deletion non-empty.
	if err := svc.Delete(ctx, userID, "/Memory_Only%", false); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("non-recursive memory-only delete = %v, want ErrNotEmpty", err)
	}

	// Recursive deletion is deliberately stricter: it returns the dedicated
	// sentinel and leaves both the folder tree and memories intact.
	if err := svc.Delete(ctx, userID, movedPath, true); !errors.Is(err, ErrContainsMemories) {
		t.Fatalf("recursive delete with memories = %v, want ErrContainsMemories", err)
	}
	if _, err := svc.Get(ctx, userID, movedPath); err != nil {
		t.Fatalf("folder changed despite blocked recursive delete: %v", err)
	}
	if got := folderTestMemoryPath(t, ctx, pool, rows[1].id); got != movedPath+"/Child" {
		t.Fatalf("memory changed despite blocked recursive delete: %q", got)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_tasks (workspace_id, task_key, scope_path)
		 VALUES ($1, $2, $3)`,
		workspaceID, "folder-guard-"+uuid.NewString(), "/Task_Only%/Child"); err != nil {
		t.Fatalf("insert Agent task: %v", err)
	}
	taskFolder, err := svc.Get(ctx, userID, "/Task_Only%")
	if err != nil {
		t.Fatalf("load task-scoped folder: %v", err)
	}
	if err := svc.Relocate(
		ctx,
		userID,
		taskFolder.ID,
		taskFolder.Path,
		"/Task_Only%/Child",
		"cycle",
	); !errors.Is(err, ErrCycle) {
		t.Fatalf("cyclic Relocate = %v, want ErrCycle", err)
	}
	if err := svc.Relocate(
		ctx,
		userID,
		taskFolder.ID,
		taskFolder.Path,
		"/CreatedByBlockedTaskRelocate/Nested",
		"Task_Moved",
	); !errors.Is(err, ErrContainsTaskState) {
		t.Fatalf("Relocate with task checkpoint state = %v, want ErrContainsTaskState", err)
	}
	if _, err := svc.Get(ctx, userID, "/CreatedByBlockedTaskRelocate"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blocked task Relocate created destination: err=%v", err)
	}
	if err := svc.Rename(ctx, userID, "/Task_Only%", "Task_Moved"); !errors.Is(err, ErrContainsTaskState) {
		t.Fatalf("rename with task checkpoint state = %v, want ErrContainsTaskState", err)
	}
	if err := svc.Move(ctx, userID, "/Task_Only%", "/Destination_100%"); !errors.Is(err, ErrContainsTaskState) {
		t.Fatalf("move with task checkpoint state = %v, want ErrContainsTaskState", err)
	}
	if err := svc.Delete(ctx, userID, "/Task_Only%", true); !errors.Is(err, ErrContainsTaskState) {
		t.Fatalf("recursive delete with task checkpoint state = %v, want ErrContainsTaskState", err)
	}
	if _, err := svc.Get(ctx, userID, "/Task_Only%"); err != nil {
		t.Fatalf("task-scoped folder changed despite guard: %v", err)
	}
}

func folderTestUUID(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	query string,
	args ...any,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, query, args...).Scan(&id); err != nil {
		t.Fatalf("query UUID: %v", err)
	}
	return id
}

func folderTestMemoryPath(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	id uuid.UUID,
) string {
	t.Helper()
	var path string
	if err := pool.QueryRow(ctx, `SELECT path FROM memories WHERE id = $1`, id).Scan(&path); err != nil {
		t.Fatalf("select memory path: %v", err)
	}
	return path
}

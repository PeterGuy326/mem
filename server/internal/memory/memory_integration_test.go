package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	memdb "github.com/PeterGuy326/mem/server/internal/db"
)

// TestMemoryPostgres exercises the concurrency and lexical semantics that
// cannot be represented faithfully by a mock. It is opt-in so ordinary unit
// tests do not require a local database.
//
//	MEM_TEST_DB=postgres://mem:mem@localhost:5432/mem_test?sslmode=disable \
//	  go test ./internal/memory -run TestMemoryPostgres
func TestMemoryPostgres(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping PostgreSQL integration test")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse MEM_TEST_DB: %v", err)
	}
	if !strings.HasSuffix(config.ConnConfig.Database, "_test") {
		t.Fatalf("refusing to modify non-test database %q; MEM_TEST_DB must end in _test",
			config.ConnConfig.Database)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	database, err := memdb.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	userA, workspaceA := createTestTenant(t, ctx, database, "memory-a")
	userB, workspaceB := createTestTenant(t, ctx, database, "memory-b")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = database.Pool.Exec(cleanupCtx,
			`DELETE FROM users WHERE id = $1 OR id = $2`,
			userA, userB)
	})

	service := New(database.Pool)
	command := func(key, content, path string) Command {
		actorID := userA
		tokenID := uuid.New()
		return Command{
			WorkspaceID:      workspaceA,
			CreatedByUserID:  &actorID,
			CreatedByTokenID: &tokenID,
			Kind:             KindDecision,
			Content:          content,
			Attributes:       []byte(`{"confidence":"confirmed"}`),
			Path:             path,
			SourceType:       "agent",
			SourceRef:        "agent://codex/integration-test",
			SourceLocator:    []byte(`{"kind":"message","index":3}`),
			ProducerAgent:    "codex",
			ProducerSession:  "memory-integration",
			IdempotencyKey:   key,
		}
	}

	t.Run("replay conflict and occurrence identity", func(t *testing.T) {
		key := "replay-" + uuid.NewString()
		original := command(key, "Keep immutable occurrence semantics", "/Projects//mem/")
		first, err := service.Remember(ctx, original)
		if err != nil {
			t.Fatal(err)
		}
		if first.Replayed {
			t.Fatal("first write reported replay")
		}
		if first.Memory.Path != "/Projects/mem" {
			t.Fatalf("stored path = %q", first.Memory.Path)
		}

		retry := original
		retry.Path = "/Projects/mem"
		retry.Attributes = []byte(`{"confidence":"confirmed"}`)
		rotatedToken := uuid.New()
		retry.CreatedByTokenID = &rotatedToken
		replayed, err := service.Remember(ctx, retry)
		if err != nil {
			t.Fatal(err)
		}
		if !replayed.Replayed || replayed.Memory.ID != first.Memory.ID {
			t.Fatalf("replay = %+v; first id = %s", replayed, first.Memory.ID)
		}

		conflict := retry
		conflict.Content = "Overwrite the existing occurrence"
		if _, err := service.Remember(ctx, conflict); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("conflict error = %v", err)
		}

		secondKey := command(
			"same-content-"+uuid.NewString(),
			first.Memory.Content,
			"/Projects/mem",
		)
		second, err := service.Remember(ctx, secondKey)
		if err != nil {
			t.Fatal(err)
		}
		if second.Memory.ID == first.Memory.ID {
			t.Fatal("same content under a different key was deduplicated")
		}
	})

	t.Run("workspace and segment-safe path isolation", func(t *testing.T) {
		needle := "isolation-" + uuid.NewString()
		inside, err := service.Remember(ctx, command(
			"path-inside-"+uuid.NewString(),
			needle+" inside",
			"/Work/secret",
		))
		if err != nil {
			t.Fatal(err)
		}
		outside, err := service.Remember(ctx, command(
			"path-outside-"+uuid.NewString(),
			needle+" outside",
			"/Workflows",
		))
		if err != nil {
			t.Fatal(err)
		}

		if _, err := service.Get(ctx, Query{
			WorkspaceID: workspaceB,
			MemoryID:    inside.Memory.ID,
		}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-workspace Get error = %v", err)
		}
		if _, err := service.Get(ctx, Query{
			WorkspaceID:  workspaceA,
			MemoryID:     inside.Memory.ID,
			AllowedPaths: []string{"/Other"},
		}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("out-of-path Get error = %v", err)
		}
		if _, err := service.Get(ctx, Query{
			WorkspaceID:  workspaceA,
			MemoryID:     inside.Memory.ID,
			AllowedPaths: []string{"/Work"},
		}); err != nil {
			t.Fatalf("in-scope Get: %v", err)
		}

		hits, err := service.Recall(ctx, RecallQuery{
			WorkspaceID:  workspaceA,
			Text:         needle,
			AllowedPaths: []string{"/Work"},
			Limit:        20,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !containsMemory(hits, inside.Memory.ID) {
			t.Fatalf("in-scope recall omitted %s: %+v", inside.Memory.ID, hits)
		}
		if containsMemory(hits, outside.Memory.ID) {
			t.Fatalf("/Work incorrectly authorized /Workflows memory %s", outside.Memory.ID)
		}

		hits, err = service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceB,
			Text:        needle,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 0 {
			t.Fatalf("cross-workspace recall leaked %+v", hits)
		}

		deniedWrite := command(
			"denied-write-"+uuid.NewString(),
			"must never be persisted",
			"/Private",
		)
		deniedWrite.AllowedPaths = []string{"/Work"}
		if _, err := service.Remember(ctx, deniedWrite); !errors.Is(err, ErrNotFound) {
			t.Fatalf("out-of-path Remember error = %v", err)
		}

		movedCommand := command(
			"moved-replay-"+uuid.NewString(),
			"replay must authorize the stored current path",
			"/Work/moved",
		)
		moved, err := service.Remember(ctx, movedCommand)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Pool.Exec(ctx, `
			UPDATE memories
			   SET path = '/Private/moved', updated_at = now()
			 WHERE id = $1
		`, moved.Memory.ID); err != nil {
			t.Fatal(err)
		}
		movedCommand.AllowedPaths = []string{"/Work"}
		if _, err := service.Remember(ctx, movedCommand); !errors.Is(err, ErrNotFound) {
			t.Fatalf("replay after path move error = %v", err)
		}
	})

	t.Run("stable list pagination preserves workspace and path isolation", func(t *testing.T) {
		scope := "/Browse-" + uuid.NewString()
		ids := make([]uuid.UUID, 0, 3)
		for i := 1; i <= 3; i++ {
			result, err := service.Remember(ctx, command(
				fmt.Sprintf("list-%d-%s", i, uuid.NewString()),
				fmt.Sprintf("Browsable memory %d", i),
				scope+"/Project",
			))
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, result.Memory.ID)
		}
		outside, err := service.Remember(ctx, command(
			"list-prefix-trap-"+uuid.NewString(),
			"Must not match a segment prefix",
			scope+"Extra/Project",
		))
		if err != nil {
			t.Fatal(err)
		}

		// Equal timestamps exercise the UUID tie-breaker instead of relying on
		// incidental insert timing.
		fixedCreatedAt := time.Date(2025, 7, 28, 0, 0, 0, 0, time.UTC)
		for _, id := range ids {
			if _, err := database.Pool.Exec(ctx, `
				UPDATE memories
				   SET created_at = $2, updated_at = $2
				 WHERE id = $1
			`, id, fixedCreatedAt); err != nil {
				t.Fatal(err)
			}
		}
		sort.Slice(ids, func(i, j int) bool {
			return ids[i].String() > ids[j].String()
		})

		first, err := service.List(ctx, ListQuery{
			WorkspaceID:  workspaceA,
			Scope:        scope,
			AllowedPaths: []string{scope},
			Limit:        2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Memories) != 2 ||
			first.Memories[0].ID != ids[0] ||
			first.Memories[1].ID != ids[1] ||
			first.NextCursor == "" {
			t.Fatalf("first page=%+v sorted ids=%v", first, ids)
		}
		for _, record := range first.Memories {
			if record.ID == outside.Memory.ID {
				t.Fatalf("segment-prefix path leaked into first page: %+v", record)
			}
		}

		// A concurrent new row sorts ahead of the cursor and therefore cannot
		// duplicate or displace the older second page.
		late, err := service.Remember(ctx, command(
			"list-late-"+uuid.NewString(),
			"Created after page one",
			scope+"/Project",
		))
		if err != nil {
			t.Fatal(err)
		}
		second, err := service.List(ctx, ListQuery{
			WorkspaceID:  workspaceA,
			Scope:        scope,
			AllowedPaths: []string{scope},
			Limit:        2,
			Cursor:       first.NextCursor,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(second.Memories) != 1 ||
			second.Memories[0].ID != ids[2] ||
			second.NextCursor != "" {
			t.Fatalf("second page=%+v sorted ids=%v", second, ids)
		}
		if second.Memories[0].ID == late.Memory.ID {
			t.Fatalf("newer concurrent memory crossed the cursor: %+v", second)
		}

		otherWorkspace, err := service.List(ctx, ListQuery{
			WorkspaceID:  workspaceB,
			Scope:        scope,
			AllowedPaths: []string{scope},
			Limit:        20,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(otherWorkspace.Memories) != 0 {
			t.Fatalf("cross-workspace list leaked %+v", otherWorkspace.Memories)
		}
	})

	t.Run("Chinese exact FTS trigram and metadata filters", func(t *testing.T) {
		eventAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		chineseCommand := command(
			"chinese-"+uuid.NewString(),
			"项目决定使用 PostgreSQL 作为长期记忆数据库",
			"/Projects/mem",
		)
		chineseCommand.EventAt = &eventAt
		chinese, err := service.Remember(ctx, chineseCommand)
		if err != nil {
			t.Fatal(err)
		}
		hits, err := service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Text:        "长期记忆数据库",
			Kinds:       []string{KindDecision},
		})
		if err != nil {
			t.Fatal(err)
		}
		hit, ok := findMemory(hits, chinese.Memory.ID)
		if !ok {
			t.Fatalf("Chinese recall omitted %s: %+v", chinese.Memory.ID, hits)
		}
		if hit.Reason != "exact" || hit.Citation != chinese.Memory.Citation() {
			t.Fatalf("Chinese hit = %+v", hit)
		}
		if hit.Provenance.ProducerAgent != "codex" {
			t.Fatalf("provenance = %+v", hit.Provenance)
		}

		fts, err := service.Remember(ctx, command(
			"fts-"+uuid.NewString(),
			"alpha beta gamma architecture",
			"/Retrieval",
		))
		if err != nil {
			t.Fatal(err)
		}
		hits, err = service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Text:        "alpha gamma",
		})
		if err != nil {
			t.Fatal(err)
		}
		if hit, ok = findMemory(hits, fts.Memory.ID); !ok || hit.Reason != "fts" {
			t.Fatalf("FTS hit = %+v, found=%t; all=%+v", hit, ok, hits)
		}

		trigram, err := service.Remember(ctx, command(
			"trigram-"+uuid.NewString(),
			"deployment architecture uses an immutable journal",
			"/Retrieval",
		))
		if err != nil {
			t.Fatal(err)
		}
		hits, err = service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Text:        "architecutre",
			Limit:       20,
		})
		if err != nil {
			t.Fatal(err)
		}
		if hit, ok = findMemory(hits, trigram.Memory.ID); !ok || hit.Reason != "trigram" {
			t.Fatalf("trigram hit = %+v, found=%t; all=%+v", hit, ok, hits)
		}

		future := eventAt.Add(time.Hour)
		hits, err = service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Text:        "长期记忆数据库",
			Since:       &future,
		})
		if err != nil {
			t.Fatal(err)
		}
		if containsMemory(hits, chinese.Memory.ID) {
			t.Fatal("since filter returned an older event")
		}
		hits, err = service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Text:        "长期记忆数据库",
			Kinds:       []string{KindNote},
		})
		if err != nil {
			t.Fatal(err)
		}
		if containsMemory(hits, chinese.Memory.ID) {
			t.Fatal("kind filter returned a decision for note-only recall")
		}

		if _, err := database.Pool.Exec(ctx, `
			UPDATE memories
			   SET lifecycle_status = 'archived', updated_at = now()
			 WHERE id = $1
		`, chinese.Memory.ID); err != nil {
			t.Fatal(err)
		}
		hits, err = service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Text:        "长期记忆数据库",
		})
		if err != nil {
			t.Fatal(err)
		}
		if containsMemory(hits, chinese.Memory.ID) {
			t.Fatal("default active recall returned an archived memory")
		}
		_, err = service.Recall(ctx, RecallQuery{
			WorkspaceID:     workspaceA,
			Text:            "长期记忆数据库",
			LifecycleStatus: StatusArchived,
		})
		if !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("archived recall error = %v, want ErrInvalidCommand", err)
		}
	})

	t.Run("auditable lifecycle feedback and hard forget", func(t *testing.T) {
		actorID, tokenID := userA, uuid.New()
		remember := command(
			"lifecycle-"+uuid.NewString(),
			"Lifecycle evidence remains source verifiable",
			"/Lifecycle/Project",
		)
		remember.AllowedPaths = []string{"/Lifecycle"}
		created, err := service.Remember(ctx, remember)
		if err != nil {
			t.Fatal(err)
		}
		if created.Memory.StateVersion != 1 || created.Memory.Pinned {
			t.Fatalf("initial control state = %+v", created.Memory)
		}

		pinKey := "pin-plaintext-" + uuid.NewString()
		pin := FeedbackCommand{
			WorkspaceID:     workspaceA,
			MemoryID:        created.Memory.ID,
			AllowedPaths:    []string{"/Lifecycle"},
			ActorUserID:     &actorID,
			ActorTokenID:    &tokenID,
			Action:          FeedbackPin,
			IdempotencyKey:  pinKey,
			ExpectedVersion: 1,
		}
		pinned, err := service.Feedback(ctx, pin)
		if err != nil {
			t.Fatal(err)
		}
		if !pinned.Memory.Pinned || pinned.Memory.StateVersion != 2 ||
			pinned.Event.Action != FeedbackPin {
			t.Fatalf("pin result = %+v", pinned)
		}
		replayedPin, err := service.Feedback(ctx, pin)
		if err != nil {
			t.Fatal(err)
		}
		if !replayedPin.Replayed || replayedPin.Event.ID != pinned.Event.ID {
			t.Fatalf("pin replay = %+v", replayedPin)
		}
		var storedKeyHash string
		if err := database.Pool.QueryRow(ctx, `
			SELECT idempotency_key_sha256
			  FROM memory_events
			 WHERE id = $1
		`, pinned.Event.ID).Scan(&storedKeyHash); err != nil {
			t.Fatal(err)
		}
		expectedKeyHash := sha256.Sum256([]byte(pinKey))
		if storedKeyHash != hex.EncodeToString(expectedKeyHash[:]) ||
			strings.Contains(storedKeyHash, pinKey) {
			t.Fatalf("persisted idempotency value = %q", storedKeyHash)
		}
		if _, err := service.Feedback(ctx, FeedbackCommand{
			WorkspaceID:     workspaceA,
			MemoryID:        created.Memory.ID,
			AllowedPaths:    []string{"/Lifecycle"},
			Action:          FeedbackUseful,
			IdempotencyKey:  pinKey,
			ExpectedVersion: 2,
		}); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("reused mutation key error = %v", err)
		}

		hits, err := service.Recall(ctx, RecallQuery{
			WorkspaceID:  workspaceA,
			Text:         "Lifecycle evidence",
			AllowedPaths: []string{"/Lifecycle"},
		})
		if err != nil {
			t.Fatal(err)
		}
		hit, ok := findMemory(hits, created.Memory.ID)
		if !ok || hit.Reason != "exact+pinned" || hit.Score <= 0.95 {
			t.Fatalf("pinned recall hit = %+v, found=%t", hit, ok)
		}

		if _, err := service.Feedback(ctx, FeedbackCommand{
			WorkspaceID:     workspaceA,
			MemoryID:        created.Memory.ID,
			AllowedPaths:    []string{"/Other"},
			Action:          FeedbackUseful,
			IdempotencyKey:  "outside-" + uuid.NewString(),
			ExpectedVersion: 2,
		}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("out-of-path feedback error = %v", err)
		}
		if _, err := service.Archive(ctx, LifecycleCommand{
			WorkspaceID:     workspaceA,
			MemoryID:        created.Memory.ID,
			AllowedPaths:    []string{"/Lifecycle"},
			IdempotencyKey:  "stale-" + uuid.NewString(),
			ExpectedVersion: 1,
		}); !errors.Is(err, ErrVersionConflict) {
			t.Fatalf("stale archive error = %v", err)
		}

		useful, err := service.Feedback(ctx, FeedbackCommand{
			WorkspaceID:     workspaceA,
			MemoryID:        created.Memory.ID,
			AllowedPaths:    []string{"/Lifecycle"},
			ActorUserID:     &actorID,
			ActorTokenID:    &tokenID,
			Action:          FeedbackUseful,
			IdempotencyKey:  "useful-" + uuid.NewString(),
			ExpectedVersion: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if useful.Memory.UsefulCount != 1 || useful.Memory.FeedbackScore != 1 ||
			useful.Memory.StateVersion != 3 {
			t.Fatalf("useful result = %+v", useful.Memory)
		}
		archived, err := service.Archive(ctx, LifecycleCommand{
			WorkspaceID:     workspaceA,
			MemoryID:        created.Memory.ID,
			AllowedPaths:    []string{"/Lifecycle"},
			ActorUserID:     &actorID,
			ActorTokenID:    &tokenID,
			IdempotencyKey:  "archive-" + uuid.NewString(),
			ExpectedVersion: 3,
		})
		if err != nil {
			t.Fatal(err)
		}
		if archived.Memory.LifecycleStatus != StatusArchived ||
			archived.Memory.StateVersion != 4 {
			t.Fatalf("archive result = %+v", archived.Memory)
		}
		hits, err = service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Text:        "Lifecycle evidence",
		})
		if err != nil {
			t.Fatal(err)
		}
		if containsMemory(hits, created.Memory.ID) {
			t.Fatal("archived memory remained in recall")
		}
		activePage, err := service.List(ctx, ListQuery{
			WorkspaceID:  workspaceA,
			Scope:        "/Lifecycle",
			AllowedPaths: []string{"/Lifecycle"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if containsSummary(activePage.Memories, created.Memory.ID) {
			t.Fatal("default active list returned archived memory")
		}
		archivedPage, err := service.List(ctx, ListQuery{
			WorkspaceID:       workspaceA,
			Scope:             "/Lifecycle",
			AllowedPaths:      []string{"/Lifecycle"},
			LifecycleStatuses: []string{StatusArchived},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !containsSummary(archivedPage.Memories, created.Memory.ID) {
			t.Fatal("archived list omitted archived memory")
		}
		restored, err := service.Restore(ctx, LifecycleCommand{
			WorkspaceID:     workspaceA,
			MemoryID:        created.Memory.ID,
			AllowedPaths:    []string{"/Lifecycle"},
			ActorUserID:     &actorID,
			ActorTokenID:    &tokenID,
			IdempotencyKey:  "restore-" + uuid.NewString(),
			ExpectedVersion: 4,
		})
		if err != nil {
			t.Fatal(err)
		}
		if restored.Memory.LifecycleStatus != StatusActive ||
			restored.Memory.StateVersion != 5 {
			t.Fatalf("restore result = %+v", restored.Memory)
		}

		forget := ForgetCommand{
			LifecycleCommand: LifecycleCommand{
				WorkspaceID:     workspaceA,
				MemoryID:        created.Memory.ID,
				AllowedPaths:    []string{"/Lifecycle"},
				ActorUserID:     &actorID,
				ActorTokenID:    &tokenID,
				IdempotencyKey:  "forget-" + uuid.NewString(),
				ExpectedVersion: 5,
			},
			Reason: ForgetReasonSensitive,
		}
		forgotten, err := service.Forget(ctx, forget)
		if err != nil {
			t.Fatal(err)
		}
		if forgotten.Tombstone.LifecycleStatus != StatusForgotten ||
			forgotten.Tombstone.StateVersion != 6 ||
			forgotten.Event.Reason != ForgetReasonSensitive {
			t.Fatalf("forget result = %+v", forgotten)
		}
		replayedForget, err := service.Forget(ctx, forget)
		if err != nil {
			t.Fatal(err)
		}
		if !replayedForget.Replayed ||
			replayedForget.Event.ID != forgotten.Event.ID {
			t.Fatalf("forget replay = %+v", replayedForget)
		}
		rotatedToken := uuid.New()
		rotatedTokenRetry := forget
		rotatedTokenRetry.ActorTokenID = &rotatedToken
		rotatedReplay, err := service.Forget(ctx, rotatedTokenRetry)
		if err != nil {
			t.Fatalf("rotated-token forget replay: %v", err)
		}
		if !rotatedReplay.Replayed ||
			rotatedReplay.Event.ID != forgotten.Event.ID {
			t.Fatalf("rotated-token forget replay = %+v", rotatedReplay)
		}
		otherUser := userB
		otherPrincipal := forget
		otherPrincipal.ActorUserID = &otherUser
		if _, err := service.Forget(ctx, otherPrincipal); !errors.Is(err, ErrNotFound) {
			t.Fatalf("different-user forget replay error = %v", err)
		}
		if _, err := service.Get(ctx, Query{
			WorkspaceID:  workspaceA,
			MemoryID:     created.Memory.ID,
			AllowedPaths: []string{"/Lifecycle"},
		}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("restricted Get forgotten error = %v", err)
		}
		if _, err := service.Get(ctx, Query{
			WorkspaceID: workspaceA,
			MemoryID:    created.Memory.ID,
		}); !errors.Is(err, ErrForgotten) {
			t.Fatalf("unrestricted Get forgotten error = %v", err)
		}
		if _, err := service.Remember(ctx, remember); !errors.Is(err, ErrNotFound) {
			t.Fatalf("restricted remember retry after forget error = %v", err)
		}
		unrestrictedRemember := remember
		unrestrictedRemember.AllowedPaths = nil
		if _, err := service.Remember(ctx, unrestrictedRemember); !errors.Is(err, ErrForgotten) {
			t.Fatalf("unrestricted remember retry after forget error = %v", err)
		}
		hits, err = service.Recall(ctx, RecallQuery{
			WorkspaceID: workspaceA,
			Text:        "Lifecycle evidence",
		})
		if err != nil {
			t.Fatal(err)
		}
		if containsMemory(hits, created.Memory.ID) {
			t.Fatal("forgotten memory remained in recall")
		}

		var (
			path, kind, content, sourceType, sourceRef, producer string
			requestHash, contentHash, rememberKeyHash            string
			attributes, locator                                  []byte
			sourceFileID                                         *uuid.UUID
			createdByUserID, createdByTokenID                    *uuid.UUID
			forgottenByUserID, forgottenByTokenID                *uuid.UUID
			pinnedAt                                             *time.Time
			createdAt, updatedAt, forgottenAt                    time.Time
			usefulCount, notUsefulCount                          int
		)
		if err := database.Pool.QueryRow(ctx, `
			SELECT path, kind, content, attributes, source_type, source_ref,
			       source_file_id, source_locator, producer_agent,
			       request_sha256, content_sha256, idempotency_key_sha256,
			       created_by_user_id, created_by_token_id,
			       forgotten_by_user_id, forgotten_by_token_id,
			       pinned_at, useful_count, not_useful_count,
			       created_at, updated_at, forgotten_at
			  FROM memories
			 WHERE id = $1
		`, created.Memory.ID).Scan(
			&path,
			&kind,
			&content,
			&attributes,
			&sourceType,
			&sourceRef,
			&sourceFileID,
			&locator,
			&producer,
			&requestHash,
			&contentHash,
			&rememberKeyHash,
			&createdByUserID,
			&createdByTokenID,
			&forgottenByUserID,
			&forgottenByTokenID,
			&pinnedAt,
			&usefulCount,
			&notUsefulCount,
			&createdAt,
			&updatedAt,
			&forgottenAt,
		); err != nil {
			t.Fatal(err)
		}
		expectedRememberKeyHash := sha256.Sum256([]byte(remember.IdempotencyKey))
		if path != "/" || kind != KindForgotten ||
			content != "" || string(attributes) != "{}" ||
			sourceType != StatusForgotten || sourceRef != "" ||
			sourceFileID != nil || string(locator) != "{}" ||
			producer != "" || requestHash != strings.Repeat("0", 64) ||
			contentHash != strings.Repeat("0", 64) ||
			rememberKeyHash != hex.EncodeToString(expectedRememberKeyHash[:]) ||
			createdByUserID != nil || createdByTokenID != nil ||
			forgottenByUserID != nil || forgottenByTokenID != nil ||
			!createdAt.Equal(forgottenAt) || !updatedAt.Equal(forgottenAt) ||
			pinnedAt != nil || usefulCount != 0 || notUsefulCount != 0 {
			t.Fatalf(
				"forgotten payload not redacted: path=%q kind=%q content=%q attributes=%s source=%q/%q file=%v locator=%s producer=%q request=%q content_hash=%q key_hash=%q actors=%v/%v forgotten_by=%v/%v times=%v/%v/%v pinned=%v feedback=%d/%d",
				path,
				kind,
				content,
				attributes,
				sourceType,
				sourceRef,
				sourceFileID,
				locator,
				producer,
				requestHash,
				contentHash,
				rememberKeyHash,
				createdByUserID,
				createdByTokenID,
				forgottenByUserID,
				forgottenByTokenID,
				createdAt,
				updatedAt,
				forgottenAt,
				pinnedAt,
				usefulCount,
				notUsefulCount,
			)
		}
		var (
			rawRememberKeyColumnCount int
			rawEventActorCount        int
			forgetReceipt             string
			forgetActorUserID         *uuid.UUID
			forgetActorTokenID        *uuid.UUID
			historicalKeyHash         string
			historicalRequestHash     string
		)
		if err := database.Pool.QueryRow(ctx, `
			SELECT count(*)
			  FROM information_schema.columns
			 WHERE table_schema = current_schema()
			   AND table_name = 'memories'
			   AND column_name = 'idempotency_key'
		`).Scan(&rawRememberKeyColumnCount); err != nil {
			t.Fatal(err)
		}
		if rawRememberKeyColumnCount != 0 {
			t.Fatal("memories still persists raw idempotency_key")
		}
		if err := database.Pool.QueryRow(ctx, `
			SELECT count(*)
			  FROM memory_events
			 WHERE workspace_id = $1
			   AND memory_id = $2
			   AND (actor_user_id IS NOT NULL OR actor_token_id IS NOT NULL)
		`, workspaceA, created.Memory.ID).Scan(&rawEventActorCount); err != nil {
			t.Fatal(err)
		}
		if rawEventActorCount != 0 {
			t.Fatalf("forgotten event history retained %d raw actor rows", rawEventActorCount)
		}
		if err := database.Pool.QueryRow(ctx, `
			SELECT idempotency_key_sha256, request_sha256
			  FROM memory_events
			 WHERE id = $1
		`, pinned.Event.ID).Scan(
			&historicalKeyHash,
			&historicalRequestHash,
		); err != nil {
			t.Fatal(err)
		}
		expectedHistoricalKeyHash := sha256.Sum256([]byte(
			"mem/redacted-event/v1|" + pinned.Event.ID.String(),
		))
		if historicalKeyHash != hex.EncodeToString(expectedHistoricalKeyHash[:]) ||
			historicalRequestHash != strings.Repeat("0", 64) {
			t.Fatalf(
				"historical event fingerprints not redacted: key=%q request=%q",
				historicalKeyHash,
				historicalRequestHash,
			)
		}
		if err := database.Pool.QueryRow(ctx, `
			SELECT replay_principal_sha256, actor_user_id, actor_token_id
			  FROM memory_events
			 WHERE id = $1
		`, forgotten.Event.ID).Scan(
			&forgetReceipt,
			&forgetActorUserID,
			&forgetActorTokenID,
		); err != nil {
			t.Fatal(err)
		}
		if len(forgetReceipt) != 64 ||
			forgetActorUserID != nil || forgetActorTokenID != nil {
			t.Fatalf(
				"forget receipt/actors = %q/%v/%v",
				forgetReceipt,
				forgetActorUserID,
				forgetActorTokenID,
			)
		}
	})

	t.Run("list summary bounds Unicode content", func(t *testing.T) {
		scope := "/Summary-" + uuid.NewString()
		content := strings.Repeat("界", 600)
		created, err := service.Remember(ctx, command(
			"summary-"+uuid.NewString(),
			content,
			scope,
		))
		if err != nil {
			t.Fatal(err)
		}
		page, err := service.List(ctx, ListQuery{
			WorkspaceID:  workspaceA,
			Scope:        scope,
			AllowedPaths: []string{scope},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Memories) != 1 || page.Memories[0].ID != created.Memory.ID {
			t.Fatalf("summary page = %+v", page)
		}
		if got := utf8.RuneCountInString(page.Memories[0].Excerpt); got != 500 {
			t.Fatalf("excerpt runes = %d, want 500", got)
		}
		if page.Memories[0].ContentLength != 600 {
			t.Fatalf("content length = %d, want 600", page.Memories[0].ContentLength)
		}
	})

	t.Run("forget linearizes remember retries and prevents revival", func(t *testing.T) {
		original := command(
			"forget-race-"+uuid.NewString(),
			"Payload must never revive after forget commits",
			"/Concurrency/Forget",
		)
		created, err := service.Remember(ctx, original)
		if err != nil {
			t.Fatal(err)
		}
		actorID, tokenID := userA, uuid.New()
		forget := ForgetCommand{
			LifecycleCommand: LifecycleCommand{
				WorkspaceID:     workspaceA,
				MemoryID:        created.Memory.ID,
				ActorUserID:     &actorID,
				ActorTokenID:    &tokenID,
				IdempotencyKey:  "forget-race-action-" + uuid.NewString(),
				ExpectedVersion: 1,
			},
			Reason: ForgetReasonUserRequest,
		}

		const retries = 12
		start := make(chan struct{})
		retryErrors := make(chan error, retries)
		for range retries {
			go func() {
				<-start
				_, err := service.Remember(ctx, original)
				retryErrors <- err
			}()
		}
		forgetResult := make(chan error, 1)
		go func() {
			<-start
			_, err := service.Forget(ctx, forget)
			forgetResult <- err
		}()
		close(start)

		for range retries {
			err := <-retryErrors
			if err != nil && !errors.Is(err, ErrForgotten) {
				t.Fatalf("concurrent remember retry error = %v", err)
			}
		}
		if err := <-forgetResult; err != nil {
			t.Fatalf("concurrent Forget: %v", err)
		}

		// Once Forget has returned, every retry must observe the terminal
		// tombstone rather than an MVCC version containing the old payload.
		postForgetErrors := make(chan error, retries)
		var postForgetStart sync.WaitGroup
		postForgetStart.Add(1)
		for range retries {
			go func() {
				postForgetStart.Wait()
				_, err := service.Remember(ctx, original)
				postForgetErrors <- err
			}()
		}
		postForgetStart.Done()
		for range retries {
			if err := <-postForgetErrors; !errors.Is(err, ErrForgotten) {
				t.Fatalf("post-forget remember retry error = %v", err)
			}
		}

		var status, content string
		if err := database.Pool.QueryRow(ctx, `
			SELECT lifecycle_status, content
			  FROM memories
			 WHERE id = $1
		`, created.Memory.ID).Scan(&status, &content); err != nil {
			t.Fatal(err)
		}
		if status != StatusForgotten || content != "" {
			t.Fatalf("forget race final row = status %q content %q", status, content)
		}
	})

	t.Run("concurrent feedback has one optimistic winner", func(t *testing.T) {
		created, err := service.Remember(ctx, command(
			"feedback-race-"+uuid.NewString(),
			"Concurrent feedback is linearized",
			"/Concurrency/Feedback",
		))
		if err != nil {
			t.Fatal(err)
		}
		commands := []FeedbackCommand{
			{
				WorkspaceID:     workspaceA,
				MemoryID:        created.Memory.ID,
				Action:          FeedbackUseful,
				IdempotencyKey:  "feedback-a-" + uuid.NewString(),
				ExpectedVersion: 1,
			},
			{
				WorkspaceID:     workspaceA,
				MemoryID:        created.Memory.ID,
				Action:          FeedbackNotUseful,
				IdempotencyKey:  "feedback-b-" + uuid.NewString(),
				ExpectedVersion: 1,
			},
		}
		type outcome struct {
			result *MutationResult
			err    error
		}
		start := make(chan struct{})
		outcomes := make(chan outcome, len(commands))
		for _, cmd := range commands {
			cmd := cmd
			go func() {
				<-start
				result, err := service.Feedback(ctx, cmd)
				outcomes <- outcome{result: result, err: err}
			}()
		}
		close(start)
		succeeded, conflicted := 0, 0
		for range commands {
			outcome := <-outcomes
			switch {
			case outcome.err == nil:
				succeeded++
				if outcome.result.Memory.StateVersion != 2 {
					t.Fatalf("winner state = %+v", outcome.result.Memory)
				}
			case errors.Is(outcome.err, ErrVersionConflict):
				conflicted++
			default:
				t.Fatalf("unexpected feedback race error: %v", outcome.err)
			}
		}
		if succeeded != 1 || conflicted != 1 {
			t.Fatalf("feedback race success/conflict = %d/%d", succeeded, conflicted)
		}
	})

	t.Run("concurrent replay has one winner", func(t *testing.T) {
		concurrent := command(
			"concurrent-"+uuid.NewString(),
			"Only one immutable concurrent occurrence",
			"/Concurrency",
		)
		const writers = 12
		type outcome struct {
			result *RememberResult
			err    error
		}
		outcomes := make(chan outcome, writers)
		var start sync.WaitGroup
		start.Add(1)
		for range writers {
			go func() {
				start.Wait()
				result, err := service.Remember(ctx, concurrent)
				outcomes <- outcome{result: result, err: err}
			}()
		}
		start.Done()

		ids := make(map[uuid.UUID]struct{})
		created := 0
		for range writers {
			outcome := <-outcomes
			if outcome.err != nil {
				t.Fatalf("concurrent Remember: %v", outcome.err)
			}
			ids[outcome.result.Memory.ID] = struct{}{}
			if !outcome.result.Replayed {
				created++
			}
		}
		if len(ids) != 1 || created != 1 {
			t.Fatalf("unique ids=%d newly-created=%d, want 1 and 1", len(ids), created)
		}
	})
}

var testRunSuffix = uuid.NewString()

func createTestTenant(
	t *testing.T,
	ctx context.Context,
	database *memdb.DB,
	prefix string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var userID uuid.UUID
	email := prefix + "-" + testRunSuffix + "@example.com"
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash)
		VALUES ($1, 'integration-test')
		RETURNING id
	`, email).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	var workspaceID uuid.UUID
	if err := database.Pool.QueryRow(ctx, `
		INSERT INTO workspaces (name, resource_owner_user_id)
		VALUES ($1, $2)
		RETURNING id
	`, prefix, userID).Scan(&workspaceID); err != nil {
		t.Fatalf("create test workspace: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO workspace_memberships (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, userID); err != nil {
		t.Fatalf("create test workspace membership: %v", err)
	}
	return userID, workspaceID
}

func containsMemory(hits []RecallHit, id uuid.UUID) bool {
	_, ok := findMemory(hits, id)
	return ok
}

func findMemory(hits []RecallHit, id uuid.UUID) (RecallHit, bool) {
	for _, hit := range hits {
		if hit.Memory.ID == id {
			return hit, true
		}
	}
	return RecallHit{}, false
}

func containsSummary(summaries []MemorySummary, id uuid.UUID) bool {
	for _, summary := range summaries {
		if summary.ID == id {
			return true
		}
	}
	return false
}

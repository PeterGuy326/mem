package file

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	memdb "github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/folder"
	"github.com/PeterGuy326/mem/server/internal/testutil/pglockwait"
)

// TestFilePathLockingIntegration proves file path/folder references remain
// aligned when Put or Move overlaps a folder prefix rewrite.
func TestFilePathLockingIntegration(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping DB integration test")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse MEM_TEST_DB: %v", err)
	}
	if !strings.HasSuffix(config.ConnConfig.Database, "_test") {
		t.Fatalf(
			"refusing to modify non-test database %q; MEM_TEST_DB must end in _test",
			config.ConnConfig.Database,
		)
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

	t.Run("Put then Rename has one path", func(t *testing.T) {
		userID, _ := createFileLockTenant(t, ctx, database.Pool, "put-rename")
		folders := folder.New(database.Pool)
		if _, err := folders.Create(ctx, userID, "/A"); err != nil {
			t.Fatalf("create destination: %v", err)
		}
		putBackend := pglockwait.NewBackend(t, ctx, dsn, "file-put")
		renameBackend := pglockwait.NewBackend(t, ctx, dsn, "file-put-rename")
		store := newBlockingObjectStore()
		service := New(putBackend.Pool, store, folder.New(putBackend.Pool))
		renameFolders := folder.New(renameBackend.Pool)

		type putOutcome struct {
			result *PutResult
			err    error
		}
		putDone := make(chan putOutcome, 1)
		go func() {
			result, err := service.Put(
				ctx,
				userID,
				"race.txt",
				"text/plain",
				"/A",
				4,
				nil,
				SourceMetadata{},
				bytes.NewBufferString("race"),
			)
			putDone <- putOutcome{result: result, err: err}
		}()
		select {
		case <-store.putStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("Put did not reach object storage")
		}

		fileGate, fileGatePID := lockFilesTable(t, ctx, database.Pool)
		close(store.releasePut)
		pglockwait.WaitBlocked(
			t,
			ctx,
			database.Pool,
			putBackend,
			fileGatePID,
			"INSERT INTO files",
		)

		renameDone := make(chan error, 1)
		go func() {
			renameDone <- renameFolders.Rename(ctx, userID, "/A", "B")
		}()
		pglockwait.WaitBlocked(
			t,
			ctx,
			database.Pool,
			renameBackend,
			putBackend.PID,
			"FROM workspaces",
			"FOR UPDATE",
		)
		if err := fileGate.Commit(ctx); err != nil {
			t.Fatalf("release files table: %v", err)
		}

		var outcome putOutcome
		select {
		case outcome = <-putDone:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for Put")
		}
		if outcome.err != nil {
			t.Fatalf("Put: %v", outcome.err)
		}
		if err := awaitFileOperation(t, renameDone); err != nil {
			t.Fatalf("Rename: %v", err)
		}
		assertStoredFilePathMatchesFolder(t, ctx, database.Pool, outcome.result.File.ID, "/B")
	})

	t.Run("Move then Rename has one path", func(t *testing.T) {
		userID, _ := createFileLockTenant(t, ctx, database.Pool, "move-rename")
		folders := folder.New(database.Pool)
		source, err := folders.Create(ctx, userID, "/Source")
		if err != nil {
			t.Fatalf("create source: %v", err)
		}
		if _, err := folders.Create(ctx, userID, "/A"); err != nil {
			t.Fatalf("create destination: %v", err)
		}
		fileID := uuid.New()
		if _, err := database.Pool.Exec(ctx, `
			INSERT INTO files (
				id, user_id, name, path, folder_id, size, sha256, mime,
				storage_key, tags, index_status
			)
			VALUES ($1, $2, 'move.txt', '/Source', $3, 1, $4,
			        'text/plain', $5, '{}', 'pending')
		`,
			fileID,
			userID,
			source.ID,
			strings.Repeat("b", 64),
			"workspace-lock/"+fileID.String(),
		); err != nil {
			t.Fatalf("insert source file: %v", err)
		}

		moveBackend := pglockwait.NewBackend(t, ctx, dsn, "file-move")
		renameBackend := pglockwait.NewBackend(t, ctx, dsn, "file-move-rename")
		service := New(
			moveBackend.Pool,
			&recordingObjectStore{},
			folder.New(moveBackend.Pool),
		)
		renameFolders := folder.New(renameBackend.Pool)
		fileGate, fileGatePID := lockFilesTable(t, ctx, database.Pool)
		moveDone := make(chan error, 1)
		go func() {
			_, err := service.Move(ctx, userID, fileID, "/A")
			moveDone <- err
		}()
		pglockwait.WaitBlocked(
			t,
			ctx,
			database.Pool,
			moveBackend,
			fileGatePID,
			"UPDATE files SET path",
		)

		renameDone := make(chan error, 1)
		go func() {
			renameDone <- renameFolders.Rename(ctx, userID, "/A", "B")
		}()
		pglockwait.WaitBlocked(
			t,
			ctx,
			database.Pool,
			renameBackend,
			moveBackend.PID,
			"FROM workspaces",
			"FOR UPDATE",
		)
		if err := fileGate.Commit(ctx); err != nil {
			t.Fatalf("release files table: %v", err)
		}
		if err := awaitFileOperation(t, moveDone); err != nil {
			t.Fatalf("Move: %v", err)
		}
		if err := awaitFileOperation(t, renameDone); err != nil {
			t.Fatalf("Rename: %v", err)
		}
		assertStoredFilePathMatchesFolder(t, ctx, database.Pool, fileID, "/B")
	})
}

type blockingObjectStore struct {
	putStarted chan struct{}
	releasePut chan struct{}
	startOnce  sync.Once
}

func newBlockingObjectStore() *blockingObjectStore {
	return &blockingObjectStore{
		putStarted: make(chan struct{}),
		releasePut: make(chan struct{}),
	}
}

func (store *blockingObjectStore) Put(
	ctx context.Context,
	_ string,
	_ io.Reader,
	_ int64,
	_ string,
) error {
	store.startOnce.Do(func() { close(store.putStarted) })
	select {
	case <-store.releasePut:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*blockingObjectStore) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (*blockingObjectStore) Delete(context.Context, string) error {
	return nil
}

func createFileLockTenant(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	prefix string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash)
		VALUES ($1, 'file-lock-test')
		RETURNING id
	`, prefix+"-"+uuid.NewString()+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create file-lock user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})
	var workspaceID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspaces (name, resource_owner_user_id)
		VALUES ($1, $2)
		RETURNING id
	`, prefix, userID).Scan(&workspaceID); err != nil {
		t.Fatalf("create file-lock workspace: %v", err)
	}
	return userID, workspaceID
}

func lockFilesTable(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) (pgx.Tx, int32) {
	t.Helper()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin files gate: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, `LOCK TABLE files IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock files table: %v", err)
	}
	return tx, pglockwait.BackendPID(t, ctx, tx)
}

func assertStoredFilePathMatchesFolder(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fileID uuid.UUID,
	want string,
) {
	t.Helper()
	var filePath, folderPath string
	if err := pool.QueryRow(ctx, `
		SELECT f.path, d.path
		  FROM files f
		  JOIN folders d ON d.id = f.folder_id
		 WHERE f.id = $1
	`, fileID).Scan(&filePath, &folderPath); err != nil {
		t.Fatalf("load file path: %v", err)
	}
	if filePath != want || folderPath != want {
		t.Fatalf("file.path=%q folder.path=%q, want %q", filePath, folderPath, want)
	}
}

func awaitFileOperation(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for file operation")
		return nil
	}
}

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/auth"
	memdb "github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/file"
	"github.com/PeterGuy326/mem/server/internal/folder"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

func TestRelocateHTTPPostgres(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping relocation HTTP integration test")
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

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	database, err := memdb.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	authService := auth.New(database.Pool)
	workspaceService := workspace.New(database.Pool)
	user, err := authService.CreateUser(
		ctx,
		"relocate-http-"+uuid.NewString()+"@example.test",
		"integration-password",
	)
	if err != nil {
		t.Fatalf("create relocation user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := database.Pool.Exec(
			cleanupCtx,
			`DELETE FROM users WHERE id = $1`,
			user.ID,
		); err != nil {
			t.Errorf("clean up relocation user: %v", err)
		}
	})
	currentWorkspace, err := workspaceService.Resolve(ctx, user.ID, nil)
	if err != nil {
		t.Fatalf("resolve relocation workspace: %v", err)
	}
	token, _, err := authService.CreateToken(
		ctx,
		user.ID,
		&currentWorkspace.ID,
		"relocation-writer",
		[]string{auth.ScopeWrite},
		nil,
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("create relocation token: %v", err)
	}
	restrictedToken, _, err := authService.CreateToken(
		ctx,
		user.ID,
		&currentWorkspace.ID,
		"restricted-relocation-writer",
		[]string{auth.ScopeWrite},
		[]string{"/Allowed"},
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("create restricted relocation token: %v", err)
	}

	folderService := folder.New(database.Pool)
	fileService := file.New(database.Pool, nil, folderService)
	server := httptest.NewServer((&Server{
		Auth:      authService,
		File:      fileService,
		Folder:    folderService,
		Workspace: workspaceService,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Router())
	t.Cleanup(server.Close)

	patchAs := func(t *testing.T, bearer, path, body string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPatch,
			server.URL+path,
			bytes.NewBufferString(body),
		)
		if err != nil {
			t.Fatalf("create PATCH request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("PATCH %s: %v", path, err)
		}
		defer response.Body.Close()
		responseBody, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read PATCH %s response: %v", path, err)
		}
		return response.StatusCode, responseBody
	}
	patch := func(t *testing.T, path, body string) (int, []byte) {
		t.Helper()
		return patchAs(t, token, path, body)
	}

	t.Run("file invalid name rolls back move", func(t *testing.T) {
		source, err := folderService.Create(ctx, user.ID, "/FileSource")
		if err != nil {
			t.Fatalf("create file source: %v", err)
		}
		if _, err := folderService.Create(ctx, user.ID, "/FileDestination"); err != nil {
			t.Fatalf("create file destination: %v", err)
		}
		fileID := insertRelocationHTTPFile(
			t,
			ctx,
			database.Pool,
			user.ID,
			source.ID,
			"original.txt",
			"/FileSource",
			"1",
		)

		status, body := patch(
			t,
			"/v1/files/"+fileID.String(),
			`{"path":"/FileDestination","name":"invalid/name"}`,
		)
		if status != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s, want 400", status, body)
		}
		stored, err := fileService.Get(ctx, user.ID, fileID)
		if err != nil {
			t.Fatalf("load file after rejected relocation: %v", err)
		}
		if stored.Path != "/FileSource" || stored.Name != "original.txt" {
			t.Fatalf(
				"rejected relocation committed partial state: path=%q name=%q",
				stored.Path,
				stored.Name,
			)
		}
	})

	t.Run("folder conflict rolls back move", func(t *testing.T) {
		source, err := folderService.Create(ctx, user.ID, "/FolderSource/original")
		if err != nil {
			t.Fatalf("create folder source: %v", err)
		}
		if _, err := folderService.Create(ctx, user.ID, "/FolderDestination/taken"); err != nil {
			t.Fatalf("create folder conflict: %v", err)
		}

		status, body := patch(
			t,
			"/v1/folders/"+source.ID.String(),
			`{"parent_path":"/FolderDestination","name":"taken"}`,
		)
		if status != http.StatusConflict {
			t.Fatalf("status=%d body=%s, want 409", status, body)
		}
		if _, err := folderService.Get(ctx, user.ID, "/FolderSource/original"); err != nil {
			t.Fatalf("source changed after rejected relocation: %v", err)
		}
		if _, err := folderService.Get(ctx, user.ID, "/FolderDestination/original"); !errors.Is(err, folder.ErrNotFound) {
			t.Fatalf("partial destination after rejected relocation: err=%v", err)
		}
	})

	t.Run("file database error rolls back destination creation", func(t *testing.T) {
		source, err := folderService.Create(ctx, user.ID, "/FileDatabaseErrorSource")
		if err != nil {
			t.Fatalf("create database-error file source: %v", err)
		}
		fileID := insertRelocationHTTPFile(
			t,
			ctx,
			database.Pool,
			user.ID,
			source.ID,
			"before-error.txt",
			"/FileDatabaseErrorSource",
			"3",
		)
		installRelocationHTTPFailureTrigger(t, ctx, database.Pool)

		status, body := patch(
			t,
			"/v1/files/"+fileID.String(),
			`{"path":"/CreatedInsideFailedTransaction/Nested","name":"force-database-error.txt"}`,
		)
		if status != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s, want 400", status, body)
		}
		stored, err := fileService.Get(ctx, user.ID, fileID)
		if err != nil {
			t.Fatalf("load file after database error: %v", err)
		}
		if stored.Path != "/FileDatabaseErrorSource" || stored.Name != "before-error.txt" {
			t.Fatalf(
				"database error committed file state: path=%q name=%q",
				stored.Path,
				stored.Name,
			)
		}
		if _, err := folderService.Get(ctx, user.ID, "/CreatedInsideFailedTransaction"); !errors.Is(err, folder.ErrNotFound) {
			t.Fatalf("database error committed destination folder: err=%v", err)
		}
	})

	t.Run("folder ignores intermediate-name conflict", func(t *testing.T) {
		source, err := folderService.Create(ctx, user.ID, "/IntermediateSource/original")
		if err != nil {
			t.Fatalf("create intermediate-conflict source: %v", err)
		}
		if _, err := folderService.Create(ctx, user.ID, "/IntermediateDestination/original"); err != nil {
			t.Fatalf("create intermediate-name conflict: %v", err)
		}

		status, body := patch(
			t,
			"/v1/folders/"+source.ID.String(),
			`{"parent_path":"/IntermediateDestination","name":"final"}`,
		)
		if status != http.StatusOK {
			t.Fatalf("status=%d body=%s, want 200", status, body)
		}
		if _, err := folderService.Get(ctx, user.ID, "/IntermediateDestination/final"); err != nil {
			t.Fatalf("final destination missing: %v", err)
		}
		if _, err := folderService.Get(ctx, user.ID, "/IntermediateDestination/original"); err != nil {
			t.Fatalf("pre-existing intermediate-name folder changed: %v", err)
		}
	})

	t.Run("restricted token cannot cross a sibling segment", func(t *testing.T) {
		allowed, err := folderService.Create(ctx, user.ID, "/Allowed")
		if err != nil {
			t.Fatalf("create allowed source: %v", err)
		}
		folderSource, err := folderService.Create(ctx, user.ID, "/Allowed/folder-source")
		if err != nil {
			t.Fatalf("create allowed folder source: %v", err)
		}
		if _, err := folderService.Create(ctx, user.ID, "/AllowedSibling"); err != nil {
			t.Fatalf("create denied sibling destination: %v", err)
		}
		fileID := insertRelocationHTTPFile(
			t,
			ctx,
			database.Pool,
			user.ID,
			allowed.ID,
			"scoped.txt",
			"/Allowed",
			"4",
		)

		status, body := patchAs(
			t,
			restrictedToken,
			"/v1/files/"+fileID.String(),
			`{"path":"/AllowedSibling","name":"denied.txt"}`,
		)
		if status != http.StatusForbidden {
			t.Fatalf("file status=%d body=%s, want 403", status, body)
		}
		stored, err := fileService.Get(ctx, user.ID, fileID)
		if err != nil {
			t.Fatalf("load denied file relocation: %v", err)
		}
		if stored.Path != "/Allowed" || stored.Name != "scoped.txt" {
			t.Fatalf("denied file relocation stored path=%q name=%q", stored.Path, stored.Name)
		}

		status, body = patchAs(
			t,
			restrictedToken,
			"/v1/folders/"+folderSource.ID.String(),
			`{"parent_path":"/AllowedSibling","name":"denied-folder"}`,
		)
		if status != http.StatusForbidden {
			t.Fatalf("folder status=%d body=%s, want 403", status, body)
		}
		if _, err := folderService.Get(ctx, user.ID, "/Allowed/folder-source"); err != nil {
			t.Fatalf("denied folder relocation changed source: %v", err)
		}
	})

	t.Run("single-field patches remain compatible", func(t *testing.T) {
		fileSource, err := folderService.Create(ctx, user.ID, "/SingleFileSource")
		if err != nil {
			t.Fatalf("create single-field file source: %v", err)
		}
		if _, err := folderService.Create(ctx, user.ID, "/SingleFileDestination"); err != nil {
			t.Fatalf("create single-field file destination: %v", err)
		}
		fileID := insertRelocationHTTPFile(
			t,
			ctx,
			database.Pool,
			user.ID,
			fileSource.ID,
			"before.txt",
			"/SingleFileSource",
			"5",
		)
		if status, body := patch(
			t,
			"/v1/files/"+fileID.String(),
			`{"path":"/SingleFileDestination"}`,
		); status != http.StatusOK {
			t.Fatalf("file path-only status=%d body=%s", status, body)
		}
		if status, body := patch(
			t,
			"/v1/files/"+fileID.String(),
			`{"name":"after.txt"}`,
		); status != http.StatusOK {
			t.Fatalf("file name-only status=%d body=%s", status, body)
		}
		stored, err := fileService.Get(ctx, user.ID, fileID)
		if err != nil {
			t.Fatalf("load single-field patched file: %v", err)
		}
		if stored.Path != "/SingleFileDestination" || stored.Name != "after.txt" {
			t.Fatalf("single-field file path=%q name=%q", stored.Path, stored.Name)
		}

		folderSource, err := folderService.Create(ctx, user.ID, "/SingleFolderSource/before")
		if err != nil {
			t.Fatalf("create single-field folder source: %v", err)
		}
		if _, err := folderService.Create(ctx, user.ID, "/SingleFolderDestination"); err != nil {
			t.Fatalf("create single-field folder destination: %v", err)
		}
		if status, body := patch(
			t,
			"/v1/folders/"+folderSource.ID.String(),
			`{"parent_path":"/SingleFolderDestination"}`,
		); status != http.StatusOK {
			t.Fatalf("folder path-only status=%d body=%s", status, body)
		}
		if status, body := patch(
			t,
			"/v1/folders/"+folderSource.ID.String(),
			`{"name":"after"}`,
		); status != http.StatusOK {
			t.Fatalf("folder name-only status=%d body=%s", status, body)
		}
		if _, err := folderService.Get(ctx, user.ID, "/SingleFolderDestination/after"); err != nil {
			t.Fatalf("single-field patched folder missing: %v", err)
		}
	})

	t.Run("file combined relocation succeeds", func(t *testing.T) {
		source, err := folderService.Create(ctx, user.ID, "/FileSuccessSource")
		if err != nil {
			t.Fatalf("create successful file source: %v", err)
		}
		if _, err := folderService.Create(ctx, user.ID, "/FileSuccessDestination"); err != nil {
			t.Fatalf("create successful file destination: %v", err)
		}
		fileID := insertRelocationHTTPFile(
			t,
			ctx,
			database.Pool,
			user.ID,
			source.ID,
			"before.txt",
			"/FileSuccessSource",
			"2",
		)

		status, body := patch(
			t,
			"/v1/files/"+fileID.String(),
			`{"path":"/FileSuccessDestination","name":"after.txt"}`,
		)
		if status != http.StatusOK {
			t.Fatalf("status=%d body=%s, want 200", status, body)
		}
		var response file.File
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatalf("decode file relocation response: %v; body=%s", err, body)
		}
		if response.Path != "/FileSuccessDestination" || response.Name != "after.txt" {
			t.Fatalf("file relocation response path=%q name=%q", response.Path, response.Name)
		}
		stored, err := fileService.Get(ctx, user.ID, fileID)
		if err != nil {
			t.Fatalf("load relocated file: %v", err)
		}
		destination, err := folderService.Get(ctx, user.ID, "/FileSuccessDestination")
		if err != nil {
			t.Fatalf("load relocated file destination: %v", err)
		}
		if stored.FolderID == nil || *stored.FolderID != destination.ID || stored.Path != destination.Path {
			t.Fatalf(
				"relocated file/folder mismatch: file.path=%q file.folder_id=%v destination=%+v",
				stored.Path,
				stored.FolderID,
				destination,
			)
		}
	})

	t.Run("folder combined relocation preserves literal segments", func(t *testing.T) {
		source, err := folderService.Create(ctx, user.ID, "/Source%_/before_/child")
		if err != nil {
			t.Fatalf("create successful folder source: %v", err)
		}
		root, err := folderService.Get(ctx, user.ID, "/Source%_/before_")
		if err != nil {
			t.Fatalf("load successful folder source: %v", err)
		}
		if _, err := folderService.Create(ctx, user.ID, "/Destination_100%"); err != nil {
			t.Fatalf("create successful folder destination: %v", err)
		}

		status, body := patch(
			t,
			"/v1/folders/"+root.ID.String(),
			`{"parent_path":"/Destination_100%","name":"after_%"}`,
		)
		if status != http.StatusOK {
			t.Fatalf("status=%d body=%s, want 200", status, body)
		}
		if _, err := folderService.Get(ctx, user.ID, "/Destination_100%/after_%/child"); err != nil {
			t.Fatalf("relocated literal-segment descendant missing: %v", err)
		}
		if _, err := folderService.Get(ctx, user.ID, source.Path); !errors.Is(err, folder.ErrNotFound) {
			t.Fatalf("old literal-segment descendant remains: err=%v", err)
		}
	})
}

func insertRelocationHTTPFile(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	userID, folderID uuid.UUID,
	name, path, hashDigit string,
) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO files (
			id, user_id, name, path, folder_id, size, sha256, mime,
			storage_key, tags, index_status
		)
		VALUES ($1, $2, $3, $4, $5, 1, $6, 'text/plain', $7, '{}', 'pending')
	`,
		id,
		userID,
		name,
		path,
		folderID,
		strings.Repeat(hashDigit, 64),
		"relocate-http/"+id.String(),
	); err != nil {
		t.Fatalf("insert relocation file: %v", err)
	}
	return id
}

func installRelocationHTTPFailureTrigger(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION fail_relocation_http_test()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			IF NEW.name = 'force-database-error.txt' THEN
				RAISE EXCEPTION 'forced relocation database error';
			END IF;
			RETURN NEW;
		END;
		$$;
		DROP TRIGGER IF EXISTS fail_relocation_http_test ON files;
		CREATE TRIGGER fail_relocation_http_test
			BEFORE UPDATE OF name, path ON files
			FOR EACH ROW
			EXECUTE FUNCTION fail_relocation_http_test();
	`); err != nil {
		t.Fatalf("install relocation failure trigger: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, `
			DROP TRIGGER IF EXISTS fail_relocation_http_test ON files;
			DROP FUNCTION IF EXISTS fail_relocation_http_test();
		`); err != nil {
			t.Errorf("remove relocation failure trigger: %v", err)
		}
	})
}

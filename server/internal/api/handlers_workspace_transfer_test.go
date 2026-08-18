package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/workspace"
	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
	"github.com/PeterGuy326/mem/server/internal/workspacetransfer"
	"github.com/google/uuid"
)

type workspaceTransferServiceStub struct {
	export func(
		context.Context,
		workspacetransfer.ExportRequest,
	) (*workspacetransfer.ExportResult, error)
	importBundle func(
		context.Context,
		workspacetransfer.ImportRequest,
	) (*workspacetransfer.ImportResult, error)
	importHistory func(
		context.Context,
		uuid.UUID,
		int,
	) ([]workspacetransfer.ImportHistoryEntry, error)
}

func (stub *workspaceTransferServiceStub) Export(
	ctx context.Context,
	request workspacetransfer.ExportRequest,
) (*workspacetransfer.ExportResult, error) {
	return stub.export(ctx, request)
}

func (stub *workspaceTransferServiceStub) Import(
	ctx context.Context,
	request workspacetransfer.ImportRequest,
) (*workspacetransfer.ImportResult, error) {
	return stub.importBundle(ctx, request)
}

func (stub *workspaceTransferServiceStub) ImportHistory(
	ctx context.Context,
	workspaceID uuid.UUID,
	limit int,
) ([]workspacetransfer.ImportHistoryEntry, error) {
	if stub.importHistory == nil {
		return nil, workspacetransfer.ErrNotConfigured
	}
	return stub.importHistory(ctx, workspaceID, limit)
}

func TestWorkspaceExportBuffersUntilServiceSuccess(t *testing.T) {
	workspaceID := uuid.New()
	var temporaryPath string
	service := &workspaceTransferServiceStub{
		export: func(
			_ context.Context,
			request workspacetransfer.ExportRequest,
		) (*workspacetransfer.ExportResult, error) {
			if request.WorkspaceID != workspaceID {
				t.Fatalf("workspace id = %s", request.WorkspaceID)
			}
			named, ok := request.Writer.(interface{ Name() string })
			if !ok {
				t.Fatalf("writer type = %T, want named temporary file", request.Writer)
			}
			temporaryPath = named.Name()
			if _, err := io.WriteString(request.Writer, "portable-bundle"); err != nil {
				t.Fatal(err)
			}
			return &workspacetransfer.ExportResult{BundleID: uuid.New()}, nil
		},
	}
	tempDir := t.TempDir()
	server := &Server{
		WorkspaceTransfer:       service,
		WorkspaceTransferTmpDir: tempDir,
	}
	request := workspaceTransferRequest(
		httptest.NewRequest(http.MethodGet, "/v1/workspaces/current/export", nil),
		workspaceID,
		workspace.RoleOwner,
		[]string{auth.ScopeAdmin},
		nil,
	)
	recorder := httptest.NewRecorder()

	server.handleWorkspaceExport(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != workspacebundle.BundleMediaType {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); !strings.Contains(got, ".membundle") {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := recorder.Body.String(); got != "portable-bundle" {
		t.Fatalf("body = %q", got)
	}
	if temporaryPath == "" {
		t.Fatal("service did not observe temporary file")
	}
	if filepath.Dir(temporaryPath) != tempDir {
		t.Fatalf("temporary file = %s, want directory %s", temporaryPath, tempDir)
	}
	if _, err := os.Stat(temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file still exists: %s, err=%v", temporaryPath, err)
	}
}

func TestWorkspaceExportDoesNotPublishPartialArchiveOnLateFailure(t *testing.T) {
	service := &workspaceTransferServiceStub{
		export: func(
			_ context.Context,
			request workspacetransfer.ExportRequest,
		) (*workspacetransfer.ExportResult, error) {
			_, _ = io.WriteString(request.Writer, "partial-secret-archive")
			return nil, fmt.Errorf("late checksum: %w", workspacebundle.ErrIntegrity)
		},
	}
	tempDir := t.TempDir()
	server := &Server{
		WorkspaceTransfer:       service,
		WorkspaceTransferTmpDir: tempDir,
	}
	request := workspaceTransferRequest(
		httptest.NewRequest(http.MethodGet, "/v1/workspaces/current/export", nil),
		uuid.New(),
		workspace.RoleOwner,
		[]string{auth.ScopeAdmin},
		nil,
	)
	recorder := httptest.NewRecorder()

	server.handleWorkspaceExport(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "partial-secret-archive") {
		t.Fatalf("partial archive leaked into response: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "invalid_workspace_bundle") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func TestWorkspaceExportRejectsArchiveAboveConfiguredTransportLimit(t *testing.T) {
	var temporaryPath string
	service := &workspaceTransferServiceStub{
		export: func(
			_ context.Context,
			request workspacetransfer.ExportRequest,
		) (*workspacetransfer.ExportResult, error) {
			temporaryPath = request.Writer.(interface{ Name() string }).Name()
			_, _ = io.WriteString(request.Writer, "123456789")
			return &workspacetransfer.ExportResult{BundleID: uuid.New()}, nil
		},
	}
	tempDir := t.TempDir()
	server := &Server{
		WorkspaceTransfer:       service,
		WorkspaceBundleMaxBytes: 8,
		WorkspaceTransferTmpDir: tempDir,
	}
	request := workspaceTransferRequest(
		httptest.NewRequest(http.MethodGet, "/v1/workspaces/current/export", nil),
		uuid.New(),
		workspace.RoleOwner,
		[]string{auth.ScopeAdmin},
		nil,
	)
	recorder := httptest.NewRecorder()

	server.handleWorkspaceExport(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "123456789") {
		t.Fatalf("oversized archive leaked into response: %s", recorder.Body.String())
	}
	if _, err := os.Stat(temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file still exists: %s, err=%v", temporaryPath, err)
	}
	if filepath.Dir(temporaryPath) != tempDir {
		t.Fatalf("temporary file = %s, want directory %s", temporaryPath, tempDir)
	}
}

func TestWorkspaceImportSpoolsSecureTemporaryFile(t *testing.T) {
	workspaceID := uuid.New()
	bundleID := uuid.New()
	sourceWorkspaceID := uuid.New()
	importedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	var temporaryPath string
	service := &workspaceTransferServiceStub{
		importBundle: func(
			_ context.Context,
			request workspacetransfer.ImportRequest,
		) (*workspacetransfer.ImportResult, error) {
			if request.WorkspaceID != workspaceID ||
				request.Mode != workspacetransfer.RestoreModeFresh ||
				request.Size != int64(len("portable-bundle")) {
				t.Fatalf("request = %+v", request)
			}
			file, ok := request.Reader.(*os.File)
			if !ok {
				t.Fatalf("reader type = %T, want *os.File", request.Reader)
			}
			temporaryPath = file.Name()
			info, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("temporary mode = %#o", info.Mode().Perm())
			}
			body := make([]byte, request.Size)
			if _, err := request.Reader.ReadAt(body, 0); err != nil {
				t.Fatal(err)
			}
			if string(body) != "portable-bundle" {
				t.Fatalf("body = %q", body)
			}
			return &workspacetransfer.ImportResult{
				BundleID:          bundleID,
				ArchiveSHA256:     strings.Repeat("a", 64),
				SourceWorkspaceID: sourceWorkspaceID,
				ImportedAt:        importedAt,
				Counts: workspacebundle.ObjectCounts{
					Files:     2,
					Memories:  3,
					BlobBytes: 42,
				},
				Replayed: true,
			}, nil
		},
	}
	tempDir := t.TempDir()
	server := &Server{
		WorkspaceTransfer:       service,
		WorkspaceTransferTmpDir: tempDir,
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/workspaces/current/import?mode=fresh",
		strings.NewReader("portable-bundle"),
	)
	request.Header.Set("Content-Type", workspacebundle.BundleMediaType)
	request = workspaceTransferRequest(
		request,
		workspaceID,
		workspace.RoleAdmin,
		[]string{auth.ScopeAdmin},
		nil,
	)
	recorder := httptest.NewRecorder()

	server.handleWorkspaceImport(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response workspaceImportResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.BundleID != bundleID.String() ||
		response.SourceWorkspaceID != sourceWorkspaceID.String() ||
		response.Counts.Files != 2 ||
		response.Counts.Memories != 3 ||
		response.Counts.BlobBytes != 42 ||
		!response.Replayed {
		t.Fatalf("response = %+v", response)
	}
	if _, err := os.Stat(temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file still exists: %s, err=%v", temporaryPath, err)
	}
	if filepath.Dir(temporaryPath) != tempDir {
		t.Fatalf("temporary file = %s, want directory %s", temporaryPath, tempDir)
	}
}

func TestWorkspaceImportRejectsTransportViolationsBeforeService(t *testing.T) {
	tests := []struct {
		name          string
		contentType   string
		path          string
		contentLength int64
		maxBytes      int64
		wantStatus    int
	}{
		{
			name:       "missing content type",
			path:       "/v1/workspaces/current/import?mode=fresh",
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name:        "version media type parameter rejected",
			contentType: workspacebundle.BundleMediaType + "; version=1",
			path:        "/v1/workspaces/current/import?mode=fresh",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "missing mode",
			contentType: workspacebundle.BundleMediaType,
			path:        "/v1/workspaces/current/import",
			wantStatus:  http.StatusUnprocessableEntity,
		},
		{
			name:        "fake merge mode",
			contentType: workspacebundle.BundleMediaType,
			path:        "/v1/workspaces/current/import?mode=merge",
			wantStatus:  http.StatusUnprocessableEntity,
		},
		{
			name:          "declared body too large",
			contentType:   workspacebundle.BundleMediaType,
			path:          "/v1/workspaces/current/import?mode=fresh",
			contentLength: 9,
			maxBytes:      8,
			wantStatus:    http.StatusRequestEntityTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			server := &Server{
				WorkspaceTransfer: &workspaceTransferServiceStub{
					importBundle: func(
						context.Context,
						workspacetransfer.ImportRequest,
					) (*workspacetransfer.ImportResult, error) {
						called = true
						return nil, nil
					},
				},
				WorkspaceBundleMaxBytes: test.maxBytes,
			}
			request := httptest.NewRequest(
				http.MethodPost,
				test.path,
				strings.NewReader("bundle"),
			)
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.contentLength > 0 {
				request.ContentLength = test.contentLength
			}
			request = workspaceTransferRequest(
				request,
				uuid.New(),
				workspace.RoleOwner,
				[]string{auth.ScopeAdmin},
				nil,
			)
			recorder := httptest.NewRecorder()

			server.handleWorkspaceImport(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d, body = %s",
					recorder.Code,
					test.wantStatus,
					recorder.Body.String(),
				)
			}
			if called {
				t.Fatal("service called for rejected request")
			}
		})
	}
}

func TestWorkspaceImportEnforcesConfiguredLimitForChunkedBody(t *testing.T) {
	called := false
	server := &Server{
		WorkspaceTransfer: &workspaceTransferServiceStub{
			importBundle: func(
				context.Context,
				workspacetransfer.ImportRequest,
			) (*workspacetransfer.ImportResult, error) {
				called = true
				return nil, nil
			},
		},
		WorkspaceBundleMaxBytes: 8,
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/workspaces/current/import?mode=fresh",
		strings.NewReader("123456789"),
	)
	request.ContentLength = -1
	request.Header.Set("Content-Type", workspacebundle.BundleMediaType)
	request = workspaceTransferRequest(
		request,
		uuid.New(),
		workspace.RoleOwner,
		[]string{auth.ScopeAdmin},
		nil,
	)
	recorder := httptest.NewRecorder()

	server.handleWorkspaceImport(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if called {
		t.Fatal("service called for oversized chunked request")
	}
}

func TestWorkspaceTransferServiceErrorMapping(t *testing.T) {
	conflict := &workspacetransfer.ConflictError{
		Conflicts: []workspacetransfer.Conflict{{
			Kind:     "path",
			Resource: "files",
			Value:    "/Projects/plan.md",
			Detail:   "storage-key=must-not-leak",
		}},
		Total:     201,
		Truncated: true,
	}
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantHint   string
	}{
		{"invalid", workspacebundle.ErrInvalidBundle, http.StatusBadRequest, "invalid_workspace_bundle", ""},
		{"integrity", workspacebundle.ErrIntegrity, http.StatusBadRequest, "invalid_workspace_bundle", ""},
		{"dependency", workspacebundle.ErrDependency, http.StatusBadRequest, "invalid_workspace_bundle", ""},
		{"limit", workspacebundle.ErrLimitExceeded, http.StatusRequestEntityTooLarge, "workspace_bundle_too_large", ""},
		{"conflict", conflict, http.StatusConflict, "workspace_import_conflict", ""},
		{"unsupported mode", workspacetransfer.ErrUnsupportedMode, http.StatusUnprocessableEntity, "unsupported_workspace_bundle", ""},
		{"unsupported version", workspacebundle.ErrUnsupportedVersion, http.StatusUnprocessableEntity, "unsupported_workspace_bundle", ""},
		{
			"commit indeterminate",
			fmt.Errorf(
				"database password must-not-leak: %w",
				workspacetransfer.ErrCommitIndeterminate,
			),
			http.StatusServiceUnavailable,
			"workspace_import_commit_indeterminate",
			"uploaded objects were preserved; retry the exact same bundle",
		},
		{"storage exhausted", syscall.ENOSPC, http.StatusInsufficientStorage, "workspace_transfer_storage_exhausted", ""},
		{"internal", errors.New("database password must-not-leak"), http.StatusInternalServerError, "workspace_transfer_failed", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{WorkspaceTransfer: &workspaceTransferServiceStub{
				importBundle: func(
					context.Context,
					workspacetransfer.ImportRequest,
				) (*workspacetransfer.ImportResult, error) {
					return nil, fmt.Errorf("wrapped: %w", test.err)
				},
			}}
			request := httptest.NewRequest(
				http.MethodPost,
				"/v1/workspaces/current/import?mode=fresh",
				bytes.NewReader([]byte("bundle")),
			)
			request.Header.Set("Content-Type", workspacebundle.BundleMediaType)
			request = workspaceTransferRequest(
				request,
				uuid.New(),
				workspace.RoleOwner,
				[]string{auth.ScopeAdmin},
				nil,
			)
			recorder := httptest.NewRecorder()

			server.handleWorkspaceImport(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d, body = %s",
					recorder.Code,
					test.wantStatus,
					recorder.Body.String(),
				)
			}
			if !strings.Contains(recorder.Body.String(), test.wantCode) {
				t.Fatalf("body = %s", recorder.Body.String())
			}
			if test.wantHint != "" &&
				!strings.Contains(recorder.Body.String(), test.wantHint) {
				t.Fatalf(
					"body missing recovery hint %q: %s",
					test.wantHint,
					recorder.Body.String(),
				)
			}
			if strings.Contains(recorder.Body.String(), "must-not-leak") {
				t.Fatalf("internal detail leaked: %s", recorder.Body.String())
			}
			if test.name == "conflict" {
				if !strings.Contains(recorder.Body.String(), "/Projects/plan.md") {
					t.Fatalf("safe conflict not projected: %s", recorder.Body.String())
				}
				var response workspaceImportConflictResponse
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Total != 201 || !response.Truncated {
					t.Fatalf("conflict metadata = %+v", response)
				}
			}
		})
	}
}

func TestWorkspaceTransferRBAC(t *testing.T) {
	tests := []struct {
		name       string
		export     bool
		scopes     []string
		paths      []string
		role       string
		wantStatus int
	}{
		{"export owner admin", true, []string{auth.ScopeAdmin}, nil, workspace.RoleOwner, http.StatusNoContent},
		{"import admin role admin", false, []string{auth.ScopeAdmin}, nil, workspace.RoleAdmin, http.StatusNoContent},
		{"root path is unrestricted", true, []string{auth.ScopeAdmin}, []string{"/"}, workspace.RoleOwner, http.StatusNoContent},
		{"export read without admin", true, []string{auth.ScopeRead}, nil, workspace.RoleOwner, http.StatusForbidden},
		{"import write without admin", false, []string{auth.ScopeWrite}, nil, workspace.RoleOwner, http.StatusForbidden},
		{"restricted path", false, []string{auth.ScopeAdmin}, []string{"/Projects"}, workspace.RoleOwner, http.StatusForbidden},
		{"member role", true, []string{auth.ScopeAdmin}, nil, workspace.RoleMember, http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{}
			var handler http.Handler = http.HandlerFunc(func(
				w http.ResponseWriter,
				_ *http.Request,
			) {
				w.WriteHeader(http.StatusNoContent)
			})
			handler = server.requireWorkspaceTransfer(handler)
			handler = server.requireUnrestrictedPaths(handler)
			handler = server.requireScope(auth.ScopeAdmin)(handler)
			if test.export {
				handler = server.requireScope(auth.ScopeRead)(handler)
			} else {
				handler = server.requireScope(auth.ScopeWrite)(handler)
			}
			request := workspaceTransferRequest(
				httptest.NewRequest(http.MethodGet, "/", nil),
				uuid.New(),
				test.role,
				test.scopes,
				test.paths,
			)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d, body = %s",
					recorder.Code,
					test.wantStatus,
					recorder.Body.String(),
				)
			}
		})
	}
}

func TestWorkspaceTransferRoutesUseConfiguredRequestTimeout(t *testing.T) {
	const transferTimeout = 2 * time.Hour
	server := &Server{WorkspaceTransferTimeout: transferTimeout}
	tests := []struct {
		path        string
		wantTimeout time.Duration
	}{
		{"/v1/workspaces/current/export", transferTimeout},
		{"/v1/workspaces/current/import", transferTimeout},
		{"/v1/files", time.Minute},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			var remaining time.Duration
			handler := server.requestTimeoutMiddleware(http.HandlerFunc(func(
				w http.ResponseWriter,
				r *http.Request,
			) {
				deadline, ok := r.Context().Deadline()
				if !ok {
					t.Fatal("request context has no deadline")
				}
				remaining = time.Until(deadline)
				w.WriteHeader(http.StatusNoContent)
			}))
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(
				recorder,
				httptest.NewRequest(http.MethodGet, test.path, nil),
			)

			if remaining < test.wantTimeout-time.Second ||
				remaining > test.wantTimeout {
				t.Fatalf(
					"context timeout = %s, want approximately %s",
					remaining,
					test.wantTimeout,
				)
			}
		})
	}
}

func TestWorkspaceTransferCapacityIsNonBlockingAndAlwaysReleased(t *testing.T) {
	gate := make(chan struct{}, 1)
	server := &Server{WorkspaceTransferGate: gate}
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan struct{})
	handler := server.requireWorkspaceTransferCapacity(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	go func() {
		defer close(firstDone)
		handler.ServeHTTP(
			httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/", nil),
		)
	}()
	<-entered

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/", nil),
	)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("429 response missing Retry-After")
	}

	close(release)
	<-firstDone
	if len(gate) != 0 {
		t.Fatalf("gate still occupied after success: %d", len(gate))
	}

	errorHandler := server.requireWorkspaceTransferCapacity(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		writeWorkspaceTransferInternalError(w)
	}))
	errorHandler.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/", nil),
	)
	if len(gate) != 0 {
		t.Fatalf("gate still occupied after handler error: %d", len(gate))
	}

	cancelEntered := make(chan struct{})
	cancelHandler := server.requireWorkspaceTransferCapacity(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		close(cancelEntered)
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancelledDone := make(chan struct{})
	go func() {
		defer close(cancelledDone)
		cancelHandler.ServeHTTP(
			httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx),
		)
	}()
	<-cancelEntered
	cancel()
	<-cancelledDone
	if len(gate) != 0 {
		t.Fatalf("gate still occupied after cancellation: %d", len(gate))
	}
}

func TestWorkspaceTransferStorageExhaustionDetection(t *testing.T) {
	for _, storageError := range []error{syscall.ENOSPC, syscall.EDQUOT} {
		if !isWorkspaceTransferStorageExhausted(
			fmt.Errorf("temporary file: %w", storageError),
		) {
			t.Fatalf("storage exhaustion not detected: %v", storageError)
		}
	}
	if isWorkspaceTransferStorageExhausted(errors.New("permission denied")) {
		t.Fatal("ordinary error classified as storage exhaustion")
	}
}

func TestWorkspaceCapabilitiesExposeTransferAvailabilityAndPermission(t *testing.T) {
	service := &workspaceTransferServiceStub{}
	tests := []struct {
		name          string
		service       WorkspaceTransferService
		role          string
		paths         []string
		wantFeature   bool
		wantPermitted bool
	}{
		{"owner", service, workspace.RoleOwner, nil, true, true},
		{"admin", service, workspace.RoleAdmin, nil, true, true},
		{"member", service, workspace.RoleMember, nil, true, false},
		{"restricted", service, workspace.RoleOwner, []string{"/Projects"}, true, false},
		{"disabled", nil, workspace.RoleOwner, nil, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{WorkspaceTransfer: test.service}
			request := workspaceTransferRequest(
				httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil),
				uuid.New(),
				test.role,
				[]string{auth.ScopeAdmin},
				test.paths,
			)
			recorder := httptest.NewRecorder()

			server.handleCapabilities(recorder, request)

			var response struct {
				Features                      map[string]bool `json:"features"`
				Permissions                   map[string]bool `json:"permissions"`
				WorkspaceRestoreModes         []string        `json:"workspace_restore_modes"`
				WorkspaceBundleSchemaVersions []int           `json:"workspace_bundle_schema_versions"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"workspace_export", "workspace_import"} {
				if response.Features[key] != test.wantFeature {
					t.Errorf(
						"features[%s] = %t, want %t",
						key,
						response.Features[key],
						test.wantFeature,
					)
				}
				if response.Permissions[key] != test.wantPermitted {
					t.Errorf(
						"permissions[%s] = %t, want %t",
						key,
						response.Permissions[key],
						test.wantPermitted,
					)
				}
			}
			if test.wantFeature {
				if len(response.WorkspaceRestoreModes) != 2 ||
					response.WorkspaceRestoreModes[0] != workspacetransfer.RestoreModeFresh ||
					response.WorkspaceRestoreModes[1] != workspacetransfer.RestoreModeMergeConservative {
					t.Errorf("restore modes = %v", response.WorkspaceRestoreModes)
				}
				if len(response.WorkspaceBundleSchemaVersions) != 2 ||
					response.WorkspaceBundleSchemaVersions[0] != workspacebundle.SchemaVersionV1 ||
					response.WorkspaceBundleSchemaVersions[1] != workspacebundle.CurrentSchemaVersion {
					t.Errorf(
						"bundle schema versions = %v",
						response.WorkspaceBundleSchemaVersions,
					)
				}
			} else if len(response.WorkspaceRestoreModes) != 0 ||
				len(response.WorkspaceBundleSchemaVersions) != 0 {
				t.Errorf(
					"disabled transfer advertised modes=%v versions=%v",
					response.WorkspaceRestoreModes,
					response.WorkspaceBundleSchemaVersions,
				)
			}
		})
	}
}

func TestWorkspaceImportHistoryProjectsLedgerEntries(t *testing.T) {
	workspaceID := uuid.New()
	bundleID := uuid.New()
	sourceWorkspaceID := uuid.New()
	importedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	var observedWorkspaceID uuid.UUID
	var observedLimit int
	server := &Server{WorkspaceTransfer: &workspaceTransferServiceStub{
		importHistory: func(
			_ context.Context,
			targetID uuid.UUID,
			limit int,
		) ([]workspacetransfer.ImportHistoryEntry, error) {
			observedWorkspaceID = targetID
			observedLimit = limit
			return []workspacetransfer.ImportHistoryEntry{{
				BundleID:          bundleID,
				ArchiveSHA256:     strings.Repeat("a", 64),
				SourceWorkspaceID: sourceWorkspaceID,
				SchemaVersion:     2,
				RestoreMode:       workspacetransfer.RestoreModeFresh,
				ResultStatus:      workspacetransfer.ImportStatusSucceeded,
				ConflictCount:     0,
				SkippedCount:      0,
				ImportedAt:        importedAt,
			}}, nil
		},
	}}
	request := workspaceTransferRequest(
		httptest.NewRequest(http.MethodGet, "/v1/workspaces/current/imports", nil),
		workspaceID,
		workspace.RoleOwner,
		[]string{auth.ScopeAdmin},
		nil,
	)
	recorder := httptest.NewRecorder()

	server.handleWorkspaceImportHistory(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if observedWorkspaceID != workspaceID {
		t.Fatalf("workspace id = %s, want %s", observedWorkspaceID, workspaceID)
	}
	if observedLimit != workspacetransfer.DefaultImportHistoryLimit {
		t.Fatalf("limit = %d, want default %d", observedLimit, workspacetransfer.DefaultImportHistoryLimit)
	}
	var response workspaceImportHistoryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Count != 1 || len(response.Items) != 1 {
		t.Fatalf("response = %+v", response)
	}
	entry := response.Items[0]
	if entry.BundleID != bundleID.String() ||
		entry.ArchiveSHA256 != strings.Repeat("a", 64) ||
		entry.SourceWorkspaceID != sourceWorkspaceID.String() ||
		entry.SchemaVersion != 2 ||
		entry.RestoreMode != workspacetransfer.RestoreModeFresh ||
		entry.ResultStatus != workspacetransfer.ImportStatusSucceeded ||
		entry.ConflictCount != 0 ||
		entry.SkippedCount != 0 ||
		!entry.ImportedAt.Equal(importedAt) {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestWorkspaceImportHistoryReturnsEmptyListNotNull(t *testing.T) {
	server := &Server{WorkspaceTransfer: &workspaceTransferServiceStub{
		importHistory: func(
			context.Context,
			uuid.UUID,
			int,
		) ([]workspacetransfer.ImportHistoryEntry, error) {
			return []workspacetransfer.ImportHistoryEntry{}, nil
		},
	}}
	request := workspaceTransferRequest(
		httptest.NewRequest(http.MethodGet, "/v1/workspaces/current/imports", nil),
		uuid.New(),
		workspace.RoleOwner,
		[]string{auth.ScopeAdmin},
		nil,
	)
	recorder := httptest.NewRecorder()

	server.handleWorkspaceImportHistory(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"items":[]`) {
		t.Fatalf("body = %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"count":0`) {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func TestWorkspaceImportHistoryValidatesLimitBeforeService(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantLimit  int
	}{
		{"default", "/v1/workspaces/current/imports", http.StatusOK, 50},
		{"explicit", "/v1/workspaces/current/imports?limit=7", http.StatusOK, 7},
		{"upper bound", "/v1/workspaces/current/imports?limit=100", http.StatusOK, 100},
		{"zero", "/v1/workspaces/current/imports?limit=0", http.StatusBadRequest, 0},
		{"too large", "/v1/workspaces/current/imports?limit=101", http.StatusBadRequest, 0},
		{"not a number", "/v1/workspaces/current/imports?limit=abc", http.StatusBadRequest, 0},
		{"negative", "/v1/workspaces/current/imports?limit=-3", http.StatusBadRequest, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			var observedLimit int
			server := &Server{WorkspaceTransfer: &workspaceTransferServiceStub{
				importHistory: func(
					_ context.Context,
					_ uuid.UUID,
					limit int,
				) ([]workspacetransfer.ImportHistoryEntry, error) {
					called = true
					observedLimit = limit
					return []workspacetransfer.ImportHistoryEntry{}, nil
				},
			}}
			request := workspaceTransferRequest(
				httptest.NewRequest(http.MethodGet, test.path, nil),
				uuid.New(),
				workspace.RoleOwner,
				[]string{auth.ScopeAdmin},
				nil,
			)
			recorder := httptest.NewRecorder()

			server.handleWorkspaceImportHistory(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d, body = %s",
					recorder.Code,
					test.wantStatus,
					recorder.Body.String(),
				)
			}
			if test.wantStatus != http.StatusOK {
				if called {
					t.Fatal("service called for rejected limit")
				}
				if !strings.Contains(recorder.Body.String(), "bad_limit") {
					t.Fatalf("body = %s", recorder.Body.String())
				}
				return
			}
			if !called || observedLimit != test.wantLimit {
				t.Fatalf("called = %t, limit = %d, want %d", called, observedLimit, test.wantLimit)
			}
		})
	}
}

func TestWorkspaceImportHistoryErrorMapping(t *testing.T) {
	requestFor := func() *http.Request {
		return workspaceTransferRequest(
			httptest.NewRequest(http.MethodGet, "/v1/workspaces/current/imports", nil),
			uuid.New(),
			workspace.RoleOwner,
			[]string{auth.ScopeAdmin},
			nil,
		)
	}

	t.Run("internal error does not leak detail", func(t *testing.T) {
		server := &Server{WorkspaceTransfer: &workspaceTransferServiceStub{
			importHistory: func(
				context.Context,
				uuid.UUID,
				int,
			) ([]workspacetransfer.ImportHistoryEntry, error) {
				return nil, fmt.Errorf("database password must-not-leak: %w", errors.New("boom"))
			},
		}}
		recorder := httptest.NewRecorder()

		server.handleWorkspaceImportHistory(recorder, requestFor())

		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "workspace_transfer_failed") {
			t.Fatalf("body = %s", recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "must-not-leak") {
			t.Fatalf("internal detail leaked: %s", recorder.Body.String())
		}
	})

	t.Run("not configured service", func(t *testing.T) {
		server := &Server{WorkspaceTransfer: &workspaceTransferServiceStub{}}
		recorder := httptest.NewRecorder()

		server.handleWorkspaceImportHistory(recorder, requestFor())

		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "workspace_transfer_unavailable") {
			t.Fatalf("body = %s", recorder.Body.String())
		}
	})

	t.Run("nil service", func(t *testing.T) {
		server := &Server{}
		recorder := httptest.NewRecorder()

		server.handleWorkspaceImportHistory(recorder, requestFor())

		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	})
}

func workspaceTransferRequest(
	request *http.Request,
	workspaceID uuid.UUID,
	role string,
	scopes, paths []string,
) *http.Request {
	ctx := context.WithValue(request.Context(), ctxToken, &auth.Token{
		Scopes: scopes,
		Paths:  paths,
	})
	ctx = context.WithValue(ctx, ctxWorkspace, &workspace.Workspace{
		ID:   workspaceID,
		Role: role,
	})
	return request.WithContext(ctx)
}

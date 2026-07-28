package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/auth"
	memdb "github.com/PeterGuy326/mem/server/internal/db"
	"github.com/PeterGuy326/mem/server/internal/handoff"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

// TestHandoffCrossAgentHTTPIntegration is the P0 acceptance path for moving a
// live task from Claude Code to Codex without carrying over the original
// process or session. It deliberately traverses the real HTTP middleware,
// chi.Router, authentication, workspace authorization, and PostgreSQL service.
//
// Use a disposable database rather than a shared test database:
//
//	MEM_TEST_DB=postgres://mem:mem@localhost:5432/mem_handoff_e2e_test?sslmode=disable \
//	  go test ./internal/api -run TestHandoffCrossAgentHTTPIntegration -count=1
func TestHandoffCrossAgentHTTPIntegration(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping handoff HTTP integration test")
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
	claudeUser, claudeWorkspace := createHandoffE2ETenant(
		t, ctx, database, authService, workspaceService, "cross-agent",
	)
	otherUser, otherWorkspace := createHandoffE2ETenant(
		t, ctx, database, authService, workspaceService, "other-workspace",
	)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := database.Pool.Exec(
			cleanupCtx,
			`DELETE FROM users WHERE id = $1 OR id = $2`,
			claudeUser.ID,
			otherUser.ID,
		); err != nil {
			t.Errorf("clean up handoff HTTP tenants: %v", err)
		}
	})

	const scopePath = "/Projects/Agent Drive"
	claudeToken, claudeMeta := createHandoffE2EToken(
		t,
		ctx,
		authService,
		claudeUser.ID,
		claudeWorkspace.ID,
		"claude-code",
		[]string{auth.ScopeRead, auth.ScopeWrite},
		[]string{scopePath},
	)
	codexToken, codexMeta := createHandoffE2EToken(
		t,
		ctx,
		authService,
		claudeUser.ID,
		claudeWorkspace.ID,
		"codex",
		[]string{auth.ScopeRead},
		[]string{scopePath},
	)
	wrongPathToken, _ := createHandoffE2EToken(
		t,
		ctx,
		authService,
		claudeUser.ID,
		claudeWorkspace.ID,
		"codex-wrong-path",
		[]string{auth.ScopeRead},
		[]string{"/Private/Unrelated"},
	)
	otherWorkspaceToken, _ := createHandoffE2EToken(
		t,
		ctx,
		authService,
		otherUser.ID,
		otherWorkspace.ID,
		"codex-other-workspace",
		[]string{auth.ScopeRead},
		[]string{scopePath},
	)
	if claudeToken == codexToken || claudeMeta.ID == codexMeta.ID {
		t.Fatal("Claude Code and Codex must use independent credentials")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	apiServer := httptest.NewServer((&Server{
		Auth:      authService,
		Workspace: workspaceService,
		Handoff:   handoff.New(database.Pool),
		Log:       logger,
	}).Router())
	t.Cleanup(apiServer.Close)

	taskKey := "客户迁移 / sprint α"
	goal := "让任务在 Claude Code 与 Codex 之间无痛迁移"
	progressSummary := "已冻结 vendor-neutral handoff v1，并持久化首个 checkpoint"
	completed := "完成真实 HTTP checkpoint 写入"
	decision := "以不可变 checkpoint 作为跨 Agent 的交接边界"
	nextStep := "由 Codex 在新会话中恢复任务并继续实现"
	document := handoff.HandoffV1{
		Contract:       handoff.ContractName,
		SchemaVersion:  handoff.SchemaVersionV1,
		CheckpointKind: handoff.CheckpointKindHandoff,
		TaskKey:        taskKey,
		ScopePath:      scopePath,
		State: handoff.StateV1{
			Status: handoff.TaskStatusReady,
			Goal:   goal,
			Progress: handoff.ProgressV1{
				Summary:   progressSummary,
				Completed: []string{completed},
			},
			Decisions: []handoff.DecisionV1{{
				Summary:    decision,
				Rationale:  "Agent 只依赖稳定契约，不依赖原厂会话格式",
				References: []string{},
			}},
			NextSteps: []handoff.NextStepV1{{
				Summary:    nextStep,
				References: []string{},
			}},
			Blockers:      []handoff.BlockerV1{},
			OpenQuestions: []string{},
			Artifacts:     []handoff.ArtifactV1{},
			WorkspaceState: &handoff.WorkspaceState{
				WorkingDirectory: "/workspace/mem",
				VCS: &handoff.VCSState{
					Revision: "0123456789abcdef",
					Branch:   "feature/agent-handoff",
					Dirty:    true,
				},
			},
		},
		Producer: handoff.ProducerV1{
			AgentID:   "claude-code",
			SessionID: "claude-session-e2e",
		},
	}
	checkpointBody, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal checkpoint request: %v", err)
	}

	// Match the production API client exactly: one PathEscape over the logical
	// key. The embedded slash must remain one chi route parameter.
	escapedTaskKey := url.PathEscape(taskKey)
	if !strings.Contains(escapedTaskKey, "%2F") ||
		!strings.Contains(escapedTaskKey, "%20") ||
		!strings.Contains(escapedTaskKey, "%E5%AE%A2") {
		t.Fatalf("task key did not exercise slash, space, and Unicode escaping: %q", escapedTaskKey)
	}
	checkpointPath := "/v1/tasks/" + escapedTaskKey + "/checkpoints"
	resumePath := "/v1/tasks/" + escapedTaskKey + "/resume"

	created := handoffE2ERequest(
		t,
		ctx,
		apiServer.Client(),
		http.MethodPost,
		apiServer.URL+checkpointPath,
		claudeToken,
		"claude-checkpoint-"+uuid.NewString(),
		checkpointBody,
	)
	if created.status != http.StatusCreated {
		t.Fatalf("Claude checkpoint status=%d body=%s", created.status, created.body)
	}
	if got := created.header.Get("Location"); !strings.HasPrefix(got, checkpointPath+"/") {
		t.Fatalf("checkpoint Location=%q, want prefix %q", got, checkpointPath+"/")
	}
	var checkpointResult handoff.CheckpointResult
	if err := json.Unmarshal(created.body, &checkpointResult); err != nil {
		t.Fatalf("decode checkpoint response: %v; body=%s", err, created.body)
	}
	if checkpointResult.Replayed ||
		checkpointResult.Checkpoint.TaskKey != taskKey ||
		checkpointResult.Checkpoint.Sequence != 1 {
		t.Fatalf("checkpoint response=%+v", checkpointResult)
	}

	// POST is intentional: resume is a read operation even though it accepts a
	// request body. A read-only Codex token must be able to restore the task.
	resumedHTTP := handoffE2ERequest(
		t,
		ctx,
		apiServer.Client(),
		http.MethodPost,
		apiServer.URL+resumePath,
		codexToken,
		"",
		[]byte(`{}`),
	)
	if resumedHTTP.status != http.StatusOK {
		t.Fatalf("Codex resume status=%d body=%s", resumedHTTP.status, resumedHTTP.body)
	}
	var resumed resumeResponse
	if err := json.Unmarshal(resumedHTTP.body, &resumed); err != nil {
		t.Fatalf("decode resume response: %v; body=%s", err, resumedHTTP.body)
	}
	if resumed.Contract != handoff.ResumeContractName ||
		resumed.SchemaVersion != handoff.SchemaVersionV1 ||
		resumed.Task.WorkspaceID != claudeWorkspace.ID ||
		resumed.Task.TaskKey != taskKey ||
		resumed.Checkpoint.ID != checkpointResult.Checkpoint.ID {
		t.Fatalf("resumed envelope=%+v", resumed)
	}
	state := resumed.Checkpoint.Handoff.State
	if state.Goal != goal ||
		state.Progress.Summary != progressSummary ||
		len(state.Progress.Completed) != 1 ||
		state.Progress.Completed[0] != completed {
		t.Fatalf("resumed goal/progress=%+v", state)
	}
	if len(state.Decisions) != 1 ||
		state.Decisions[0].Summary != decision ||
		len(state.NextSteps) != 1 ||
		state.NextSteps[0].Summary != nextStep {
		t.Fatalf("resumed decisions/next_steps=%+v", state)
	}
	if resumed.Checkpoint.Handoff.Producer.AgentID != "claude-code" ||
		resumed.Checkpoint.Handoff.Producer.SessionID != "claude-session-e2e" {
		t.Fatalf("resumed producer=%+v", resumed.Checkpoint.Handoff.Producer)
	}
	if !resumed.Complete || len(resumed.Missing) != 0 {
		t.Fatalf("resume completeness=%t missing=%+v", resumed.Complete, resumed.Missing)
	}
	canonicalPayload, err := json.Marshal(resumed.Checkpoint.Handoff)
	if err != nil {
		t.Fatalf("marshal resumed handoff: %v", err)
	}
	payloadHash := sha256.Sum256(canonicalPayload)
	if got := hex.EncodeToString(payloadHash[:]); got != resumed.Checkpoint.PayloadSHA256 {
		t.Fatalf(
			"payload integrity mismatch: response=%q recomputed=%q",
			resumed.Checkpoint.PayloadSHA256,
			got,
		)
	}

	codexWrite := handoffE2ERequest(
		t,
		ctx,
		apiServer.Client(),
		http.MethodPost,
		apiServer.URL+checkpointPath,
		codexToken,
		"codex-write-"+uuid.NewString(),
		checkpointBody,
	)
	assertHandoffE2EError(t, codexWrite, http.StatusForbidden, "forbidden")

	otherWorkspaceResume := handoffE2ERequest(
		t,
		ctx,
		apiServer.Client(),
		http.MethodPost,
		apiServer.URL+resumePath,
		otherWorkspaceToken,
		"",
		[]byte(`{}`),
	)
	assertHandoffE2EError(t, otherWorkspaceResume, http.StatusNotFound, "not_found")

	wrongPathResume := handoffE2ERequest(
		t,
		ctx,
		apiServer.Client(),
		http.MethodPost,
		apiServer.URL+resumePath,
		wrongPathToken,
		"",
		[]byte(`{}`),
	)
	assertHandoffE2EError(t, wrongPathResume, http.StatusNotFound, "not_found")
}

type handoffE2EHTTPResult struct {
	status int
	header http.Header
	body   []byte
}

func handoffE2ERequest(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	method string,
	endpoint string,
	token string,
	idempotencyKey string,
	body []byte,
) handoffE2EHTTPResult {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create %s %s request: %v", method, endpoint, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, endpoint, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s %s response: %v", method, endpoint, err)
	}
	return handoffE2EHTTPResult{
		status: response.StatusCode,
		header: response.Header.Clone(),
		body:   responseBody,
	}
}

func assertHandoffE2EError(
	t *testing.T,
	result handoffE2EHTTPResult,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("decode error response: %v; body=%s", err, result.body)
	}
	if result.status != wantStatus || body.Error != wantCode {
		t.Fatalf(
			"HTTP error status=%d code=%q body=%s; want status=%d code=%q",
			result.status,
			body.Error,
			result.body,
			wantStatus,
			wantCode,
		)
	}
}

func createHandoffE2ETenant(
	t *testing.T,
	ctx context.Context,
	database *memdb.DB,
	authService *auth.Service,
	workspaceService *workspace.Service,
	label string,
) (*auth.User, *workspace.Workspace) {
	t.Helper()
	user, err := authService.CreateUser(
		ctx,
		label+"-"+uuid.NewString()+"@example.com",
		"integration-password",
	)
	if err != nil {
		t.Fatalf("create %s user: %v", label, err)
	}
	current, err := workspaceService.Resolve(ctx, user.ID, nil)
	if err != nil {
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID)
		t.Fatalf("resolve %s workspace: %v", label, err)
	}
	return user, current
}

func createHandoffE2EToken(
	t *testing.T,
	ctx context.Context,
	authService *auth.Service,
	userID uuid.UUID,
	workspaceID uuid.UUID,
	name string,
	scopes []string,
	paths []string,
) (string, *auth.Token) {
	t.Helper()
	plaintext, token, err := authService.CreateToken(
		ctx,
		userID,
		&workspaceID,
		name,
		scopes,
		paths,
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("create %s token: %v", name, err)
	}
	return plaintext, token
}

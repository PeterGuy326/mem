package api

import (
	"bytes"
	"context"
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
	"github.com/PeterGuy326/mem/server/internal/entitlement"
	"github.com/PeterGuy326/mem/server/internal/search"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

func TestManagedEmbeddingHTTPAuthorizationPostgres(t *testing.T) {
	dsn := os.Getenv("MEM_TEST_DB")
	if dsn == "" {
		t.Skip("MEM_TEST_DB not set; skipping managed embedding HTTP integration test")
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
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	authService := auth.New(database.Pool)
	workspaceService := workspace.New(database.Pool)
	userA, err := authService.CreateUser(
		ctx,
		"managed-http-a-"+uuid.NewString()+"@example.test",
		"secret-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	userB, err := authService.CreateUser(
		ctx,
		"managed-http-b-"+uuid.NewString()+"@example.test",
		"secret-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	workspaceA, err := workspaceService.Resolve(ctx, userA.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	workspaceB, err := workspaceService.Resolve(ctx, userB.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := database.Pool.Exec(
			cleanupCtx,
			`DELETE FROM users WHERE id = $1 OR id = $2`,
			userA.ID,
			userB.ID,
		); err != nil {
			t.Errorf("cleanup managed HTTP tenants: %v", err)
		}
	})

	unboundA, _, err := authService.CreateToken(
		ctx,
		userA.ID,
		nil,
		"unbound-a",
		[]string{auth.ScopeSearch},
		nil,
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	boundA, _, err := authService.CreateToken(
		ctx,
		userA.ID,
		&workspaceA.ID,
		"bound-a",
		[]string{auth.ScopeSearch},
		nil,
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	providerSpec := "openai:text-embedding-3-small"
	searchFake := &managedSearchFake{
		spec: providerSpec,
		hits: []search.Hit{{
			EvidenceID: uuid.NewString(),
			FileID:     uuid.New(),
			Source:     search.RouteText,
			Score:      0.5,
		}},
	}
	usageFake := &managedEntitlementFake{
		reservation: &entitlement.Reservation{
			ID:     uuid.New(),
			Status: entitlement.StatusReserved,
			Summary: entitlement.Summary{
				WorkspaceID: workspaceA.ID,
				Plan:        "member",
				Status:      "active",
				Qualifying:  true,
				Remaining:   9,
				ResetAt:     time.Now().UTC().Add(time.Hour),
			},
		},
		finalSummary: entitlement.Summary{
			WorkspaceID: workspaceA.ID,
			Plan:        "member",
			Status:      "active",
			Qualifying:  true,
			Remaining:   9,
			ResetAt:     time.Now().UTC().Add(time.Hour),
		},
	}
	server := httptest.NewServer((&Server{
		Auth:                     authService,
		Workspace:                workspaceService,
		Search:                   searchFake,
		DeploymentMode:           "saas",
		ManagedEmbeddingProvider: providerSpec,
		Entitlements:             usageFake,
		Log:                      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Router())
	t.Cleanup(server.Close)

	resetCalls := func() {
		searchFake.searchCalls = 0
		searchFake.embeddingCalls = 0
		usageFake.reserveCalls = 0
		usageFake.finalizeCalls = 0
	}
	request := func(token string, workspaceID uuid.UUID, key string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			server.URL+"/v1/search",
			bytes.NewBufferString(`{"query":"member recall","route":"text"}`),
		)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if workspaceID != uuid.Nil {
			req.Header.Set("X-Workspace-ID", workspaceID.String())
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		return response
	}

	resetCalls()
	if response := request("", workspaceA.ID, "unauthenticated"); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.StatusCode)
	}
	if usageFake.reserveCalls != 0 || searchFake.embeddingCalls != 0 ||
		searchFake.searchCalls != 0 {
		t.Fatal("unauthenticated request reached workspace, entitlement, or provider")
	}

	resetCalls()
	if response := request(unboundA, workspaceB.ID, "nonmember"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("nonmember status = %d", response.StatusCode)
	}
	if usageFake.reserveCalls != 0 || searchFake.embeddingCalls != 0 ||
		searchFake.searchCalls != 0 {
		t.Fatal("nonmember request reached entitlement/provider resolution")
	}

	resetCalls()
	if response := request(boundA, workspaceB.ID, "cross-workspace"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-workspace status = %d", response.StatusCode)
	}
	if usageFake.reserveCalls != 0 || searchFake.embeddingCalls != 0 ||
		searchFake.searchCalls != 0 {
		t.Fatal("cross-workspace token reached entitlement/provider resolution")
	}

	resetCalls()
	usageFake.reserveErr = &entitlement.DecisionError{
		Kind: entitlement.ErrPlanRequired,
		Summary: entitlement.Summary{
			WorkspaceID: workspaceA.ID,
			Plan:        "free",
			Status:      "inactive",
			ResetAt:     time.Now().UTC().Add(time.Hour),
		},
	}
	usageFake.reservation = nil
	if response := request(boundA, workspaceA.ID, "no-plan"); response.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("no-plan status = %d", response.StatusCode)
	}
	if usageFake.reserveCalls != 1 || searchFake.searchCalls != 0 {
		t.Fatalf("no-plan reserve=%d provider=%d", usageFake.reserveCalls, searchFake.searchCalls)
	}

	resetCalls()
	usageFake.reserveErr = &entitlement.DecisionError{
		Kind: entitlement.ErrQuotaExhausted,
		Summary: entitlement.Summary{
			WorkspaceID: workspaceA.ID,
			Plan:        "member",
			Status:      "active",
			ResetAt:     time.Now().UTC().Add(time.Hour),
		},
	}
	if response := request(boundA, workspaceA.ID, "quota"); response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("quota status = %d", response.StatusCode)
	} else if response.Header.Get("Retry-After") == "" {
		t.Fatal("quota response lacks Retry-After")
	}
	if usageFake.reserveCalls != 1 || searchFake.searchCalls != 0 {
		t.Fatalf("quota reserve=%d provider=%d", usageFake.reserveCalls, searchFake.searchCalls)
	}

	resetCalls()
	usageFake.reserveErr = nil
	usageFake.reservation = &entitlement.Reservation{
		ID:     uuid.New(),
		Status: entitlement.StatusReserved,
		Summary: entitlement.Summary{
			WorkspaceID: workspaceA.ID,
			Plan:        "member",
			Status:      "active",
			Qualifying:  true,
			Remaining:   9,
			ResetAt:     time.Now().UTC().Add(time.Hour),
		},
	}
	if response := request(boundA, workspaceA.ID, "member"); response.StatusCode != http.StatusOK {
		t.Fatalf("member status = %d", response.StatusCode)
	}
	if usageFake.reserveCalls != 1 || searchFake.searchCalls != 1 ||
		usageFake.finalizeCalls != 1 {
		t.Fatalf(
			"member reserve=%d provider=%d finalize=%d",
			usageFake.reserveCalls,
			searchFake.searchCalls,
			usageFake.finalizeCalls,
		)
	}
}

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/auth"
	"github.com/PeterGuy326/mem/server/internal/contextpack"
	"github.com/PeterGuy326/mem/server/internal/entitlement"
	"github.com/PeterGuy326/mem/server/internal/search"
	"github.com/PeterGuy326/mem/server/internal/workspace"
)

type managedSearchFake struct {
	mu             sync.Mutex
	spec           string
	hits           []search.Hit
	searchErr      error
	replayErr      error
	searchCalls    int
	replayCalls    int
	embeddingCalls int
	lastQuery      search.Query
	events         *[]string
}

func (f *managedSearchFake) Search(
	_ context.Context,
	query search.Query,
) ([]search.Hit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searchCalls++
	f.lastQuery = query
	if f.events != nil {
		*f.events = append(*f.events, "provider")
	}
	return f.hits, f.searchErr
}

func (f *managedSearchFake) EmbeddingSpec(
	context.Context,
	uuid.UUID,
) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.embeddingCalls++
	return f.spec, nil
}

func (f *managedSearchFake) Replay(
	_ context.Context,
	query search.Query,
	_ []entitlement.ReplayReference,
) ([]search.Hit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replayCalls++
	f.lastQuery = query
	if f.events != nil {
		*f.events = append(*f.events, "replay")
	}
	return f.hits, f.replayErr
}

type managedEntitlementFake struct {
	reservation        *entitlement.Reservation
	reserveErr         error
	finalSummary       entitlement.Summary
	finalizeErr        error
	indeterminateErr   error
	reserveCalls       int
	finalizeCalls      int
	releaseCalls       int
	indeterminateCalls int
	events             *[]string
}

func (f *managedEntitlementFake) Ready(context.Context) error { return nil }
func (f *managedEntitlementFake) Summary(
	context.Context,
	uuid.UUID,
) (entitlement.Summary, error) {
	return f.finalSummary, nil
}
func (f *managedEntitlementFake) Reserve(
	context.Context,
	entitlement.ReserveCommand,
) (*entitlement.Reservation, error) {
	f.reserveCalls++
	if f.events != nil {
		*f.events = append(*f.events, "reserve")
	}
	return f.reservation, f.reserveErr
}
func (f *managedEntitlementFake) Finalize(
	_ context.Context,
	_ uuid.UUID,
	_ []entitlement.ReplayReference,
) (entitlement.Summary, error) {
	f.finalizeCalls++
	if f.events != nil {
		*f.events = append(*f.events, "finalize")
	}
	return f.finalSummary, f.finalizeErr
}
func (f *managedEntitlementFake) Release(
	context.Context,
	uuid.UUID,
) (entitlement.Summary, error) {
	f.releaseCalls++
	return f.finalSummary, nil
}
func (f *managedEntitlementFake) MarkIndeterminate(
	context.Context,
	uuid.UUID,
) (entitlement.Summary, error) {
	f.indeterminateCalls++
	if f.events != nil {
		*f.events = append(*f.events, "indeterminate")
	}
	return f.finalSummary, f.indeterminateErr
}

func TestManagedSearchReservesBeforeProviderAndFinalizesOnce(t *testing.T) {
	events := []string{}
	fileID := uuid.New()
	evidenceID := uuid.New()
	summary := entitlement.Summary{
		WorkspaceID: uuid.New(),
		Plan:        "member",
		Status:      "active",
		Qualifying:  true,
		UnitLimit:   10,
		Remaining:   9,
		ResetAt:     time.Now().UTC().Add(time.Hour),
	}
	searchFake := &managedSearchFake{
		spec: "openai:text-embedding-3-small",
		hits: []search.Hit{{
			EvidenceID: evidenceID.String(),
			FileID:     fileID,
			Source:     search.RouteText,
			Score:      0.9,
		}},
		events: &events,
	}
	usageFake := &managedEntitlementFake{
		reservation: &entitlement.Reservation{
			ID:      uuid.New(),
			Status:  entitlement.StatusReserved,
			Summary: summary,
		},
		finalSummary: summary,
		events:       &events,
	}
	executor := &managedSearchExecutor{
		base:         searchFake,
		entitlements: usageFake,
		providerSpec: searchFake.spec,
		command: entitlement.ReserveCommand{
			WorkspaceID:        summary.WorkspaceID,
			Operation:          "search.query",
			ProviderSpec:       searchFake.spec,
			Units:              1,
			IdempotencyKey:     "search-1",
			RequestFingerprint: strings.Repeat("a", 64),
		},
	}

	hits, err := executor.Search(context.Background(), search.Query{
		UserID: uuid.New(),
		Route:  search.RouteText,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || searchFake.searchCalls != 1 ||
		usageFake.reserveCalls != 1 || usageFake.finalizeCalls != 1 ||
		usageFake.indeterminateCalls != 0 {
		t.Fatalf(
			"hits=%d search=%d reserve=%d finalize=%d indeterminate=%d",
			len(hits),
			searchFake.searchCalls,
			usageFake.reserveCalls,
			usageFake.finalizeCalls,
			usageFake.indeterminateCalls,
		)
	}
	if got := strings.Join(events, ","); got != "reserve,provider,finalize" {
		t.Fatalf("call order = %q", got)
	}
	if searchFake.lastQuery.EmbeddingProvider != searchFake.spec {
		t.Fatalf("provider was not pinned: %+v", searchFake.lastQuery)
	}
}

func TestManagedSearchReplayNeverCallsProvider(t *testing.T) {
	fileID := uuid.New()
	evidenceID := uuid.New()
	searchFake := &managedSearchFake{
		hits: []search.Hit{{
			EvidenceID: evidenceID.String(),
			FileID:     fileID,
			Source:     search.RouteText,
		}},
	}
	usageFake := &managedEntitlementFake{
		reservation: &entitlement.Reservation{
			ID:       uuid.New(),
			Status:   entitlement.StatusSucceeded,
			Replayed: true,
			References: []entitlement.ReplayReference{{
				Source:     search.RouteText,
				EvidenceID: evidenceID,
				FileID:     fileID,
			}},
		},
	}
	executor := &managedSearchExecutor{
		base:         searchFake,
		entitlements: usageFake,
		providerSpec: "openai:text-embedding-3-small",
		command: entitlement.ReserveCommand{
			WorkspaceID:        uuid.New(),
			Operation:          "search.query",
			ProviderSpec:       "openai:text-embedding-3-small",
			Units:              1,
			IdempotencyKey:     "replay",
			RequestFingerprint: strings.Repeat("a", 64),
		},
	}
	if _, err := executor.Search(context.Background(), search.Query{UserID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	if searchFake.searchCalls != 0 || searchFake.replayCalls != 1 ||
		usageFake.finalizeCalls != 0 {
		t.Fatalf(
			"provider=%d replay=%d finalize=%d",
			searchFake.searchCalls,
			searchFake.replayCalls,
			usageFake.finalizeCalls,
		)
	}

	searchFake.replayErr = search.ErrReplayReferenceUnavailable
	_, err := executor.Search(context.Background(), search.Query{UserID: uuid.New()})
	if !errors.Is(err, search.ErrReplayReferenceUnavailable) {
		t.Fatalf("missing replay error = %v", err)
	}
	if searchFake.searchCalls != 0 {
		t.Fatalf("missing replay invoked provider %d times", searchFake.searchCalls)
	}
	rec := httptest.NewRecorder()
	writeManagedEmbeddingError(rec, err)
	if rec.Code != http.StatusGone ||
		!strings.Contains(rec.Body.String(), "idempotency_result_unavailable") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestManagedContextPlanFailurePreservesLexicalMemory(t *testing.T) {
	searchFake := &managedSearchFake{spec: "openai:text-embedding-3-small"}
	usageFake := &managedEntitlementFake{
		reserveErr: &entitlement.DecisionError{
			Kind: entitlement.ErrPlanRequired,
			Summary: entitlement.Summary{
				Plan:      "free",
				Status:    "inactive",
				ResetAt:   time.Now().UTC().Add(time.Hour),
				Remaining: 0,
			},
		},
	}
	memoryFake := &contextMemoryStub{}
	server := &Server{
		Search:                   searchFake,
		Context:                  contextpack.New(nil, memoryFake),
		DeploymentMode:           "saas",
		ManagedEmbeddingProvider: searchFake.spec,
		Entitlements:             usageFake,
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/context",
		strings.NewReader(`{"query":"database decision","source":"all"}`),
	)
	request.Header.Set("Idempotency-Key", "context-plan")
	user := &auth.User{ID: uuid.New()}
	token := &auth.Token{ID: uuid.New()}
	ws := &workspace.Workspace{ID: uuid.New(), ResourceOwnerUserID: user.ID}
	ctx := context.WithValue(request.Context(), ctxUser, user)
	ctx = context.WithValue(ctx, ctxToken, token)
	ctx = context.WithValue(ctx, ctxWorkspace, ws)
	recorder := httptest.NewRecorder()

	server.handleContext(recorder, request.WithContext(ctx))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"partial":true`) ||
		!strings.Contains(body, `"code":"managed_embedding_plan_required"`) ||
		!strings.Contains(body, `"source_kind":"memory"`) {
		t.Fatalf("partial lexical response = %s", body)
	}
	if usageFake.reserveCalls != 1 || searchFake.searchCalls != 0 {
		t.Fatalf(
			"reserve=%d provider=%d",
			usageFake.reserveCalls,
			searchFake.searchCalls,
		)
	}
}

func TestManagedSearcherClassifiesEveryConfiguredProfileGeneration(t *testing.T) {
	const (
		legacy  = "openai:text-embedding-3-large"
		current = "idealab:text-embedding-3-large"
	)
	for _, test := range []struct {
		name    string
		spec    string
		managed bool
	}{
		{name: "persisted V1", spec: legacy, managed: true},
		{name: "current V2", spec: current, managed: true},
		{name: "unconfigured provider", spec: "openai:text-embedding-3-small"},
	} {
		t.Run(test.name, func(t *testing.T) {
			searchFake := &managedSearchFake{spec: test.spec}
			server := &Server{
				Search:                    searchFake,
				DeploymentMode:            "saas",
				ManagedEmbeddingProvider:  current,
				ManagedEmbeddingProviders: []string{current, legacy},
				Entitlements:              &managedEntitlementFake{},
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/search", nil)
			request.Header.Set("Idempotency-Key", "profile-generation")
			token := &auth.Token{ID: uuid.New()}
			ws := &workspace.Workspace{ID: uuid.New()}
			ctx := context.WithValue(request.Context(), ctxToken, token)
			ctx = context.WithValue(ctx, ctxWorkspace, ws)

			searcher, executor, err := server.managedSearcher(
				request.WithContext(ctx),
				"search.query",
				map[string]string{"query": "migration"},
				search.Query{UserID: uuid.New(), Route: search.RouteText},
			)
			if err != nil {
				t.Fatal(err)
			}
			if test.managed {
				if executor == nil || searcher != executor ||
					executor.providerSpec != test.spec {
					t.Fatalf(
						"managed route = searcher:%T executor:%+v",
						searcher,
						executor,
					)
				}
				return
			}
			if executor != nil || searcher != searchFake {
				t.Fatalf(
					"unmanaged route = searcher:%T executor:%+v",
					searcher,
					executor,
				)
			}
		})
	}
}

func TestManagedProviderTimeoutIsRedactedAndIndeterminate(t *testing.T) {
	searchFake := &managedSearchFake{searchErr: context.DeadlineExceeded}
	usageFake := &managedEntitlementFake{
		reservation: &entitlement.Reservation{ID: uuid.New()},
		finalSummary: entitlement.Summary{
			ResetAt: time.Now().UTC().Add(time.Hour),
		},
	}
	executor := &managedSearchExecutor{
		base:         searchFake,
		entitlements: usageFake,
		providerSpec: "openai:text-embedding-3-small",
		command: entitlement.ReserveCommand{
			WorkspaceID:        uuid.New(),
			Operation:          "search.query",
			ProviderSpec:       "openai:text-embedding-3-small",
			Units:              1,
			IdempotencyKey:     "timeout",
			RequestFingerprint: strings.Repeat("a", 64),
		},
	}
	_, err := executor.Search(context.Background(), search.Query{UserID: uuid.New()})
	if !errors.Is(err, errManagedProviderTimeout) ||
		usageFake.indeterminateCalls != 1 {
		t.Fatalf("timeout error=%v indeterminate=%d", err, usageFake.indeterminateCalls)
	}
	recorder := httptest.NewRecorder()
	writeManagedEmbeddingError(recorder, err)
	if recorder.Code != http.StatusGatewayTimeout ||
		strings.Contains(recorder.Body.String(), "DeadlineExceeded") ||
		strings.Contains(recorder.Body.String(), "openai") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestManagedSearchFinalizeFailureIsImmediatelyIndeterminate(t *testing.T) {
	searchFake := &managedSearchFake{hits: []search.Hit{{
		EvidenceID: uuid.New().String(),
		FileID:     uuid.New(),
		Source:     search.RouteText,
	}}}
	usageFake := &managedEntitlementFake{
		reservation: &entitlement.Reservation{ID: uuid.New(), Status: entitlement.StatusReserved},
		finalizeErr: errors.New("finalize store unavailable"),
		finalSummary: entitlement.Summary{
			ResetAt: time.Now().UTC().Add(time.Hour),
		},
	}
	executor := &managedSearchExecutor{
		base:         searchFake,
		entitlements: usageFake,
		providerSpec: "openai:text-embedding-3-large",
		command: entitlement.ReserveCommand{
			WorkspaceID:        uuid.New(),
			Operation:          "search.query",
			ProviderSpec:       "openai:text-embedding-3-large",
			Units:              1,
			IdempotencyKey:     "finalize-failure",
			RequestFingerprint: strings.Repeat("a", 64),
		},
	}

	_, err := executor.Search(context.Background(), search.Query{UserID: uuid.New()})
	if !errors.Is(err, errManagedUsageCommit) {
		t.Fatalf("Search() error = %v, want usage commit error", err)
	}
	if usageFake.reserveCalls != 1 || usageFake.finalizeCalls != 1 || usageFake.indeterminateCalls != 1 {
		t.Fatalf(
			"reserve/finalize/indeterminate = %d/%d/%d, want 1/1/1",
			usageFake.reserveCalls,
			usageFake.finalizeCalls,
			usageFake.indeterminateCalls,
		)
	}
}

func TestManagedErrorStatusDecisionTable(t *testing.T) {
	reset := time.Now().UTC().Add(time.Hour)
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{
			"plan",
			&entitlement.DecisionError{
				Kind:    entitlement.ErrPlanRequired,
				Summary: entitlement.Summary{ResetAt: reset},
			},
			http.StatusPaymentRequired,
			"plan_required",
		},
		{
			"quota",
			&entitlement.DecisionError{
				Kind:    entitlement.ErrQuotaExhausted,
				Summary: entitlement.Summary{ResetAt: reset},
			},
			http.StatusTooManyRequests,
			"quota_exhausted",
		},
		{
			"conflict",
			entitlement.ErrIdempotencyConflict,
			http.StatusConflict,
			"idempotency_conflict",
		},
		{
			"provider",
			errManagedProviderUnavailable,
			http.StatusBadGateway,
			"managed_embedding_unavailable",
		},
		{
			"timeout",
			errManagedProviderTimeout,
			http.StatusGatewayTimeout,
			"managed_embedding_timeout",
		},
		{
			"store",
			entitlement.ErrEntitlementUnavailable,
			http.StatusServiceUnavailable,
			"entitlement_unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeManagedEmbeddingError(recorder, test.err)
			if recorder.Code != test.status ||
				!strings.Contains(recorder.Body.String(), test.code) {
				t.Fatalf(
					"status=%d body=%s, want %d/%s",
					recorder.Code,
					recorder.Body.String(),
					test.status,
					test.code,
				)
			}
			if test.status == http.StatusTooManyRequests &&
				recorder.Header().Get("Retry-After") == "" {
				t.Fatal("quota response lacks Retry-After")
			}
		})
	}
}

func TestReadinessIsDeploymentModeAwareAndPlanIndependent(t *testing.T) {
	t.Run("private bypasses commercial store", func(t *testing.T) {
		server := &Server{DeploymentMode: "private"}
		recorder := httptest.NewRecorder()
		server.handleReadiness(
			recorder,
			httptest.NewRequest(http.MethodGet, "/readyz", nil),
		)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("saas fails closed without config or store", func(t *testing.T) {
		server := &Server{DeploymentMode: "saas"}
		recorder := httptest.NewRecorder()
		server.handleReadiness(
			recorder,
			httptest.NewRequest(http.MethodGet, "/readyz", nil),
		)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("saas readiness does not require a paid workspace", func(t *testing.T) {
		server := &Server{
			DeploymentMode:           "saas",
			ManagedEmbeddingProvider: "openai:text-embedding-3-small",
			Entitlements:             &managedEntitlementFake{},
		}
		recorder := httptest.NewRecorder()
		server.handleReadiness(
			recorder,
			httptest.NewRequest(http.MethodGet, "/readyz", nil),
		)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})
}

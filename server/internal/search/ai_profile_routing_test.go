package search

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
)

func TestSearchRouteFromAIProfileBuildsEmbeddingOnlyProfileContract(t *testing.T) {
	selection := searchTestProfileSelection(t, aiprofile.IdealabQualityV2)
	if !selection.LLM.Enabled || selection.VLM.Enabled || selection.Rerank.Enabled {
		t.Fatalf("quality selection precondition = %#v", selection)
	}

	route, err := searchRouteFromAIProfile(selection)
	if err != nil {
		t.Fatalf("searchRouteFromAIProfile() error = %v", err)
	}
	if route.textSpec != "idealab:text-embedding-3-large" || route.visualSpec != "" {
		t.Fatalf("route specs = %#v", route)
	}
	if route.dataEgress != aiprofile.DataEgressManagedIdealab {
		t.Fatalf("route data egress = %q", route.dataEgress)
	}
	if route.profile == nil {
		t.Fatal("profile route omitted AIProfileOptions")
	}
	profile := route.profile
	if profile.Contract != aiProfileContract ||
		profile.ID != selection.ProfileID ||
		profile.Revision != selection.ProfileRevision ||
		profile.PipelineRevision != selection.PipelineRevision {
		t.Fatalf("profile identity = %#v", profile)
	}
	if profile.Embedding != (workerclient.ProviderStage{
		Enabled: true, Provider: "idealab:text-embedding-3-large", Dimensions: 768,
	}) || profile.VisualEmbedding != (workerclient.ProviderStage{
		Enabled: false,
	}) {
		t.Fatalf("profile embedding stages = %#v", profile)
	}
	for name, stage := range map[string]workerclient.ProviderStage{
		"llm":    profile.LLM,
		"vlm":    profile.VLM,
		"asr":    profile.ASR,
		"rerank": profile.Rerank,
	} {
		if stage != (workerclient.ProviderStage{Enabled: false}) {
			t.Fatalf("query profile %s stage = %#v, want explicit disabled stage", name, stage)
		}
	}
}

func TestManagedProfileTextEmbeddingRequiresExactReservationCapability(t *testing.T) {
	quality := searchTestProfileSelection(t, aiprofile.IdealabQualityV2)
	route, err := searchRouteFromAIProfile(quality)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{requireManagedProfileReservation: true}

	if err := service.requireManagedReservation(context.Background(), route); !errors.Is(err, ErrManagedProfileReservationRequired) {
		t.Fatalf("unreserved managed profile error = %v", err)
	}
	wrong := WithManagedEmbeddingReservation(context.Background(), "openai:text-embedding-3-small")
	if err := service.requireManagedReservation(wrong, route); !errors.Is(err, ErrManagedProfileReservationRequired) {
		t.Fatalf("wrong-provider reservation error = %v", err)
	}
	reserved := WithManagedEmbeddingReservation(context.Background(), route.textSpec)
	if err := service.requireManagedReservation(reserved, route); err != nil {
		t.Fatalf("reserved managed profile error = %v", err)
	}

	local := searchTestProfileSelection(t, aiprofile.LocalFastV1)
	localRoute, err := searchRouteFromAIProfile(local)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.requireManagedReservation(context.Background(), localRoute); err != nil {
		t.Fatalf("local profile unexpectedly required reservation: %v", err)
	}
}

type managedV1ProfileWorker struct {
	calls   int
	profile workerclient.AIProfileOptions
	err     error
}

func (*managedV1ProfileWorker) Enabled() bool {
	return true
}

func (*managedV1ProfileWorker) EmbedTextWith(
	context.Context,
	string,
	string,
) ([]float32, error) {
	return nil, errors.New("legacy provider route unexpectedly bypassed AI profile")
}

func (w *managedV1ProfileWorker) EmbedTextWithProfile(
	_ context.Context,
	_ string,
	profile workerclient.AIProfileOptions,
) ([]float32, error) {
	w.calls++
	w.profile = profile
	return nil, w.err
}

func TestResolvedManagedV1SelectionRequiresEntitlementAndRoutesExactSnapshot(t *testing.T) {
	selection := searchTestProfileSelection(t, aiprofile.IdealabQualityV1)
	route, err := searchRouteFromAIProfile(selection)
	if err != nil {
		t.Fatalf("searchRouteFromAIProfile(V1) error = %v", err)
	}
	const v1Provider = "openai:text-embedding-3-large"
	if route.textSpec != v1Provider ||
		route.dataEgress != aiprofile.DataEgressManagedIdealab ||
		route.profile == nil {
		t.Fatalf("managed V1 route = %#v", route)
	}

	invoked := errors.New("profile-capable Worker boundary reached")
	worker := &managedV1ProfileWorker{err: invoked}
	service := &Service{
		worker:                           worker,
		requireManagedProfileReservation: true,
	}
	query := Query{
		UserID:            uuid.New(),
		EmbeddingProvider: v1Provider,
		Route:             RouteText,
	}

	if _, err := service.searchTextWithRoute(
		context.Background(),
		query,
		"legacy workspace query",
		route,
	); !errors.Is(err, ErrManagedProfileReservationRequired) {
		t.Fatalf("unreserved managed V1 route error = %v", err)
	}
	if worker.calls != 0 {
		t.Fatalf("unreserved managed V1 route invoked Worker %d times", worker.calls)
	}

	ctx := WithManagedEmbeddingReservation(context.Background(), v1Provider)
	if _, err := service.searchTextWithRoute(
		ctx,
		query,
		"legacy workspace query",
		route,
	); !errors.Is(err, invoked) {
		t.Fatalf("reserved managed V1 route error = %v, want Worker sentinel", err)
	}
	if worker.calls != 1 {
		t.Fatalf("reserved managed V1 Worker calls = %d, want 1", worker.calls)
	}
	wantProfile := workerclient.AIProfileOptions{
		Contract:         aiProfileContract,
		ID:               aiprofile.IdealabQualityV1,
		Revision:         "2026-07-29",
		PipelineRevision: "file-enrichment-v1",
		DataEgress:       aiprofile.DataEgressManagedIdealab,
		Embedding: workerclient.ProviderStage{
			Enabled: true, Provider: v1Provider, Dimensions: 768,
		},
		VisualEmbedding: workerclient.ProviderStage{
			Enabled: true, Provider: "clip:ViT-B-32", Dimensions: 512,
		},
		LLM:    workerclient.ProviderStage{Enabled: false},
		VLM:    workerclient.ProviderStage{Enabled: false},
		ASR:    workerclient.ProviderStage{Enabled: false},
		Rerank: workerclient.ProviderStage{Enabled: false},
	}
	if worker.profile != wantProfile {
		t.Fatalf("managed V1 query profile = %#v, want %#v", worker.profile, wantProfile)
	}
}

func TestDisabledProfileVisualStageNeverFallsBackToCLIP(t *testing.T) {
	quality := searchTestProfileSelection(t, aiprofile.IdealabQualityV2)
	route, err := searchRouteFromAIProfile(quality)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := visualProviderForRoute(route); err == nil {
		t.Fatal("disabled profile visual stage fell back to CLIP")
	}
	if got, err := visualProviderForRoute(embeddingRoute{}); err != nil ||
		got != "clip:ViT-B-32" {
		t.Fatalf("legacy visual provider = %q, %v", got, err)
	}
}

func TestAutoWithDisabledProfileVisualStageReturnsEmptyTextResult(t *testing.T) {
	quality := searchTestProfileSelection(t, aiprofile.IdealabQualityV2)
	route, err := searchRouteFromAIProfile(quality)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{}
	visualCalled := false

	hits, err := service.runAutoSearch(
		Query{Limit: 10},
		route,
		func() ([]Hit, error) { return nil, nil },
		func() ([]Hit, error) {
			visualCalled = true
			return nil, errors.New("disabled visual stage was invoked")
		},
	)
	if err != nil {
		t.Fatalf("text-only auto search error = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("text-only auto search hits = %#v, want empty", hits)
	}
	if visualCalled {
		t.Fatal("text-only auto search invoked disabled visual stage")
	}
}

func searchTestProfileSelection(t *testing.T, profileID string) *aiprofile.Selection {
	t.Helper()
	definition, ok := aiprofile.Find(profileID)
	if !ok {
		t.Fatalf("profile %q missing from catalog", profileID)
	}
	return &aiprofile.Selection{
		DataEgress:       definition.DataEgress,
		ProfileID:        definition.ID,
		ProfileRevision:  definition.Revision,
		PipelineRevision: definition.PipelineRevision,
		Embedding:        definition.Embedding,
		VisualEmbedding:  definition.VisualEmbedding,
		LLM:              definition.LLM,
		VLM:              definition.VLM,
		ASR:              definition.ASR,
		Rerank:           definition.Rerank,
		AllowedMIMETypes: append([]string(nil), definition.AllowedMIMETypes...),
	}
}

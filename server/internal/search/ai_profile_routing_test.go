package search

import (
	"context"
	"errors"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
)

func TestSearchRouteFromAIProfileBuildsEmbeddingOnlyProfileContract(t *testing.T) {
	selection := searchTestProfileSelection(t, aiprofile.IdealabQualityV1)
	if !selection.LLM.Enabled || selection.VLM.Enabled || selection.Rerank.Enabled {
		t.Fatalf("quality selection precondition = %#v", selection)
	}

	route, err := searchRouteFromAIProfile(selection)
	if err != nil {
		t.Fatalf("searchRouteFromAIProfile() error = %v", err)
	}
	if route.textSpec != "openai:text-embedding-3-large" || route.visualSpec != "clip:ViT-B-32" {
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
		Enabled: true, Provider: "openai:text-embedding-3-large", Dimensions: 768,
	}) || profile.VisualEmbedding != (workerclient.ProviderStage{
		Enabled: true, Provider: "clip:ViT-B-32", Dimensions: 512,
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
	quality := searchTestProfileSelection(t, aiprofile.IdealabQualityV1)
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

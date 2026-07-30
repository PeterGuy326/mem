package indexer

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/entitlement"
	"github.com/PeterGuy326/mem/server/internal/file"
	"github.com/PeterGuy326/mem/server/internal/managedusage"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
)

func TestRouteFromAIProfileMapsAllStagesAndMIMEBoundary(t *testing.T) {
	selection := indexerTestProfileSelection(t, aiprofile.IdealabQualityV1)
	route, err := routeFromAIProfile(selection)
	if err != nil {
		t.Fatalf("routeFromAIProfile() error = %v", err)
	}
	if route.EmbeddingProvider != "openai:text-embedding-3-large" ||
		route.VisualEmbeddingProvider != "clip:ViT-B-32" ||
		route.LLMProvider != "openai:qwen3.7-max-2026-06-08" ||
		route.VLMProvider != "" ||
		route.ASRProvider != "" {
		t.Fatalf("legacy-compatible route fields = %#v", route)
	}
	if route.AIProfile == nil {
		t.Fatal("profile route omitted AIProfileOptions")
	}
	profile := route.AIProfile
	if profile.Contract != aiProfileContract ||
		profile.ID != selection.ProfileID ||
		profile.Revision != selection.ProfileRevision ||
		profile.PipelineRevision != selection.PipelineRevision {
		t.Fatalf("profile identity = %#v", profile)
	}
	for name, pair := range map[string]struct {
		got  workerclient.ProviderStage
		want workerclient.ProviderStage
	}{
		"embedding": {
			got: profile.Embedding,
			want: workerclient.ProviderStage{
				Enabled: true, Provider: "openai:text-embedding-3-large", Dimensions: 768,
			},
		},
		"visual_embedding": {
			got: profile.VisualEmbedding,
			want: workerclient.ProviderStage{
				Enabled: true, Provider: "clip:ViT-B-32", Dimensions: 512,
			},
		},
		"llm": {
			got: profile.LLM,
			want: workerclient.ProviderStage{
				Enabled: true, Provider: "openai:qwen3.7-max-2026-06-08",
			},
		},
		"vlm": {
			got:  profile.VLM,
			want: workerclient.ProviderStage{Enabled: false},
		},
		"asr": {
			got:  profile.ASR,
			want: workerclient.ProviderStage{Enabled: false},
		},
		"rerank": {
			got:  profile.Rerank,
			want: workerclient.ProviderStage{Enabled: false},
		},
	} {
		if pair.got != pair.want {
			t.Fatalf("profile %s = %#v, want %#v", name, pair.got, pair.want)
		}
	}

	for _, test := range []struct {
		mime string
		want bool
	}{
		{"text/plain", true},
		{"application/pdf; charset=utf-8", true},
		{"application/json", true},
		{"audio/wav", true},
		{"image/png", true},
		{"video/mp4", false},
		{"application/octet-stream", false},
		{"not-a-mime", false},
	} {
		if got := route.allowsMIME(test.mime); got != test.want {
			t.Errorf("allowsMIME(%q) = %t, want %t", test.mime, got, test.want)
		}
	}
}

func TestManagedStagesForMIMEOnlyIncludesReachableProfileStages(t *testing.T) {
	selection := indexerTestProfileSelection(t, aiprofile.IdealabQualityV1)
	selection.WorkspaceID = uuid.New()
	route, err := routeFromAIProfile(selection)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		mime string
		want []managedusage.Stage
	}{
		{"text/plain", []managedusage.Stage{managedusage.StageEmbedding, managedusage.StageLLM}},
		{"application/pdf", []managedusage.Stage{managedusage.StageEmbedding, managedusage.StageLLM}},
		{"application/json", []managedusage.Stage{managedusage.StageEmbedding, managedusage.StageLLM}},
		// The quality profile keeps image bytes local until a reviewed Idealab
		// VLM contract is available; CLIP itself is not a managed stage.
		{"image/png", []managedusage.Stage{managedusage.StageVisualEmbedding}},
		// Quality ASR is explicitly disabled, so AudioProcessor exits before
		// any downstream managed text stage can run.
		{"audio/mpeg", nil},
		{"video/mp4", nil},
	}
	for _, test := range tests {
		t.Run(test.mime, func(t *testing.T) {
			gotSpecs := managedStagesForMIME(route, test.mime)
			var got []managedusage.Stage
			for _, spec := range gotSpecs {
				got = append(got, spec.Stage)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("managed stages = %#v, want %#v", gotSpecs, test.want)
			}
			for _, spec := range gotSpecs {
				if spec.Stage == managedusage.StageRerank {
					t.Fatal("unimplemented rerank stage reached an accounting command")
				}
			}
		})
	}

	local := indexerTestProfileSelection(t, aiprofile.LocalFastV1)
	localRoute, err := routeFromAIProfile(local)
	if err != nil {
		t.Fatal(err)
	}
	if got := managedStagesForMIME(localRoute, "text/plain"); len(got) != 0 {
		t.Fatalf("local profile produced managed stages: %#v", got)
	}
}

type captureManagedUsage struct {
	command managedusage.Command
	calls   int
}

func (c *captureManagedUsage) Prepare(
	_ context.Context,
	command managedusage.Command,
) (*managedusage.Handle, error) {
	c.calls++
	c.command = command
	return &managedusage.Handle{}, nil
}

func TestPrepareManagedUsageUsesServerResolvedFileIdentity(t *testing.T) {
	selection := indexerTestProfileSelection(t, aiprofile.IdealabQualityV1)
	selection.WorkspaceID = uuid.New()
	route, err := routeFromAIProfile(selection)
	if err != nil {
		t.Fatal(err)
	}
	capture := &captureManagedUsage{}
	service := &Service{managedUsage: capture}
	input := &file.File{
		ID:     uuid.New(),
		SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MIME:   "text/plain",
	}
	if _, err := service.prepareManagedUsage(context.Background(), input, route); err != nil {
		t.Fatalf("prepareManagedUsage() error = %v", err)
	}
	if capture.calls != 1 || capture.command.WorkspaceID != selection.WorkspaceID ||
		capture.command.FileID != input.ID || capture.command.ContentSHA256 != input.SHA256 ||
		capture.command.ProfileID != selection.ProfileID ||
		capture.command.ProfileRevision != selection.ProfileRevision {
		t.Fatalf("managed usage command = %#v", capture.command)
	}
	if got := []managedusage.Stage{capture.command.Stages[0].Stage, capture.command.Stages[1].Stage}; !reflect.DeepEqual(got, []managedusage.Stage{managedusage.StageEmbedding, managedusage.StageLLM}) {
		t.Fatalf("managed command stages = %#v", capture.command.Stages)
	}
}

type replayLedger struct {
	reserves int
	released []uuid.UUID
}

func (l *replayLedger) Reserve(
	_ context.Context,
	_ entitlement.ReserveCommand,
) (*entitlement.Reservation, error) {
	l.reserves++
	if l.reserves == 1 {
		return &entitlement.Reservation{
			ID:       uuid.New(),
			Status:   entitlement.StatusSucceeded,
			Replayed: true,
		}, nil
	}
	return &entitlement.Reservation{ID: uuid.New(), Status: entitlement.StatusReserved}, nil
}

func (l *replayLedger) Finalize(
	context.Context,
	uuid.UUID,
	[]entitlement.ReplayReference,
) (entitlement.Summary, error) {
	return entitlement.Summary{}, nil
}

func (l *replayLedger) Release(
	_ context.Context,
	usageID uuid.UUID,
) (entitlement.Summary, error) {
	l.released = append(l.released, usageID)
	return entitlement.Summary{}, nil
}

func (l *replayLedger) MarkIndeterminate(
	context.Context,
	uuid.UUID,
) (entitlement.Summary, error) {
	return entitlement.Summary{}, nil
}

func TestReleaseManagedReplaySiblingsReleasesOnlyUninvokedStages(t *testing.T) {
	definition, ok := aiprofile.Find(aiprofile.IdealabQualityV1)
	if !ok {
		t.Fatal("quality profile missing")
	}
	ledger := &replayLedger{}
	handle, err := managedusage.New(ledger).Prepare(context.Background(), managedusage.Command{
		WorkspaceID:     uuid.New(),
		FileID:          uuid.New(),
		ContentSHA256:   strings.Repeat("a", 64),
		ProfileID:       definition.ID,
		ProfileRevision: definition.Revision,
		Stages: []managedusage.StageSpec{
			{Stage: managedusage.StageEmbedding, ProviderSpec: definition.Embedding.Provider},
			{Stage: managedusage.StageLLM, ProviderSpec: definition.LLM.Provider},
		},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if !handle.HasReplay() {
		t.Fatal("test precondition: expected replayed embedding reservation")
	}
	if err := releaseManagedReplaySiblings(context.Background(), handle); err != nil {
		t.Fatalf("releaseManagedReplaySiblings() error = %v", err)
	}
	if len(ledger.released) != 1 {
		t.Fatalf("released reservations = %d, want the one new sibling", len(ledger.released))
	}
}

func TestRouteFromAIProfileRejectsWrongEmbeddingDimensions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*aiprofile.Selection)
	}{
		{
			name: "text embedding",
			mutate: func(selection *aiprofile.Selection) {
				selection.Embedding.Dimensions = 767
			},
		},
		{
			name: "visual embedding",
			mutate: func(selection *aiprofile.Selection) {
				selection.VisualEmbedding.Dimensions = 511
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selection := indexerTestProfileSelection(t, aiprofile.IdealabQualityV1)
			test.mutate(selection)
			if _, err := routeFromAIProfile(selection); err == nil {
				t.Fatal("routeFromAIProfile accepted a wrong vector dimension")
			}
		})
	}
}

func indexerTestProfileSelection(t *testing.T, profileID string) *aiprofile.Selection {
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

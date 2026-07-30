package indexer

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/entitlement"
	"github.com/PeterGuy326/mem/server/internal/file"
	"github.com/PeterGuy326/mem/server/internal/managedusage"
	"github.com/PeterGuy326/mem/server/internal/workerclient"
	"github.com/PeterGuy326/mem/server/internal/workerpb"
)

func TestRouteFromAIProfileMapsAllStagesAndMIMEBoundary(t *testing.T) {
	selection := indexerTestProfileSelection(t, aiprofile.IdealabQualityV2)
	route, err := routeFromAIProfile(selection)
	if err != nil {
		t.Fatalf("routeFromAIProfile() error = %v", err)
	}
	if route.EmbeddingProvider != "idealab:text-embedding-3-large" ||
		route.VisualEmbeddingProvider != "" ||
		route.LLMProvider != "idealab:qwen3.7-max-2026-06-08" ||
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
				Enabled: true, Provider: "idealab:text-embedding-3-large", Dimensions: 768,
			},
		},
		"visual_embedding": {
			got: profile.VisualEmbedding,
			want: workerclient.ProviderStage{
				Enabled: false,
			},
		},
		"llm": {
			got: profile.LLM,
			want: workerclient.ProviderStage{
				Enabled: true, Provider: "idealab:qwen3.7-max-2026-06-08",
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
		{"audio/wav", false},
		{"image/png", false},
		{"video/mp4", false},
		{"application/octet-stream", false},
		{"not-a-mime", false},
	} {
		if got := route.allowsMIME(test.mime); got != test.want {
			t.Errorf("allowsMIME(%q) = %t, want %t", test.mime, got, test.want)
		}
	}
}

func TestValidateAIProfileEmbeddingResultBindsKindsProvidersAndDimensions(t *testing.T) {
	v1Route, err := routeFromAIProfile(
		indexerTestProfileSelection(t, aiprofile.IdealabQualityV1),
	)
	if err != nil {
		t.Fatal(err)
	}
	v2Route, err := routeFromAIProfile(
		indexerTestProfileSelection(t, aiprofile.IdealabQualityV2),
	)
	if err != nil {
		t.Fatal(err)
	}
	embedding := func(provider string, dimensions, rowDimensions int) *workerpb.Embedding {
		return &workerpb.Embedding{
			Provider: provider,
			Dim:      int32(dimensions),
			Rows: []*workerpb.EmbeddingRow{{
				Values: make([]float32, rowDimensions),
			}},
		}
	}

	tests := []struct {
		name    string
		route   providerRoute
		result  map[string]*workerpb.Embedding
		wantErr bool
	}{
		{
			name:  "declared V1 text and visual",
			route: v1Route,
			result: map[string]*workerpb.Embedding{
				"text": embedding(v1Route.AIProfile.Embedding.Provider, 768, 768),
				"visual": embedding(
					v1Route.AIProfile.VisualEmbedding.Provider,
					512,
					512,
				),
			},
		},
		{
			name:  "wrong visual provider",
			route: v1Route,
			result: map[string]*workerpb.Embedding{
				"visual": embedding("clip:wrong-model", 512, 512),
			},
			wantErr: true,
		},
		{
			name:  "wrong visual set dimensions",
			route: v1Route,
			result: map[string]*workerpb.Embedding{
				"visual": embedding(
					v1Route.AIProfile.VisualEmbedding.Provider,
					511,
					512,
				),
			},
			wantErr: true,
		},
		{
			name:  "wrong visual row dimensions",
			route: v1Route,
			result: map[string]*workerpb.Embedding{
				"visual": embedding(
					v1Route.AIProfile.VisualEmbedding.Provider,
					512,
					511,
				),
			},
			wantErr: true,
		},
		{
			name:  "disabled V2 visual",
			route: v2Route,
			result: map[string]*workerpb.Embedding{
				"visual": embedding("clip:ViT-B-32", 512, 512),
			},
			wantErr: true,
		},
		{
			name:  "unknown embedding kind",
			route: v1Route,
			result: map[string]*workerpb.Embedding{
				"audio": embedding("test:unknown", 768, 768),
			},
			wantErr: true,
		},
		{
			name:  "published V1 fixed face contract",
			route: v1Route,
			result: map[string]*workerpb.Embedding{
				"face": embedding(legacyFaceProvider, 512, 512),
			},
		},
		{
			name:  "V1 face wrong provider",
			route: v1Route,
			result: map[string]*workerpb.Embedding{
				"face": embedding("insightface:unreviewed", 512, 512),
			},
			wantErr: true,
		},
		{
			name:  "V1 face wrong dimensions",
			route: v1Route,
			result: map[string]*workerpb.Embedding{
				"face": embedding(legacyFaceProvider, 512, 511),
			},
			wantErr: true,
		},
		{
			name:  "V2 face is undeclared",
			route: v2Route,
			result: map[string]*workerpb.Embedding{
				"face": embedding(legacyFaceProvider, 512, 512),
			},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateAIProfileEmbeddingResult(
				test.route,
				&workerpb.ProcessResponse{Embeddings: test.result},
			)
			if (err != nil) != test.wantErr {
				t.Fatalf("validation error = %v, wantErr %t", err, test.wantErr)
			}
			if test.wantErr && !errors.Is(err, errAIProfileResultContract) {
				t.Fatalf("validation error = %v, want deterministic contract rejection", err)
			}
		})
	}
}

func TestValidateLegacyEmbeddingResultAtFinalLockBoundary(t *testing.T) {
	const provider = "ollama:qwen3-embedding:0.6b"
	embedding := func(gotProvider string, dimensions, rowDimensions int) *workerpb.ProcessResponse {
		return &workerpb.ProcessResponse{
			Embeddings: map[string]*workerpb.Embedding{
				"text": {
					Provider: gotProvider,
					Dim:      int32(dimensions),
					Rows: []*workerpb.EmbeddingRow{{
						Values: make([]float32, rowDimensions),
					}},
				},
			},
		}
	}
	tests := []struct {
		name     string
		response *workerpb.ProcessResponse
		wantErr  bool
	}{
		{
			name:     "exact dispatch provider",
			response: embedding(provider, 768, 768),
		},
		{
			name:     "empty provider",
			response: embedding("", 768, 768),
			wantErr:  true,
		},
		{
			name:     "different provider",
			response: embedding("ollama:different", 768, 768),
			wantErr:  true,
		},
		{
			name:     "wrong set dimensions",
			response: embedding(provider, 767, 768),
			wantErr:  true,
		},
		{
			name:     "wrong row dimensions",
			response: embedding(provider, 768, 767),
			wantErr:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateLegacyEmbeddingResult(
				providerRoute{EmbeddingProvider: provider},
				test.response,
			)
			if (err != nil) != test.wantErr {
				t.Fatalf("validation error = %v, wantErr %t", err, test.wantErr)
			}
			if test.wantErr && !errors.Is(err, errAIProfileResultContract) {
				t.Fatalf("validation error = %v, want deterministic contract rejection", err)
			}
		})
	}
}

func TestManagedStagesForMIMEOnlyIncludesReachableProfileStages(t *testing.T) {
	selection := indexerTestProfileSelection(t, aiprofile.IdealabQualityV2)
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
		{"image/png", nil},
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

func (c *captureManagedUsage) SettleUsage(
	context.Context,
	uuid.UUID,
	managedusage.Outcome,
) error {
	return nil
}

func TestPrepareManagedUsageUsesServerResolvedFileIdentity(t *testing.T) {
	selection := indexerTestProfileSelection(t, aiprofile.IdealabQualityV2)
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
		capture.command.ProfileRevision != selection.ProfileRevision ||
		capture.command.PipelineRevision != selection.PipelineRevision {
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

func TestReleaseManagedReplaySkipsLaterStages(t *testing.T) {
	definition, ok := aiprofile.Find(aiprofile.IdealabQualityV2)
	if !ok {
		t.Fatal("quality profile missing")
	}
	ledger := &replayLedger{}
	handle, err := managedusage.New(ledger).Prepare(context.Background(), managedusage.Command{
		WorkspaceID:      uuid.New(),
		FileID:           uuid.New(),
		ContentSHA256:    strings.Repeat("a", 64),
		ProfileID:        definition.ID,
		ProfileRevision:  definition.Revision,
		PipelineRevision: definition.PipelineRevision,
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
	if ledger.reserves != 1 {
		t.Fatalf("reserve calls = %d, want replay to stop before later stages", ledger.reserves)
	}
	if len(ledger.released) != 0 {
		t.Fatalf("released reservations = %d, want no later sibling reservation", len(ledger.released))
	}
}

type stageSettlementLedger struct {
	operations    map[uuid.UUID]string
	finalized     []string
	released      []string
	indeterminate []string
}

func (l *stageSettlementLedger) Reserve(
	_ context.Context,
	command entitlement.ReserveCommand,
) (*entitlement.Reservation, error) {
	if l.operations == nil {
		l.operations = make(map[uuid.UUID]string)
	}
	id := uuid.New()
	l.operations[id] = command.Operation
	return &entitlement.Reservation{ID: id, Status: entitlement.StatusReserved}, nil
}

func (l *stageSettlementLedger) Finalize(
	_ context.Context,
	id uuid.UUID,
	_ []entitlement.ReplayReference,
) (entitlement.Summary, error) {
	l.finalized = append(l.finalized, l.operations[id])
	return entitlement.Summary{}, nil
}

func (l *stageSettlementLedger) Release(
	_ context.Context,
	id uuid.UUID,
) (entitlement.Summary, error) {
	l.released = append(l.released, l.operations[id])
	return entitlement.Summary{}, nil
}

func (l *stageSettlementLedger) MarkIndeterminate(
	_ context.Context,
	id uuid.UUID,
) (entitlement.Summary, error) {
	l.indeterminate = append(l.indeterminate, l.operations[id])
	return entitlement.Summary{}, nil
}

func TestManagedUsageReceiptSettlesEachStageExactly(t *testing.T) {
	definition, ok := aiprofile.Find(aiprofile.IdealabQualityV2)
	if !ok {
		t.Fatal("quality profile missing")
	}
	tests := []struct {
		name              string
		stages            string
		wantFinalized     []string
		wantReleased      []string
		wantIndeterminate []string
	}{
		{
			name:          "short text releases uncalled LLM",
			stages:        `{"embedding":"succeeded","llm":"not_invoked"}`,
			wantFinalized: []string{"file.ai.embedding"},
			wantReleased:  []string{"file.ai.llm"},
		},
		{
			name:         "empty text releases every stage",
			stages:       `{"embedding":"not_invoked","llm":"not_invoked"}`,
			wantReleased: []string{"file.ai.embedding", "file.ai.llm"},
		},
		{
			name:          "long text finalizes both calls",
			stages:        `{"embedding":"succeeded","llm":"succeeded"}`,
			wantFinalized: []string{"file.ai.embedding", "file.ai.llm"},
		},
		{
			name:              "provider uncertainty is retained per stage",
			stages:            `{"embedding":"succeeded","llm":"indeterminate"}`,
			wantFinalized:     []string{"file.ai.embedding"},
			wantIndeterminate: []string{"file.ai.llm"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := &stageSettlementLedger{}
			handle, err := managedusage.New(ledger).Prepare(
				context.Background(),
				managedusage.Command{
					WorkspaceID:      uuid.New(),
					FileID:           uuid.New(),
					ContentSHA256:    strings.Repeat("a", 64),
					ProfileID:        definition.ID,
					ProfileRevision:  definition.Revision,
					PipelineRevision: definition.PipelineRevision,
					Stages: []managedusage.StageSpec{
						{Stage: managedusage.StageEmbedding, ProviderSpec: definition.Embedding.Provider},
						{Stage: managedusage.StageLLM, ProviderSpec: definition.LLM.Provider},
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			resp := &workerpb.ProcessResponse{MetadataJson: []byte(
				`{"managed_usage":{"contract":"mem.managed-stage-receipt/v1","stages":` +
					test.stages + `}}`,
			)}
			plan, err := managedUsagePlanFromResponse(handle, resp)
			if err != nil {
				t.Fatalf("managedUsagePlanFromResponse() error = %v", err)
			}
			if err := applyManagedUsagePlan(context.Background(), handle, plan); err != nil {
				t.Fatalf("applyManagedUsagePlan() error = %v", err)
			}
			if !reflect.DeepEqual(ledger.finalized, test.wantFinalized) ||
				!reflect.DeepEqual(ledger.released, test.wantReleased) ||
				!reflect.DeepEqual(ledger.indeterminate, test.wantIndeterminate) {
				t.Fatalf(
					"settlement finalized/released/indeterminate = %#v/%#v/%#v",
					ledger.finalized,
					ledger.released,
					ledger.indeterminate,
				)
			}
		})
	}
}

func TestManagedUsageReceiptRejectsMissingExtraOrUnknownStages(t *testing.T) {
	definition, ok := aiprofile.Find(aiprofile.IdealabQualityV2)
	if !ok {
		t.Fatal("quality profile missing")
	}
	for _, stages := range []string{
		`{"embedding":"succeeded"}`,
		`{"embedding":"succeeded","llm":"not_invoked","vlm":"not_invoked"}`,
		`{"embedding":"succeeded","llm":"failed"}`,
	} {
		ledger := &stageSettlementLedger{}
		handle, err := managedusage.New(ledger).Prepare(
			context.Background(),
			managedusage.Command{
				WorkspaceID:      uuid.New(),
				FileID:           uuid.New(),
				ContentSHA256:    strings.Repeat("b", 64),
				ProfileID:        definition.ID,
				ProfileRevision:  definition.Revision,
				PipelineRevision: definition.PipelineRevision,
				Stages: []managedusage.StageSpec{
					{Stage: managedusage.StageEmbedding, ProviderSpec: definition.Embedding.Provider},
					{Stage: managedusage.StageLLM, ProviderSpec: definition.LLM.Provider},
				},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		resp := &workerpb.ProcessResponse{MetadataJson: []byte(
			`{"managed_usage":{"contract":"mem.managed-stage-receipt/v1","stages":` +
				stages + `}}`,
		)}
		if _, err := managedUsagePlanFromResponse(handle, resp); err == nil {
			t.Fatalf("receipt stages %s unexpectedly accepted", stages)
		}
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
			selection := indexerTestProfileSelection(t, aiprofile.IdealabQualityV2)
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

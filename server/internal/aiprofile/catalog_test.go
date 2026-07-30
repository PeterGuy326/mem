package aiprofile

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCatalogContainsPinnedLocalAndManagedProfiles(t *testing.T) {
	profiles := Catalog()
	if len(profiles) != 4 {
		t.Fatalf("catalog length = %d, want 4", len(profiles))
	}

	legacyLocal, ok := Find(LocalFastV1)
	if !ok {
		t.Fatalf("Find(%q) did not return the local profile", LocalFastV1)
	}
	if legacyLocal.Revision != "2026-07-29" ||
		legacyLocal.PipelineRevision != "file-enrichment-v1" ||
		legacyLocal.VisualEmbedding != (Stage{
			Enabled:    true,
			Provider:   "clip:ViT-B-32",
			Dimensions: visualEmbeddingDimension,
		}) {
		t.Fatalf("published local V1 changed = %#v", legacyLocal)
	}

	legacyQuality, ok := Find(IdealabQualityV1)
	if !ok {
		t.Fatalf("Find(%q) did not return the managed profile", IdealabQualityV1)
	}
	if legacyQuality.Revision != "2026-07-29" ||
		legacyQuality.PipelineRevision != "file-enrichment-v1" ||
		legacyQuality.Embedding.Provider != "openai:text-embedding-3-large" ||
		legacyQuality.LLM.Provider != "openai:qwen3.7-max-2026-06-08" ||
		!legacyQuality.VisualEmbedding.Enabled {
		t.Fatalf("published managed V1 changed = %#v", legacyQuality)
	}

	local, ok := Find(LocalFastV2)
	if !ok {
		t.Fatalf("Find(%q) did not return the current local profile", LocalFastV2)
	}
	if local.DataEgress != DataEgressLocalOnly ||
		local.Revision != "2026-07-30.1" ||
		local.PipelineRevision != "file-enrichment-v2" ||
		local.Embedding != (Stage{
			Enabled:    true,
			Provider:   "ollama:qwen3-embedding:0.6b",
			Dimensions: textEmbeddingDimension,
		}) ||
		local.VisualEmbedding != (Stage{}) {
		t.Fatalf("local profile = %#v", local)
	}
	if local.LLM.Enabled || local.VLM.Enabled || local.ASR.Enabled || local.Rerank.Enabled {
		t.Fatalf("local optional stages must be explicitly disabled: %#v", local)
	}

	quality, ok := Find(IdealabQualityV2)
	if !ok {
		t.Fatalf("Find(%q) did not return the current managed profile", IdealabQualityV2)
	}
	if quality.DataEgress != DataEgressManagedIdealab ||
		quality.Revision != "2026-07-30.1" ||
		quality.PipelineRevision != "file-enrichment-v2" ||
		quality.Embedding != (Stage{
			Enabled:    true,
			Provider:   "idealab:text-embedding-3-large",
			Dimensions: textEmbeddingDimension,
		}) ||
		quality.VisualEmbedding.Enabled ||
		!quality.LLM.Enabled || quality.LLM.Provider != "idealab:qwen3.7-max-2026-06-08" ||
		quality.VLM.Enabled || quality.VLM.Provider != "" ||
		quality.Rerank.Enabled || quality.Rerank.Provider != "" {
		t.Fatalf("managed profile = %#v", quality)
	}
	if quality.ASR.Enabled {
		t.Fatalf("managed ASR must be explicitly disabled: %#v", quality.ASR)
	}

	for _, profile := range profiles {
		if err := profile.Validate(); err != nil {
			t.Errorf("Validate(%q): %v", profile.ID, err)
		}
		for _, stage := range []Stage{
			profile.Embedding,
			profile.VisualEmbedding,
			profile.LLM,
			profile.VLM,
			profile.ASR,
			profile.Rerank,
		} {
			if stage.Enabled && containsMutableAlias(stage.Provider) {
				t.Errorf("profile %q has a mutable provider alias %q", profile.ID, stage.Provider)
			}
		}
	}
}

func TestValidateSelectionRequiresAnExactCompiledSnapshot(t *testing.T) {
	definition, ok := Find(IdealabQualityV2)
	if !ok {
		t.Fatal("quality profile missing")
	}
	selection := selectionFromDefinition(definition, uuid.New(), uuid.New(), time.Now())
	if err := ValidateSelection(&selection); err != nil {
		t.Fatalf("ValidateSelection() error = %v", err)
	}
	selection.Embedding.Provider = "idealab:unapproved-same-dimension-model"
	if err := ValidateSelection(&selection); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("ValidateSelection() error = %v, want ErrInvalidSelection", err)
	}

	staleProfile := selectionFromDefinition(definition, uuid.New(), uuid.New(), time.Now())
	staleProfile.ProfileRevision = "2026-07-29"
	if err := ValidateSelection(&staleProfile); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("stale profile revision error = %v, want ErrInvalidSelection", err)
	}

	stalePipeline := selectionFromDefinition(definition, uuid.New(), uuid.New(), time.Now())
	stalePipeline.PipelineRevision = "file-enrichment-v1"
	if err := ValidateSelection(&stalePipeline); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("stale pipeline revision error = %v, want ErrInvalidSelection", err)
	}
}

func TestCatalogAndFindReturnDefensiveCopies(t *testing.T) {
	profiles := Catalog()
	profiles[0].AllowedMIMETypes[0] = "application/not-the-catalog"

	local, ok := Find(LocalFastV1)
	if !ok {
		t.Fatal("local profile missing")
	}
	if local.AllowedMIMETypes[0] != "text/*" {
		t.Fatalf("catalog was mutated through a returned value: %#v", local.AllowedMIMETypes)
	}

	local.AllowedMIMETypes[0] = "application/also-not-the-catalog"
	second, ok := Find(LocalFastV1)
	if !ok {
		t.Fatal("local profile missing on second lookup")
	}
	if second.AllowedMIMETypes[0] != "text/*" {
		t.Fatalf("Find result was not a defensive copy: %#v", second.AllowedMIMETypes)
	}
}

func TestDefinitionValidateRejectsUnsafeOrAmbiguousConfiguration(t *testing.T) {
	base, ok := Find(LocalFastV1)
	if !ok {
		t.Fatal("local profile missing")
	}

	tests := []struct {
		name   string
		mutate func(*Definition)
	}{
		{
			name: "mutable provider alias",
			mutate: func(profile *Definition) {
				profile.Embedding.Provider = "ollama:latest"
			},
		},
		{
			name: "URL provider spec",
			mutate: func(profile *Definition) {
				profile.Embedding.Provider = "openai:https://gateway.example.invalid/v1"
			},
		},
		{
			name: "managed local stage",
			mutate: func(profile *Definition) {
				profile.Embedding.Provider = "idealab:text-embedding-3-large"
			},
		},
		{
			name: "disabled optional stage with a provider",
			mutate: func(profile *Definition) {
				profile.LLM.Provider = "ollama:qwen3"
			},
		},
		{
			name: "wrong embedding dimension",
			mutate: func(profile *Definition) {
				profile.Embedding.Dimensions = textEmbeddingDimension - 1
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := cloneDefinition(base)
			test.mutate(&profile)
			if err := profile.Validate(); !errors.Is(err, ErrInvalidDefinition) {
				t.Fatalf("Validate() error = %v, want ErrInvalidDefinition", err)
			}
		})
	}
}

func containsMutableAlias(spec string) bool {
	for i := 0; i+len("latest") <= len(spec); i++ {
		if spec[i:i+len("latest")] == "latest" {
			return true
		}
	}
	return false
}

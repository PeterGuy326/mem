package indexgeneration

import (
	"testing"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
)

func TestPersistedProfileSnapshotValidationIsCatalogRevisionIndependent(t *testing.T) {
	definition, ok := aiprofile.Find(aiprofile.LocalFastV2)
	if !ok {
		t.Fatal("compiled local profile not found")
	}
	snapshot := snapshotFromDefinition(definition)
	snapshot.Revision = "historical-revision-no-longer-in-catalog"
	snapshot.PipelineRevision = "historical-pipeline"
	if err := validateProfileSnapshot(snapshot); err != nil {
		t.Fatalf("historical immutable snapshot rejected: %v", err)
	}
}

func TestPersistedProfileSnapshotValidationRejectsUnsafeOrImplicitStages(t *testing.T) {
	definition, ok := aiprofile.Find(aiprofile.LocalFastV2)
	if !ok {
		t.Fatal("compiled local profile not found")
	}
	tests := []struct {
		name   string
		mutate func(*ProfileSnapshot)
	}{
		{
			name: "credential-shaped provider",
			mutate: func(snapshot *ProfileSnapshot) {
				snapshot.Embedding.Provider = "ollama:user@example.test/model"
			},
		},
		{
			name: "mutable alias",
			mutate: func(snapshot *ProfileSnapshot) {
				snapshot.Embedding.Provider = "ollama:model-latest"
			},
		},
		{
			name: "disabled stage carries fallback",
			mutate: func(snapshot *ProfileSnapshot) {
				snapshot.LLM.Provider = "ollama:qwen"
			},
		},
		{
			name: "local profile managed egress",
			mutate: func(snapshot *ProfileSnapshot) {
				snapshot.Embedding.Provider = "idealab:embedding-v1"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := snapshotFromDefinition(definition)
			test.mutate(&snapshot)
			if err := validateProfileSnapshot(snapshot); err == nil {
				t.Fatal("unsafe snapshot was accepted")
			}
		})
	}
}

func TestPersistedManagedSnapshotRejectsUnboundProviderWithoutCatalogLookup(t *testing.T) {
	definition, ok := aiprofile.Find(aiprofile.IdealabQualityV2)
	if !ok {
		t.Fatal("compiled managed profile not found")
	}
	snapshot := snapshotFromDefinition(definition)
	snapshot.Embedding.Provider = "external:credential-free-but-unbound-v1"
	if err := validateProfileSnapshot(snapshot); err == nil {
		t.Fatal("unbound managed provider was accepted")
	}
}

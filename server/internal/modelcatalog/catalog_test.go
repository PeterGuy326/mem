package modelcatalog

import (
	"strings"
	"testing"
)

func TestEmbeddedCatalogIsValidAndVendorNeutral(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if catalog.SchemaVersion != "mem.model-catalog/v1" ||
		catalog.CorpusDimension != CorpusDimension {
		t.Fatalf("catalog header = %#v", catalog)
	}

	want := map[string]struct {
		dimension   int
		installable bool
		sizeBytes   uint64
	}{
		"nomic-embed-text-v1.5-ollama": {
			dimension: 768, installable: true, sizeBytes: 274302030,
		},
		"qwen3-embedding-0.6b-ollama": {
			dimension: 768, installable: true, sizeBytes: 639150592,
		},
		"granite-embedding-278m-multilingual-ollama": {
			dimension: 768, installable: true, sizeBytes: 562776964,
		},
		"bge-m3-567m-ollama": {
			dimension: 1024, installable: false, sizeBytes: 1157672268,
		},
	}
	if len(catalog.Profiles) != len(want) {
		t.Fatalf("profile count = %d, want %d", len(catalog.Profiles), len(want))
	}
	for id, expected := range want {
		profile, ok := catalog.Find(id)
		if !ok {
			t.Fatalf("missing profile %q", id)
		}
		if profile.ExpectedDimension != expected.dimension ||
			profile.Installable != expected.installable ||
			profile.ArtifactSizeBytes != expected.sizeBytes {
			t.Fatalf("profile %q = %#v", id, profile)
		}
		if !strings.HasPrefix(profile.ManifestDigest, "sha256:") ||
			len(profile.ManifestDigest) != len("sha256:")+64 {
			t.Fatalf("profile %q digest = %q", id, profile.ManifestDigest)
		}
		if !strings.Contains(profile.RuntimeSourceURL, "ollama.com/library/") ||
			!strings.Contains(profile.RuntimeManifestURL, "registry.ollama.ai/") {
			t.Fatalf("profile %q runtime provenance = %#v", id, profile)
		}
	}
}

func TestCatalogValidationRejectsUnsafeInstallableDimension(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	catalog.Profiles[0].ExpectedDimension = 1024
	err = catalog.Validate()
	if err == nil || !strings.Contains(err.Error(), "want corpus dimension 768") {
		t.Fatalf("error = %v", err)
	}
}

func TestSupportsLanguage(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	nomic, _ := catalog.Find("nomic-embed-text-v1.5-ollama")
	if !nomic.SupportsLanguage("en") || nomic.SupportsLanguage("zh") {
		t.Fatalf("unexpected nomic language support: %#v", nomic.Languages)
	}
	qwen, _ := catalog.Find("qwen3-embedding-0.6b-ollama")
	if !qwen.SupportsLanguage("zh") || !qwen.SupportsLanguage("ja") {
		t.Fatalf("unexpected qwen language support: %#v", qwen.Languages)
	}
	granite, _ := catalog.Find("granite-embedding-278m-multilingual-ollama")
	if granite.SupportsLanguage("hi") {
		t.Fatalf("granite must not claim undocumented Hindi coverage: %#v", granite.Languages)
	}
}

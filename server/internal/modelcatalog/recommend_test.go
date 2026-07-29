package modelcatalog

import "testing"

func TestEvaluateRejectsHardwareAndUnavailableProfiles(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	device := Device{
		OperatingSystem: "linux",
		Architecture:    "amd64",
		MemoryAvailable: 3 << 30,
		DiskAvailable:   4 << 30,
		Ollama: RuntimeState{
			Available: true,
			BaseURL:   "http://localhost:11434",
			Models:    []InstalledModel{},
		},
	}
	statuses := Evaluate(catalog, device)
	byID := make(map[string]ProfileStatus, len(statuses))
	for _, status := range statuses {
		byID[status.Profile.ID] = status
	}
	if !byID["nomic-embed-text-v1.5-ollama"].Compatibility.Compatible {
		t.Fatalf("nomic status = %#v", byID["nomic-embed-text-v1.5-ollama"])
	}
	if byID["qwen3-embedding-0.6b-ollama"].Compatibility.Compatible {
		t.Fatalf("qwen status = %#v", byID["qwen3-embedding-0.6b-ollama"])
	}
	bge := byID["bge-m3-567m-ollama"].Compatibility
	if bge.Status != "unavailable" || bge.Compatible {
		t.Fatalf("bge status = %#v", bge)
	}
}

func TestRecommendUsesLanguageResourcesAndSizeNotVendorPopularity(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	device := Device{
		OperatingSystem: "linux",
		Architecture:    "arm64",
		MemoryAvailable: 16 << 30,
		DiskAvailable:   20 << 30,
		Ollama: RuntimeState{
			Available: true,
			BaseURL:   "http://localhost:11434",
			Models:    []InstalledModel{},
		},
	}
	statuses := Evaluate(catalog, device)
	english := Recommend(statuses, "en", device)
	if len(english) < 3 || english[0].ProfileID != "nomic-embed-text-v1.5-ollama" {
		t.Fatalf("english recommendations = %#v", english)
	}
	chinese := Recommend(statuses, "zh", device)
	if len(chinese) < 2 ||
		chinese[0].ProfileID != "granite-embedding-278m-multilingual-ollama" {
		t.Fatalf("Chinese recommendations = %#v", chinese)
	}
	for _, recommendation := range chinese {
		if recommendation.ProfileID == "nomic-embed-text-v1.5-ollama" ||
			recommendation.ProfileID == "bge-m3-567m-ollama" {
			t.Fatalf("ineligible recommendation = %#v", recommendation)
		}
	}
}

func TestVerifiedInstallationWinsAndDoesNotNeedFreeDownloadDisk(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := catalog.Find("qwen3-embedding-0.6b-ollama")
	device := Device{
		OperatingSystem: "darwin",
		Architecture:    "arm64",
		MemoryAvailable: 8 << 30,
		DiskAvailable:   1,
		Ollama: RuntimeState{
			Available: true,
			BaseURL:   "http://localhost:11434",
			Models: []InstalledModel{{
				Name:   profile.Model,
				Digest: profile.ManifestDigest,
			}},
		},
	}
	statuses := Evaluate(catalog, device)
	recommendations := Recommend(statuses, "zh", device)
	if len(recommendations) == 0 ||
		recommendations[0].ProfileID != profile.ID ||
		recommendations[0].Score < 100 {
		t.Fatalf("recommendations = %#v", recommendations)
	}
}

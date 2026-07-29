// Package modelcatalog owns mem's curated, versioned local embedding profiles.
//
// The catalog is intentionally small and conservative. A profile is marked
// installable only when its runtime artifact and output dimension are both
// compatible with the current corpus contract.
package modelcatalog

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

const CorpusDimension = 768

var (
	digestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	profileIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
)

//go:embed catalog.v1.json
var catalogJSON []byte

type Catalog struct {
	SchemaVersion   string    `json:"schema_version"`
	CatalogVersion  string    `json:"catalog_version"`
	CorpusDimension int       `json:"corpus_dimension"`
	Profiles        []Profile `json:"profiles"`
}

type Profile struct {
	ID                 string          `json:"id"`
	DisplayName        string          `json:"display_name"`
	Runtime            string          `json:"runtime"`
	SourceModel        string          `json:"source_model"`
	Model              string          `json:"model"`
	RuntimeSourceURL   string          `json:"runtime_source_url"`
	RuntimeManifestURL string          `json:"runtime_manifest_url"`
	ExpectedDimension  int             `json:"expected_dimension"`
	ArtifactSizeBytes  uint64          `json:"artifact_size_bytes"`
	ManifestDigest     string          `json:"manifest_digest"`
	License            string          `json:"license"`
	LicenseURL         string          `json:"license_url"`
	SourceURL          string          `json:"source_url"`
	Languages          []string        `json:"languages"`
	LanguageNotes      string          `json:"language_notes"`
	MinimumHardware    MinimumHardware `json:"minimum_hardware"`
	Installable        bool            `json:"installable"`
	UnavailableReason  string          `json:"unavailable_reason,omitempty"`
}

type MinimumHardware struct {
	MemoryBytes      uint64   `json:"memory_bytes"`
	DiskBytes        uint64   `json:"disk_bytes"`
	OperatingSystems []string `json:"operating_systems"`
	Architectures    []string `json:"architectures"`
}

func Load() (Catalog, error) {
	var catalog Catalog
	decoder := json.NewDecoder(strings.NewReader(string(catalogJSON)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode embedded model catalog: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func (c Catalog) Validate() error {
	if c.SchemaVersion != "mem.model-catalog/v1" {
		return fmt.Errorf("unsupported model catalog schema %q", c.SchemaVersion)
	}
	if strings.TrimSpace(c.CatalogVersion) == "" {
		return errors.New("model catalog version is empty")
	}
	if c.CorpusDimension != CorpusDimension {
		return fmt.Errorf(
			"catalog corpus dimension is %d, want %d",
			c.CorpusDimension,
			CorpusDimension,
		)
	}
	if len(c.Profiles) == 0 {
		return errors.New("model catalog has no profiles")
	}
	seen := make(map[string]struct{}, len(c.Profiles))
	for i, profile := range c.Profiles {
		if err := validateProfile(profile); err != nil {
			return fmt.Errorf("profile %d: %w", i, err)
		}
		if _, exists := seen[profile.ID]; exists {
			return fmt.Errorf("duplicate model profile id %q", profile.ID)
		}
		seen[profile.ID] = struct{}{}
	}
	return nil
}

func validateProfile(profile Profile) error {
	for name, value := range map[string]string{
		"id":                   profile.ID,
		"display_name":         profile.DisplayName,
		"runtime":              profile.Runtime,
		"source_model":         profile.SourceModel,
		"model":                profile.Model,
		"runtime_source_url":   profile.RuntimeSourceURL,
		"runtime_manifest_url": profile.RuntimeManifestURL,
		"license":              profile.License,
		"license_url":          profile.LicenseURL,
		"source_url":           profile.SourceURL,
		"language_notes":       profile.LanguageNotes,
		"manifest_digest":      profile.ManifestDigest,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is empty", name)
		}
	}
	if profile.Runtime != "ollama" {
		return fmt.Errorf("unsupported runtime %q", profile.Runtime)
	}
	if !profileIDPattern.MatchString(profile.ID) {
		return fmt.Errorf("id %q is not a stable lowercase profile ID", profile.ID)
	}
	if strings.ContainsAny(profile.Model, "\r\n\t") {
		return errors.New("model contains a control character")
	}
	if profile.ExpectedDimension <= 0 {
		return errors.New("expected_dimension must be positive")
	}
	if profile.ArtifactSizeBytes == 0 {
		return errors.New("artifact_size_bytes must be positive")
	}
	if !digestPattern.MatchString(profile.ManifestDigest) {
		return fmt.Errorf("manifest_digest %q is not a full SHA-256 digest", profile.ManifestDigest)
	}
	for name, raw := range map[string]string{
		"license_url":          profile.LicenseURL,
		"source_url":           profile.SourceURL,
		"runtime_source_url":   profile.RuntimeSourceURL,
		"runtime_manifest_url": profile.RuntimeManifestURL,
	} {
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return fmt.Errorf("%s %q must be an absolute HTTPS URL", name, raw)
		}
	}
	if len(profile.Languages) == 0 {
		return errors.New("languages must not be empty")
	}
	seenLanguages := make(map[string]struct{}, len(profile.Languages))
	for _, language := range profile.Languages {
		if language == "" || language != strings.ToLower(strings.TrimSpace(language)) {
			return fmt.Errorf("language %q must be a normalized lowercase code", language)
		}
		if _, exists := seenLanguages[language]; exists {
			return fmt.Errorf("language %q is duplicated", language)
		}
		seenLanguages[language] = struct{}{}
	}
	if profile.MinimumHardware.MemoryBytes == 0 ||
		profile.MinimumHardware.DiskBytes == 0 ||
		len(profile.MinimumHardware.OperatingSystems) == 0 ||
		len(profile.MinimumHardware.Architectures) == 0 {
		return errors.New("minimum_hardware guidance is incomplete")
	}
	if profile.Installable {
		if profile.ExpectedDimension != CorpusDimension {
			return fmt.Errorf(
				"installable profile dimension is %d, want corpus dimension %d",
				profile.ExpectedDimension,
				CorpusDimension,
			)
		}
		if profile.UnavailableReason != "" {
			return errors.New("installable profile has unavailable_reason")
		}
	} else if strings.TrimSpace(profile.UnavailableReason) == "" {
		return errors.New("unavailable profile is missing unavailable_reason")
	}
	return nil
}

func (c Catalog) Find(id string) (Profile, bool) {
	for _, profile := range c.Profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return Profile{}, false
}

func (p Profile) SupportsLanguage(language string) bool {
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "" || language == "auto" {
		return true
	}
	return slices.Contains(p.Languages, language)
}

// Package aiprofile owns mem's fixed, auditable workspace AI profiles.
//
// A profile is a server-owned pipeline definition, not a user-supplied model
// configuration.  It deliberately contains provider/model identifiers and
// harmless pipeline metadata only: endpoint URLs, credentials, raw provider
// options, and prompts do not belong in this package or its persisted
// snapshots.
package aiprofile

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	// LocalFastV1 is the privacy-preserving local profile.  Optional
	// generative stages are explicitly disabled rather than left to Worker
	// environment defaults.
	LocalFastV1 = "local-fast-v1"

	// IdealabQualityV1 is the pinned managed-quality profile.  It must never
	// be selected through an arbitrary provider/model API path.
	IdealabQualityV1 = "idealab-quality-v1"

	// DataEgressLocalOnly means no stage in the profile is permitted to send
	// source material or embeddings to a managed provider.
	DataEgressLocalOnly = "local_only"

	// DataEgressManagedIdealab marks the fixed managed Idealab pipeline.
	DataEgressManagedIdealab = "managed_idealab"

	textEmbeddingDimension   = 768
	visualEmbeddingDimension = 512
)

var (
	// ErrUnknownProfile is returned for an ID outside the compiled catalog.
	ErrUnknownProfile = errors.New("unknown workspace AI profile")

	// ErrInvalidDefinition indicates a programming/catalog error.  It is not
	// appropriate to expose its details to untrusted API callers.
	ErrInvalidDefinition = errors.New("invalid workspace AI profile definition")
)

var profileIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// Stage fixes one model-bearing capability in a profile.  A disabled stage
// must not carry a provider; this makes disabling distinct from an omitted
// override and prevents fallback to a process-wide default model.
type Stage struct {
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
}

// Definition is an immutable, server-owned AI pipeline.  Revision is bumped
// whenever any concrete model or behavior changes; PipelineRevision tracks
// chunking/prompt/pipeline semantics independently from the profile ID.
type Definition struct {
	ID               string   `json:"id"`
	Revision         string   `json:"revision"`
	PipelineRevision string   `json:"pipeline_revision"`
	DataEgress       string   `json:"data_egress"`
	AllowedMIMETypes []string `json:"allowed_mime_types"`

	Embedding       Stage `json:"embedding"`
	VisualEmbedding Stage `json:"visual_embedding"`
	LLM             Stage `json:"llm"`
	VLM             Stage `json:"vlm"`
	ASR             Stage `json:"asr"`
	Rerank          Stage `json:"rerank"`
}

// Catalog returns defensive copies of every compiled profile in stable order.
// Callers cannot mutate the process-global catalog through the returned
// slices.
func Catalog() []Definition {
	out := make([]Definition, len(compiledCatalog))
	for i, definition := range compiledCatalog {
		out[i] = cloneDefinition(definition)
	}
	return out
}

// Find resolves one compiled profile by its stable ID.  The returned value is
// a defensive copy.
func Find(id string) (Definition, bool) {
	for _, definition := range compiledCatalog {
		if definition.ID == id {
			return cloneDefinition(definition), true
		}
	}
	return Definition{}, false
}

// Validate verifies the invariants expected of a compiled profile.  Keeping
// this exported lets tests and future reviewed catalog additions validate the
// exact same contract before they are enabled.
func (d Definition) Validate() error {
	if !profileIDPattern.MatchString(d.ID) {
		return fmt.Errorf("%w: profile id", ErrInvalidDefinition)
	}
	for _, field := range []string{d.Revision, d.PipelineRevision} {
		// The Worker independently bounds these profile identifiers at 64
		// characters. Keep the catalog contract no wider than the execution
		// boundary so a future profile cannot be persisted but rejected only
		// after a file has been dispatched.
		if !safeIdentifier(field, 64) {
			return fmt.Errorf("%w: revision", ErrInvalidDefinition)
		}
	}
	if d.DataEgress != DataEgressLocalOnly && d.DataEgress != DataEgressManagedIdealab {
		return fmt.Errorf("%w: data egress", ErrInvalidDefinition)
	}
	if len(d.AllowedMIMETypes) == 0 {
		return fmt.Errorf("%w: allowed MIME types", ErrInvalidDefinition)
	}
	seenMIMEs := make(map[string]struct{}, len(d.AllowedMIMETypes))
	for _, mime := range d.AllowedMIMETypes {
		if !safeMIMEPattern(mime) {
			return fmt.Errorf("%w: MIME pattern", ErrInvalidDefinition)
		}
		if _, exists := seenMIMEs[mime]; exists {
			return fmt.Errorf("%w: duplicate MIME pattern", ErrInvalidDefinition)
		}
		seenMIMEs[mime] = struct{}{}
	}
	if err := validateStage(d.Embedding, true, textEmbeddingDimension); err != nil {
		return err
	}
	if err := validateStage(d.VisualEmbedding, true, visualEmbeddingDimension); err != nil {
		return err
	}
	for _, stage := range []Stage{d.LLM, d.VLM, d.ASR, d.Rerank} {
		if err := validateStage(stage, false, 0); err != nil {
			return err
		}
	}
	if d.DataEgress == DataEgressLocalOnly {
		for _, stage := range []Stage{
			d.Embedding,
			d.VisualEmbedding,
			d.LLM,
			d.VLM,
			d.ASR,
			d.Rerank,
		} {
			if stage.Enabled && !isLocalProvider(stage.Provider) {
				return fmt.Errorf("%w: local profile has managed provider", ErrInvalidDefinition)
			}
		}
	}
	return nil
}

func validateStage(stage Stage, requireEnabled bool, requiredDimension int) error {
	if !stage.Enabled {
		if requireEnabled || stage.Provider != "" || stage.Dimensions != 0 {
			return fmt.Errorf("%w: disabled stage", ErrInvalidDefinition)
		}
		return nil
	}
	if !safeProviderSpec(stage.Provider) {
		return fmt.Errorf("%w: provider", ErrInvalidDefinition)
	}
	if strings.Contains(strings.ToLower(stage.Provider), "latest") {
		return fmt.Errorf("%w: mutable provider alias", ErrInvalidDefinition)
	}
	if requiredDimension > 0 && stage.Dimensions != requiredDimension {
		return fmt.Errorf("%w: embedding dimensions", ErrInvalidDefinition)
	}
	if requiredDimension == 0 && stage.Dimensions != 0 {
		return fmt.Errorf("%w: non-embedding dimensions", ErrInvalidDefinition)
	}
	return nil
}

func cloneDefinition(d Definition) Definition {
	d.AllowedMIMETypes = slices.Clone(d.AllowedMIMETypes)
	return d
}

func safeIdentifier(value string, limit int) bool {
	if value == "" || len(value) > limit || strings.TrimSpace(value) != value {
		return false
	}
	return !strings.ContainsAny(value, "\r\n\t\x00")
}

func safeMIMEPattern(value string) bool {
	if !safeIdentifier(value, 255) {
		return false
	}
	return strings.Contains(value, "/")
}

// safeProviderSpec intentionally permits model names containing colons (for
// example an Ollama tag) while refusing whitespace, URL syntax, and control
// characters.  Provider credentials and base URLs are never a profile field.
func safeProviderSpec(value string) bool {
	if value == "" || len(value) > 255 || strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\r\n\t\x00@") || strings.Contains(value, "://") {
		return false
	}
	vendor, model, ok := strings.Cut(value, ":")
	if !ok || vendor == "" || model == "" || !profileIDPattern.MatchString(vendor) {
		return false
	}
	return !strings.ContainsAny(model, " \r\n\t\x00")
}

func isLocalProvider(spec string) bool {
	vendor, _, ok := strings.Cut(spec, ":")
	if !ok {
		return false
	}
	switch vendor {
	case "ollama", "clip", "faster-whisper", "whisper":
		return true
	default:
		return false
	}
}

var compiledCatalog = func() []Definition {
	profiles := []Definition{
		{
			ID:               LocalFastV1,
			Revision:         "2026-07-29",
			PipelineRevision: "file-enrichment-v1",
			DataEgress:       DataEgressLocalOnly,
			AllowedMIMETypes: []string{
				"text/*", "application/pdf", "audio/*", "image/*",
				"application/json", "application/xml", "application/yaml",
				"application/x-yaml", "application/javascript", "application/typescript",
				"application/x-sh", "application/x-python", "application/x-toml",
			},
			Embedding: Stage{
				Enabled:    true,
				Provider:   "ollama:qwen3-embedding:0.6b",
				Dimensions: textEmbeddingDimension,
			},
			VisualEmbedding: Stage{
				Enabled:    true,
				Provider:   "clip:ViT-B-32",
				Dimensions: visualEmbeddingDimension,
			},
			// A local model must be explicitly installed and verified before
			// a future profile revision enables generative/ASR stages.  Leaving
			// them disabled is intentional and must not fall back to MEM_DEFAULT.
			LLM:    Stage{},
			VLM:    Stage{},
			ASR:    Stage{},
			Rerank: Stage{},
		},
		{
			ID:               IdealabQualityV1,
			Revision:         "2026-07-29",
			PipelineRevision: "file-enrichment-v1",
			DataEgress:       DataEgressManagedIdealab,
			AllowedMIMETypes: []string{
				"text/*", "application/pdf", "audio/*", "image/*",
				"application/json", "application/xml", "application/yaml",
				"application/x-yaml", "application/javascript", "application/typescript",
				"application/x-sh", "application/x-python", "application/x-toml",
			},
			// Idealab's market exposes this OpenAI embedding model and its
			// compatible embedding API accepts an explicit dimensions request.
			// We pin the output to the existing vector(768) schema and probe the
			// actual response before activation; no unverified v4 alias or
			// implicit default can enter the index.
			Embedding: Stage{
				Enabled:    true,
				Provider:   "openai:text-embedding-3-large",
				Dimensions: textEmbeddingDimension,
			},
			VisualEmbedding: Stage{
				Enabled:    true,
				Provider:   "clip:ViT-B-32",
				Dimensions: visualEmbeddingDimension,
			},
			LLM: Stage{
				Enabled:  true,
				Provider: "openai:qwen3.7-max-2026-06-08",
			},
			// The verified market record covers the Chat API only. Do not send
			// image bytes to it until a reviewed Idealab VLM capability and
			// contract are available.
			VLM: Stage{},
			// Idealab currently has no verified qwen3 rerank API contract. Keep
			// this stage explicitly disabled rather than issuing a guessed
			// endpoint request or silently substituting another reranker.
			Rerank: Stage{},
		},
	}
	for _, profile := range profiles {
		if err := profile.Validate(); err != nil {
			panic(err)
		}
	}
	return profiles
}()

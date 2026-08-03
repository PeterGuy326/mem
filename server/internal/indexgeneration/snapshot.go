package indexgeneration

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
)

var profileIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func snapshotFromDefinition(d aiprofile.Definition) ProfileSnapshot {
	stage := func(value aiprofile.Stage) StageSnapshot {
		return StageSnapshot{
			Enabled: value.Enabled, Provider: value.Provider, Dimensions: value.Dimensions,
		}
	}
	return ProfileSnapshot{
		ID: d.ID, Revision: d.Revision, PipelineRevision: d.PipelineRevision,
		DataEgress:       d.DataEgress,
		AllowedMIMETypes: append([]string(nil), d.AllowedMIMETypes...),
		Embedding:        stage(d.Embedding),
		VisualEmbedding:  stage(d.VisualEmbedding),
		LLM:              stage(d.LLM),
		VLM:              stage(d.VLM),
		ASR:              stage(d.ASR),
		Rerank:           stage(d.Rerank),
	}
}

// validateProfileSnapshot checks the credential-free persisted contract on
// its own terms. It deliberately does not resolve the current compiled
// catalog: a reviewed historical revision remains interpretable after the
// catalog moves forward, while Service.enabled remains the independent
// operator kill switch.
func validateProfileSnapshot(snapshot ProfileSnapshot) error {
	if !profileIDPattern.MatchString(snapshot.ID) ||
		!safeSnapshotIdentifier(snapshot.Revision, 64) ||
		!safeSnapshotIdentifier(snapshot.PipelineRevision, 64) {
		return fmt.Errorf("invalid snapshot identity")
	}
	if snapshot.DataEgress != aiprofile.DataEgressLocalOnly &&
		snapshot.DataEgress != aiprofile.DataEgressManagedIdealab {
		return fmt.Errorf("invalid snapshot data egress")
	}
	if len(snapshot.AllowedMIMETypes) == 0 || len(snapshot.AllowedMIMETypes) > 128 {
		return fmt.Errorf("invalid snapshot MIME allowlist")
	}
	seenMIMEs := make(map[string]struct{}, len(snapshot.AllowedMIMETypes))
	for _, mime := range snapshot.AllowedMIMETypes {
		if !safeSnapshotIdentifier(mime, 255) || !strings.Contains(mime, "/") {
			return fmt.Errorf("invalid snapshot MIME pattern")
		}
		if _, exists := seenMIMEs[mime]; exists {
			return fmt.Errorf("duplicate snapshot MIME pattern")
		}
		seenMIMEs[mime] = struct{}{}
	}
	if err := validateSnapshotStage(snapshot.Embedding, true, true); err != nil {
		return err
	}
	if err := validateSnapshotStage(snapshot.VisualEmbedding, false, true); err != nil {
		return err
	}
	for _, stage := range []StageSnapshot{snapshot.LLM, snapshot.VLM, snapshot.ASR, snapshot.Rerank} {
		if err := validateSnapshotStage(stage, false, false); err != nil {
			return err
		}
	}
	if snapshot.DataEgress == aiprofile.DataEgressLocalOnly {
		for _, stage := range []StageSnapshot{
			snapshot.Embedding, snapshot.VisualEmbedding, snapshot.LLM,
			snapshot.VLM, snapshot.ASR, snapshot.Rerank,
		} {
			if stage.Enabled && !localSnapshotProvider(stage.Provider) {
				return fmt.Errorf("local snapshot contains managed provider")
			}
		}
	} else {
		for _, stage := range []StageSnapshot{
			snapshot.Embedding, snapshot.VisualEmbedding, snapshot.LLM,
			snapshot.VLM, snapshot.ASR, snapshot.Rerank,
		} {
			if stage.Enabled && !localSnapshotProvider(stage.Provider) &&
				!managedSnapshotProvider(stage.Provider) {
				return fmt.Errorf("managed snapshot contains unbound provider")
			}
		}
	}
	return nil
}

func validateSnapshotStage(stage StageSnapshot, required, embedding bool) error {
	if !stage.Enabled {
		if required || stage.Provider != "" || stage.Dimensions != 0 {
			return fmt.Errorf("invalid disabled snapshot stage")
		}
		return nil
	}
	if !safeSnapshotProvider(stage.Provider) ||
		strings.Contains(strings.ToLower(stage.Provider), "latest") {
		return fmt.Errorf("invalid snapshot provider")
	}
	if (embedding && stage.Dimensions <= 0) || (!embedding && stage.Dimensions != 0) {
		return fmt.Errorf("invalid snapshot dimensions")
	}
	return nil
}

func safeSnapshotIdentifier(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\t\x00")
}

func safeSnapshotProvider(value string) bool {
	if !safeSnapshotIdentifier(value, 255) || strings.ContainsAny(value, "@") ||
		strings.Contains(value, "://") {
		return false
	}
	vendor, model, ok := strings.Cut(value, ":")
	return ok && profileIDPattern.MatchString(vendor) && model != "" &&
		!strings.ContainsAny(model, " \r\n\t\x00")
}

func localSnapshotProvider(value string) bool {
	vendor, _, ok := strings.Cut(value, ":")
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

func managedSnapshotProvider(value string) bool {
	vendor, _, ok := strings.Cut(value, ":")
	if !ok {
		return false
	}
	if vendor == "idealab" {
		return true
	}
	// Exact published V1 compatibility. New managed snapshots must use the
	// deployment-owned idealab namespace.
	return value == "openai:text-embedding-3-large" ||
		value == "openai:qwen3.7-max-2026-06-08"
}

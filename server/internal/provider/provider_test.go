package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type fakeProbeWorker struct {
	enabled  bool
	embed    func(context.Context, string, string) ([]float32, error)
	probeVLM func(context.Context, string) (string, error)
}

func (f *fakeProbeWorker) Enabled() bool {
	return f.enabled
}

func (f *fakeProbeWorker) EmbedTextWith(
	ctx context.Context,
	text, spec string,
) ([]float32, error) {
	if f.embed == nil {
		return nil, errors.New("unexpected EmbedTextWith call")
	}
	return f.embed(ctx, text, spec)
}

func (f *fakeProbeWorker) ProbeVLM(ctx context.Context, spec string) (string, error) {
	if f.probeVLM == nil {
		return "", errors.New("unexpected ProbeVLM call")
	}
	return f.probeVLM(ctx, spec)
}

func TestValidKindsLocksVisualEmbeddingToCLIP(t *testing.T) {
	if validKind(KindVisualEmbedding) {
		t.Fatal("visual_embedding must stay locked until versioned index generations exist")
	}
}

func TestCheckEmbeddingDimension(t *testing.T) {
	if err := checkEmbeddingDimension(768, 768); err != nil {
		t.Fatalf("matching dimension rejected: %v", err)
	}
	err := checkEmbeddingDimension(1024, 768)
	if !errors.Is(err, ErrEmbeddingDimConflict) {
		t.Fatalf("expected conflict sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "1024") || !strings.Contains(err.Error(), "768") {
		t.Fatalf("conflict lacks dimensions: %v", err)
	}
}

func TestEmbeddingProbeErrorDoesNotLeakProviderDiagnostics(t *testing.T) {
	const secret = "embedding-provider-secret"
	worker := &fakeProbeWorker{
		enabled: true,
		embed: func(context.Context, string, string) ([]float32, error) {
			return nil, fmt.Errorf("upstream echoed Authorization: Bearer %s", secret)
		},
	}

	_, err := (&Service{worker: worker}).Test(
		context.Background(), uuid.New(), KindEmbedding, "openai:embedding-model",
	)
	if err == nil {
		t.Fatal("expected failed probe")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "Authorization") {
		t.Fatalf("probe error leaked provider diagnostics: %v", err)
	}
	if err.Error() != "embedding provider probe failed" {
		t.Fatalf("unexpected safe error: %v", err)
	}
}

func TestVLMTestPerformsRealWorkerImageProbe(t *testing.T) {
	var gotSpec string
	worker := &fakeProbeWorker{
		enabled: true,
		probeVLM: func(
			_ context.Context,
			spec string,
		) (string, error) {
			gotSpec = spec
			return "可用", nil
		},
	}

	out, err := (&Service{worker: worker}).Test(
		context.Background(), uuid.New(), KindVLM, "ollama:minicpm-v",
	)
	if err != nil {
		t.Fatalf("Test returned error: %v", err)
	}
	if gotSpec != "ollama:minicpm-v" {
		t.Fatalf("worker probed spec %q", gotSpec)
	}
	result := probeResultMap(t, out)
	if result["reply_chars"] != 2 {
		t.Fatalf("reply_chars = %#v, want 2 Unicode characters", result["reply_chars"])
	}
	if _, softOK := result["note"]; softOK {
		t.Fatalf("VLM test still returned a soft OK: %#v", result)
	}
}

func TestVLMProbeErrorDoesNotLeakProviderDiagnostics(t *testing.T) {
	const secret = "vlm-provider-secret"
	worker := &fakeProbeWorker{
		enabled: true,
		probeVLM: func(context.Context, string) (string, error) {
			return "", fmt.Errorf("gateway echoed Authorization: Bearer %s", secret)
		},
	}

	_, err := (&Service{worker: worker}).Test(
		context.Background(), uuid.New(), KindVLM, "openai:qwen-vl-max",
	)
	if err == nil {
		t.Fatal("expected failed probe")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "Authorization") {
		t.Fatalf("probe error leaked provider diagnostics: %v", err)
	}
	if err.Error() != "vlm provider probe failed" {
		t.Fatalf("unexpected safe error: %v", err)
	}
}

func probeResultMap(t *testing.T, out any) map[string]any {
	t.Helper()
	result, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", out)
	}
	return result
}

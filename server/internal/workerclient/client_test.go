package workerclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"github.com/PeterGuy326/mem/server/internal/workerpb"
)

type fakeProcessorServiceClient struct {
	process func(context.Context, *workerpb.ProcessRequest) (*workerpb.ProcessResponse, error)
}

func (f *fakeProcessorServiceClient) Process(
	ctx context.Context,
	req *workerpb.ProcessRequest,
	_ ...grpc.CallOption,
) (*workerpb.ProcessResponse, error) {
	if f.process == nil {
		return nil, errors.New("unexpected Process call")
	}
	return f.process(ctx, req)
}

func (*fakeProcessorServiceClient) Chat(
	context.Context,
	*workerpb.ChatRequest,
	...grpc.CallOption,
) (*workerpb.ChatResponse, error) {
	return nil, errors.New("unexpected Chat call")
}

func (*fakeProcessorServiceClient) HealthCheck(
	context.Context,
	*workerpb.HealthCheckRequest,
	...grpc.CallOption,
) (*workerpb.HealthCheckResponse, error) {
	return nil, errors.New("unexpected HealthCheck call")
}

func TestProbeVLMSendsImageAndProbeOptions(t *testing.T) {
	var gotReq *workerpb.ProcessRequest
	stub := &fakeProcessorServiceClient{
		process: func(
			_ context.Context,
			req *workerpb.ProcessRequest,
		) (*workerpb.ProcessResponse, error) {
			gotReq = req
			return &workerpb.ProcessResponse{
				Caption: "  a tiny image  ",
				Status:  workerpb.ProcessStatus_STATUS_OK,
			}, nil
		},
	}
	client := &Client{
		addr:   "test-worker",
		conn:   &grpc.ClientConn{},
		stub:   stub,
		dialed: true,
	}

	caption, err := client.ProbeVLM(context.Background(), "openai:qwen-vl-max")
	if err != nil {
		t.Fatalf("ProbeVLM returned error: %v", err)
	}
	if caption != "a tiny image" {
		t.Fatalf("caption = %q", caption)
	}
	if gotReq == nil {
		t.Fatal("worker Process was not called")
	}
	if gotReq.FileId != "provider-probe" || gotReq.Mime != "image/png" ||
		gotReq.Name != "provider-probe.png" {
		t.Fatalf("unexpected request metadata: %#v", gotReq)
	}

	var options map[string]any
	if err := json.Unmarshal(gotReq.OptionsJson, &options); err != nil {
		t.Fatalf("decode options: %v", err)
	}
	if options["vlm_provider"] != "openai:qwen-vl-max" || options["provider_probe"] != true {
		t.Fatalf("probe options = %#v", options)
	}

	head, encoded, ok := strings.Cut(gotReq.StorageUri, ",")
	if !ok || head != "data:image/png;base64" {
		t.Fatalf("storage URI is not an inline PNG: %q", gotReq.StorageUri)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode probe image: %v", err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("probe image is invalid: %v", err)
	}
	if cfg.Width != 1 || cfg.Height != 1 {
		t.Fatalf("probe image size = %dx%d, want 1x1", cfg.Width, cfg.Height)
	}
}

func TestProbeVLMRequiresNonEmptyCaption(t *testing.T) {
	client := &Client{
		addr: "test-worker",
		conn: &grpc.ClientConn{},
		stub: &fakeProcessorServiceClient{
			process: func(
				context.Context,
				*workerpb.ProcessRequest,
			) (*workerpb.ProcessResponse, error) {
				return &workerpb.ProcessResponse{
					Status: workerpb.ProcessStatus_STATUS_PARTIAL,
				}, nil
			},
		},
		dialed: true,
	}

	_, err := client.ProbeVLM(context.Background(), "ollama:minicpm-v")
	if err == nil || !strings.Contains(err.Error(), "no caption") {
		t.Fatalf("empty caption was not rejected: %v", err)
	}
}

func TestBuildVLMProbeRequestRejectsEmptySpec(t *testing.T) {
	if _, err := buildVLMProbeRequest(" \t"); err == nil {
		t.Fatal("empty VLM spec was accepted")
	}
}

func TestBuildOptionsJSONProfileIsAuthoritativeAndCredentialFree(t *testing.T) {
	options := buildOptionsJSON(FileMeta{
		// Legacy values must not leak into or alter a profiled request.
		EmbeddingProvider: "openai:untrusted-client-model",
		VLMProvider:       "openai:untrusted-client-vlm",
		AIProfile: &AIProfileOptions{
			Contract:         "mem.ai-profile/v1",
			ID:               "idealab-quality-v1",
			Revision:         "2026-07-29",
			PipelineRevision: "enrichment-v1",
			Embedding: ProviderStage{
				Enabled: true, Provider: "openai:text-embedding-3-large", Dimensions: 768,
			},
			VisualEmbedding: ProviderStage{
				Enabled: true, Provider: "clip:ViT-B-32", Dimensions: 512,
			},
			LLM:    ProviderStage{Enabled: true, Provider: "openai:qwen3.7-max-2026-06-08"},
			VLM:    ProviderStage{Enabled: true, Provider: "openai:qwen3.7-max-2026-06-08"},
			ASR:    ProviderStage{Enabled: true, Provider: "faster-whisper:tiny"},
			Rerank: ProviderStage{Enabled: false},
		},
	})
	if len(options) == 0 {
		t.Fatal("profile options were omitted")
	}
	var got map[string]any
	if err := json.Unmarshal(options, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("options keys = %#v", got)
	}
	profile, ok := got["ai_profile"].(map[string]any)
	if !ok {
		t.Fatalf("ai_profile = %#v", got["ai_profile"])
	}
	if profile["id"] != "idealab-quality-v1" ||
		profile["pipeline_revision"] != "enrichment-v1" {
		t.Fatalf("profile = %#v", profile)
	}
	embedding, ok := profile["embedding"].(map[string]any)
	if !ok || embedding["provider"] != "openai:text-embedding-3-large" ||
		embedding["dimensions"] != float64(768) {
		t.Fatalf("embedding = %#v", profile["embedding"])
	}
	for _, forbidden := range []string{
		"api_key", "credential", "base_url", "embedding_provider", "vlm_provider",
	} {
		if _, found := got[forbidden]; found {
			t.Fatalf("profile options leaked %q: %#v", forbidden, got)
		}
	}
}

func TestBuildOptionsJSONLegacyIncludesLLMAndASR(t *testing.T) {
	options := buildOptionsJSON(FileMeta{
		EmbeddingProvider: "ollama:qwen3-embedding:0.6b",
		LLMProvider:       "ollama:qwen3:4b",
		ASRProvider:       "faster-whisper:tiny",
	})
	var got map[string]string
	if err := json.Unmarshal(options, &got); err != nil {
		t.Fatal(err)
	}
	if got["llm_provider"] != "ollama:qwen3:4b" ||
		got["asr_provider"] != "faster-whisper:tiny" {
		t.Fatalf("legacy options = %#v", got)
	}
}

func TestProbeEmbeddingUsesExplicitProfileDimensions(t *testing.T) {
	var got *workerpb.ProcessRequest
	client := &Client{
		addr: "test-worker",
		conn: &grpc.ClientConn{},
		stub: &fakeProcessorServiceClient{process: func(
			_ context.Context,
			req *workerpb.ProcessRequest,
		) (*workerpb.ProcessResponse, error) {
			got = req
			return &workerpb.ProcessResponse{
				Status: workerpb.ProcessStatus_STATUS_OK,
				Embeddings: map[string]*workerpb.Embedding{
					"text": {
						Provider: "openai:text-embedding-3-large",
						Rows:     []*workerpb.EmbeddingRow{{Values: make([]float32, 768)}},
					},
				},
			}, nil
		}},
		dialed: true,
	}
	dimension, err := client.ProbeEmbedding(
		context.Background(), "openai:text-embedding-3-large", 768,
	)
	if err != nil || dimension != 768 {
		t.Fatalf("ProbeEmbedding = %d, %v", dimension, err)
	}
	var envelope map[string]any
	if got == nil || json.Unmarshal(got.OptionsJson, &envelope) != nil {
		t.Fatalf("profile probe request = %#v", got)
	}
	profile, ok := envelope["ai_profile"].(map[string]any)
	if !ok {
		t.Fatalf("options = %#v", envelope)
	}
	embedding := profile["embedding"].(map[string]any)
	if embedding["provider"] != "openai:text-embedding-3-large" ||
		embedding["dimensions"] != float64(768) ||
		profile["id"] != "profile-probe-v1" {
		t.Fatalf("profile = %#v", profile)
	}
	if profile["llm"].(map[string]any)["enabled"] != false ||
		profile["vlm"].(map[string]any)["enabled"] != false {
		t.Fatalf("probe allowed generative stages: %#v", profile)
	}
}

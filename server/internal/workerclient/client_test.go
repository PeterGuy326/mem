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

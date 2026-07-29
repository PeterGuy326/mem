// Package workerclient is the Go-side gRPC client for the Python AI Worker.
//
// Lifecycle:
//   - New(addr, bucket) dials the worker (lazy: failure is logged, not fatal)
//   - Client.Index(ctx, file) fires a Process RPC for a freshly uploaded file
//   - Client.EmbedText(ctx, query) embeds a search query by re-using Process
//     on a synthetic text payload (returns the first chunk vector)
//   - Client.Close() releases the connection
//
// Design notes:
//   - The connection is created with grpc.NewClient + WithTransportCredentials
//     insecure — worker is expected to be on the trusted LAN/cluster, not WAN.
//   - Index() is intentionally blocking; callers wrap it in a goroutine for
//     fire-and-forget semantics (see internal/file).
package workerclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/PeterGuy326/mem/server/internal/workerpb"
)

// Client wraps a gRPC connection to the AI worker.
//
// A zero-value Client is *not* usable; use New().
// A Client where addr=="" is a no-op stub (used when MEM_WORKER_GRPC is unset
// — local dev without the worker still has functioning upload/download).
type Client struct {
	addr   string
	bucket string

	mu     sync.Mutex
	conn   *grpc.ClientConn
	stub   workerpb.ProcessorServiceClient
	dialed bool
}

// New constructs a Client. addr is "host:port" (or "" to disable the worker
// entirely). bucket is the S3 bucket name memd writes objects to — used to
// build the s3:// URI passed to the worker.
func New(addr, bucket string) *Client {
	return &Client{addr: addr, bucket: bucket}
}

// Enabled reports whether the worker is configured. Callers should fast-path
// out when this is false.
func (c *Client) Enabled() bool {
	return c != nil && c.addr != ""
}

// Close releases the underlying gRPC connection.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	c.stub = nil
	c.dialed = false
	return err
}

// ensureDialed performs the gRPC Dial on first use and caches the result.
// We dial lazily so memd can start even when the worker is down.
func (c *Client) ensureDialed() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dialed && c.conn != nil {
		return nil
	}
	if c.addr == "" {
		return errors.New("workerclient: addr is empty (MEM_WORKER_GRPC not set)")
	}
	conn, err := grpc.NewClient(
		c.addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(64*1024*1024),
			grpc.MaxCallSendMsgSize(64*1024*1024),
		),
	)
	if err != nil {
		return fmt.Errorf("grpc dial %s: %w", c.addr, err)
	}
	c.conn = conn
	c.stub = workerpb.NewProcessorServiceClient(conn)
	c.dialed = true
	return nil
}

// FileMeta is the minimum subset of file metadata the worker needs.
type FileMeta struct {
	FileID     string
	UserID     string
	Name       string
	MIME       string
	SHA256     string
	StorageKey string // bucket-relative S3 key
	// Provider overrides (optional). Empty values fall back to worker defaults.
	EmbeddingProvider       string
	VisualEmbeddingProvider string // CLIP image-tower embedder for image/* files
	VLMProvider             string
	LLMProvider             string
	ASRProvider             string
	// AIProfile is the server-resolved, immutable profile contract for this
	// indexing request. When set it takes precedence over the legacy provider
	// fields above: the worker must honour the explicit enabled/disabled state
	// and must not read a process-wide MEM_DEFAULT_* fallback.
	//
	// No URL, credential, or raw source field is representable here. The worker
	// receives only fixed provider identifiers and harmless provenance.
	AIProfile *AIProfileOptions
}

// ProviderStage is one explicitly enabled or disabled processing stage in a
// server-owned AI profile. Dimensions is meaningful only for embedding stages.
type ProviderStage struct {
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
}

// AIProfileOptions is the options_json contract shared with the Worker. It is
// intentionally a small, credential-free snapshot rather than a reference to
// mutable process defaults. Keep Contract stable when changing this shape.
type AIProfileOptions struct {
	Contract         string        `json:"contract"`
	ID               string        `json:"id"`
	Revision         string        `json:"revision"`
	PipelineRevision string        `json:"pipeline_revision"`
	Embedding        ProviderStage `json:"embedding"`
	VisualEmbedding  ProviderStage `json:"visual_embedding"`
	LLM              ProviderStage `json:"llm"`
	VLM              ProviderStage `json:"vlm"`
	ASR              ProviderStage `json:"asr"`
	Rerank           ProviderStage `json:"rerank"`
}

// Index runs Process synchronously and returns the response. Callers wanting
// fire-and-forget semantics should call this from a goroutine.
//
// Returns the raw *ProcessResponse so the caller (indexer) can decide how to
// persist it.
func (c *Client) Index(ctx context.Context, m FileMeta) (*workerpb.ProcessResponse, error) {
	if !c.Enabled() {
		return nil, errors.New("workerclient: disabled")
	}
	if err := c.ensureDialed(); err != nil {
		return nil, err
	}
	uri := fmt.Sprintf("s3://%s/%s", c.bucket, m.StorageKey)
	req := &workerpb.ProcessRequest{
		FileId:     m.FileID,
		StorageUri: uri,
		Mime:       m.MIME,
		Sha256:     m.SHA256,
		UserId:     m.UserID,
		Name:       m.Name,
	}
	if opts := buildOptionsJSON(m); opts != nil {
		req.OptionsJson = opts
	}
	return c.stub.Process(ctx, req)
}

// buildOptionsJSON encodes per-request provider overrides into a JSON blob
// the worker understands. An AIProfile is authoritative and deliberately does
// not fall through to any legacy/global provider setting.
func buildOptionsJSON(m FileMeta) []byte {
	if m.AIProfile != nil {
		out := map[string]any{"ai_profile": m.AIProfile}
		b, _ := json.Marshal(out)
		return b
	}
	if m.EmbeddingProvider == "" && m.VisualEmbeddingProvider == "" &&
		m.VLMProvider == "" && m.LLMProvider == "" && m.ASRProvider == "" {
		return nil
	}
	out := map[string]string{}
	if m.EmbeddingProvider != "" {
		out["embedding_provider"] = m.EmbeddingProvider
	}
	if m.VisualEmbeddingProvider != "" {
		out["visual_embedding_provider"] = m.VisualEmbeddingProvider
	}
	if m.VLMProvider != "" {
		out["vlm_provider"] = m.VLMProvider
	}
	if m.LLMProvider != "" {
		out["llm_provider"] = m.LLMProvider
	}
	if m.ASRProvider != "" {
		out["asr_provider"] = m.ASRProvider
	}
	b, _ := json.Marshal(out)
	return b
}

const vlmProbeDataURI = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

// ProbeVLM sends a tiny in-memory image through the worker's real VLM adapter
// and requires a non-empty caption. provider_probe tells ImageProcessor to
// return immediately after captioning, without loading CLIP/face models.
func (c *Client) ProbeVLM(ctx context.Context, providerSpec string) (string, error) {
	if !c.Enabled() {
		return "", errors.New("workerclient: disabled")
	}
	if err := c.ensureDialed(); err != nil {
		return "", err
	}
	req, err := buildVLMProbeRequest(providerSpec)
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := c.stub.Process(cctx, req)
	if err != nil {
		return "", fmt.Errorf("worker process(VLM probe): %w", err)
	}
	if resp.Status == workerpb.ProcessStatus_STATUS_FAILED {
		return "", fmt.Errorf("worker VLM probe failed: %s", resp.Error)
	}
	caption := strings.TrimSpace(resp.Caption)
	if caption == "" {
		return "", errors.New("worker VLM probe returned no caption")
	}
	return caption, nil
}

func buildVLMProbeRequest(providerSpec string) (*workerpb.ProcessRequest, error) {
	if strings.TrimSpace(providerSpec) == "" {
		return nil, errors.New("workerclient: VLM provider spec is empty")
	}
	options, err := json.Marshal(map[string]any{
		"vlm_provider":   providerSpec,
		"provider_probe": true,
	})
	if err != nil {
		return nil, fmt.Errorf("encode VLM probe options: %w", err)
	}
	return &workerpb.ProcessRequest{
		FileId:      "provider-probe",
		StorageUri:  vlmProbeDataURI,
		Mime:        "image/png",
		Name:        "provider-probe.png",
		OptionsJson: options,
	}, nil
}

// EmbedTextWith embeds q using a specific provider spec override (e.g.
// "openai:text-embedding-3-small"). Empty spec means worker default.
func (c *Client) EmbedTextWith(ctx context.Context, q, providerSpec string) ([]float32, error) {
	return c.embedText(ctx, q, providerSpec)
}

// EmbedTextWithProfile embeds query text under one immutable, server-resolved
// profile. Unlike EmbedTextWith it transmits explicit stage enablement and
// dimensions, so the Worker cannot fall back to MEM_DEFAULT_* settings.
func (c *Client) EmbedTextWithProfile(
	ctx context.Context,
	q string,
	profile AIProfileOptions,
) ([]float32, error) {
	return c.embedTextProfile(ctx, q, profile)
}

// ProbeEmbedding satisfies aiprofile.EmbeddingProbe without importing that
// package (and therefore without creating a dependency cycle). It makes the
// Worker exercise the exact dimensions requested by the profile contract.
func (c *Client) ProbeEmbedding(
	ctx context.Context,
	providerSpec string,
	dimensions int,
) (int, error) {
	if strings.TrimSpace(providerSpec) == "" || dimensions <= 0 {
		return 0, errors.New("workerclient: invalid embedding profile probe")
	}
	vec, err := c.embedTextProfile(ctx, "workspace AI profile embedding probe", AIProfileOptions{
		Contract:         "mem.ai-profile/v1",
		ID:               "profile-probe-v1",
		Revision:         "probe-v1",
		PipelineRevision: "probe-v1",
		Embedding: ProviderStage{
			Enabled: true, Provider: providerSpec, Dimensions: dimensions,
		},
		VisualEmbedding: ProviderStage{
			Enabled: true, Provider: "clip:ViT-B-32", Dimensions: 512,
		},
		LLM:    ProviderStage{Enabled: false},
		VLM:    ProviderStage{Enabled: false},
		ASR:    ProviderStage{Enabled: false},
		Rerank: ProviderStage{Enabled: false},
	})
	if err != nil {
		return 0, err
	}
	return len(vec), nil
}

// EmbedText returns an embedding vector for q by reusing the Process
// pipeline on a synthetic data: URI (chunked by the worker, first chunk wins).
//
// This avoids inventing a second RPC just for query-time embedding; the cost
// is one extra HTTP roundtrip (worker→Ollama) which is the dominant latency
// anyway.
func (c *Client) EmbedText(ctx context.Context, q string) ([]float32, error) {
	return c.embedText(ctx, q, "")
}

func (c *Client) embedText(ctx context.Context, q, providerSpec string) ([]float32, error) {
	if !c.Enabled() {
		return nil, errors.New("workerclient: disabled")
	}
	if err := c.ensureDialed(); err != nil {
		return nil, err
	}
	// data: URI lets the worker fetch_bytes return the raw text without S3.
	// We base64-encode in storage.py via the http fetcher? Simpler: pass the
	// payload through a `mem-internal:` scheme handled in workerclient_helper.
	// But storage.py doesn't know that scheme. Cleanest fix: server stages a
	// temporary text file in S3 — overkill. Pragmatic: extend storage.py to
	// support `data:text/plain;base64,...` URIs. Implemented in storage.py.
	dataURI := encodeDataURI(q)
	req := &workerpb.ProcessRequest{
		FileId:     "query",
		StorageUri: dataURI,
		Mime:       "text/plain",
		UserId:     "",
		Name:       "query.txt",
	}
	if providerSpec != "" {
		req.OptionsJson = []byte(fmt.Sprintf(`{"embedding_provider":%q}`, providerSpec))
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.stub.Process(cctx, req)
	if err != nil {
		return nil, fmt.Errorf("worker process(query): %w", err)
	}
	if resp.Status == workerpb.ProcessStatus_STATUS_FAILED {
		return nil, fmt.Errorf("worker process(query) failed: %s", resp.Error)
	}
	emb, ok := resp.Embeddings["text"]
	if !ok || len(emb.Rows) == 0 {
		return nil, errors.New("worker returned no text embedding for query")
	}
	// First chunk is enough — query text is short by construction.
	return emb.Rows[0].Values, nil
}

func (c *Client) embedTextProfile(
	ctx context.Context,
	q string,
	profile AIProfileOptions,
) ([]float32, error) {
	if !c.Enabled() {
		return nil, errors.New("workerclient: disabled")
	}
	if err := c.ensureDialed(); err != nil {
		return nil, err
	}
	options, err := json.Marshal(map[string]any{"ai_profile": profile})
	if err != nil {
		return nil, fmt.Errorf("encode AI profile query options: %w", err)
	}
	req := &workerpb.ProcessRequest{
		FileId:      "profile-query",
		StorageUri:  encodeDataURI(q),
		Mime:        "text/plain",
		UserId:      "",
		Name:        "profile-query.txt",
		OptionsJson: options,
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.stub.Process(cctx, req)
	if err != nil {
		return nil, fmt.Errorf("worker process(profile query): %w", err)
	}
	if resp.Status == workerpb.ProcessStatus_STATUS_FAILED {
		return nil, fmt.Errorf("worker process(profile query) failed: %s", resp.Error)
	}
	emb, ok := resp.Embeddings["text"]
	if !ok || len(emb.Rows) == 0 {
		return nil, errors.New("worker returned no text embedding for profile query")
	}
	if profile.Embedding.Dimensions > 0 && len(emb.Rows[0].Values) != profile.Embedding.Dimensions {
		return nil, fmt.Errorf("worker profile embedding dimension mismatch: got %d, want %d",
			len(emb.Rows[0].Values), profile.Embedding.Dimensions)
	}
	return emb.Rows[0].Values, nil
}

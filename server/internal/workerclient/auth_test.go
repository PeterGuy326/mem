package workerclient

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/workerpb"
)

func TestWorkerRequestAuthenticationGoldenVector(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 24))
	auth, err := newChannelAuth("memd-primary", key)
	if err != nil {
		t.Fatal(err)
	}
	auth.now = func() time.Time { return time.Unix(1_785_363_200, 0).UTC() }
	auth.nonce = func() (string, error) { return nonce, nil }
	request := &workerpb.ProcessRequest{
		FileId:      "file-123",
		StorageUri:  "s3://mem/workspaces/w/files/f",
		Mime:        "text/plain",
		Sha256:      "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		UserId:      "user-456",
		Name:        "notes.txt",
		OptionsJson: []byte(`{"ai_profile":{"id":"local-fast-v2"}}`),
	}

	signed, _, err := auth.signedContext(
		context.Background(),
		workerpb.ProcessorService_Process_FullMethodName,
		processScope,
		request,
	)
	if err != nil {
		t.Fatal(err)
	}
	md, ok := metadata.FromOutgoingContext(signed)
	if !ok {
		t.Fatal("signed context has no outgoing metadata")
	}
	bodyDigest, err := deterministicMessageDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	if bodyDigest != "6b125310b228c3e22c0736a09a90c574a55722d48878fb618272424e297d080e" {
		t.Fatalf("deterministic request digest = %q", bodyDigest)
	}
	signature, ok := singleMetadataValue(md, authMetadataSignature)
	if !ok {
		t.Fatal("request signature missing")
	}
	if signature != "kaLOhFn8FjcXZM8iIsxwLlfjrBj033Vgjf7NvnUC5mA" {
		t.Fatalf("request signature = %q", signature)
	}
	if got, _ := singleMetadataValue(md, authMetadataScope); got != processScope {
		t.Fatalf("scope = %q", got)
	}

	response := &workerpb.ProcessResponse{
		Status:    workerpb.ProcessStatus_STATUS_OK,
		Processor: "text",
	}
	responseDigest, err := deterministicMessageDigest(response)
	if err != nil {
		t.Fatal(err)
	}
	if responseDigest != "dbcea0ccc80d34c29c12107b1e88a8d6a4a1595b0a2616edad2d2164c033b323" {
		t.Fatalf("deterministic response digest = %q", responseDigest)
	}
	responseSignature := base64.RawURLEncoding.EncodeToString(hmacSHA256(
		key,
		responseCanonical(
			workerpb.ProcessorService_Process_FullMethodName,
			processScope,
			"memd-primary",
			nonce,
			responseDigest,
		),
	))
	if responseSignature != "SiM53fRN6wY1OD7ja6YgyYHdeEVjMRDMet7Pc96RFEY" {
		t.Fatalf("response signature = %q", responseSignature)
	}
}

func TestWorkerResponseAuthenticationRejectsTamperAndDuplicateMetadata(t *testing.T) {
	key := bytes.Repeat([]byte{0x5a}, 32)
	auth, err := newChannelAuth("memd-primary", key)
	if err != nil {
		t.Fatal(err)
	}
	proof := requestProof{
		method: workerpb.ProcessorService_Process_FullMethodName,
		scope:  processScope,
		nonce:  base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x24}, 24)),
	}
	response := &workerpb.ProcessResponse{
		Status: workerpb.ProcessStatus_STATUS_OK,
		Embeddings: map[string]*workerpb.Embedding{
			"text": {
				Provider: "idealab:text-embedding-3-large",
				Rows: []*workerpb.EmbeddingRow{{
					Values: []float32{0.25, 0.5},
				}},
			},
		},
	}
	trailer := signedResponseTrailer(t, auth, proof, response)
	if err := auth.verifyResponse(proof, response, trailer); err != nil {
		t.Fatalf("valid response proof rejected: %v", err)
	}

	tampered := proto.Clone(response).(*workerpb.ProcessResponse)
	tampered.Embeddings = map[string]*workerpb.Embedding{
		"text": {
			Provider: "idealab:different-model",
			Rows:     response.Embeddings["text"].Rows,
		},
	}
	if err := auth.verifyResponse(proof, tampered, trailer); !errors.Is(err, errWorkerResponseAuth) {
		t.Fatalf("tampered response error = %v", err)
	}

	duplicate := trailer.Copy()
	duplicate[responseMetadataSignature] = append(
		duplicate[responseMetadataSignature],
		duplicate[responseMetadataSignature][0],
	)
	if err := auth.verifyResponse(proof, response, duplicate); !errors.Is(err, errWorkerResponseAuth) {
		t.Fatalf("duplicate response metadata error = %v", err)
	}
}

func TestAuthenticatedProcessAndReadinessRequireVerifiedResponseProof(t *testing.T) {
	key := bytes.Repeat([]byte{0x6b}, 32)
	auth, err := newChannelAuth("memd-primary", key)
	if err != nil {
		t.Fatal(err)
	}
	auth.now = func() time.Time { return time.Unix(1_785_363_200, 0).UTC() }
	auth.nonce = func() (string, error) {
		return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 24)), nil
	}

	includeProof := true
	processCalls, healthCalls := 0, 0
	stub := &fakeProcessorServiceClient{}
	stub.processWithOptions = func(
		ctx context.Context,
		request *workerpb.ProcessRequest,
		options ...grpc.CallOption,
	) (*workerpb.ProcessResponse, error) {
		processCalls++
		md, _ := metadata.FromOutgoingContext(ctx)
		assertSignedRequestMetadata(t, auth, md, request, processScope)
		response := &workerpb.ProcessResponse{Status: workerpb.ProcessStatus_STATUS_OK}
		if includeProof {
			setTrailerOption(
				t,
				options,
				signedResponseTrailer(t, auth, requestProof{
					method: workerpb.ProcessorService_Process_FullMethodName,
					scope:  processScope,
					nonce:  md.Get(authMetadataNonce)[0],
				}, response),
			)
		}
		return response, nil
	}
	stub.health = func(
		ctx context.Context,
		request *workerpb.HealthCheckRequest,
		options ...grpc.CallOption,
	) (*workerpb.HealthCheckResponse, error) {
		healthCalls++
		md, _ := metadata.FromOutgoingContext(ctx)
		scope := readinessScopePrefix + "idealab:text-embedding-3-large"
		assertSignedRequestMetadata(t, auth, md, request, scope)
		response := &workerpb.HealthCheckResponse{
			Status:  workerpb.HealthCheckResponse_SERVING,
			Version: "test",
		}
		setTrailerOption(
			t,
			options,
			signedResponseTrailer(t, auth, requestProof{
				method: workerpb.ProcessorService_HealthCheck_FullMethodName,
				scope:  scope,
				nonce:  md.Get(authMetadataNonce)[0],
			}, response),
		)
		return response, nil
	}
	client := &Client{
		addr:   "test-worker",
		conn:   &grpc.ClientConn{},
		stub:   stub,
		dialed: true,
		auth:   auth,
	}

	if _, err := client.callProcess(
		context.Background(),
		&workerpb.ProcessRequest{FileId: "authenticated"},
	); err != nil {
		t.Fatalf("authenticated Process failed: %v", err)
	}
	includeProof = false
	if _, err := client.callProcess(
		context.Background(),
		&workerpb.ProcessRequest{FileId: "missing-proof"},
	); !errors.Is(err, errWorkerResponseAuth) {
		t.Fatalf("missing response proof error = %v", err)
	}
	if err := client.ReadyAuthenticated(
		context.Background(),
		"idealab:text-embedding-3-large",
	); err != nil {
		t.Fatalf("authenticated readiness failed: %v", err)
	}
	if err := client.ReadyAuthenticated(
		context.Background(),
		"idealab:uncompiled-model",
	); !errors.Is(err, errWorkerAuthConfiguration) {
		t.Fatalf("uncompiled readiness provider error = %v", err)
	}
	if processCalls != 2 || healthCalls != 1 {
		t.Fatalf("calls Process/HealthCheck = %d/%d", processCalls, healthCalls)
	}
}

func TestAuthenticatedReadinessAcceptsExactManagedV1Binding(t *testing.T) {
	const provider = "openai:text-embedding-3-large"
	key := bytes.Repeat([]byte{0x71}, 32)
	auth, err := newChannelAuth("memd-primary", key)
	if err != nil {
		t.Fatal(err)
	}
	auth.now = func() time.Time { return time.Unix(1_785_363_200, 0).UTC() }
	auth.nonce = func() (string, error) {
		return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 24)), nil
	}

	healthCalls := 0
	stub := &fakeProcessorServiceClient{
		health: func(
			ctx context.Context,
			request *workerpb.HealthCheckRequest,
			options ...grpc.CallOption,
		) (*workerpb.HealthCheckResponse, error) {
			healthCalls++
			md, _ := metadata.FromOutgoingContext(ctx)
			scope := readinessScopePrefix + provider
			assertSignedRequestMetadata(t, auth, md, request, scope)
			response := &workerpb.HealthCheckResponse{
				Status:  workerpb.HealthCheckResponse_SERVING,
				Version: "test-v1-binding",
			}
			setTrailerOption(
				t,
				options,
				signedResponseTrailer(t, auth, requestProof{
					method: workerpb.ProcessorService_HealthCheck_FullMethodName,
					scope:  scope,
					nonce:  md.Get(authMetadataNonce)[0],
				}, response),
			)
			return response, nil
		},
	}
	client := &Client{
		addr:                       "test-worker",
		conn:                       &grpc.ClientConn{},
		stub:                       stub,
		dialed:                     true,
		auth:                       auth,
		managedOpenAIBindingActive: true,
	}

	if err := client.ReadyAuthenticated(context.Background(), provider); err != nil {
		t.Fatalf("authenticated V1 readiness failed: %v", err)
	}
	if healthCalls != 1 {
		t.Fatalf("health calls = %d, want 1", healthCalls)
	}
}

func TestAuthenticatedManagedV1ProfileQueryPreservesExactSnapshot(t *testing.T) {
	definition, ok := aiprofile.Find(aiprofile.IdealabQualityV1)
	if !ok {
		t.Fatal("managed V1 profile missing from compiled catalog")
	}
	profile := AIProfileOptions{
		Contract:         "mem.ai-profile/v1",
		ID:               definition.ID,
		Revision:         definition.Revision,
		PipelineRevision: definition.PipelineRevision,
		DataEgress:       definition.DataEgress,
		Embedding:        providerStageFromCatalog(definition.Embedding),
		VisualEmbedding:  providerStageFromCatalog(definition.VisualEmbedding),
		// Query-time profile projection must never invoke enrichment stages.
		LLM:    ProviderStage{Enabled: false},
		VLM:    ProviderStage{Enabled: false},
		ASR:    ProviderStage{Enabled: false},
		Rerank: ProviderStage{Enabled: false},
	}
	if profile.Embedding.Provider != "openai:text-embedding-3-large" ||
		definition.LLM.Provider != "openai:qwen3.7-max-2026-06-08" {
		t.Fatalf("managed V1 catalog snapshot = %#v", profile)
	}

	key := bytes.Repeat([]byte{0x72}, 32)
	auth, err := newChannelAuth("memd-primary", key)
	if err != nil {
		t.Fatal(err)
	}
	auth.now = func() time.Time { return time.Unix(1_785_363_200, 0).UTC() }
	auth.nonce = func() (string, error) {
		return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 24)), nil
	}

	processCalls := 0
	stub := &fakeProcessorServiceClient{
		processWithOptions: func(
			ctx context.Context,
			request *workerpb.ProcessRequest,
			options ...grpc.CallOption,
		) (*workerpb.ProcessResponse, error) {
			processCalls++
			md, _ := metadata.FromOutgoingContext(ctx)
			assertSignedRequestMetadata(t, auth, md, request, processScope)
			var payload struct {
				AIProfile AIProfileOptions `json:"ai_profile"`
			}
			if err := json.Unmarshal(request.OptionsJson, &payload); err != nil {
				t.Fatalf("decode signed V1 profile options: %v", err)
			}
			if !reflect.DeepEqual(payload.AIProfile, profile) {
				t.Fatalf("signed V1 profile = %#v, want %#v", payload.AIProfile, profile)
			}
			response := &workerpb.ProcessResponse{
				Status: workerpb.ProcessStatus_STATUS_OK,
				Embeddings: map[string]*workerpb.Embedding{
					"text": {
						Provider: profile.Embedding.Provider,
						Rows: []*workerpb.EmbeddingRow{{
							Values: make([]float32, profile.Embedding.Dimensions),
						}},
					},
				},
			}
			setTrailerOption(
				t,
				options,
				signedResponseTrailer(t, auth, requestProof{
					method: workerpb.ProcessorService_Process_FullMethodName,
					scope:  processScope,
					nonce:  md.Get(authMetadataNonce)[0],
				}, response),
			)
			return response, nil
		},
	}
	client := &Client{
		addr:                       "test-worker",
		conn:                       &grpc.ClientConn{},
		stub:                       stub,
		dialed:                     true,
		auth:                       auth,
		managedOpenAIBindingActive: true,
	}

	vector, err := client.EmbedTextWithProfile(
		context.Background(),
		"legacy workspace query",
		profile,
	)
	if err != nil {
		t.Fatalf("authenticated managed V1 profile query failed: %v", err)
	}
	if len(vector) != profile.Embedding.Dimensions || processCalls != 1 {
		t.Fatalf("vector dimensions/calls = %d/%d", len(vector), processCalls)
	}
}

func providerStageFromCatalog(stage aiprofile.Stage) ProviderStage {
	if !stage.Enabled {
		return ProviderStage{Enabled: false}
	}
	return ProviderStage{
		Enabled:    true,
		Provider:   stage.Provider,
		Dimensions: stage.Dimensions,
	}
}

func signedResponseTrailer(
	t *testing.T,
	auth *channelAuth,
	proof requestProof,
	response proto.Message,
) metadata.MD {
	t.Helper()
	bodyDigest, err := deterministicMessageDigest(response)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.RawURLEncoding.EncodeToString(hmacSHA256(
		auth.key,
		responseCanonical(proof.method, proof.scope, auth.keyID, proof.nonce, bodyDigest),
	))
	return metadata.Pairs(
		responseMetadataContract, responseAuthContract,
		responseMetadataKeyID, auth.keyID,
		responseMetadataNonce, proof.nonce,
		responseMetadataSignature, signature,
	)
}

func assertSignedRequestMetadata(
	t *testing.T,
	auth *channelAuth,
	md metadata.MD,
	request proto.Message,
	scope string,
) {
	t.Helper()
	method := workerpb.ProcessorService_Process_FullMethodName
	if scope != processScope {
		method = workerpb.ProcessorService_HealthCheck_FullMethodName
	}
	keyID, ok := singleMetadataValue(md, authMetadataKeyID)
	if !ok || keyID != auth.keyID {
		t.Fatalf("key id metadata = %#v", md.Get(authMetadataKeyID))
	}
	timestamp, ok := singleMetadataValue(md, authMetadataTimestamp)
	if !ok {
		t.Fatal("timestamp metadata missing")
	}
	nonce, ok := singleMetadataValue(md, authMetadataNonce)
	if !ok {
		t.Fatal("nonce metadata missing")
	}
	gotScope, ok := singleMetadataValue(md, authMetadataScope)
	if !ok || gotScope != scope {
		t.Fatalf("scope metadata = %#v, want %q", md.Get(authMetadataScope), scope)
	}
	signature, ok := singleMetadataValue(md, authMetadataSignature)
	if !ok {
		t.Fatal("signature metadata missing")
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	expected := hmacSHA256(auth.key, requestCanonical(
		method,
		scope,
		auth.keyID,
		timestamp,
		nonce,
		hex.EncodeToString(digest[:]),
	))
	decoded, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(decoded, expected) {
		t.Fatal("request signature does not cover deterministic request body")
	}
}

func setTrailerOption(t *testing.T, options []grpc.CallOption, trailer metadata.MD) {
	t.Helper()
	for _, option := range options {
		if target, ok := option.(grpc.TrailerCallOption); ok {
			*target.TrailerAddr = trailer
			return
		}
	}
	t.Fatal("authenticated RPC omitted grpc.Trailer call option")
}

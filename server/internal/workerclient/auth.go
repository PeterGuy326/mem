package workerclient

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/PeterGuy326/mem/server/internal/workerpb"
)

const (
	requestAuthContract  = "mem.worker.hmac/v1"
	responseAuthContract = "mem.worker.response-hmac/v1"

	requestAuthDomain  = "mem.worker.request-auth/v1"
	responseAuthDomain = "mem.worker.response-auth/v1"

	processScope         = "process"
	readinessScopePrefix = "readiness:"

	authMetadataContract  = "x-mem-auth-contract"
	authMetadataKeyID     = "x-mem-auth-key-id"
	authMetadataTimestamp = "x-mem-auth-timestamp"
	authMetadataNonce     = "x-mem-auth-nonce"
	authMetadataScope     = "x-mem-auth-scope"
	authMetadataSignature = "x-mem-auth-signature"

	responseMetadataContract  = "x-mem-auth-response-contract"
	responseMetadataKeyID     = "x-mem-auth-response-key-id"
	responseMetadataNonce     = "x-mem-auth-response-nonce"
	responseMetadataSignature = "x-mem-auth-response-signature"

	authKeyBytes   = 32
	authNonceBytes = 24
)

var (
	errWorkerAuthConfiguration = errors.New("workerclient: invalid request authentication configuration")
	errWorkerResponseAuth      = errors.New("workerclient: Worker response authentication failed")

	authKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// Option configures a Worker client without breaking the existing local
// New(addr, bucket) API.
type Option func(*Client)

// WithHMACAuth signs every Worker request and verifies every successful
// response. The key is defensively copied and never included in an error.
func WithHMACAuth(keyID string, key []byte) Option {
	return func(client *Client) {
		auth, err := newChannelAuth(keyID, key)
		client.auth = auth
		client.authErr = err
	}
}

type channelAuth struct {
	keyID string
	key   []byte
	now   func() time.Time
	nonce func() (string, error)
}

type requestProof struct {
	method string
	scope  string
	nonce  string
}

func newChannelAuth(keyID string, key []byte) (*channelAuth, error) {
	if !authKeyIDPattern.MatchString(keyID) || len(key) != authKeyBytes {
		return nil, errWorkerAuthConfiguration
	}
	return &channelAuth{
		keyID: keyID,
		key:   append([]byte(nil), key...),
		now:   time.Now,
		nonce: randomAuthNonce,
	}, nil
}

func randomAuthNonce() (string, error) {
	raw := make([]byte, authNonceBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("workerclient: generate authentication nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (a *channelAuth) signedContext(
	ctx context.Context,
	method, scope string,
	message proto.Message,
) (context.Context, requestProof, error) {
	if a == nil || a.now == nil || a.nonce == nil {
		return nil, requestProof{}, errWorkerAuthConfiguration
	}
	if (method != workerpb.ProcessorService_Process_FullMethodName || scope != processScope) &&
		(method != workerpb.ProcessorService_HealthCheck_FullMethodName ||
			!strings.HasPrefix(scope, readinessScopePrefix)) {
		return nil, requestProof{}, errWorkerAuthConfiguration
	}
	nonce, err := a.nonce()
	if err != nil {
		return nil, requestProof{}, err
	}
	if _, err := base64.RawURLEncoding.DecodeString(nonce); err != nil ||
		len(nonce) != base64.RawURLEncoding.EncodedLen(authNonceBytes) {
		return nil, requestProof{}, errWorkerAuthConfiguration
	}
	timestamp := strconv.FormatInt(a.now().UTC().Unix(), 10)
	bodyDigest, err := deterministicMessageDigest(message)
	if err != nil {
		return nil, requestProof{}, err
	}
	canonical := requestCanonical(
		method,
		scope,
		a.keyID,
		timestamp,
		nonce,
		bodyDigest,
	)
	signature := base64.RawURLEncoding.EncodeToString(hmacSHA256(a.key, canonical))

	outgoing, _ := metadata.FromOutgoingContext(ctx)
	outgoing = outgoing.Copy()
	for _, key := range requestAuthMetadataKeys {
		delete(outgoing, key)
	}
	outgoing.Set(authMetadataContract, requestAuthContract)
	outgoing.Set(authMetadataKeyID, a.keyID)
	outgoing.Set(authMetadataTimestamp, timestamp)
	outgoing.Set(authMetadataNonce, nonce)
	outgoing.Set(authMetadataScope, scope)
	outgoing.Set(authMetadataSignature, signature)
	return metadata.NewOutgoingContext(ctx, outgoing), requestProof{
		method: method,
		scope:  scope,
		nonce:  nonce,
	}, nil
}

var requestAuthMetadataKeys = []string{
	authMetadataContract,
	authMetadataKeyID,
	authMetadataTimestamp,
	authMetadataNonce,
	authMetadataScope,
	authMetadataSignature,
}

func (a *channelAuth) verifyResponse(
	proof requestProof,
	message proto.Message,
	trailer metadata.MD,
) error {
	if a == nil {
		return errWorkerResponseAuth
	}
	contract, ok := singleMetadataValue(trailer, responseMetadataContract)
	if !ok || contract != responseAuthContract {
		return errWorkerResponseAuth
	}
	keyID, ok := singleMetadataValue(trailer, responseMetadataKeyID)
	if !ok || keyID != a.keyID {
		return errWorkerResponseAuth
	}
	nonce, ok := singleMetadataValue(trailer, responseMetadataNonce)
	if !ok || nonce != proof.nonce {
		return errWorkerResponseAuth
	}
	encodedSignature, ok := singleMetadataValue(trailer, responseMetadataSignature)
	if !ok {
		return errWorkerResponseAuth
	}
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil || len(signature) != sha256.Size {
		return errWorkerResponseAuth
	}
	bodyDigest, err := deterministicMessageDigest(message)
	if err != nil {
		return errWorkerResponseAuth
	}
	canonical := responseCanonical(
		proof.method,
		proof.scope,
		a.keyID,
		proof.nonce,
		bodyDigest,
	)
	expected := hmacSHA256(a.key, canonical)
	if !hmac.Equal(signature, expected) {
		return errWorkerResponseAuth
	}
	return nil
}

func deterministicMessageDigest(message proto.Message) (string, error) {
	body, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return "", fmt.Errorf("workerclient: encode authenticated protobuf: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func requestCanonical(
	method, scope, keyID, timestamp, nonce, bodyDigest string,
) []byte {
	return []byte(strings.Join([]string{
		requestAuthDomain,
		method,
		scope,
		keyID,
		timestamp,
		nonce,
		bodyDigest,
	}, "\n"))
}

func responseCanonical(
	method, scope, keyID, nonce, bodyDigest string,
) []byte {
	return []byte(strings.Join([]string{
		responseAuthDomain,
		method,
		scope,
		keyID,
		nonce,
		"0",
		bodyDigest,
	}, "\n"))
}

func hmacSHA256(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func singleMetadataValue(md metadata.MD, key string) (string, bool) {
	values := md.Get(key)
	if len(values) != 1 || values[0] == "" {
		return "", false
	}
	return values[0], true
}

func (c *Client) callProcess(
	ctx context.Context,
	request *workerpb.ProcessRequest,
) (*workerpb.ProcessResponse, error) {
	if c == nil || c.stub == nil {
		return nil, errors.New("workerclient: Worker stub unavailable")
	}
	if c.auth == nil {
		return c.stub.Process(ctx, request)
	}
	signed, proof, err := c.auth.signedContext(
		ctx,
		workerpb.ProcessorService_Process_FullMethodName,
		processScope,
		request,
	)
	if err != nil {
		return nil, err
	}
	var trailer metadata.MD
	response, err := c.stub.Process(signed, request, grpc.Trailer(&trailer))
	if err != nil {
		return nil, err
	}
	if err := c.auth.verifyResponse(proof, response, trailer); err != nil {
		return nil, err
	}
	return response, nil
}

// ReadyAuthenticated proves that the reachable Worker has the same channel
// key, working shared replay protection, and a complete managed-provider
// binding. It is the SaaS startup gate; ordinary unsigned HealthCheck remains
// available to deployment liveness probes.
func (c *Client) ReadyAuthenticated(ctx context.Context, expectedProvider string) error {
	if c == nil || c.auth == nil {
		return errWorkerAuthConfiguration
	}
	if expectedProvider != "idealab:text-embedding-3-large" &&
		expectedProvider != "openai:text-embedding-3-large" {
		return errWorkerAuthConfiguration
	}
	if err := c.ensureDialed(); err != nil {
		return err
	}
	request := &workerpb.HealthCheckRequest{}
	signed, proof, err := c.auth.signedContext(
		ctx,
		workerpb.ProcessorService_HealthCheck_FullMethodName,
		readinessScopePrefix+expectedProvider,
		request,
	)
	if err != nil {
		return err
	}
	var trailer metadata.MD
	response, err := c.stub.HealthCheck(signed, request, grpc.Trailer(&trailer))
	if err != nil {
		return fmt.Errorf("workerclient: authenticated readiness: %w", err)
	}
	if err := c.auth.verifyResponse(proof, response, trailer); err != nil {
		return err
	}
	if response.Status != workerpb.HealthCheckResponse_SERVING {
		return errors.New("workerclient: authenticated Worker is not ready")
	}
	return nil
}

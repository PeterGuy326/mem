package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNormalizeMutationHashIgnoresActorAndAuthorizationEnvelope(t *testing.T) {
	workspaceID, memoryID := uuid.New(), uuid.New()
	userA, userB := uuid.New(), uuid.New()
	tokenA, tokenB := uuid.New(), uuid.New()
	first, err := normalizeMutationBase(
		workspaceID,
		memoryID,
		[]string{"/Work/contracts", "/Work"},
		&userA,
		&tokenA,
		FeedbackPin,
		"stable-key",
		1,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalizeMutationBase(
		workspaceID,
		memoryID,
		[]string{"/Work"},
		&userB,
		&tokenB,
		FeedbackPin,
		"stable-key",
		1,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.requestSHA256 != second.requestSHA256 {
		t.Fatalf("actor/token rotation changed request hash:\n%s\n%s",
			first.requestSHA256, second.requestSHA256)
	}
	if first.actorUserID == nil || *first.actorUserID != userA {
		t.Fatalf("actor provenance lost: %+v", first)
	}
	if first.replayPrincipalSHA256 == second.replayPrincipalSHA256 {
		t.Fatal("different principals produced the same forget replay receipt")
	}
	rotatedToken, err := normalizeMutationBase(
		workspaceID,
		memoryID,
		[]string{"/Work"},
		&userA,
		&tokenB,
		FeedbackPin,
		"stable-key",
		1,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.replayPrincipalSHA256 != rotatedToken.replayPrincipalSHA256 {
		t.Fatal("token rotation changed the stable-user replay receipt")
	}

	changed, err := normalizeMutationBase(
		workspaceID,
		memoryID,
		[]string{"/Work"},
		&userA,
		&tokenA,
		FeedbackUnpin,
		"stable-key",
		1,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if changed.requestSHA256 == first.requestSHA256 {
		t.Fatal("different action produced identical request hash")
	}
}

func TestForgetRequiresReplayPrincipal(t *testing.T) {
	service := &Service{}
	command := ForgetCommand{
		LifecycleCommand: LifecycleCommand{
			WorkspaceID:     uuid.New(),
			MemoryID:        uuid.New(),
			IdempotencyKey:  "forget-key",
			ExpectedVersion: 1,
		},
	}
	_, err := service.Forget(t.Context(), command)
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("error = %v, want ErrInvalidCommand", err)
	}
	actorUserID := uuid.New()
	command.ActorUserID = &actorUserID
	_, err = service.Forget(t.Context(), command)
	if errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("actor user with no token was rejected: %v", err)
	}
}

func TestNormalizeMutationValidation(t *testing.T) {
	workspaceID, memoryID := uuid.New(), uuid.New()
	tests := []struct {
		name      string
		workspace uuid.UUID
		memory    uuid.UUID
		paths     []string
		key       string
		version   int64
	}{
		{"workspace", uuid.Nil, memoryID, nil, "key", 1},
		{"memory", workspaceID, uuid.Nil, nil, "key", 1},
		{"path", workspaceID, memoryID, []string{""}, "key", 1},
		{"key", workspaceID, memoryID, nil, "", 1},
		{"key too long", workspaceID, memoryID, nil, strings.Repeat("界", 201), 1},
		{"version", workspaceID, memoryID, nil, "key", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeMutationBase(
				tc.workspace,
				tc.memory,
				tc.paths,
				nil,
				nil,
				FeedbackUseful,
				tc.key,
				tc.version,
				"",
			)
			if !errors.Is(err, ErrInvalidCommand) {
				t.Fatalf("error = %v, want ErrInvalidCommand", err)
			}
		})
	}
}

func TestValidateTransitionStateMachine(t *testing.T) {
	active := Memory{LifecycleStatus: StatusActive}
	archived := Memory{LifecycleStatus: StatusArchived}
	forgotten := Memory{LifecycleStatus: StatusForgotten}
	if err := validateTransition(active, FeedbackPin); err != nil {
		t.Fatal(err)
	}
	active.Pinned = true
	if err := validateTransition(active, FeedbackPin); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("double pin error = %v", err)
	}
	if err := validateTransition(active, FeedbackUnpin); err != nil {
		t.Fatal(err)
	}
	if err := validateTransition(active, "archive"); err != nil {
		t.Fatal(err)
	}
	if err := validateTransition(archived, "restore"); err != nil {
		t.Fatal(err)
	}
	if err := validateTransition(archived, "forget"); err != nil {
		t.Fatal(err)
	}
	if err := validateTransition(forgotten, FeedbackUseful); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("forgotten feedback error = %v", err)
	}
}

func TestMemoryEventJSONHidesMutationSecrets(t *testing.T) {
	tokenID := uuid.New()
	encoded, err := json.Marshal(MemoryEvent{
		ID:                    uuid.New(),
		WorkspaceID:           uuid.New(),
		MemoryID:              uuid.New(),
		Action:                FeedbackUseful,
		ActorTokenID:          &tokenID,
		IdempotencyKeySHA256:  strings.Repeat("b", 64),
		RequestSHA256:         strings.Repeat("a", 64),
		ReplayPrincipalSHA256: strings.Repeat("c", 64),
		CreatedAt:             time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	for _, forbidden := range []string{
		"actor_token_id",
		"idempotency_key_sha256",
		"request_sha256",
		"replay_principal_sha256",
		"secret-key",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("event JSON leaked %q: %s", forbidden, raw)
		}
	}
}

func TestTombstoneContainsNoLivePayload(t *testing.T) {
	record := Memory{
		ID:              uuid.New(),
		WorkspaceID:     uuid.New(),
		Path:            "/",
		Kind:            KindForgotten,
		Content:         "sensitive",
		SourceRef:       "agent://secret",
		LifecycleStatus: StatusForgotten,
		StateVersion:    4,
	}
	tombstone := tombstoneFromMemory(record)
	encoded, err := json.Marshal(tombstone)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	if strings.Contains(raw, "sensitive") || strings.Contains(raw, "agent://secret") {
		t.Fatalf("tombstone leaked payload: %s", raw)
	}
	for _, forbidden := range []string{"workspace_id", `"path"`, `"kind"`} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("minimal tombstone exposed %q: %s", forbidden, raw)
		}
	}
}

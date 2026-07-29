// Package managedusage reserves auditable managed-AI usage before a Worker
// call can be made. It is intentionally transport-agnostic: HTTP handlers,
// queue consumers, and future batch jobs can all use the same small protocol.
//
// Callers provide only stable file/content identities, a server-resolved
// profile snapshot, and the stages that will actually be invoked. Raw content,
// client idempotency keys, credentials, provider URLs, prompts, vectors, and
// upstream replies do not enter this package or the entitlement ledger.
package managedusage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/entitlement"
)

const (
	// Contract identifies the canonical, hashed reservation identity. Bump it
	// if any field or its meaning changes so old and new calls cannot collide.
	Contract = "mem.managed-ai-usage/v1"

	// Every exact managed stage currently consumes one unit. Keeping the value
	// server-owned prevents a caller from choosing an arbitrary billable unit
	// count through an indexing or worker options payload.
	unitsPerStage int64 = 1
)

var (
	// ErrInvalidCommand never includes a caller-supplied field in its message.
	// Callers may have accidentally passed source content or credentials.
	ErrInvalidCommand = errors.New("invalid managed AI usage command")

	// ErrInvalidProfile means the supplied immutable profile snapshot does not
	// match the compiled catalog. Accounting fails closed rather than allowing
	// a stale or arbitrary provider/model route.
	ErrInvalidProfile = errors.New("invalid managed AI usage profile")

	// ErrInvalidStage means the caller named an unsupported, duplicate, or
	// profile-incompatible stage/provider pair.
	ErrInvalidStage = errors.New("invalid managed AI usage stage")

	// ErrEntitlementUnavailable is returned only when a managed stage needs
	// accounting but no entitlement implementation has been configured. Keep
	// the entitlement sentinel so callers can apply the existing operational
	// readiness/error policy without a second error taxonomy.
	ErrEntitlementUnavailable = entitlement.ErrEntitlementUnavailable

	// ErrInvalidReservation protects the Worker boundary from a broken ledger
	// adapter that claims a stage was reserved without a usable reservation.
	ErrInvalidReservation = errors.New("invalid managed AI usage reservation")
)

// Stage identifies a bounded model-bearing capability. It deliberately uses
// stable server-owned names instead of accepting arbitrary operation strings.
type Stage string

const (
	StageEmbedding       Stage = "embedding"
	StageVisualEmbedding Stage = "visual_embedding"
	StageLLM             Stage = "llm"
	StageVLM             Stage = "vlm"
	StageASR             Stage = "asr"
	StageRerank          Stage = "rerank"
)

var stageOrder = []Stage{
	StageEmbedding,
	StageVisualEmbedding,
	StageLLM,
	StageVLM,
	StageASR,
	StageRerank,
}

// StageSpec describes one stage that the caller has proved may be invoked for
// this particular file/content unit. An empty ProviderSpec means that stage is
// disabled for this invocation and is intentionally ignored.
//
// The provider must exactly match the compiled profile stage. That rejects
// both a quality-profile downgrade and arbitrary OpenAI-compatible routing.
type StageSpec struct {
	Stage        Stage
	ProviderSpec string
}

// Command is the complete, non-secret identity for one pre-invocation usage
// decision. ContentSHA256 is a precomputed, canonical lowercase SHA-256 hex
// digest; callers must never put raw content in this field.
type Command struct {
	WorkspaceID uuid.UUID
	FileID      uuid.UUID

	ContentSHA256   string
	ProfileID       string
	ProfileRevision string

	Stages []StageSpec
}

// Ledger is the small entitlement contract managedusage needs. entitlement.Service
// implements it directly; keeping the interface here makes the package usable
// in workers and straightforward to test without a database.
type Ledger interface {
	Reserve(context.Context, entitlement.ReserveCommand) (*entitlement.Reservation, error)
	Finalize(context.Context, uuid.UUID, []entitlement.ReplayReference) (entitlement.Summary, error)
	Release(context.Context, uuid.UUID) (entitlement.Summary, error)
	MarkIndeterminate(context.Context, uuid.UUID) (entitlement.Summary, error)
}

// Service performs all reserve decisions before its caller may invoke a
// managed Worker stage.
type Service struct {
	ledger Ledger
}

// New constructs a usage coordinator. A nil ledger is permitted only for a
// local/no-stage command; a command that resolves to a managed stage fails
// closed with ErrEntitlementUnavailable.
func New(ledger Ledger) *Service {
	return &Service{ledger: ledger}
}

// StageReservation is the safe, immutable accounting result for one stage.
// ProviderSpec is a compiled catalog identifier, never a URL or credential.
type StageReservation struct {
	Stage        Stage
	ProviderSpec string
	UsageID      uuid.UUID
	Replayed     bool
}

// Handle owns every reservation made before one Worker invocation. It is safe
// to call its completion method more than once because entitlement transitions
// are idempotent for the same target state.
//
// A replayed reservation proves a prior invocation already succeeded. Callers
// must not invoke a Worker stage through this handle when HasReplay is true;
// instead, ReleaseUninvoked releases any newly-created sibling reservations.
type Handle struct {
	ledger       Ledger
	reservations []StageReservation

	mu      sync.Mutex
	settled map[uuid.UUID]struct{}
}

// Prepare validates the server-owned profile/stage route, then reserves every
// managed stage in canonical order before returning a Handle. A caller must
// not invoke its Worker until this method returns a nil error.
//
// When a later stage cannot reserve, Prepare releases earlier newly-created
// reservations because no Worker invocation has begun. If that cleanup cannot
// be proven, it returns the handle as well as the error so the caller can retry
// ReleaseUninvoked; it must not invoke the Worker in either case.
func (s *Service) Prepare(ctx context.Context, command Command) (*Handle, error) {
	stages, err := normalize(command)
	if err != nil {
		return nil, err
	}
	if len(stages) == 0 {
		return &Handle{}, nil
	}
	if s == nil || s.ledger == nil {
		return nil, ErrEntitlementUnavailable
	}

	handle := &Handle{
		ledger:       s.ledger,
		reservations: make([]StageReservation, 0, len(stages)),
		settled:      make(map[uuid.UUID]struct{}, len(stages)),
	}
	for _, stage := range stages {
		reserve, err := reserveCommand(command, stage)
		if err != nil {
			return nil, err
		}
		reservation, err := s.ledger.Reserve(ctx, reserve)
		if err != nil {
			return releaseAfterPrepareFailure(ctx, handle, err)
		}
		if err := validateReservation(reservation); err != nil {
			return releaseAfterPrepareFailure(ctx, handle, err)
		}
		handle.reservations = append(handle.reservations, StageReservation{
			Stage:        stage.Stage,
			ProviderSpec: stage.ProviderSpec,
			UsageID:      reservation.ID,
			Replayed:     reservation.Replayed,
		})
	}
	return handle, nil
}

// PrepareEmbeddingProbe reserves the one managed embedding call made while a
// workspace activates a profile. Unlike file indexing, the probe has no user
// file; its idempotency identity is the immutable workspace/profile revision
// tuple plus the exact catalog embedding stage. A completed reservation is
// therefore safe proof for a later reselect to skip the duplicate probe.
func (s *Service) PrepareEmbeddingProbe(
	ctx context.Context,
	workspaceID uuid.UUID,
	definition aiprofile.Definition,
) (aiprofile.ManagedProbeReservation, error) {
	catalog, ok := aiprofile.Find(definition.ID)
	if workspaceID == uuid.Nil || !ok || !sameDefinition(definition, catalog) {
		return nil, ErrInvalidProfile
	}
	if definition.DataEgress == aiprofile.DataEgressLocalOnly {
		return &Handle{}, nil
	}
	if definition.DataEgress != aiprofile.DataEgressManagedIdealab ||
		!definition.Embedding.Enabled || !isManagedProvider(definition.Embedding.Provider) {
		return nil, ErrInvalidProfile
	}
	if s == nil || s.ledger == nil {
		return nil, ErrEntitlementUnavailable
	}
	command, err := profileProbeReserveCommand(workspaceID, definition)
	if err != nil {
		return nil, err
	}
	reservation, err := s.ledger.Reserve(ctx, command)
	if err != nil {
		return nil, err
	}
	if err := validateReservation(reservation); err != nil {
		if reservation == nil || reservation.ID == uuid.Nil {
			return nil, err
		}
		handle := &Handle{
			ledger: s.ledger,
			reservations: []StageReservation{{
				Stage:        StageEmbedding,
				ProviderSpec: definition.Embedding.Provider,
				UsageID:      reservation.ID,
				Replayed:     reservation.Replayed,
			}},
			settled: make(map[uuid.UUID]struct{}, 1),
		}
		return releaseAfterPrepareFailure(ctx, handle, err)
	}
	handle := &Handle{
		ledger: s.ledger,
		reservations: []StageReservation{{
			Stage:        StageEmbedding,
			ProviderSpec: definition.Embedding.Provider,
			UsageID:      reservation.ID,
			Replayed:     reservation.Replayed,
		}},
		settled: make(map[uuid.UUID]struct{}, 1),
	}
	return handle, nil
}

func releaseAfterPrepareFailure(ctx context.Context, handle *Handle, cause error) (*Handle, error) {
	if handle == nil || len(handle.pending()) == 0 {
		return nil, cause
	}
	if cleanupErr := handle.ReleaseUninvoked(ctx); cleanupErr != nil {
		return handle, errors.Join(cause, cleanupErr)
	}
	return nil, cause
}

// Reservations returns a defensive copy of the safe stage reservation list.
func (h *Handle) Reservations() []StageReservation {
	if h == nil {
		return []StageReservation{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.reservations) == 0 {
		return []StageReservation{}
	}
	return append([]StageReservation(nil), h.reservations...)
}

// HasManagedStages reports whether any stage needed managed accounting.
func (h *Handle) HasManagedStages() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.reservations) > 0
}

// HasReplay reports whether any stage was already finalized by a previous,
// identical invocation. A caller must not start a new Worker call when true.
func (h *Handle) HasReplay() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, reservation := range h.reservations {
		if reservation.Replayed {
			return true
		}
	}
	return false
}

// Finalize records known successful completion for every newly-reserved stage.
// It stores no replay payload because file indexing has no safe replay result
// to persist. The caller must use this only after the Worker outcome is known.
func (h *Handle) Finalize(ctx context.Context) error {
	if h == nil {
		return ErrInvalidReservation
	}
	return h.transitionAll(ctx, func(ctx context.Context, usageID uuid.UUID) error {
		_, err := h.ledger.Finalize(ctx, usageID, nil)
		return err
	})
}

// ReleaseUninvoked releases every newly-reserved stage only when the caller
// can prove that no Worker invocation began (for example, before dispatch or
// after a reservation failure). It must never be used after a timeout or an
// ambiguous Worker/network failure; use MarkIndeterminate in that case.
func (h *Handle) ReleaseUninvoked(ctx context.Context) error {
	if h == nil {
		return ErrInvalidReservation
	}
	return h.transitionAll(ctx, func(ctx context.Context, usageID uuid.UUID) error {
		_, err := h.ledger.Release(ctx, usageID)
		return err
	})
}

// MarkIndeterminate retains all newly-reserved units when a Worker/network
// failure makes it impossible to prove whether a managed provider ran.
func (h *Handle) MarkIndeterminate(ctx context.Context) error {
	if h == nil {
		return ErrInvalidReservation
	}
	return h.transitionAll(ctx, func(ctx context.Context, usageID uuid.UUID) error {
		_, err := h.ledger.MarkIndeterminate(ctx, usageID)
		return err
	})
}

func (h *Handle) transitionAll(
	ctx context.Context,
	transition func(context.Context, uuid.UUID) error,
) error {
	pending := h.pending()
	if len(pending) == 0 {
		return nil
	}
	if h.ledger == nil {
		return ErrEntitlementUnavailable
	}
	var errs []error
	for _, reservation := range pending {
		if err := transition(ctx, reservation.UsageID); err != nil {
			errs = append(errs, err)
			continue
		}
		h.markSettled(reservation.UsageID)
	}
	return errors.Join(errs...)
}

func (h *Handle) pending() []StageReservation {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	pending := make([]StageReservation, 0, len(h.reservations))
	for _, reservation := range h.reservations {
		if !reservation.Replayed {
			if _, settled := h.settled[reservation.UsageID]; settled {
				continue
			}
			pending = append(pending, reservation)
		}
	}
	return pending
}

func (h *Handle) markSettled(usageID uuid.UUID) {
	if h == nil || usageID == uuid.Nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.settled == nil {
		h.settled = make(map[uuid.UUID]struct{})
	}
	h.settled[usageID] = struct{}{}
}

type normalizedStage struct {
	Stage        Stage
	ProviderSpec string
}

func normalize(command Command) ([]normalizedStage, error) {
	definition, err := validateCommand(command)
	if err != nil {
		return nil, err
	}

	byStage := make(map[Stage]string, len(command.Stages))
	for _, input := range command.Stages {
		if input.Stage == "" && input.ProviderSpec == "" {
			// A zero StageSpec is a convenient representation of an omitted
			// optional stage. It has no provider call or ledger effect.
			continue
		}
		if !isKnownStage(input.Stage) {
			return nil, ErrInvalidStage
		}
		if _, exists := byStage[input.Stage]; exists {
			return nil, ErrInvalidStage
		}
		if input.ProviderSpec != "" && strings.TrimSpace(input.ProviderSpec) != input.ProviderSpec {
			return nil, ErrInvalidStage
		}
		byStage[input.Stage] = input.ProviderSpec
	}

	stages := make([]normalizedStage, 0, len(byStage))
	for _, stage := range stageOrder {
		providerSpec, specified := byStage[stage]
		if !specified || providerSpec == "" {
			continue
		}
		expected, ok := profileStage(definition, stage)
		if !ok || !expected.Enabled || expected.Provider != providerSpec {
			return nil, ErrInvalidStage
		}
		// This exact catalog predicate is one half of the billing/egress
		// boundary. A managed profile may still contain a local fixed stage
		// such as CLIP visual embedding, so exclude those local runtimes after
		// the catalog check. They are valid but deliberately have no ledger
		// reservation.
		if !isManagedProvider(providerSpec) {
			continue
		}
		stages = append(stages, normalizedStage{Stage: stage, ProviderSpec: providerSpec})
	}
	return stages, nil
}

func isManagedProvider(providerSpec string) bool {
	if !aiprofile.IsManagedCatalogProvider(providerSpec) {
		return false
	}
	vendor, _, ok := strings.Cut(providerSpec, ":")
	if !ok {
		return false
	}
	switch vendor {
	case "ollama", "clip", "faster-whisper", "whisper":
		return false
	default:
		return true
	}
}

func validateCommand(command Command) (aiprofile.Definition, error) {
	if command.WorkspaceID == uuid.Nil || command.FileID == uuid.Nil ||
		!isSHA256(command.ContentSHA256) {
		return aiprofile.Definition{}, ErrInvalidCommand
	}
	definition, ok := aiprofile.Find(command.ProfileID)
	if !ok || command.ProfileRevision != definition.Revision {
		return aiprofile.Definition{}, ErrInvalidProfile
	}
	return definition, nil
}

func sameDefinition(left, right aiprofile.Definition) bool {
	if left.ID != right.ID || left.Revision != right.Revision ||
		left.PipelineRevision != right.PipelineRevision ||
		left.DataEgress != right.DataEgress ||
		left.Embedding != right.Embedding ||
		left.VisualEmbedding != right.VisualEmbedding ||
		left.LLM != right.LLM || left.VLM != right.VLM ||
		left.ASR != right.ASR || left.Rerank != right.Rerank ||
		len(left.AllowedMIMETypes) != len(right.AllowedMIMETypes) {
		return false
	}
	for i := range left.AllowedMIMETypes {
		if left.AllowedMIMETypes[i] != right.AllowedMIMETypes[i] {
			return false
		}
	}
	return true
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isKnownStage(stage Stage) bool {
	for _, known := range stageOrder {
		if stage == known {
			return true
		}
	}
	return false
}

func profileStage(definition aiprofile.Definition, stage Stage) (aiprofile.Stage, bool) {
	switch stage {
	case StageEmbedding:
		return definition.Embedding, true
	case StageVisualEmbedding:
		return definition.VisualEmbedding, true
	case StageLLM:
		return definition.LLM, true
	case StageVLM:
		return definition.VLM, true
	case StageASR:
		return definition.ASR, true
	case StageRerank:
		return definition.Rerank, true
	default:
		return aiprofile.Stage{}, false
	}
}

func reserveCommand(command Command, stage normalizedStage) (entitlement.ReserveCommand, error) {
	identity, err := json.Marshal(reservationIdentity{
		Contract:        Contract,
		WorkspaceID:     command.WorkspaceID.String(),
		FileID:          command.FileID.String(),
		ContentSHA256:   command.ContentSHA256,
		ProfileID:       command.ProfileID,
		ProfileRevision: command.ProfileRevision,
		Stage:           string(stage.Stage),
		ProviderSpec:    stage.ProviderSpec,
		Units:           unitsPerStage,
	})
	if err != nil {
		// reservationIdentity contains only fixed scalar types, but fail closed
		// if that invariant is ever changed.
		return entitlement.ReserveCommand{}, fmt.Errorf("%w: encode identity", ErrInvalidCommand)
	}
	return entitlement.ReserveCommand{
		WorkspaceID:        command.WorkspaceID,
		Operation:          "file.ai." + string(stage.Stage),
		ProviderSpec:       stage.ProviderSpec,
		Units:              unitsPerStage,
		IdempotencyKey:     domainSHA256("mem/managed-ai-usage/idempotency/v1", identity),
		RequestFingerprint: domainSHA256("mem/managed-ai-usage/fingerprint/v1", identity),
	}, nil
}

func profileProbeReserveCommand(
	workspaceID uuid.UUID,
	definition aiprofile.Definition,
) (entitlement.ReserveCommand, error) {
	identity, err := json.Marshal(profileProbeIdentity{
		Contract:        Contract,
		WorkspaceID:     workspaceID.String(),
		ProfileID:       definition.ID,
		ProfileRevision: definition.Revision,
		Stage:           string(StageEmbedding),
		ProviderSpec:    definition.Embedding.Provider,
		Dimensions:      definition.Embedding.Dimensions,
		Units:           unitsPerStage,
	})
	if err != nil {
		return entitlement.ReserveCommand{}, fmt.Errorf("%w: encode probe identity", ErrInvalidCommand)
	}
	return entitlement.ReserveCommand{
		WorkspaceID:        workspaceID,
		Operation:          "profile.ai_probe.embedding",
		ProviderSpec:       definition.Embedding.Provider,
		Units:              unitsPerStage,
		IdempotencyKey:     domainSHA256("mem/managed-ai-usage/profile-probe/idempotency/v1", identity),
		RequestFingerprint: domainSHA256("mem/managed-ai-usage/profile-probe/fingerprint/v1", identity),
	}, nil
}

type reservationIdentity struct {
	Contract        string `json:"contract"`
	WorkspaceID     string `json:"workspace_id"`
	FileID          string `json:"file_id"`
	ContentSHA256   string `json:"content_sha256"`
	ProfileID       string `json:"profile_id"`
	ProfileRevision string `json:"profile_revision"`
	Stage           string `json:"stage"`
	ProviderSpec    string `json:"provider_spec"`
	Units           int64  `json:"units"`
}

type profileProbeIdentity struct {
	Contract        string `json:"contract"`
	WorkspaceID     string `json:"workspace_id"`
	ProfileID       string `json:"profile_id"`
	ProfileRevision string `json:"profile_revision"`
	Stage           string `json:"stage"`
	ProviderSpec    string `json:"provider_spec"`
	Dimensions      int    `json:"dimensions"`
	Units           int64  `json:"units"`
}

func domainSHA256(domain string, payload []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(payload)
	return hex.EncodeToString(hash.Sum(nil))
}

func validateReservation(reservation *entitlement.Reservation) error {
	if reservation == nil || reservation.ID == uuid.Nil {
		return ErrInvalidReservation
	}
	if reservation.Replayed {
		if reservation.Status != entitlement.StatusSucceeded {
			return ErrInvalidReservation
		}
		return nil
	}
	if reservation.Status != entitlement.StatusReserved {
		return ErrInvalidReservation
	}
	return nil
}

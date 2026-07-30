package aiprofile

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeterGuy326/mem/server/internal/indexmeta"
)

var (
	// ErrNotFound means this workspace has not explicitly selected a fixed
	// profile.  Callers can then decide whether a legacy local path is allowed.
	ErrNotFound = errors.New("workspace AI profile not found")

	// ErrProfileDisabled means an operator did not enable this compiled profile
	// for the running deployment.
	ErrProfileDisabled = errors.New("workspace AI profile is not enabled")

	// ErrStoreUnavailable prevents profile selection when persistence has not
	// been wired.  It is deliberately fail-closed before a probe/provider call.
	ErrStoreUnavailable = errors.New("workspace AI profile store unavailable")

	// ErrProbeUnavailable prevents selecting a profile whose required 768-d
	// embedding capability has not been verified by the Worker.
	ErrProbeUnavailable = errors.New("workspace AI profile probe unavailable")

	// ErrEmbeddingProbeFailed deliberately has no upstream detail.  Gateways
	// can echo authorization headers or request bodies in diagnostic text.
	ErrEmbeddingProbeFailed = errors.New("workspace AI profile embedding probe failed")

	// ErrEmbeddingDimensionMismatch means the Worker did not return exactly
	// the profile's fixed embedding size.
	ErrEmbeddingDimensionMismatch = errors.New("workspace AI profile embedding dimension mismatch")

	// ErrManagedUsageUnavailable prevents selecting a managed profile when the
	// server cannot reserve its paid embedding preflight. The probe itself is a
	// real provider invocation and must not bypass the same accounting boundary
	// as indexing and query embedding.
	ErrManagedUsageUnavailable = errors.New("workspace AI profile managed usage is unavailable")

	// ErrInvalidSelection means a persisted snapshot no longer matches the
	// compiled, server-owned profile definition. Treat it as a control-plane
	// integrity failure: callers must never route a Worker request using a
	// provider/model string merely because it happened to be stored in SQL.
	ErrInvalidSelection = errors.New("workspace AI profile selection is invalid")

	// ErrWorkspaceNotFound means the selection target no longer has a
	// workspace row. Selection must not infer an owner from caller input.
	ErrWorkspaceNotFound = errors.New("workspace AI profile workspace not found")

	// ErrProfileIndexingInFlight prevents an online profile switch while an
	// existing file is pending or being indexed. A generation migration is
	// required before a corpus can safely move to a different pipeline.
	ErrProfileIndexingInFlight = errors.New("workspace AI profile switch blocked by indexing in flight")

	// ErrProfileCorpusIdentityUnknown prevents selection when legacy or mixed
	// vectors cannot prove a single embedding provider identity.
	ErrProfileCorpusIdentityUnknown = errors.New("workspace AI profile corpus embedding identity is not verified")

	// ErrProfileCorpusMismatch prevents a profile from changing an existing
	// corpus's embedding space, even when both providers happen to return 768
	// dimensions. #55's versioned-index generation flow owns that migration.
	ErrProfileCorpusMismatch = errors.New("workspace AI profile embedding provider conflicts with active corpus")

	ErrWorkspaceRequired = errors.New("workspace ID is required")
	ErrActorRequired     = errors.New("profile selection actor is required")
)

// EmbeddingProbe is intentionally narrow and provider-agnostic.  The future
// workerclient adapter must pass the requested dimensions to the compatible
// embedding endpoint, then return the actual vector length.  This package
// never receives API keys, base URLs, raw vectors, or raw provider replies.
type EmbeddingProbe interface {
	ProbeEmbedding(context.Context, string, int) (int, error)
}

// ManagedProbeReservation owns a paid profile preflight reservation. It is
// deliberately expressed in terms of lifecycle semantics rather than a
// billing implementation so this package remains free of entitlement or
// credential details.
type ManagedProbeReservation interface {
	HasReplay() bool
	Finalize(context.Context) error
	ReleaseUninvoked(context.Context) error
	MarkIndeterminate(context.Context) error
}

// ManagedProbeUsage reserves the exact compiled embedding probe before the
// Worker is asked to call it. Local profiles return a no-op reservation.
type ManagedProbeUsage interface {
	PrepareEmbeddingProbe(context.Context, uuid.UUID, Definition) (ManagedProbeReservation, error)
}

// Selection is the persisted, harmless snapshot of a profile active in one
// workspace.  It is copied from a compiled Definition at selection time so a
// later source-code catalog change cannot reinterpret historical state.
type Selection struct {
	WorkspaceID      uuid.UUID  `json:"workspace_id"`
	ProfileID        string     `json:"profile_id"`
	ProfileRevision  string     `json:"profile_revision"`
	PipelineRevision string     `json:"pipeline_revision"`
	Embedding        Stage      `json:"embedding"`
	VisualEmbedding  Stage      `json:"visual_embedding"`
	LLM              Stage      `json:"llm"`
	VLM              Stage      `json:"vlm"`
	ASR              Stage      `json:"asr"`
	Rerank           Stage      `json:"rerank"`
	DataEgress       string     `json:"data_egress"`
	AllowedMIMETypes []string   `json:"allowed_mime_types"`
	SelectedByUserID *uuid.UUID `json:"selected_by_user_id,omitempty"`
	SelectedAt       time.Time  `json:"selected_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// Service stores and resolves workspace selections.  It deliberately does
// not decide membership/role authorization; the HTTP layer resolves that
// before calling Select and supplies the authenticated actor ID.
type Service struct {
	store      selectionStore
	probe      EmbeddingProbe
	probeUsage ManagedProbeUsage
	enabled    map[string]struct{}
	now        func() time.Time
}

// New constructs a profile selection service.  Passing no enabled IDs enables
// every compiled profile; deployments that need a tighter policy can pass the
// exact allowlisted IDs.  A nil probe intentionally leaves Select fail-closed
// until production wires a Worker adapter.
func New(pool *pgxpool.Pool, probe EmbeddingProbe, enabledIDs ...string) *Service {
	var store selectionStore
	if pool != nil {
		store = &postgresStore{pool: pool}
	}
	return &Service{
		store:   store,
		probe:   probe,
		enabled: enabledSet(enabledIDs),
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// SetManagedProbeUsage installs the paid-preflight reservation boundary. It
// is optional for purely local profiles but required before a managed profile
// can be selected.
func (s *Service) SetManagedProbeUsage(usage ManagedProbeUsage) {
	if s == nil {
		return
	}
	s.probeUsage = usage
}

// List returns the enabled, server-allowlisted definitions in stable catalog
// order.  Returned definitions are defensive copies.
func (s *Service) List() []Definition {
	if s == nil {
		return []Definition{}
	}
	out := make([]Definition, 0, len(compiledCatalog))
	for _, definition := range compiledCatalog {
		if s.isEnabled(definition.ID) {
			out = append(out, cloneDefinition(definition))
		}
	}
	return out
}

// Get returns the selected snapshot for a workspace.
func (s *Service) Get(ctx context.Context, workspaceID uuid.UUID) (*Selection, error) {
	if workspaceID == uuid.Nil {
		return nil, ErrWorkspaceRequired
	}
	if s == nil || s.store == nil {
		return nil, ErrStoreUnavailable
	}
	selection, err := s.store.get(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return cloneSelection(selection), nil
}

// ResolveForOwner returns the selected snapshot for the workspace physically
// owned by ownerID.  mem's current data model has one workspace per resource
// owner, making this the safe bridge for the indexer/search services that own
// a file user ID rather than an HTTP workspace object.
func (s *Service) ResolveForOwner(ctx context.Context, ownerID uuid.UUID) (*Selection, error) {
	if ownerID == uuid.Nil {
		return nil, ErrWorkspaceRequired
	}
	if s == nil || s.store == nil {
		return nil, ErrStoreUnavailable
	}
	selection, err := s.store.resolveForOwner(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	return cloneSelection(selection), nil
}

// Select verifies the exact catalog embedding contract before atomically
// upserting the workspace snapshot.  It accepts only a stable catalog ID;
// callers cannot pass credentials, provider URLs, arbitrary model IDs, or
// prompts through this method.
func (s *Service) Select(
	ctx context.Context,
	workspaceID, actorID uuid.UUID,
	profileID string,
) (*Selection, error) {
	if workspaceID == uuid.Nil {
		return nil, ErrWorkspaceRequired
	}
	if actorID == uuid.Nil {
		return nil, ErrActorRequired
	}
	if s == nil || s.store == nil {
		return nil, ErrStoreUnavailable
	}
	definition, err := s.enabledDefinition(profileID)
	if err != nil {
		return nil, err
	}
	ownerID, err := s.store.workspaceOwner(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if ownerID == uuid.Nil {
		return nil, ErrWorkspaceNotFound
	}

	// The lock is keyed by the resource owner because indexing has always
	// used that identity for files and corpus provenance. Hold it across the
	// compatibility gate, paid probe, and snapshot upsert so no in-process
	// index job can begin between validation and activation.
	unlockSwitch := indexmeta.LockProviderSwitch(ownerID)
	defer unlockSwitch()
	if err := s.requireCompatibleCorpus(ctx, ownerID, definition.Embedding.Provider); err != nil {
		return nil, err
	}
	probeReservation, err := s.prepareManagedProbe(ctx, workspaceID, definition)
	if err != nil {
		return nil, err
	}
	if probeReservation != nil && probeReservation.HasReplay() {
		// A completed reservation is durable evidence that this exact
		// workspace/profile/revision probe already returned the required
		// dimensions. Never duplicate the paid provider call on reselect.
		return s.saveSelection(ctx, definition, workspaceID, actorID)
	}
	completed, probeErr := s.probeEmbedding(ctx, definition.Embedding)
	if probeReservation != nil {
		if probeErr != nil {
			// Do not finalize a wrong-dimension result as a replayable success:
			// a later selection must never mistake that failed contract check for
			// proof that 768 dimensions were returned. Retain it conservatively
			// until the managed-usage reconciler/operator resolves the outcome.
			if err := markManagedProbeIndeterminate(ctx, probeReservation); err != nil {
				return nil, ErrManagedUsageUnavailable
			}
		} else if completed {
			if err := finalizeManagedProbe(ctx, probeReservation); err != nil {
				_ = markManagedProbeIndeterminate(ctx, probeReservation)
				return nil, ErrManagedUsageUnavailable
			}
		} else if err := markManagedProbeIndeterminate(ctx, probeReservation); err != nil {
			return nil, ErrManagedUsageUnavailable
		}
	}
	if probeErr != nil {
		return nil, probeErr
	}
	return s.saveSelection(ctx, definition, workspaceID, actorID)
}

func (s *Service) saveSelection(
	ctx context.Context,
	definition Definition,
	workspaceID, actorID uuid.UUID,
) (*Selection, error) {
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	snapshot := selectionFromDefinition(definition, workspaceID, actorID, now)
	selection, err := s.store.save(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	return cloneSelection(selection), nil
}

func (s *Service) enabledDefinition(profileID string) (Definition, error) {
	definition, ok := Find(profileID)
	if !ok {
		return Definition{}, ErrUnknownProfile
	}
	if s == nil || !s.isEnabled(profileID) {
		return Definition{}, ErrProfileDisabled
	}
	if err := definition.Validate(); err != nil {
		return Definition{}, ErrInvalidDefinition
	}
	return definition, nil
}

func (s *Service) isEnabled(profileID string) bool {
	if s == nil {
		return false
	}
	_, ok := s.enabled[profileID]
	return ok
}

// probeEmbedding reports completion independently from profile validity. The
// caller may finalize only a valid response; a wrong-dimension reply remains
// conservatively indeterminate because the ledger's replay contract cannot
// encode a failed dimension assertion.
func (s *Service) probeEmbedding(ctx context.Context, stage Stage) (bool, error) {
	if !stage.Enabled || stage.Provider == "" || stage.Dimensions <= 0 {
		return false, ErrInvalidDefinition
	}
	if s == nil || s.probe == nil {
		return false, ErrProbeUnavailable
	}
	dimension, err := s.probe.ProbeEmbedding(ctx, stage.Provider, stage.Dimensions)
	if err != nil {
		return false, ErrEmbeddingProbeFailed
	}
	if dimension != stage.Dimensions {
		return true, ErrEmbeddingDimensionMismatch
	}
	return true, nil
}

func (s *Service) prepareManagedProbe(
	ctx context.Context,
	workspaceID uuid.UUID,
	definition Definition,
) (ManagedProbeReservation, error) {
	if definition.DataEgress != DataEgressManagedIdealab {
		return nil, nil
	}
	if s == nil || s.probeUsage == nil {
		return nil, ErrManagedUsageUnavailable
	}
	reservation, err := s.probeUsage.PrepareEmbeddingProbe(ctx, workspaceID, definition)
	if err == nil {
		return reservation, nil
	}
	if reservation != nil {
		// A returned handle means reservation cleanup needs another safe,
		// no-invocation release attempt. The Worker has not been called.
		_ = releaseManagedProbe(ctx, reservation)
	}
	return nil, ErrManagedUsageUnavailable
}

const managedProbeSettlementTimeout = 5 * time.Second

func finalizeManagedProbe(ctx context.Context, reservation ManagedProbeReservation) error {
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managedProbeSettlementTimeout)
	defer cancel()
	return reservation.Finalize(settleCtx)
}

func releaseManagedProbe(ctx context.Context, reservation ManagedProbeReservation) error {
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managedProbeSettlementTimeout)
	defer cancel()
	return reservation.ReleaseUninvoked(settleCtx)
}

func markManagedProbeIndeterminate(ctx context.Context, reservation ManagedProbeReservation) error {
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managedProbeSettlementTimeout)
	defer cancel()
	return reservation.MarkIndeterminate(settleCtx)
}

// requireCompatibleCorpus is intentionally checked before the embedding
// probe. A rejected profile switch must not mutate the active snapshot or
// consume a managed-provider request. Matching dimensions alone are not
// enough: provider/model identity defines the vector space.
func (s *Service) requireCompatibleCorpus(
	ctx context.Context,
	ownerID uuid.UUID,
	requestedEmbeddingProvider string,
) error {
	inFlight, err := s.store.hasIndexingInFlight(ctx, ownerID)
	if err != nil {
		return fmt.Errorf("workspace AI profile indexing state: %w", err)
	}
	if inFlight {
		return ErrProfileIndexingInFlight
	}
	corpusProvider, hasCorpus, err := s.store.textCorpusProvider(ctx, ownerID)
	if err != nil {
		if errors.Is(err, indexmeta.ErrUnknownProvider) || errors.Is(err, indexmeta.ErrMixedProviders) {
			return ErrProfileCorpusIdentityUnknown
		}
		return fmt.Errorf("workspace AI profile corpus identity: %w", err)
	}
	if hasCorpus && corpusProvider != requestedEmbeddingProvider {
		return ErrProfileCorpusMismatch
	}
	return nil
}

func enabledSet(ids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(compiledCatalog))
	if len(ids) == 0 {
		for _, definition := range compiledCatalog {
			out[definition.ID] = struct{}{}
		}
		return out
	}
	for _, id := range ids {
		if _, ok := Find(id); ok {
			out[id] = struct{}{}
		}
	}
	return out
}

func selectionFromDefinition(
	definition Definition,
	workspaceID, actorID uuid.UUID,
	at time.Time,
) Selection {
	actor := actorID
	return Selection{
		WorkspaceID:      workspaceID,
		ProfileID:        definition.ID,
		ProfileRevision:  definition.Revision,
		PipelineRevision: definition.PipelineRevision,
		Embedding:        definition.Embedding,
		VisualEmbedding:  definition.VisualEmbedding,
		LLM:              definition.LLM,
		VLM:              definition.VLM,
		ASR:              definition.ASR,
		Rerank:           definition.Rerank,
		DataEgress:       definition.DataEgress,
		AllowedMIMETypes: slicesClone(definition.AllowedMIMETypes),
		SelectedByUserID: &actor,
		SelectedAt:       at.UTC(),
		UpdatedAt:        at.UTC(),
	}
}

func cloneSelection(selection *Selection) *Selection {
	if selection == nil {
		return nil
	}
	out := *selection
	out.AllowedMIMETypes = slicesClone(selection.AllowedMIMETypes)
	if selection.SelectedByUserID != nil {
		actor := *selection.SelectedByUserID
		out.SelectedByUserID = &actor
	}
	return &out
}

// ValidateSelection verifies that a persisted selection is still an exact
// snapshot of a compiled profile. It is deliberately stricter than checking
// only vector dimensions: a same-sized arbitrary model would still create a
// different embedding space and could turn a database corruption or stale
// deployment into an unapproved managed-provider call.
//
// A profile revision is immutable. If a deployment removes an old revision,
// the safe behavior is to block routing until an operator performs the
// versioned migration, not to reinterpret old vectors with a new model.
func ValidateSelection(selection *Selection) error {
	if selection == nil || selection.ProfileID == "" ||
		selection.ProfileRevision == "" || selection.PipelineRevision == "" {
		return ErrInvalidSelection
	}
	definition, ok := Find(selection.ProfileID)
	if !ok || definition.ProfileSnapshotMismatch(selection) {
		return ErrInvalidSelection
	}
	return nil
}

func (d Definition) ProfileSnapshotMismatch(selection *Selection) bool {
	if selection == nil {
		return true
	}
	return selection.ProfileRevision != d.Revision ||
		selection.PipelineRevision != d.PipelineRevision ||
		selection.DataEgress != d.DataEgress ||
		selection.Embedding != d.Embedding ||
		selection.VisualEmbedding != d.VisualEmbedding ||
		selection.LLM != d.LLM ||
		selection.VLM != d.VLM ||
		selection.ASR != d.ASR ||
		selection.Rerank != d.Rerank ||
		!slices.Equal(selection.AllowedMIMETypes, d.AllowedMIMETypes)
}

func slicesClone(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

type selectionStore interface {
	get(context.Context, uuid.UUID) (*Selection, error)
	resolveForOwner(context.Context, uuid.UUID) (*Selection, error)
	save(context.Context, Selection) (*Selection, error)
	workspaceOwner(context.Context, uuid.UUID) (uuid.UUID, error)
	hasIndexingInFlight(context.Context, uuid.UUID) (bool, error)
	textCorpusProvider(context.Context, uuid.UUID) (string, bool, error)
}

type postgresStore struct{ pool *pgxpool.Pool }

const selectionColumns = `
	workspace_id,
	profile_id,
	profile_revision,
	pipeline_revision,
	embedding_provider,
	embedding_dimensions,
	visual_embedding_provider,
	visual_embedding_dimensions,
	llm_provider,
	vlm_provider,
	asr_provider,
	rerank_provider,
	data_egress,
	allowed_mime_types,
	selected_by_user_id,
	selected_at,
	updated_at`

func (s *postgresStore) get(ctx context.Context, workspaceID uuid.UUID) (*Selection, error) {
	if s == nil || s.pool == nil {
		return nil, ErrStoreUnavailable
	}
	return scanSelection(s.pool.QueryRow(ctx,
		`SELECT `+selectionColumns+`
		   FROM workspace_ai_profiles
		  WHERE workspace_id = $1`, workspaceID))
}

func (s *postgresStore) resolveForOwner(ctx context.Context, ownerID uuid.UUID) (*Selection, error) {
	if s == nil || s.pool == nil {
		return nil, ErrStoreUnavailable
	}
	return scanSelection(s.pool.QueryRow(ctx,
		`SELECT `+stringsReplaceColumns("p.")+`
		   FROM workspace_ai_profiles p
		   JOIN workspaces w ON w.id = p.workspace_id
		  WHERE w.resource_owner_user_id = $1`, ownerID))
}

func (s *postgresStore) save(ctx context.Context, selection Selection) (*Selection, error) {
	if s == nil || s.pool == nil {
		return nil, ErrStoreUnavailable
	}
	return scanSelection(s.pool.QueryRow(ctx, `
		INSERT INTO workspace_ai_profiles (
			workspace_id,
			profile_id,
			profile_revision,
			pipeline_revision,
			embedding_provider,
			embedding_dimensions,
			visual_embedding_provider,
			visual_embedding_dimensions,
			llm_provider,
			vlm_provider,
			asr_provider,
			rerank_provider,
			data_egress,
			allowed_mime_types,
			selected_by_user_id,
			selected_at,
			updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17
		)
		ON CONFLICT (workspace_id) DO UPDATE SET
			profile_id = EXCLUDED.profile_id,
			profile_revision = EXCLUDED.profile_revision,
			pipeline_revision = EXCLUDED.pipeline_revision,
			embedding_provider = EXCLUDED.embedding_provider,
			embedding_dimensions = EXCLUDED.embedding_dimensions,
			visual_embedding_provider = EXCLUDED.visual_embedding_provider,
			visual_embedding_dimensions = EXCLUDED.visual_embedding_dimensions,
			llm_provider = EXCLUDED.llm_provider,
			vlm_provider = EXCLUDED.vlm_provider,
			asr_provider = EXCLUDED.asr_provider,
			rerank_provider = EXCLUDED.rerank_provider,
			data_egress = EXCLUDED.data_egress,
			allowed_mime_types = EXCLUDED.allowed_mime_types,
			selected_by_user_id = EXCLUDED.selected_by_user_id,
			selected_at = EXCLUDED.selected_at,
			updated_at = EXCLUDED.updated_at
		RETURNING `+selectionColumns,
		selection.WorkspaceID,
		selection.ProfileID,
		selection.ProfileRevision,
		selection.PipelineRevision,
		selection.Embedding.Provider,
		selection.Embedding.Dimensions,
		optionalProvider(selection.VisualEmbedding),
		optionalDimensions(selection.VisualEmbedding),
		optionalProvider(selection.LLM),
		optionalProvider(selection.VLM),
		optionalProvider(selection.ASR),
		optionalProvider(selection.Rerank),
		selection.DataEgress,
		selection.AllowedMIMETypes,
		selection.SelectedByUserID,
		selection.SelectedAt,
		selection.UpdatedAt,
	))
}

func (s *postgresStore) workspaceOwner(ctx context.Context, workspaceID uuid.UUID) (uuid.UUID, error) {
	if s == nil || s.pool == nil {
		return uuid.Nil, ErrStoreUnavailable
	}
	var ownerID uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT resource_owner_user_id FROM workspaces WHERE id = $1`, workspaceID,
	).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrWorkspaceNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("workspace AI profile workspace lookup: %w", err)
	}
	return ownerID, nil
}

func (s *postgresStore) hasIndexingInFlight(ctx context.Context, ownerID uuid.UUID) (bool, error) {
	if s == nil || s.pool == nil {
		return false, ErrStoreUnavailable
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM files
			 WHERE user_id = $1
			   AND index_status IN ('pending', 'processing')
		)`, ownerID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("workspace AI profile indexing lookup: %w", err)
	}
	return exists, nil
}

func (s *postgresStore) textCorpusProvider(
	ctx context.Context,
	ownerID uuid.UUID,
) (string, bool, error) {
	if s == nil || s.pool == nil {
		return "", false, ErrStoreUnavailable
	}
	return indexmeta.TextProvider(ctx, s.pool, ownerID)
}

func optionalProvider(stage Stage) any {
	if !stage.Enabled {
		return nil
	}
	return stage.Provider
}

func optionalDimensions(stage Stage) any {
	if !stage.Enabled {
		return nil
	}
	return stage.Dimensions
}

type scanner interface{ Scan(...any) error }

func scanSelection(row scanner) (*Selection, error) {
	var (
		selection                                                             Selection
		visualProvider, llmProvider, vlmProvider, asrProvider, rerankProvider *string
		visualDimensions                                                      *int
	)
	err := row.Scan(
		&selection.WorkspaceID,
		&selection.ProfileID,
		&selection.ProfileRevision,
		&selection.PipelineRevision,
		&selection.Embedding.Provider,
		&selection.Embedding.Dimensions,
		&visualProvider,
		&visualDimensions,
		&llmProvider,
		&vlmProvider,
		&asrProvider,
		&rerankProvider,
		&selection.DataEgress,
		&selection.AllowedMIMETypes,
		&selection.SelectedByUserID,
		&selection.SelectedAt,
		&selection.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("workspace AI profile store: %w", err)
	}
	selection.Embedding.Enabled = true
	selection.VisualEmbedding = stageFromNullable(visualProvider, visualDimensions)
	selection.LLM = stageFromNullable(llmProvider, nil)
	selection.VLM = stageFromNullable(vlmProvider, nil)
	selection.ASR = stageFromNullable(asrProvider, nil)
	selection.Rerank = stageFromNullable(rerankProvider, nil)
	selection.SelectedAt = selection.SelectedAt.UTC()
	selection.UpdatedAt = selection.UpdatedAt.UTC()
	return &selection, nil
}

func stageFromNullable(provider *string, dimensions *int) Stage {
	if provider == nil {
		return Stage{}
	}
	stage := Stage{Enabled: true, Provider: *provider}
	if dimensions != nil {
		stage.Dimensions = *dimensions
	}
	return stage
}

// stringsReplaceColumns adds a table alias to every selected column without
// duplicating the source-of-truth list used by Get and Save.
func stringsReplaceColumns(prefix string) string {
	parts := make([]string, 0, 17)
	for _, raw := range []string{
		"workspace_id", "profile_id", "profile_revision", "pipeline_revision",
		"embedding_provider", "embedding_dimensions", "visual_embedding_provider",
		"visual_embedding_dimensions", "llm_provider", "vlm_provider", "asr_provider",
		"rerank_provider", "data_egress", "allowed_mime_types", "selected_by_user_id",
		"selected_at", "updated_at",
	} {
		parts = append(parts, prefix+raw)
	}
	return joinColumns(parts)
}

func joinColumns(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, part := range parts[1:] {
		result += ",\n\t" + part
	}
	return result
}

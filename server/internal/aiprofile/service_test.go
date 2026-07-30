package aiprofile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/indexmeta"
)

type fakeSelectionStore struct {
	current    *Selection
	resolved   *Selection
	getErr     error
	resolveErr error
	saveErr    error

	ownerID     uuid.UUID
	ownerErr    error
	inFlight    bool
	inFlightErr error
	corpusSpec  string
	hasCorpus   bool
	corpusErr   error
	hasDerived  bool
	derivedErr  error

	saveCalls int
	saved     []Selection
}

func (s *fakeSelectionStore) get(_ context.Context, _ uuid.UUID) (*Selection, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return cloneSelection(s.current), nil
}

func (s *fakeSelectionStore) resolveForOwner(_ context.Context, _ uuid.UUID) (*Selection, error) {
	if s.resolveErr != nil {
		return nil, s.resolveErr
	}
	return cloneSelection(s.resolved), nil
}

func (s *fakeSelectionStore) save(_ context.Context, selection Selection) (*Selection, error) {
	s.saveCalls++
	s.saved = append(s.saved, *cloneSelection(&selection))
	if s.saveErr != nil {
		return nil, s.saveErr
	}
	return cloneSelection(&selection), nil
}

func (s *fakeSelectionStore) workspaceOwner(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	if s.ownerErr != nil {
		return uuid.Nil, s.ownerErr
	}
	if s.ownerID == uuid.Nil {
		return uuid.Nil, ErrWorkspaceNotFound
	}
	return s.ownerID, nil
}

func (s *fakeSelectionStore) withProfileLock(
	ctx context.Context,
	_ uuid.UUID,
	fn func(uuid.UUID, selectionSnapshotStore) error,
) error {
	ownerID, err := s.workspaceOwner(ctx, uuid.Nil)
	if err != nil {
		return err
	}
	return fn(ownerID, s)
}

func (s *fakeSelectionStore) hasIndexingInFlight(_ context.Context, _ uuid.UUID) (bool, error) {
	return s.inFlight, s.inFlightErr
}

func (s *fakeSelectionStore) textCorpusProvider(
	_ context.Context,
	_ uuid.UUID,
) (string, bool, error) {
	return s.corpusSpec, s.hasCorpus, s.corpusErr
}

func (s *fakeSelectionStore) hasDerivedCorpus(
	_ context.Context,
	_ uuid.UUID,
) (bool, error) {
	return s.hasDerived, s.derivedErr
}

type fakeEmbeddingProbe struct {
	dimension int
	err       error
	calls     int
	spec      string
	wantDim   int
}

type fakeManagedProbeReservation struct {
	replay        bool
	finalizeErr   error
	releaseErr    error
	markErr       error
	finalizeCalls int
	releaseCalls  int
	markCalls     int
}

func (r *fakeManagedProbeReservation) HasReplay() bool { return r.replay }

func (r *fakeManagedProbeReservation) Finalize(_ context.Context) error {
	r.finalizeCalls++
	return r.finalizeErr
}

func (r *fakeManagedProbeReservation) ReleaseUninvoked(_ context.Context) error {
	r.releaseCalls++
	return r.releaseErr
}

func (r *fakeManagedProbeReservation) MarkIndeterminate(_ context.Context) error {
	r.markCalls++
	return r.markErr
}

type fakeManagedProbeUsage struct {
	reservation ManagedProbeReservation
	err         error
	calls       int
	workspaceID uuid.UUID
	definition  Definition
}

func (u *fakeManagedProbeUsage) PrepareEmbeddingProbe(
	_ context.Context,
	workspaceID uuid.UUID,
	definition Definition,
) (ManagedProbeReservation, error) {
	u.calls++
	u.workspaceID = workspaceID
	u.definition = cloneDefinition(definition)
	return u.reservation, u.err
}

func (p *fakeEmbeddingProbe) ProbeEmbedding(
	_ context.Context,
	spec string,
	dimensions int,
) (int, error) {
	p.calls++
	p.spec = spec
	p.wantDim = dimensions
	return p.dimension, p.err
}

type signalingEmbeddingProbe struct {
	dimension int
	entered   chan struct{}
}

func (p *signalingEmbeddingProbe) ProbeEmbedding(
	_ context.Context,
	_ string,
	_ int,
) (int, error) {
	close(p.entered)
	return p.dimension, nil
}

func newTestService(
	store selectionStore,
	probe EmbeddingProbe,
	enabledIDs ...string,
) *Service {
	return &Service{
		store: store,
		probe: probe,
		probeUsage: &fakeManagedProbeUsage{
			reservation: &fakeManagedProbeReservation{},
		},
		enabled: enabledSet(enabledIDs),
		now: func() time.Time {
			return time.Date(2026, 7, 29, 12, 34, 56, 0, time.UTC)
		},
	}
}

func TestServiceSelectProbesAndPersistsAnImmutableSnapshot(t *testing.T) {
	workspaceID, actorID := uuid.New(), uuid.New()
	store := &fakeSelectionStore{ownerID: uuid.New()}
	probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension}
	service := newTestService(store, probe, IdealabQualityV2)

	selection, err := service.Select(context.Background(), workspaceID, actorID, IdealabQualityV2)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if probe.calls != 1 || probe.spec != "idealab:text-embedding-3-large" || probe.wantDim != textEmbeddingDimension {
		t.Fatalf("probe = %#v", probe)
	}
	usage := service.probeUsage.(*fakeManagedProbeUsage)
	reservation := usage.reservation.(*fakeManagedProbeReservation)
	if usage.calls != 1 || usage.workspaceID != workspaceID ||
		usage.definition.ID != IdealabQualityV2 || reservation.finalizeCalls != 1 {
		t.Fatalf("managed probe usage = %#v reservation = %#v", usage, reservation)
	}
	if store.saveCalls != 1 || len(store.saved) != 1 {
		t.Fatalf("save calls/results = %d/%d, want 1/1", store.saveCalls, len(store.saved))
	}
	if selection.WorkspaceID != workspaceID || selection.ProfileID != IdealabQualityV2 ||
		selection.DataEgress != DataEgressManagedIdealab ||
		selection.Embedding != (Stage{
			Enabled:    true,
			Provider:   "idealab:text-embedding-3-large",
			Dimensions: textEmbeddingDimension,
		}) ||
		!selection.LLM.Enabled || selection.LLM.Provider != "idealab:qwen3.7-max-2026-06-08" ||
		selection.SelectedByUserID == nil || *selection.SelectedByUserID != actorID {
		t.Fatalf("selection = %#v", selection)
	}
	wantTime := time.Date(2026, 7, 29, 12, 34, 56, 0, time.UTC)
	if !selection.SelectedAt.Equal(wantTime) || !selection.UpdatedAt.Equal(wantTime) {
		t.Fatalf("selection timestamps = %s/%s, want %s", selection.SelectedAt, selection.UpdatedAt, wantTime)
	}

	selection.AllowedMIMETypes[0] = "application/mutated"
	if store.saved[0].AllowedMIMETypes[0] != "text/*" {
		t.Fatalf("returned selection mutated saved snapshot: %#v", store.saved[0])
	}
}

func TestServiceSelectManagedProbeFailsClosedAndDoesNotDuplicateReplay(t *testing.T) {
	workspaceID, actorID, ownerID := uuid.New(), uuid.New(), uuid.New()

	t.Run("missing usage gate blocks before the paid probe", func(t *testing.T) {
		store := &fakeSelectionStore{ownerID: ownerID}
		probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension}
		service := newTestService(store, probe, IdealabQualityV2)
		service.probeUsage = nil

		_, err := service.Select(context.Background(), workspaceID, actorID, IdealabQualityV2)
		if !errors.Is(err, ErrManagedUsageUnavailable) {
			t.Fatalf("Select() error = %v, want ErrManagedUsageUnavailable", err)
		}
		if probe.calls != 0 || store.saveCalls != 0 {
			t.Fatalf("missing usage gate probe/save = %d/%d, want 0/0", probe.calls, store.saveCalls)
		}
	})

	t.Run("replayed successful preflight skips the worker call", func(t *testing.T) {
		store := &fakeSelectionStore{ownerID: ownerID}
		probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension}
		service := newTestService(store, probe, IdealabQualityV2)
		service.probeUsage = &fakeManagedProbeUsage{
			reservation: &fakeManagedProbeReservation{replay: true},
		}

		selection, err := service.Select(context.Background(), workspaceID, actorID, IdealabQualityV2)
		if err != nil || selection == nil {
			t.Fatalf("Select() = %#v, %v", selection, err)
		}
		if probe.calls != 0 || store.saveCalls != 1 {
			t.Fatalf("replayed usage probe/save = %d/%d, want 0/1", probe.calls, store.saveCalls)
		}
	})

	t.Run("failed contract check remains indeterminate rather than replayable", func(t *testing.T) {
		store := &fakeSelectionStore{ownerID: ownerID}
		probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension - 1}
		reservation := &fakeManagedProbeReservation{}
		service := newTestService(store, probe, IdealabQualityV2)
		service.probeUsage = &fakeManagedProbeUsage{reservation: reservation}

		_, err := service.Select(context.Background(), workspaceID, actorID, IdealabQualityV2)
		if !errors.Is(err, ErrEmbeddingDimensionMismatch) {
			t.Fatalf("Select() error = %v, want ErrEmbeddingDimensionMismatch", err)
		}
		if reservation.finalizeCalls != 0 || reservation.markCalls != 1 {
			t.Fatalf("failed preflight finalized/marked = %d/%d, want 0/1", reservation.finalizeCalls, reservation.markCalls)
		}
	})
}

func TestServiceSelectBlocksUnsafeCorpusSwitchBeforeProbeOrWrite(t *testing.T) {
	workspaceID, actorID, ownerID := uuid.New(), uuid.New(), uuid.New()

	tests := []struct {
		name  string
		store *fakeSelectionStore
		want  error
	}{
		{
			name:  "pending or processing files",
			store: &fakeSelectionStore{ownerID: ownerID, inFlight: true},
			want:  ErrProfileIndexingInFlight,
		},
		{
			name: "different provider with the same 768 dimensions",
			store: &fakeSelectionStore{
				ownerID:    ownerID,
				hasCorpus:  true,
				corpusSpec: "ollama:other-768-embedding",
			},
			want: ErrProfileCorpusMismatch,
		},
		{
			name: "legacy unknown corpus identity",
			store: &fakeSelectionStore{
				ownerID:   ownerID,
				corpusErr: indexmeta.ErrUnknownProvider,
			},
			want: ErrProfileCorpusIdentityUnknown,
		},
		{
			name: "mixed corpus identity",
			store: &fakeSelectionStore{
				ownerID:   ownerID,
				corpusErr: indexmeta.ErrMixedProviders,
			},
			want: ErrProfileCorpusIdentityUnknown,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension}
			service := newTestService(test.store, probe, IdealabQualityV2)

			_, err := service.Select(context.Background(), workspaceID, actorID, IdealabQualityV2)
			if !errors.Is(err, test.want) {
				t.Fatalf("Select() error = %v, want %v", err, test.want)
			}
			if probe.calls != 0 {
				t.Fatalf("rejected selection used %d paid probes", probe.calls)
			}
			if test.store.saveCalls != 0 {
				t.Fatalf("rejected selection saved %d snapshots", test.store.saveCalls)
			}
		})
	}
}

func TestServiceSelectRequiresExactPipelineIdentityForExistingCorpus(t *testing.T) {
	workspaceID, actorID, ownerID := uuid.New(), uuid.New(), uuid.New()
	legacy, ok := Find(LocalFastV1)
	if !ok {
		t.Fatal("legacy local profile missing")
	}
	current, ok := Find(LocalFastV2)
	if !ok {
		t.Fatal("current local profile missing")
	}
	legacySelection := selectionFromDefinition(
		legacy,
		workspaceID,
		actorID,
		time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
	)
	currentSelection := selectionFromDefinition(
		current,
		workspaceID,
		actorID,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)

	tests := []struct {
		name       string
		current    *Selection
		hasCorpus  bool
		wantErr    error
		wantSaves  int
		wantProbes int
	}{
		{
			name:       "V1 corpus cannot be reinterpreted as V2",
			current:    &legacySelection,
			hasCorpus:  true,
			wantErr:    ErrProfileCorpusMismatch,
			wantSaves:  0,
			wantProbes: 0,
		},
		{
			name:       "empty workspace may move from V1 snapshot to V2",
			current:    &legacySelection,
			hasCorpus:  false,
			wantSaves:  1,
			wantProbes: 1,
		},
		{
			name:       "exact V2 corpus may be reselected",
			current:    &currentSelection,
			hasCorpus:  true,
			wantSaves:  1,
			wantProbes: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeSelectionStore{
				ownerID:    ownerID,
				current:    test.current,
				hasCorpus:  test.hasCorpus,
				corpusSpec: current.Embedding.Provider,
			}
			probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension}
			service := newTestService(store, probe, LocalFastV2)
			_, err := service.Select(
				context.Background(),
				workspaceID,
				actorID,
				LocalFastV2,
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Select() error = %v, want %v", err, test.wantErr)
			}
			if store.saveCalls != test.wantSaves || probe.calls != test.wantProbes {
				t.Fatalf(
					"save/probe calls = %d/%d, want %d/%d",
					store.saveCalls,
					probe.calls,
					test.wantSaves,
					test.wantProbes,
				)
			}
		})
	}
}

func TestServiceSelectDoesNotDependOnProcessLocalIndexLock(t *testing.T) {
	workspaceID, actorID, ownerID := uuid.New(), uuid.New(), uuid.New()
	store := &fakeSelectionStore{ownerID: ownerID}
	probe := &signalingEmbeddingProbe{
		dimension: textEmbeddingDimension,
		entered:   make(chan struct{}),
	}
	service := newTestService(store, probe, IdealabQualityV2)

	unlockIndexing := indexmeta.LockIndexing(ownerID)
	defer unlockIndexing()
	result := make(chan error, 1)
	go func() {
		_, err := service.Select(context.Background(), workspaceID, actorID, IdealabQualityV2)
		result <- err
	}()

	select {
	case <-probe.entered:
	case <-time.After(time.Second):
		t.Fatal("Select still depended on the process-local indexing lock")
	}

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Select() error while local indexing lock was held: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Select did not finish through the store coordination boundary")
	}
	if store.saveCalls != 1 {
		t.Fatalf("save calls = %d, want 1", store.saveCalls)
	}
}

func TestServiceSelectFailsClosed(t *testing.T) {
	workspaceID, actorID, ownerID := uuid.New(), uuid.New(), uuid.New()
	secret := "gateway returned Authorization: Bearer secret-value"

	t.Run("nil store is rejected before probe", func(t *testing.T) {
		probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension}
		_, err := New(nil, probe).Select(context.Background(), workspaceID, actorID, IdealabQualityV2)
		if !errors.Is(err, ErrStoreUnavailable) {
			t.Fatalf("Select() error = %v, want ErrStoreUnavailable", err)
		}
		if probe.calls != 0 {
			t.Fatalf("nil-store selection used %d probes", probe.calls)
		}
	})

	t.Run("workspace owner lookup fails before probe", func(t *testing.T) {
		store := &fakeSelectionStore{ownerErr: ErrWorkspaceNotFound}
		probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension}
		_, err := newTestService(store, probe, IdealabQualityV2).Select(
			context.Background(), workspaceID, actorID, IdealabQualityV2,
		)
		if !errors.Is(err, ErrWorkspaceNotFound) {
			t.Fatalf("Select() error = %v, want ErrWorkspaceNotFound", err)
		}
		if probe.calls != 0 || store.saveCalls != 0 {
			t.Fatalf("owner lookup failure probe/save = %d/%d, want 0/0", probe.calls, store.saveCalls)
		}
	})

	t.Run("nil probe", func(t *testing.T) {
		store := &fakeSelectionStore{ownerID: ownerID}
		_, err := newTestService(store, nil, IdealabQualityV2).Select(
			context.Background(), workspaceID, actorID, IdealabQualityV2,
		)
		if !errors.Is(err, ErrProbeUnavailable) {
			t.Fatalf("Select() error = %v, want ErrProbeUnavailable", err)
		}
		if store.saveCalls != 0 {
			t.Fatalf("nil-probe selection saved %d snapshots", store.saveCalls)
		}
	})

	t.Run("dimension mismatch", func(t *testing.T) {
		store := &fakeSelectionStore{ownerID: ownerID}
		probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension - 1}
		_, err := newTestService(store, probe, IdealabQualityV2).Select(
			context.Background(), workspaceID, actorID, IdealabQualityV2,
		)
		if !errors.Is(err, ErrEmbeddingDimensionMismatch) {
			t.Fatalf("Select() error = %v, want ErrEmbeddingDimensionMismatch", err)
		}
		if store.saveCalls != 0 {
			t.Fatalf("dimension mismatch saved %d snapshots", store.saveCalls)
		}
	})

	t.Run("probe error is redacted", func(t *testing.T) {
		store := &fakeSelectionStore{ownerID: ownerID}
		probe := &fakeEmbeddingProbe{err: errors.New(secret)}
		_, err := newTestService(store, probe, IdealabQualityV2).Select(
			context.Background(), workspaceID, actorID, IdealabQualityV2,
		)
		if !errors.Is(err, ErrEmbeddingProbeFailed) {
			t.Fatalf("Select() error = %v, want ErrEmbeddingProbeFailed", err)
		}
		if strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("error leaked probe material: %q", err)
		}
		if store.saveCalls != 0 {
			t.Fatalf("probe failure saved %d snapshots", store.saveCalls)
		}
	})

	t.Run("disabled and unknown profiles do not enter the switch path", func(t *testing.T) {
		store := &fakeSelectionStore{ownerID: ownerID}
		probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension}
		service := newTestService(store, probe, LocalFastV1)
		if _, err := service.Select(context.Background(), workspaceID, actorID, IdealabQualityV2); !errors.Is(err, ErrProfileDisabled) {
			t.Fatalf("disabled Select() error = %v, want ErrProfileDisabled", err)
		}
		if _, err := service.Select(context.Background(), workspaceID, actorID, "does-not-exist"); !errors.Is(err, ErrUnknownProfile) {
			t.Fatalf("unknown Select() error = %v, want ErrUnknownProfile", err)
		}
		if probe.calls != 0 || store.saveCalls != 0 {
			t.Fatalf("invalid profile probe/save = %d/%d, want 0/0", probe.calls, store.saveCalls)
		}
	})

	t.Run("missing workspace or actor", func(t *testing.T) {
		store := &fakeSelectionStore{ownerID: ownerID}
		probe := &fakeEmbeddingProbe{dimension: textEmbeddingDimension}
		service := newTestService(store, probe, LocalFastV1)
		if _, err := service.Select(context.Background(), uuid.Nil, actorID, LocalFastV1); !errors.Is(err, ErrWorkspaceRequired) {
			t.Fatalf("nil workspace error = %v, want ErrWorkspaceRequired", err)
		}
		if _, err := service.Select(context.Background(), workspaceID, uuid.Nil, LocalFastV1); !errors.Is(err, ErrActorRequired) {
			t.Fatalf("nil actor error = %v, want ErrActorRequired", err)
		}
		if probe.calls != 0 || store.saveCalls != 0 {
			t.Fatalf("invalid request probe/save = %d/%d, want 0/0", probe.calls, store.saveCalls)
		}
	})
}

func TestServiceListGetAndResolveReturnDefensiveCopies(t *testing.T) {
	workspaceID, ownerID := uuid.New(), uuid.New()
	actorID := uuid.New()
	definition, ok := Find(LocalFastV1)
	if !ok {
		t.Fatalf("Find(%q) failed", LocalFastV1)
	}
	snapshot := selectionFromDefinition(
		definition,
		workspaceID,
		actorID,
		time.Date(2026, 7, 29, 12, 34, 56, 0, time.UTC),
	)
	store := &fakeSelectionStore{current: &snapshot, resolved: &snapshot}
	service := newTestService(store, nil, LocalFastV1, LocalFastV2)

	profiles := service.List()
	if len(profiles) != 1 || profiles[0].ID != LocalFastV2 {
		t.Fatalf("List() = %#v", profiles)
	}
	profiles[0].AllowedMIMETypes[0] = "application/mutated"
	if second := service.List(); second[0].AllowedMIMETypes[0] != "text/*" {
		t.Fatalf("List() leaked catalog storage: %#v", second)
	}

	got, err := service.Get(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	got.AllowedMIMETypes[0] = "application/mutated"
	*got.SelectedByUserID = uuid.New()
	if snapshot.AllowedMIMETypes[0] != "text/*" || *snapshot.SelectedByUserID != actorID {
		t.Fatalf("Get() leaked store storage: %#v", snapshot)
	}

	resolved, err := service.ResolveForOwner(context.Background(), ownerID)
	if err != nil {
		t.Fatalf("ResolveForOwner() error = %v", err)
	}
	if resolved.WorkspaceID != workspaceID || resolved.ProfileID != LocalFastV1 {
		t.Fatalf("ResolveForOwner() = %#v", resolved)
	}
}

func TestServiceDeprecatedProfilesResolveButCannotBeSelected(t *testing.T) {
	workspaceID, ownerID, actorID := uuid.New(), uuid.New(), uuid.New()
	for _, profileID := range []string{LocalFastV1, IdealabQualityV1} {
		t.Run(profileID, func(t *testing.T) {
			definition, ok := Find(profileID)
			if !ok {
				t.Fatalf("Find(%q) failed", profileID)
			}
			snapshot := selectionFromDefinition(
				definition,
				workspaceID,
				actorID,
				time.Date(2026, 7, 29, 12, 34, 56, 0, time.UTC),
			)
			store := &fakeSelectionStore{
				ownerID:  ownerID,
				current:  &snapshot,
				resolved: &snapshot,
			}
			service := newTestService(store, nil, profileID)

			if got := service.List(); len(got) != 0 {
				t.Fatalf("List() exposed deprecated profile: %#v", got)
			}
			if _, err := service.Get(context.Background(), workspaceID); err != nil {
				t.Fatalf("Get() rejected exact persisted V1: %v", err)
			}
			if _, err := service.ResolveForOwner(context.Background(), ownerID); err != nil {
				t.Fatalf("ResolveForOwner() rejected exact persisted V1: %v", err)
			}
			if _, err := service.Select(
				context.Background(),
				workspaceID,
				actorID,
				profileID,
			); !errors.Is(err, ErrProfileDisabled) {
				t.Fatalf("Select() error = %v, want ErrProfileDisabled", err)
			}
		})
	}
}

func TestServiceGetAndResolveRevalidatePersistedSelections(t *testing.T) {
	workspaceID, ownerID, actorID := uuid.New(), uuid.New(), uuid.New()
	at := time.Date(2026, 7, 29, 12, 34, 56, 0, time.UTC)

	quality, ok := Find(IdealabQualityV2)
	if !ok {
		t.Fatalf("Find(%q) failed", IdealabQualityV2)
	}
	disabled := selectionFromDefinition(quality, workspaceID, actorID, at)

	local, ok := Find(LocalFastV1)
	if !ok {
		t.Fatalf("Find(%q) failed", LocalFastV1)
	}
	invalid := selectionFromDefinition(local, workspaceID, actorID, at)
	invalid.Embedding.Provider = "ollama:unapproved-model"

	tests := []struct {
		name      string
		selection Selection
		wantErr   error
	}{
		{
			name:      "current allowlist disables persisted profile",
			selection: disabled,
			wantErr:   ErrProfileDisabled,
		},
		{
			name:      "catalog mismatch invalidates persisted profile",
			selection: invalid,
			wantErr:   ErrInvalidSelection,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeSelectionStore{
				current:  &test.selection,
				resolved: &test.selection,
			}
			service := newTestService(store, nil, LocalFastV1)

			if _, err := service.Get(context.Background(), workspaceID); !errors.Is(err, test.wantErr) {
				t.Fatalf("Get() error = %v, want %v", err, test.wantErr)
			}
			if _, err := service.ResolveForOwner(context.Background(), ownerID); !errors.Is(err, test.wantErr) {
				t.Fatalf("ResolveForOwner() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

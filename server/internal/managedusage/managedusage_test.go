package managedusage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
	"github.com/PeterGuy326/mem/server/internal/entitlement"
)

type ledgerFake struct {
	reserveErrAt int
	reserveErr   error
	replayedAt   int

	commands            []entitlement.ReserveCommand
	finalized           []uuid.UUID
	released            []uuid.UUID
	markedIndeterminate []uuid.UUID
	events              *[]string
}

func (f *ledgerFake) Reserve(
	_ context.Context,
	command entitlement.ReserveCommand,
) (*entitlement.Reservation, error) {
	f.commands = append(f.commands, command)
	if f.events != nil {
		*f.events = append(*f.events, "reserve:"+command.Operation)
	}
	if f.reserveErr != nil && len(f.commands) == f.reserveErrAt {
		return nil, f.reserveErr
	}
	replayed := f.replayedAt > 0 && len(f.commands) == f.replayedAt
	status := entitlement.StatusReserved
	if replayed {
		status = entitlement.StatusSucceeded
	}
	return &entitlement.Reservation{
		ID:       uuid.New(),
		Status:   status,
		Replayed: replayed,
	}, nil
}

func (f *ledgerFake) Finalize(
	_ context.Context,
	usageID uuid.UUID,
	_ []entitlement.ReplayReference,
) (entitlement.Summary, error) {
	f.finalized = append(f.finalized, usageID)
	if f.events != nil {
		*f.events = append(*f.events, "finalize")
	}
	return entitlement.Summary{}, nil
}

func (f *ledgerFake) Release(
	_ context.Context,
	usageID uuid.UUID,
) (entitlement.Summary, error) {
	f.released = append(f.released, usageID)
	if f.events != nil {
		*f.events = append(*f.events, "release")
	}
	return entitlement.Summary{}, nil
}

func (f *ledgerFake) MarkIndeterminate(
	_ context.Context,
	usageID uuid.UUID,
) (entitlement.Summary, error) {
	f.markedIndeterminate = append(f.markedIndeterminate, usageID)
	if f.events != nil {
		*f.events = append(*f.events, "indeterminate")
	}
	return entitlement.Summary{}, nil
}

func qualityCommand() Command {
	definition, ok := aiprofile.Find(aiprofile.IdealabQualityV1)
	if !ok {
		panic("quality profile missing")
	}
	return Command{
		WorkspaceID:     uuid.MustParse("01833e6e-9a2e-713d-a677-9a8e13ed8e14"),
		FileID:          uuid.MustParse("01833e6e-9a2e-713d-a677-9a8e13ed8e15"),
		ContentSHA256:   strings.Repeat("a", 64),
		ProfileID:       definition.ID,
		ProfileRevision: definition.Revision,
		Stages: []StageSpec{
			{Stage: StageEmbedding, ProviderSpec: definition.Embedding.Provider},
			{Stage: StageLLM, ProviderSpec: definition.LLM.Provider},
		},
	}
}

func TestPrepareReservesEveryManagedStageBeforeWorkerAndFinalizes(t *testing.T) {
	events := []string{}
	ledger := &ledgerFake{events: &events}
	service := New(ledger)

	handle, err := service.Prepare(context.Background(), qualityCommand())
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if got, want := events, []string{
		"reserve:file.ai.embedding",
		"reserve:file.ai.llm",
	}; !sameStrings(got, want) {
		t.Fatalf("events before worker = %#v, want %#v", got, want)
	}
	// The coordinator has no Worker dependency. A caller can only get here
	// after every managed stage reserve succeeded.
	events = append(events, "worker")
	if err := handle.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if got, want := events, []string{
		"reserve:file.ai.embedding",
		"reserve:file.ai.llm",
		"worker",
		"finalize",
		"finalize",
	}; !sameStrings(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if len(ledger.finalized) != 2 {
		t.Fatalf("finalized = %d, want 2", len(ledger.finalized))
	}
	for _, command := range ledger.commands {
		if command.Units != 1 || len(command.IdempotencyKey) != 64 ||
			len(command.RequestFingerprint) != 64 {
			t.Fatalf("unsafe reserve command = %#v", command)
		}
		if strings.Contains(command.IdempotencyKey, "secret") ||
			strings.Contains(command.RequestFingerprint, "secret") {
			t.Fatalf("ledger hash leaked source material: %#v", command)
		}
	}
}

func TestPrepareStageIDsAreDeterministicAndCanonical(t *testing.T) {
	first, second := &ledgerFake{}, &ledgerFake{}
	command := qualityCommand()
	// Input order is intentionally not the server-owned execution order.
	command.Stages[0], command.Stages[1] = command.Stages[1], command.Stages[0]

	if _, err := New(first).Prepare(context.Background(), command); err != nil {
		t.Fatalf("first Prepare() error = %v", err)
	}
	if _, err := New(second).Prepare(context.Background(), qualityCommand()); err != nil {
		t.Fatalf("second Prepare() error = %v", err)
	}
	if len(first.commands) != len(second.commands) {
		t.Fatalf("command lengths = %d/%d", len(first.commands), len(second.commands))
	}
	for i := range first.commands {
		got, want := first.commands[i], second.commands[i]
		if got.Operation != want.Operation || got.IdempotencyKey != want.IdempotencyKey ||
			got.RequestFingerprint != want.RequestFingerprint {
			t.Fatalf("command %d is not deterministic: %#v != %#v", i, got, want)
		}
	}
	if got := []string{
		first.commands[0].Operation,
		first.commands[1].Operation,
	}; !sameStrings(got, []string{"file.ai.embedding", "file.ai.llm"}) {
		t.Fatalf("canonical operations = %#v", got)
	}

	changed := qualityCommand()
	changed.ContentSHA256 = strings.Repeat("b", 64)
	third := &ledgerFake{}
	if _, err := New(third).Prepare(context.Background(), changed); err != nil {
		t.Fatalf("changed Prepare() error = %v", err)
	}
	if first.commands[0].IdempotencyKey == third.commands[0].IdempotencyKey ||
		first.commands[0].RequestFingerprint == third.commands[0].RequestFingerprint {
		t.Fatal("content identity did not change deterministic stage hashes")
	}
}

func TestPrepareRejectsNonCatalogOrMismatchedManagedStage(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Command)
		want   error
	}{
		{
			name: "arbitrary managed-looking provider",
			mutate: func(command *Command) {
				command.Stages[0].ProviderSpec = "openai:text-embedding-3-large-preview"
			},
			want: ErrInvalidStage,
		},
		{
			name: "managed provider on the wrong stage",
			mutate: func(command *Command) {
				command.Stages[1].ProviderSpec = "openai:text-embedding-3-large"
			},
			want: ErrInvalidStage,
		},
		{
			name: "stale profile revision",
			mutate: func(command *Command) {
				command.ProfileRevision = "stale-revision"
			},
			want: ErrInvalidProfile,
		},
		{
			name: "raw content is not a content identity",
			mutate: func(command *Command) {
				command.ContentSHA256 = "raw source content with a secret"
			},
			want: ErrInvalidCommand,
		},
		{
			name: "duplicate stage",
			mutate: func(command *Command) {
				command.Stages = append(command.Stages, command.Stages[0])
			},
			want: ErrInvalidStage,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := &ledgerFake{}
			command := qualityCommand()
			test.mutate(&command)
			_, err := New(ledger).Prepare(context.Background(), command)
			if !errors.Is(err, test.want) {
				t.Fatalf("Prepare() error = %v, want %v", err, test.want)
			}
			if len(ledger.commands) != 0 {
				t.Fatalf("invalid route made %d ledger calls", len(ledger.commands))
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error leaked raw content: %q", err)
			}
		})
	}
}

func TestPrepareSkipsLocalAndEmptyStages(t *testing.T) {
	local, ok := aiprofile.Find(aiprofile.LocalFastV1)
	if !ok {
		t.Fatal("local profile missing")
	}
	command := Command{
		WorkspaceID:     uuid.New(),
		FileID:          uuid.New(),
		ContentSHA256:   strings.Repeat("c", 64),
		ProfileID:       local.ID,
		ProfileRevision: local.Revision,
		Stages: []StageSpec{
			{Stage: StageEmbedding, ProviderSpec: local.Embedding.Provider},
			{Stage: StageVisualEmbedding, ProviderSpec: local.VisualEmbedding.Provider},
			{Stage: StageLLM},
			{},
		},
	}

	ledger := &ledgerFake{}
	handle, err := New(ledger).Prepare(context.Background(), command)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if handle.HasManagedStages() || len(ledger.commands) != 0 {
		t.Fatalf("local/empty stages created managed usage: handle=%#v calls=%d", handle, len(ledger.commands))
	}
	if err := handle.Finalize(context.Background()); err != nil {
		t.Fatalf("noop Finalize() error = %v", err)
	}
	if len(ledger.finalized) != 0 {
		t.Fatalf("noop handle finalized %d reservations", len(ledger.finalized))
	}

	// The managed profile intentionally retains a local CLIP visual stage.
	// Its presence in the managed catalog must not turn it into a billable
	// network invocation.
	quality, ok := aiprofile.Find(aiprofile.IdealabQualityV1)
	if !ok {
		t.Fatal("quality profile missing")
	}
	qualityCommand := Command{
		WorkspaceID:     uuid.New(),
		FileID:          uuid.New(),
		ContentSHA256:   strings.Repeat("d", 64),
		ProfileID:       quality.ID,
		ProfileRevision: quality.Revision,
		Stages: []StageSpec{
			{Stage: StageVisualEmbedding, ProviderSpec: quality.VisualEmbedding.Provider},
			{Stage: StageASR},
		},
	}
	if _, err := New(nil).Prepare(context.Background(), qualityCommand); err != nil {
		t.Fatalf("local managed-profile stages with nil ledger error = %v", err)
	}
}

func TestPrepareEmbeddingProbeUsesManagedPreflightIdentity(t *testing.T) {
	quality, ok := aiprofile.Find(aiprofile.IdealabQualityV1)
	if !ok {
		t.Fatal("quality profile missing")
	}
	workspaceID := uuid.MustParse("01833e6e-9a2e-713d-a677-9a8e13ed8e16")
	ledger := &ledgerFake{}
	reservation, err := New(ledger).PrepareEmbeddingProbe(context.Background(), workspaceID, quality)
	if err != nil {
		t.Fatalf("PrepareEmbeddingProbe() error = %v", err)
	}
	handle, ok := reservation.(*Handle)
	if !ok || handle.HasReplay() || !handle.HasManagedStages() {
		t.Fatalf("probe reservation = %#v", reservation)
	}
	if len(ledger.commands) != 1 {
		t.Fatalf("probe reserve calls = %d, want 1", len(ledger.commands))
	}
	command := ledger.commands[0]
	if command.Operation != "profile.ai_probe.embedding" ||
		command.ProviderSpec != quality.Embedding.Provider || command.Units != 1 ||
		len(command.IdempotencyKey) != 64 || len(command.RequestFingerprint) != 64 {
		t.Fatalf("probe reserve command = %#v", command)
	}
	if err := handle.Finalize(context.Background()); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if len(ledger.finalized) != 1 {
		t.Fatalf("probe finalizations = %d, want 1", len(ledger.finalized))
	}

	local, ok := aiprofile.Find(aiprofile.LocalFastV1)
	if !ok {
		t.Fatal("local profile missing")
	}
	localReservation, err := New(nil).PrepareEmbeddingProbe(context.Background(), workspaceID, local)
	if err != nil {
		t.Fatalf("local PrepareEmbeddingProbe() error = %v", err)
	}
	localHandle, ok := localReservation.(*Handle)
	if !ok || localHandle.HasManagedStages() || localReservation.HasReplay() {
		t.Fatalf("local probe reservation = %#v", localReservation)
	}
}

func TestHandleSettlesOnlyOutstandingStages(t *testing.T) {
	ledger := &ledgerFake{}
	handle, err := New(ledger).Prepare(context.Background(), qualityCommand())
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := handle.MarkIndeterminate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(ledger.markedIndeterminate); got != 0 {
		t.Fatalf("settled stages were marked indeterminate %d times", got)
	}
}

func TestHandleReservationsReturnsDefensiveCopy(t *testing.T) {
	ledger := &ledgerFake{}
	handle, err := New(ledger).Prepare(context.Background(), qualityCommand())
	if err != nil {
		t.Fatal(err)
	}

	reservations := handle.Reservations()
	if len(reservations) != 2 {
		t.Fatalf("Reservations() length = %d, want 2", len(reservations))
	}
	reservations[0].ProviderSpec = "mutated"
	if got := handle.Reservations()[0].ProviderSpec; got == "mutated" {
		t.Fatal("Reservations() exposed mutable handle state")
	}
}

func TestHandleReleaseAndIndeterminateOnlyTouchNewReservations(t *testing.T) {
	t.Run("release when no invocation happened", func(t *testing.T) {
		ledger := &ledgerFake{}
		handle, err := New(ledger).Prepare(context.Background(), qualityCommand())
		if err != nil {
			t.Fatal(err)
		}
		if err := handle.ReleaseUninvoked(context.Background()); err != nil {
			t.Fatalf("ReleaseUninvoked() error = %v", err)
		}
		if len(ledger.released) != 2 || len(ledger.markedIndeterminate) != 0 {
			t.Fatalf("release/indeterminate = %d/%d, want 2/0", len(ledger.released), len(ledger.markedIndeterminate))
		}
	})

	t.Run("indeterminate after uncertain worker result", func(t *testing.T) {
		ledger := &ledgerFake{}
		handle, err := New(ledger).Prepare(context.Background(), qualityCommand())
		if err != nil {
			t.Fatal(err)
		}
		if err := handle.MarkIndeterminate(context.Background()); err != nil {
			t.Fatalf("MarkIndeterminate() error = %v", err)
		}
		if len(ledger.markedIndeterminate) != 2 || len(ledger.released) != 0 {
			t.Fatalf("indeterminate/release = %d/%d, want 2/0", len(ledger.markedIndeterminate), len(ledger.released))
		}
	})

	t.Run("replay is never finalized or released", func(t *testing.T) {
		ledger := &ledgerFake{replayedAt: 2}
		handle, err := New(ledger).Prepare(context.Background(), qualityCommand())
		if err != nil {
			t.Fatal(err)
		}
		if !handle.HasReplay() {
			t.Fatal("Handle.HasReplay() = false, want true")
		}
		if err := handle.ReleaseUninvoked(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(ledger.released) != 1 {
			t.Fatalf("released = %d, want 1 non-replayed reservation", len(ledger.released))
		}
	})
}

func TestPrepareReleasesEarlierReservationsWhenLaterReserveFails(t *testing.T) {
	reserveErr := errors.New("quota decision failed")
	ledger := &ledgerFake{reserveErrAt: 2, reserveErr: reserveErr}
	handle, err := New(ledger).Prepare(context.Background(), qualityCommand())
	if !errors.Is(err, reserveErr) {
		t.Fatalf("Prepare() error = %v, want reserve error", err)
	}
	if handle != nil {
		t.Fatalf("Prepare() handle = %#v, want nil after successful cleanup", handle)
	}
	if len(ledger.commands) != 2 || len(ledger.released) != 1 {
		t.Fatalf("reserve/release = %d/%d, want 2/1", len(ledger.commands), len(ledger.released))
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

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

type releasedRetryLedger struct {
	ledgerFake

	alwaysReleased  bool
	replayOperation string
	attempts        map[string]int
	releasedRows    map[string]*entitlement.Reservation
	lookups         []entitlement.ReserveCommand
	lookupOptions   []entitlement.ReservationLookupOptions
}

func (f *releasedRetryLedger) Reserve(
	_ context.Context,
	command entitlement.ReserveCommand,
) (*entitlement.Reservation, error) {
	f.commands = append(f.commands, command)
	if f.attempts == nil {
		f.attempts = make(map[string]int)
	}
	f.attempts[command.Operation]++
	attempt := f.attempts[command.Operation]
	if f.alwaysReleased || attempt == 1 {
		if f.releasedRows == nil {
			f.releasedRows = make(map[string]*entitlement.Reservation)
		}
		key := command.Operation + "\x00" + command.IdempotencyKey
		f.releasedRows[key] = &entitlement.Reservation{
			ID:     uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)),
			Status: entitlement.StatusReleased,
		}
		return nil, entitlement.ErrReleasedKey
	}
	status := entitlement.StatusReserved
	replayed := command.Operation == f.replayOperation
	if replayed {
		status = entitlement.StatusSucceeded
	}
	return &entitlement.Reservation{
		ID:       uuid.NewSHA1(uuid.NameSpaceOID, []byte(command.IdempotencyKey)),
		Status:   status,
		Replayed: replayed,
	}, nil
}

func (f *releasedRetryLedger) LookupReservation(
	_ context.Context,
	command entitlement.ReserveCommand,
	options entitlement.ReservationLookupOptions,
) (*entitlement.Reservation, error) {
	f.lookups = append(f.lookups, command)
	f.lookupOptions = append(f.lookupOptions, options)
	key := command.Operation + "\x00" + command.IdempotencyKey
	reservation, ok := f.releasedRows[key]
	if !ok {
		return nil, entitlement.ErrReservationNotFound
	}
	return reservation, nil
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
		WorkspaceID:      uuid.MustParse("01833e6e-9a2e-713d-a677-9a8e13ed8e14"),
		FileID:           uuid.MustParse("01833e6e-9a2e-713d-a677-9a8e13ed8e15"),
		ContentSHA256:    strings.Repeat("a", 64),
		ProfileID:        definition.ID,
		ProfileRevision:  definition.Revision,
		PipelineRevision: definition.PipelineRevision,
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

	changedPipeline := qualityCommand()
	changedPipeline.PipelineRevision = "different-pipeline"
	if _, err := New(&ledgerFake{}).Prepare(context.Background(), changedPipeline); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("stale pipeline Prepare() error = %v, want ErrInvalidProfile", err)
	}

	baseIdentity := qualityCommand()
	stage := normalizedStage{
		Stage:        StageEmbedding,
		ProviderSpec: baseIdentity.Stages[0].ProviderSpec,
	}
	firstIdentity, err := reserveCommand(baseIdentity, stage)
	if err != nil {
		t.Fatal(err)
	}
	baseIdentity.PipelineRevision = "file-enrichment-v3"
	secondIdentity, err := reserveCommand(baseIdentity, stage)
	if err != nil {
		t.Fatal(err)
	}
	if firstIdentity.IdempotencyKey == secondIdentity.IdempotencyKey ||
		firstIdentity.RequestFingerprint == secondIdentity.RequestFingerprint {
		t.Fatal("pipeline revision did not change deterministic stage hashes")
	}
}

func TestPrepareChainsReleasedStageKeys(t *testing.T) {
	ledger := &releasedRetryLedger{}
	handle, err := New(ledger).Prepare(context.Background(), qualityCommand())
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if handle.HasReplay() {
		t.Fatal("Handle.HasReplay() = true, want false")
	}
	if got := len(handle.Reservations()); got != 2 {
		t.Fatalf("reservations = %d, want 2", got)
	}
	if got := len(ledger.commands); got != 4 {
		t.Fatalf("reserve commands = %d, want released+fresh for each stage", got)
	}
	if got := len(ledger.lookups); got != 2 {
		t.Fatalf("released lookups = %d, want 2", got)
	}
	for i, lookup := range ledger.lookups {
		if !ledger.lookupOptions[i].IncludeReleased {
			t.Fatalf("lookup %d did not explicitly include released state", i)
		}
		first, retry := ledger.commands[i*2], ledger.commands[i*2+1]
		released := ledger.releasedRows[first.Operation+"\x00"+first.IdempotencyKey]
		want := chainedReserveCommand(first, released.ID)
		if retry != want {
			t.Fatalf("retry command %d = %#v, want deterministic chain %#v", i, retry, want)
		}
		if retry.IdempotencyKey == first.IdempotencyKey ||
			retry.RequestFingerprint == first.RequestFingerprint {
			t.Fatalf("retry command %d reused a released identity", i)
		}
		if lookup != first {
			t.Fatalf("lookup command %d = %#v, want released command %#v", i, lookup, first)
		}
	}
}

func TestPrepareStopsAfterReleasedChainReplaysSucceededStage(t *testing.T) {
	ledger := &releasedRetryLedger{replayOperation: "file.ai.embedding"}
	handle, err := New(ledger).Prepare(context.Background(), qualityCommand())
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if !handle.HasReplay() {
		t.Fatal("Handle.HasReplay() = false, want true")
	}
	reservations := handle.Reservations()
	if len(reservations) != 1 || !reservations[0].Replayed ||
		reservations[0].Stage != StageEmbedding {
		t.Fatalf("reservations = %#v, want only replayed embedding", reservations)
	}
	if got := len(ledger.commands); got != 2 {
		t.Fatalf("reserve commands = %d, want no later LLM reservation", got)
	}
	for _, command := range ledger.commands {
		if command.Operation != "file.ai.embedding" {
			t.Fatalf("reserved later stage after replay: %#v", command)
		}
	}
}

func TestPrepareReleasedRetryFailsClosedAtBound(t *testing.T) {
	ledger := &releasedRetryLedger{alwaysReleased: true}
	handle, err := New(ledger).Prepare(context.Background(), qualityCommand())
	if !errors.Is(err, ErrReleasedRetryLimit) {
		t.Fatalf("Prepare() error = %v, want ErrReleasedRetryLimit", err)
	}
	if handle != nil {
		t.Fatalf("Prepare() handle = %#v, want nil", handle)
	}
	if got := len(ledger.commands); got != maxReleasedReservationHops {
		t.Fatalf("reserve commands = %d, want bound %d", got, maxReleasedReservationHops)
	}
}

func TestPrepareGenericLedgerKeepsReleasedKeyTerminal(t *testing.T) {
	ledger := &ledgerFake{
		reserveErrAt: 1,
		reserveErr:   entitlement.ErrReleasedKey,
	}
	handle, err := New(ledger).Prepare(context.Background(), qualityCommand())
	if !errors.Is(err, entitlement.ErrReleasedKey) {
		t.Fatalf("Prepare() error = %v, want ErrReleasedKey", err)
	}
	if handle != nil {
		t.Fatalf("Prepare() handle = %#v, want nil", handle)
	}
	if got := len(ledger.commands); got != 1 {
		t.Fatalf("reserve commands = %d, want no untrusted retry", got)
	}
}

func TestSettleUsageMapsClosedOutcomes(t *testing.T) {
	ledger := &ledgerFake{}
	service := New(ledger)
	succeededID, releasedID, indeterminateID := uuid.New(), uuid.New(), uuid.New()

	for _, test := range []struct {
		usageID uuid.UUID
		outcome Outcome
	}{
		{usageID: succeededID, outcome: OutcomeSucceeded},
		{usageID: releasedID, outcome: OutcomeNotInvoked},
		{usageID: indeterminateID, outcome: OutcomeIndeterminate},
	} {
		if err := service.SettleUsage(context.Background(), test.usageID, test.outcome); err != nil {
			t.Fatalf("SettleUsage(%q) error = %v", test.outcome, err)
		}
		// Outbox delivery is at least once. Repeating the same closed outcome
		// must be passed through to the idempotent entitlement transition.
		if err := service.SettleUsage(context.Background(), test.usageID, test.outcome); err != nil {
			t.Fatalf("repeated SettleUsage(%q) error = %v", test.outcome, err)
		}
	}
	if got, want := ledger.finalized, []uuid.UUID{succeededID, succeededID}; !sameUUIDs(got, want) {
		t.Fatalf("finalized = %#v, want %#v", got, want)
	}
	if got, want := ledger.released, []uuid.UUID{releasedID, releasedID}; !sameUUIDs(got, want) {
		t.Fatalf("released = %#v, want %#v", got, want)
	}
	if got, want := ledger.markedIndeterminate, []uuid.UUID{
		indeterminateID,
		indeterminateID,
	}; !sameUUIDs(got, want) {
		t.Fatalf("indeterminate = %#v, want %#v", got, want)
	}
	if err := service.SettleUsage(context.Background(), uuid.New(), Outcome("open")); !errors.Is(err, ErrInvalidOutcome) {
		t.Fatalf("invalid outcome error = %v, want ErrInvalidOutcome", err)
	}
	if err := service.SettleUsage(context.Background(), uuid.Nil, OutcomeSucceeded); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("nil usage error = %v, want ErrInvalidReservation", err)
	}
	if err := New(nil).SettleUsage(context.Background(), uuid.New(), OutcomeSucceeded); !errors.Is(err, ErrEntitlementUnavailable) {
		t.Fatalf("nil ledger error = %v, want ErrEntitlementUnavailable", err)
	}
}

func TestHandleSettlesStagesIndependently(t *testing.T) {
	ledger := &ledgerFake{}
	handle, err := New(ledger).Prepare(context.Background(), qualityCommand())
	if err != nil {
		t.Fatal(err)
	}
	reservations := handle.Reservations()
	if len(reservations) != 2 {
		t.Fatalf("reservations = %d, want 2", len(reservations))
	}

	if err := handle.ReleaseStagesNotInvoked(
		context.Background(),
		[]Stage{StageLLM},
	); err != nil {
		t.Fatalf("ReleaseStagesNotInvoked() error = %v", err)
	}
	if err := handle.FinalizeStages(
		context.Background(),
		[]Stage{StageEmbedding},
	); err != nil {
		t.Fatalf("FinalizeStages() error = %v", err)
	}
	if len(ledger.released) != 1 || ledger.released[0] != reservations[1].UsageID {
		t.Fatalf("released = %#v, want LLM reservation", ledger.released)
	}
	if len(ledger.finalized) != 1 || ledger.finalized[0] != reservations[0].UsageID {
		t.Fatalf("finalized = %#v, want embedding reservation", ledger.finalized)
	}
	if err := handle.MarkStagesIndeterminate(
		context.Background(),
		[]Stage{StageEmbedding},
	); !errors.Is(err, ErrInvalidStage) {
		t.Fatalf("settled stage error = %v, want ErrInvalidStage", err)
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
			name: "stale pipeline revision",
			mutate: func(command *Command) {
				command.PipelineRevision = "stale-pipeline"
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
		WorkspaceID:      uuid.New(),
		FileID:           uuid.New(),
		ContentSHA256:    strings.Repeat("c", 64),
		ProfileID:        local.ID,
		ProfileRevision:  local.Revision,
		PipelineRevision: local.PipelineRevision,
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

	// Disabled optional stages must not create a billable invocation.
	quality, ok := aiprofile.Find(aiprofile.IdealabQualityV1)
	if !ok {
		t.Fatal("quality profile missing")
	}
	qualityCommand := Command{
		WorkspaceID:      uuid.New(),
		FileID:           uuid.New(),
		ContentSHA256:    strings.Repeat("d", 64),
		ProfileID:        quality.ID,
		ProfileRevision:  quality.Revision,
		PipelineRevision: quality.PipelineRevision,
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

func sameUUIDs(got, want []uuid.UUID) bool {
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

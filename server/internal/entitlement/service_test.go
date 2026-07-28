package entitlement

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestManagedProviderUsesExactServerMatch(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		resolved   string
		want       bool
	}{
		{"exact", "openai:text-embedding-3-small", "openai:text-embedding-3-small", true},
		{"empty", "", "", false},
		{"different model", "openai:text-embedding-3-small", "openai:text-embedding-3-large", false},
		{"different case", "OpenAI:text-embedding-3-small", "openai:text-embedding-3-small", false},
		{"resolved whitespace is normalized", "openai:model", " openai:model ", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsManagedProvider(test.configured, test.resolved); got != test.want {
				t.Fatalf("IsManagedProvider(%q, %q) = %t, want %t",
					test.configured, test.resolved, got, test.want)
			}
		})
	}
}

func TestReserveCommandAndReplayReferencesFailClosed(t *testing.T) {
	valid := ReserveCommand{
		WorkspaceID:        uuid.New(),
		Operation:          "context.query",
		ProviderSpec:       "openai:model",
		Units:              1,
		IdempotencyKey:     "retry-1",
		RequestFingerprint: strings.Repeat("a", 64),
	}
	if err := validateReserveCommand(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ReserveCommand){
		"workspace":   func(c *ReserveCommand) { c.WorkspaceID = uuid.Nil },
		"operation":   func(c *ReserveCommand) { c.Operation = "Search" },
		"provider":    func(c *ReserveCommand) { c.ProviderSpec = "fallback" },
		"units":       func(c *ReserveCommand) { c.Units = 0 },
		"idempotency": func(c *ReserveCommand) { c.IdempotencyKey = "" },
		"fingerprint": func(c *ReserveCommand) { c.RequestFingerprint = "raw-query" },
	} {
		t.Run(name, func(t *testing.T) {
			command := valid
			mutate(&command)
			if err := validateReserveCommand(command); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}

	reference := ReplayReference{
		Source:     "text",
		EvidenceID: uuid.New(),
		FileID:     uuid.New(),
		Score:      0.5,
	}
	if err := validateReplayReferences([]ReplayReference{reference}); err != nil {
		t.Fatal(err)
	}
	reference.Source = "query"
	if err := validateReplayReferences([]ReplayReference{reference}); !errors.Is(err, ErrReplayResultInvalid) {
		t.Fatalf("unsafe source error = %v", err)
	}
	reference.Source = "text"
	for name, score := range map[string]float32{
		"NaN":          float32(math.NaN()),
		"positive inf": float32(math.Inf(1)),
		"negative inf": float32(math.Inf(-1)),
		"above range":  1.01,
		"below range":  -1.01,
	} {
		t.Run(name, func(t *testing.T) {
			invalid := reference
			invalid.Score = score
			if err := validateReplayReferences(
				[]ReplayReference{invalid},
			); !errors.Is(err, ErrReplayResultInvalid) {
				t.Fatalf("invalid score error = %v", err)
			}
		})
	}
}

func TestRollPeriodResetsOnlyIntoCurrentWindow(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	summary := Summary{
		UnitLimit:   100,
		Reserved:    2,
		Consumed:    90,
		PeriodStart: now.Add(-3 * time.Hour),
		ResetAt:     now.Add(-2 * time.Hour),
	}
	rolled := rollPeriod(summary, now)
	if rolled.Reserved != 0 || rolled.Consumed != 0 || rolled.Remaining != 100 {
		t.Fatalf("rolled counters = %+v", rolled)
	}
	if rolled.PeriodStart.After(now) || !rolled.ResetAt.After(now) {
		t.Fatalf("rolled period = %s..%s at %s", rolled.PeriodStart, rolled.ResetAt, now)
	}
}

func TestIdempotencyDigestBindsWorkspaceAndOperation(t *testing.T) {
	key := "same-client-key"
	workspaceA := uuid.New()
	workspaceB := uuid.New()
	a := hashDomain(
		"mem/managed-embedding/idempotency/v1/"+workspaceA.String()+"/search.query",
		key,
	)
	b := hashDomain(
		"mem/managed-embedding/idempotency/v1/"+workspaceB.String()+"/search.query",
		key,
	)
	c := hashDomain(
		"mem/managed-embedding/idempotency/v1/"+workspaceA.String()+"/context.query",
		key,
	)
	if a == b || a == c || b == c {
		t.Fatal("idempotency digest must not be linkable across workspace/operation")
	}
}

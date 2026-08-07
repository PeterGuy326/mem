package memory

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestCreateRelationValidation(t *testing.T) {
	wsID := uuid.New()
	src := uuid.New()
	tgt := uuid.New()

	tests := []struct {
		name string
		cmd  CreateRelationCommand
		want error
	}{
		{
			name: "missing workspace_id",
			cmd: CreateRelationCommand{
				SourceID: src, TargetID: tgt, RelationType: RelSupersedes,
			},
			want: ErrInvalidCommand,
		},
		{
			name: "missing source_id",
			cmd: CreateRelationCommand{
				WorkspaceID: wsID, TargetID: tgt, RelationType: RelSupersedes,
			},
			want: ErrInvalidCommand,
		},
		{
			name: "missing target_id",
			cmd: CreateRelationCommand{
				WorkspaceID: wsID, SourceID: src, RelationType: RelSupersedes,
			},
			want: ErrInvalidCommand,
		},
		{
			name: "self reference",
			cmd: CreateRelationCommand{
				WorkspaceID: wsID, SourceID: src, TargetID: src, RelationType: RelSupersedes,
			},
			want: ErrInvalidCommand,
		},
		{
			name: "invalid relation_type",
			cmd: CreateRelationCommand{
				WorkspaceID: wsID, SourceID: src, TargetID: tgt, RelationType: "invalidtype",
			},
			want: ErrInvalidCommand,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCreateRelationCommand(tt.cmd)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
		})
	}
}

func TestListRelationsValidation(t *testing.T) {
	tests := []struct {
		name string
		q    ListRelationsQuery
		want error
	}{
		{
			name: "missing workspace_id",
			q:    ListRelationsQuery{MemoryID: uuid.New()},
			want: ErrInvalidCommand,
		},
		{
			name: "missing memory_id",
			q:    ListRelationsQuery{WorkspaceID: uuid.New()},
			want: ErrInvalidCommand,
		},
		{
			name: "invalid direction",
			q: ListRelationsQuery{
				WorkspaceID: uuid.New(), MemoryID: uuid.New(), Direction: "upward",
			},
			want: ErrInvalidCommand,
		},
		{
			name: "invalid relation_type filter",
			q: ListRelationsQuery{
				WorkspaceID: uuid.New(), MemoryID: uuid.New(), RelationType: "invalid",
			},
			want: ErrInvalidCommand,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateListRelationsQuery(tt.q)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
		})
	}
}

func TestRelationTypeConstants(t *testing.T) {
	// Verify the constants match the CHECK constraint values.
	expected := map[string]struct{}{
		"supersedes":    {},
		"corrects":      {},
		"occurrence_of": {},
	}
	for k := range validRelationTypes {
		if _, ok := expected[k]; !ok {
			t.Errorf("unexpected relation type constant: %q", k)
		}
	}
	if len(validRelationTypes) != len(expected) {
		t.Errorf("validRelationTypes has %d entries, expected %d",
			len(validRelationTypes), len(expected))
	}
}

func TestCreateRelationNilService(t *testing.T) {
	var svc *Service
	_, err := svc.CreateRelation(nil, CreateRelationCommand{
		WorkspaceID:  uuid.New(),
		SourceID:     uuid.New(),
		TargetID:     uuid.New(),
		RelationType: RelSupersedes,
	})
	if err == nil {
		t.Fatal("expected error on nil service")
	}
}

func TestListRelationsNilService(t *testing.T) {
	var svc *Service
	_, err := svc.ListRelations(nil, ListRelationsQuery{
		WorkspaceID: uuid.New(),
		MemoryID:    uuid.New(),
	})
	if err == nil {
		t.Fatal("expected error on nil service")
	}
}

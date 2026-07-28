package workspacebundle

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/PeterGuy326/mem/server/internal/handoff"
	"github.com/google/uuid"
)

type ProjectedReference struct {
	Ordinal        int
	Relation       string
	URI            string
	ExpectedSHA256 string
	Required       bool
	Metadata       json.RawMessage
}

type PayloadProjection struct {
	Canonical  []byte
	References []ProjectedReference
}

// CheckpointPayloadValidator is the only dependency from the archive contract
// to a versioned Agent handoff contract.
type CheckpointPayloadValidator interface {
	ValidateCheckpointPayload(
		raw []byte,
		task TaskRecord,
		checkpoint CheckpointRecord,
	) (PayloadProjection, error)
}

// HandoffV1PayloadValidator validates mem.handoff schema 1 using the
// authoritative public decoder and normalizer.
type HandoffV1PayloadValidator struct{}

func (HandoffV1PayloadValidator) ValidateCheckpointPayload(
	raw []byte,
	task TaskRecord,
	checkpoint CheckpointRecord,
) (PayloadProjection, error) {
	value, err := handoff.DecodeV1(raw)
	if err != nil {
		return PayloadProjection{}, fmt.Errorf(
			"%w: checkpoint %s payload: %v",
			ErrInvalidBundle,
			checkpoint.ID,
			err,
		)
	}
	value, err = handoff.NormalizeV1(value, task.TaskKey)
	if err != nil {
		return PayloadProjection{}, fmt.Errorf(
			"%w: checkpoint %s payload: %v",
			ErrInvalidBundle,
			checkpoint.ID,
			err,
		)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return PayloadProjection{}, fmt.Errorf(
			"%w: canonicalize checkpoint %s payload: %v",
			ErrInvalidBundle,
			checkpoint.ID,
			err,
		)
	}
	if !bytes.Equal(raw, canonical) {
		return PayloadProjection{}, fmt.Errorf(
			"%w: checkpoint %s payload is not canonical JSON",
			ErrIntegrity,
			checkpoint.ID,
		)
	}
	if got := sha256Hex(canonical); got != checkpoint.PayloadSHA256 {
		return PayloadProjection{}, fmt.Errorf(
			"%w: checkpoint %s payload sha256 is %s, record declares %s",
			ErrIntegrity,
			checkpoint.ID,
			got,
			checkpoint.PayloadSHA256,
		)
	}
	if value.Contract != checkpoint.Contract ||
		value.SchemaVersion != checkpoint.SchemaVersion ||
		value.CheckpointKind != checkpoint.CheckpointKind ||
		value.ScopePath != checkpoint.ScopePath ||
		!sameOptionalUUID(value.BaseCheckpointID, checkpoint.BaseCheckpointID) ||
		value.Producer.AgentID != checkpoint.ProducerAgent ||
		value.Producer.SessionID != checkpoint.ProducerSession {
		return PayloadProjection{}, fmt.Errorf(
			"%w: checkpoint %s projected columns disagree with payload",
			ErrIntegrity,
			checkpoint.ID,
		)
	}
	return PayloadProjection{
		Canonical:  canonical,
		References: projectHandoffV1References(value),
	}, nil
}

func projectHandoffV1References(value handoff.HandoffV1) []ProjectedReference {
	out := make([]ProjectedReference, 0)
	appendReference := func(
		relation string,
		uri string,
		expectedSHA256 string,
		required bool,
		metadata any,
	) {
		raw, _ := json.Marshal(metadata)
		out = append(out, ProjectedReference{
			Ordinal:        len(out),
			Relation:       relation,
			URI:            uri,
			ExpectedSHA256: expectedSHA256,
			Required:       required,
			Metadata:       raw,
		})
	}
	for itemIndex, decision := range value.State.Decisions {
		for referenceIndex, uri := range decision.References {
			appendReference("decision", uri, "", false, map[string]int{
				"item_index":      itemIndex,
				"reference_index": referenceIndex,
			})
		}
	}
	for itemIndex, step := range value.State.NextSteps {
		for referenceIndex, uri := range step.References {
			appendReference("next_step", uri, "", false, map[string]int{
				"item_index":      itemIndex,
				"reference_index": referenceIndex,
			})
		}
	}
	for itemIndex, blocker := range value.State.Blockers {
		for referenceIndex, uri := range blocker.References {
			appendReference("blocker", uri, "", false, map[string]int{
				"item_index":      itemIndex,
				"reference_index": referenceIndex,
			})
		}
	}
	for itemIndex, artifact := range value.State.Artifacts {
		required := false
		if artifact.Required != nil {
			required = *artifact.Required
		}
		appendReference("artifact", artifact.URI, artifact.SHA256, required, map[string]any{
			"item_index": itemIndex,
			"role":       artifact.Role,
		})
	}
	return out
}

func sameOptionalUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

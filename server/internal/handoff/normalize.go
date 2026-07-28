package handoff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/PeterGuy326/mem/server/internal/pathx"
	"github.com/google/uuid"
)

const (
	maxTaskKeyRunes        = 200
	maxIdempotencyKeyRunes = 200
	maxScopePathRunes      = 2048
	maxTextItemRunes       = 4096
	maxGoalRunes           = 16384
	maxProgressRunes       = 16384
	maxRationaleRunes      = 8192
	maxNeedsRunes          = 4096
	maxReferenceRunes      = 2048
	maxReferenceItems      = 100
	maxRoleRunes           = 200
	maxAgentIDRunes        = 200
	maxSessionIDRunes      = 200
	maxWorkingDirRunes     = 4096
	maxRevisionRunes       = 200
	maxBranchRunes         = 500
	maxStatusSummaryRunes  = 8192
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type normalizedCheckpointCommand struct {
	CheckpointCommand
	payload       []byte
	payloadSHA256 string
	requestSHA256 string
	references    []Reference
}

// DecodeV1 strictly decodes exactly one handoff.v1 JSON object. Unknown fields
// at any nesting level are rejected before semantic normalization.
func DecodeV1(raw []byte) (HandoffV1, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value HandoffV1
	if err := decoder.Decode(&value); err != nil {
		return HandoffV1{}, invalid("decode handoff v1: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return HandoffV1{}, invalid("handoff v1 must contain exactly one JSON value")
		}
		return HandoffV1{}, invalid("decode handoff v1: %v", err)
	}
	return value, nil
}

// NormalizeV1 validates and canonicalizes a v1 payload against the authoritative
// docs/schemas/handoff.v1.schema.json contract.
func NormalizeV1(in HandoffV1, routeTaskKey string) (HandoffV1, error) {
	// Normalize a deep copy. Callers may safely reuse the same command for
	// concurrent retries without this service mutating shared slices/pointers.
	in = cloneHandoffV1(in)
	if in.Contract != ContractName {
		return HandoffV1{}, invalid("contract must be %q", ContractName)
	}
	if in.SchemaVersion != SchemaVersionV1 {
		return HandoffV1{}, fmt.Errorf(
			"%w: schema_version %d is not supported",
			ErrUnsupportedVersion,
			in.SchemaVersion,
		)
	}
	switch in.CheckpointKind {
	case CheckpointKindCheckpoint, CheckpointKindHandoff:
	default:
		return HandoffV1{}, invalid("checkpoint_kind must be checkpoint or handoff")
	}

	in.TaskKey = strings.TrimSpace(in.TaskKey)
	if err := validateRequiredText("task_key", in.TaskKey, maxTaskKeyRunes); err != nil {
		return HandoffV1{}, err
	}
	routeTaskKey = strings.TrimSpace(routeTaskKey)
	if routeTaskKey == "" {
		return HandoffV1{}, invalid("task_key is required")
	}
	if in.TaskKey != routeTaskKey {
		return HandoffV1{}, invalid("payload task_key must equal route task_key")
	}
	if in.BaseCheckpointID != nil && *in.BaseCheckpointID == uuid.Nil {
		return HandoffV1{}, invalid("base_checkpoint_id must be a non-zero UUID")
	}

	rawScope := strings.TrimSpace(in.ScopePath)
	if rawScope == "" {
		return HandoffV1{}, invalid("scope_path is required")
	}
	scope, err := pathx.Normalize(rawScope)
	if err != nil {
		return HandoffV1{}, invalid("scope_path: %v", err)
	}
	if utf8.RuneCountInString(scope) > maxScopePathRunes {
		return HandoffV1{}, invalid("scope_path exceeds %d characters", maxScopePathRunes)
	}
	in.ScopePath = scope

	if err := normalizeState(&in.State); err != nil {
		return HandoffV1{}, err
	}
	in.Producer.AgentID = strings.TrimSpace(in.Producer.AgentID)
	if err := validateRequiredText("producer.agent_id", in.Producer.AgentID, maxAgentIDRunes); err != nil {
		return HandoffV1{}, err
	}
	in.Producer.SessionID = strings.TrimSpace(in.Producer.SessionID)
	if err := validateOptionalText("producer.session_id", in.Producer.SessionID, maxSessionIDRunes); err != nil {
		return HandoffV1{}, err
	}
	return in, nil
}

func cloneHandoffV1(in HandoffV1) HandoffV1 {
	out := in
	out.BaseCheckpointID = cloneUUID(in.BaseCheckpointID)
	out.State.Progress.Completed = cloneSlice(in.State.Progress.Completed)
	out.State.Decisions = cloneSlice(in.State.Decisions)
	for i := range out.State.Decisions {
		out.State.Decisions[i].References = cloneSlice(in.State.Decisions[i].References)
	}
	out.State.NextSteps = cloneSlice(in.State.NextSteps)
	for i := range out.State.NextSteps {
		out.State.NextSteps[i].References = cloneSlice(in.State.NextSteps[i].References)
	}
	out.State.Blockers = cloneSlice(in.State.Blockers)
	for i := range out.State.Blockers {
		out.State.Blockers[i].References = cloneSlice(in.State.Blockers[i].References)
	}
	out.State.OpenQuestions = cloneSlice(in.State.OpenQuestions)
	out.State.Artifacts = cloneSlice(in.State.Artifacts)
	for i := range out.State.Artifacts {
		if in.State.Artifacts[i].Required != nil {
			required := *in.State.Artifacts[i].Required
			out.State.Artifacts[i].Required = &required
		}
	}
	if in.State.WorkspaceState != nil {
		workspaceState := *in.State.WorkspaceState
		out.State.WorkspaceState = &workspaceState
		if in.State.WorkspaceState.VCS != nil {
			vcs := *in.State.WorkspaceState.VCS
			out.State.WorkspaceState.VCS = &vcs
		}
	}
	return out
}

func cloneSlice[T any](in []T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	copy(out, in)
	return out
}

func normalizeCheckpointCommand(cmd CheckpointCommand) (normalizedCheckpointCommand, error) {
	if cmd.WorkspaceID == uuid.Nil {
		return normalizedCheckpointCommand{}, invalid("workspace_id is required")
	}
	cmd.TaskKey = strings.TrimSpace(cmd.TaskKey)
	if err := validateRequiredText("task_key", cmd.TaskKey, maxTaskKeyRunes); err != nil {
		return normalizedCheckpointCommand{}, err
	}
	cmd.IdempotencyKey = strings.TrimSpace(cmd.IdempotencyKey)
	if err := validateRequiredText(
		"idempotency_key",
		cmd.IdempotencyKey,
		maxIdempotencyKeyRunes,
	); err != nil {
		return normalizedCheckpointCommand{}, err
	}

	handoff, err := NormalizeV1(cmd.Handoff, cmd.TaskKey)
	if err != nil {
		return normalizedCheckpointCommand{}, err
	}
	cmd.Handoff = handoff
	payload, err := json.Marshal(handoff)
	if err != nil {
		return normalizedCheckpointCommand{}, fmt.Errorf("marshal normalized handoff: %w", err)
	}
	payloadHash := sha256.Sum256(payload)
	requestPayload := struct {
		WorkspaceID string          `json:"workspace_id"`
		TaskKey     string          `json:"task_key"`
		Handoff     json.RawMessage `json:"handoff"`
	}{
		WorkspaceID: cmd.WorkspaceID.String(),
		TaskKey:     cmd.TaskKey,
		Handoff:     payload,
	}
	requestJSON, err := json.Marshal(requestPayload)
	if err != nil {
		return normalizedCheckpointCommand{}, fmt.Errorf("marshal checkpoint request hash: %w", err)
	}
	requestHash := sha256.Sum256(requestJSON)

	return normalizedCheckpointCommand{
		CheckpointCommand: cmd,
		payload:           payload,
		payloadSHA256:     hex.EncodeToString(payloadHash[:]),
		requestSHA256:     hex.EncodeToString(requestHash[:]),
		references:        collectReferences(handoff),
	}, nil
}

func normalizeState(state *StateV1) error {
	switch state.Status {
	case TaskStatusInProgress, TaskStatusReady, TaskStatusBlocked, TaskStatusComplete:
	default:
		return invalid("state.status must be in_progress, ready, blocked, or complete")
	}
	state.Goal = strings.TrimSpace(state.Goal)
	if err := validateRequiredText("state.goal", state.Goal, maxGoalRunes); err != nil {
		return err
	}
	state.Progress.Summary = strings.TrimSpace(state.Progress.Summary)
	if err := validateRequiredText(
		"state.progress.summary",
		state.Progress.Summary,
		maxProgressRunes,
	); err != nil {
		return err
	}
	if state.Progress.Completed == nil {
		return invalid("state.progress.completed is required")
	}
	if err := normalizeTextList(
		"state.progress.completed",
		state.Progress.Completed,
		maxTextItemRunes,
	); err != nil {
		return err
	}

	if state.Decisions == nil {
		return invalid("state.decisions is required")
	}
	if len(state.Decisions) > maxReferenceItems {
		return invalid("state.decisions exceeds %d items", maxReferenceItems)
	}
	for i := range state.Decisions {
		item := &state.Decisions[i]
		item.Summary = strings.TrimSpace(item.Summary)
		if err := validateRequiredText(
			fmt.Sprintf("state.decisions[%d].summary", i),
			item.Summary,
			maxTextItemRunes,
		); err != nil {
			return err
		}
		item.Rationale = strings.TrimSpace(item.Rationale)
		if err := validateOptionalText(
			fmt.Sprintf("state.decisions[%d].rationale", i),
			item.Rationale,
			maxRationaleRunes,
		); err != nil {
			return err
		}
		if err := normalizeReferenceList(
			fmt.Sprintf("state.decisions[%d].references", i),
			item.References,
		); err != nil {
			return err
		}
	}

	if state.NextSteps == nil {
		return invalid("state.next_steps is required")
	}
	if len(state.NextSteps) > maxReferenceItems {
		return invalid("state.next_steps exceeds %d items", maxReferenceItems)
	}
	for i := range state.NextSteps {
		item := &state.NextSteps[i]
		item.Summary = strings.TrimSpace(item.Summary)
		if err := validateRequiredText(
			fmt.Sprintf("state.next_steps[%d].summary", i),
			item.Summary,
			maxTextItemRunes,
		); err != nil {
			return err
		}
		if err := normalizeReferenceList(
			fmt.Sprintf("state.next_steps[%d].references", i),
			item.References,
		); err != nil {
			return err
		}
	}

	if state.Blockers == nil {
		return invalid("state.blockers is required")
	}
	if len(state.Blockers) > maxReferenceItems {
		return invalid("state.blockers exceeds %d items", maxReferenceItems)
	}
	for i := range state.Blockers {
		item := &state.Blockers[i]
		item.Summary = strings.TrimSpace(item.Summary)
		if err := validateRequiredText(
			fmt.Sprintf("state.blockers[%d].summary", i),
			item.Summary,
			maxTextItemRunes,
		); err != nil {
			return err
		}
		item.Needs = strings.TrimSpace(item.Needs)
		if err := validateOptionalText(
			fmt.Sprintf("state.blockers[%d].needs", i),
			item.Needs,
			maxNeedsRunes,
		); err != nil {
			return err
		}
		if err := normalizeReferenceList(
			fmt.Sprintf("state.blockers[%d].references", i),
			item.References,
		); err != nil {
			return err
		}
	}

	if state.OpenQuestions == nil {
		return invalid("state.open_questions is required")
	}
	if err := normalizeTextList(
		"state.open_questions",
		state.OpenQuestions,
		maxTextItemRunes,
	); err != nil {
		return err
	}

	if state.Artifacts == nil {
		return invalid("state.artifacts is required")
	}
	if len(state.Artifacts) > maxReferenceItems {
		return invalid("state.artifacts exceeds %d items", maxReferenceItems)
	}
	for i := range state.Artifacts {
		item := &state.Artifacts[i]
		item.URI = strings.TrimSpace(item.URI)
		if err := validateRequiredText(
			fmt.Sprintf("state.artifacts[%d].uri", i),
			item.URI,
			maxReferenceRunes,
		); err != nil {
			return err
		}
		item.Role = strings.TrimSpace(item.Role)
		if err := validateOptionalText(
			fmt.Sprintf("state.artifacts[%d].role", i),
			item.Role,
			maxRoleRunes,
		); err != nil {
			return err
		}
		item.SHA256 = strings.TrimSpace(item.SHA256)
		if item.SHA256 != "" && !sha256Pattern.MatchString(item.SHA256) {
			return invalid("state.artifacts[%d].sha256 must be 64 lowercase hex characters", i)
		}
		if item.Required == nil {
			return invalid("state.artifacts[%d].required is required", i)
		}
	}

	if state.WorkspaceState != nil {
		ws := state.WorkspaceState
		ws.WorkingDirectory = strings.TrimSpace(ws.WorkingDirectory)
		if err := validateOptionalText(
			"state.workspace_state.working_directory",
			ws.WorkingDirectory,
			maxWorkingDirRunes,
		); err != nil {
			return err
		}
		if ws.VCS != nil {
			ws.VCS.Revision = strings.TrimSpace(ws.VCS.Revision)
			ws.VCS.Branch = strings.TrimSpace(ws.VCS.Branch)
			ws.VCS.StatusSummary = strings.TrimSpace(ws.VCS.StatusSummary)
			for _, field := range []struct {
				name  string
				value string
				max   int
			}{
				{"state.workspace_state.vcs.revision", ws.VCS.Revision, maxRevisionRunes},
				{"state.workspace_state.vcs.branch", ws.VCS.Branch, maxBranchRunes},
				{"state.workspace_state.vcs.status_summary", ws.VCS.StatusSummary, maxStatusSummaryRunes},
			} {
				if err := validateOptionalText(field.name, field.value, field.max); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func normalizeReferenceList(field string, refs []string) error {
	if refs == nil {
		return invalid("%s is required", field)
	}
	return normalizeTextList(field, refs, maxReferenceRunes)
}

func normalizeTextList(field string, values []string, maxRunes int) error {
	if len(values) > maxReferenceItems {
		return invalid("%s exceeds %d items", field, maxReferenceItems)
	}
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
		if err := validateRequiredText(
			fmt.Sprintf("%s[%d]", field, i),
			values[i],
			maxRunes,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateRequiredText(field, value string, maxRunes int) error {
	if !utf8.ValidString(value) {
		return invalid("%s must be valid UTF-8", field)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return invalid("%s must not contain NUL", field)
	}
	n := utf8.RuneCountInString(value)
	if n < 1 {
		return invalid("%s must not be empty", field)
	}
	if n > maxRunes {
		return invalid("%s exceeds %d characters", field, maxRunes)
	}
	return nil
}

func validateOptionalText(field, value string, maxRunes int) error {
	if value == "" {
		return nil
	}
	return validateRequiredText(field, value, maxRunes)
}

func collectReferences(handoff HandoffV1) []Reference {
	out := make([]Reference, 0)
	appendReference := func(relation, uri, sha string, required bool, metadata any) {
		raw, _ := json.Marshal(metadata)
		out = append(out, Reference{
			Ordinal:        len(out),
			Relation:       relation,
			URI:            uri,
			ExpectedSHA256: sha,
			Required:       required,
			Metadata:       raw,
		})
	}
	for itemIndex, decision := range handoff.State.Decisions {
		for referenceIndex, uri := range decision.References {
			appendReference("decision", uri, "", false, map[string]int{
				"item_index": itemIndex, "reference_index": referenceIndex,
			})
		}
	}
	for itemIndex, step := range handoff.State.NextSteps {
		for referenceIndex, uri := range step.References {
			appendReference("next_step", uri, "", false, map[string]int{
				"item_index": itemIndex, "reference_index": referenceIndex,
			})
		}
	}
	for itemIndex, blocker := range handoff.State.Blockers {
		for referenceIndex, uri := range blocker.References {
			appendReference("blocker", uri, "", false, map[string]int{
				"item_index": itemIndex, "reference_index": referenceIndex,
			})
		}
	}
	for itemIndex, artifact := range handoff.State.Artifacts {
		appendReference("artifact", artifact.URI, artifact.SHA256, *artifact.Required, map[string]any{
			"item_index": itemIndex,
			"role":       artifact.Role,
		})
	}
	return out
}

func normalizeQueryScope(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return pathx.Root, nil
	}
	scope, err := pathx.Normalize(raw)
	if err != nil {
		return "", invalid("scope: %v", err)
	}
	return scope, nil
}

func normalizeAllowedPaths(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(raw))
	paths := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, invalid("allowed paths contain an empty path")
		}
		path, err := pathx.Normalize(item)
		if err != nil {
			return nil, invalid("allowed path %q: %v", item, err)
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	if _, unrestricted := seen[pathx.Root]; unrestricted {
		return []string{pathx.Root}, nil
	}
	sort.Slice(paths, func(i, j int) bool {
		if len(paths[i]) == len(paths[j]) {
			return paths[i] < paths[j]
		}
		return len(paths[i]) < len(paths[j])
	})
	compacted := make([]string, 0, len(paths))
	for _, candidate := range paths {
		covered := false
		for _, ancestor := range compacted {
			if pathx.IsDescendantOrSelf(candidate, ancestor) {
				covered = true
				break
			}
		}
		if !covered {
			compacted = append(compacted, candidate)
		}
	}
	sort.Strings(compacted)
	return compacted, nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidCommand, fmt.Sprintf(format, args...))
}

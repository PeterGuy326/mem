// Package workspacebundle defines and validates the portable .membundle v1/v2
// archive format.
//
// The package deliberately stops at the archive boundary. It does not perform
// authorization, database writes, object-store writes, ID remapping, or index
// rebuilds. Callers must validate an archive here before planning or applying a
// restore.
//
// Integration note: handoff.DecodeV1 and handoff.NormalizeV1 are exported, but
// the handoff package's reference projector is not. CheckpointPayloadValidator
// is therefore an explicit seam. The default v1 implementation projects the
// public handoff.HandoffV1 type with the same ordered rules; a future handoff
// package adapter can replace it without weakening this archive contract.
package workspacebundle

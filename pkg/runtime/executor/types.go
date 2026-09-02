// Package executor implements the deterministic transactional execution
// engine of the Izen runtime control plane. An LLM-emitted ProposedMutation is
// strictly untrusted: ProposalValidator performs deterministic schema, target
// safety, and patch applicability checks and produces a diff.MutationEvidence
// for UI projection; RuntimeExecutor then applies the mutation atomically
// (write-to-temp-and-rename) over a captured FileBackup snapshot, guaranteeing
// zero-orphan rollback on commit failure or user cancellation.
package executor

import (
	"github.com/PizenLabs/izen/pkg/projection/diff"
	"github.com/PizenLabs/izen/pkg/runtime/target"
)

// ProposedMutation is a non-authoritative, LLM-suggested file mutation. It
// must pass deterministic validation before the runtime arms an approval
// session against it.
type ProposedMutation struct {
	// ProposalID identifies the proposal across the approval session.
	ProposalID string
	// TargetRef is the resolved identity of the target file. It is the only
	// authority for where the mutation is applied.
	TargetRef *target.TargetRef
	// RawPatch is the mutation payload: either a unified diff or the full
	// post-mutation file content.
	RawPatch string
	// PatchLines is an optional typed line encoding of the mutation
	// (MutationAdd / MutationModify lines form the post-mutation content,
	// MutationDelete lines are dropped). When present it takes precedence
	// over RawPatch for materialization.
	PatchLines []diff.PatchLine
}

// ValidationResult is the deterministic outcome of validating a proposal.
type ValidationResult struct {
	// Valid reports whether every deterministic check passed.
	Valid bool
	// ErrorReason is a descriptive reason when Valid is false.
	ErrorReason string
	// Evidence is the diff evidence projected to the UI before approval.
	Evidence diff.MutationEvidence
	// RequiresRollback reports whether the caller must restore pre-validation
	// filesystem state before proceeding. Validation is side-effect-free, so
	// this is always false at validation time; RuntimeExecutor owns rollback
	// on commit failure.
	RequiresRollback bool
}

// FileBackup is the pre-mutation snapshot of a target file captured by
// RuntimeExecutor.PrepareSnapshot. It is the single source of truth for
// rollback: when Exists is true, Rollback restores the captured content and
// file mode byte-for-byte; when Exists is false, Rollback removes whatever
// the commit created.
type FileBackup struct {
	// Path is the filesystem path the snapshot belongs to.
	Path string
	// Exists reports whether the file existed when the snapshot was taken.
	Exists bool
	// Content is the captured byte content of the file.
	Content []byte
	// FileMode is the captured permission bits of the file (mode.Perm()).
	FileMode uint32
}

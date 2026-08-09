// Package changeset implements the strict MODEL OUTPUT → ChangeSet IR → Diff
// pipeline that decouples LLM output from the authoritative patch format.
//
// LLM output is treated as a NON-AUTHORITATIVE artifact: it is normalized,
// classified, and reduced to a typed ChangeSet IR. The Diff Compiler is the
// SINGLE AUTHORITATIVE SOURCE of ---/+++ unified diff patches in the
// application; model output is never applied to disk directly.
package changeset

import "errors"

// ChangeKind classifies how a ChangeSet mutates its target file.
type ChangeKind string

const (
	// KindApplyDiff carries an already-synthesized unified diff verbatim.
	// The payload (NewContent) is authoritative and returned as-is by the
	// compiler.
	KindApplyDiff ChangeKind = "APPLY_DIFF"
	// KindReplaceBlock replaces a located anchor (OldContent) with NewContent
	// inside the target file. The compiler reconstructs the buffer and emits
	// the authoritative unified diff.
	KindReplaceBlock ChangeKind = "REPLACE_BLOCK"
	// KindReplaceFile rewrites the whole target file with NewContent. The
	// compiler computes the programmatic diff against the on-disk original.
	KindReplaceFile ChangeKind = "REPLACE_FILE"
)

// ErrAmbiguousChange is the ambiguity guard sentinel. It is returned when model
// output cannot be mapped safely onto the target file's structure (no resolvable
// target, an unmatchable snippet, or a structurally ambiguous anchor). The
// pipeline PAUSES and NEVER falls back to a destructive full-file overwrite.
//
//nolint:staticcheck // ST1005: the trailing period is the spec-mandated pipeline pause contract.
var ErrAmbiguousChange = errors.New("Pipeline PAUSED. Reason: ambiguous change representation.")

// ChangeSet is the intermediate representation of one intended change. It is
// the boundary between the Change Extractor (model intent) and the Diff
// Compiler (authoritative diff synthesis).
type ChangeSet struct {
	// TargetFile is the workspace-relative path the change targets.
	TargetFile string
	// Kind selects how NewContent mutates TargetFile.
	Kind ChangeKind
	// OldContent is the optional context anchor for REPLACE_BLOCK. It is the
	// exact on-disk text that NewContent replaces.
	OldContent string
	// NewContent is the replacement payload: for APPLY_DIFF the raw unified
	// diff; for REPLACE_BLOCK/REPLACE_FILE the replacement file content.
	NewContent string
	// Confidence reports how confidently the extractor mapped the model intent
	// onto the target file (1.0 for explicit formats, fuzzy similarity for
	// anchor-matched blocks).
	Confidence float64
}

// Package patch implements the multi-tier patch engine from the architecture
// RFC: a separation of PatchParser, PatchValidator, PatchApplicator and
// SafetyEvaluator behind small interfaces, orchestrated by the Engine.
//
// Tiers:
//
//	Tier 1 — Structured Diff (unified diff)
//	Tier 2 — Search/Replace blocks (<<<<<<< SEARCH / ======= / >>>>>>> REPLACE)
//	Tier 3 — Whole File Rewrite fallback
//	Tier 4 — Human-in-the-Loop Approval
//
// The engine walks tiers in order. A lower tier that produces an unambiguous,
// validated, safe patch wins. Tier 4 is reached either when no lower tier can
// resolve the payload or when the ContextualSafetyEvaluator deems the change
// high-risk (destructive or a full rewrite of a source file); at that point an
// ApprovalRequested event is emitted and the caller must re-invoke with the
// Approved flag set.
package patch

import "errors"

// Tier identifies the patch strategy used to interpret a payload.
type Tier int

const (
	// Tier1StructuredDiff is a unified diff with hunk headers.
	Tier1StructuredDiff Tier = 1
	// Tier2SearchReplace is a SEARCH/REPLACE block.
	Tier2SearchReplace Tier = 2
	// Tier3WholeFile is a whole-file rewrite fallback.
	Tier3WholeFile Tier = 3
	// Tier4HumanApproval is a human-in-the-loop approval gate.
	Tier4HumanApproval Tier = 4
)

func (t Tier) String() string {
	switch t {
	case Tier1StructuredDiff:
		return "DIFF_PATCH"
	case Tier2SearchReplace:
		return "SEARCH_REPLACE"
	case Tier3WholeFile:
		return "WHOLE_FILE"
	case Tier4HumanApproval:
		return "APPROVAL"
	default:
		return "UNKNOWN"
	}
}

// Sentinel errors.
var (
	// ErrAmbiguousPatch is returned when a patch payload cannot be safely
	// interpreted by any tier (a raw snippet that is neither a unified diff,
	// a SEARCH/REPLACE block, nor a plausible full rewrite).
	ErrAmbiguousPatch = errors.New("ambiguous patch")

	// ErrSafetyViolation is returned when the SafetyEvaluator rejects a patch
	// and the change is not eligible for Tier 4 approval.
	ErrSafetyViolation = errors.New("safety violation")

	// ErrApprovalRequired is returned when Tier 4 must be engaged: the change
	// is high-risk or no lower tier could resolve it, so a human must approve
	// before the engine will apply anything.
	ErrApprovalRequired = errors.New("human approval required")

	// ErrAlreadyApplied signals idempotency: the target file already contains
	// the desired end state, so the engine refuses to re-apply.
	ErrAlreadyApplied = errors.New("patch already applied")
)

// Patch is a fully-resolved mutation: the target file, its original content,
// the resolved final content, and the tier that produced it.
type Patch struct {
	File     string
	Original string
	Modified string
	Tier     Tier
	Strategy string
}

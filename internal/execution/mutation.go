package execution

import "strings"

// ── Mutation semantics (Phase 4 — execution truth) ──────────────────────────
//
// The runtime owns the truth of a mutation attempt. The execution artifact
// owns the mutation intent. The filesystem owns the mutation reality. The
// renderer only projects these facts — it never infers "Edit(file) => file
// changed" or "task completed => mutation succeeded".
//
// This file defines the single authoritative vocabulary for:
//
//   - the semantic lifecycle stage of a mutation-capable execution
//     (MutationStage): plan → model → artifact → patch → apply → verify →
//     result. One generic "Edit" event can never represent several stages.
//
//   - the semantic outcome of a mutation attempt (MutationOutcome). NO_CHANGE
//     is ONLY valid when a valid mutation artifact existed and was applied or
//     compared against the actual filesystem with byte-for-byte identical
//     results. When no valid artifact exists the result is NO_ARTIFACT or
//     PATCH_GENERATION_FAILED — never NO_CHANGE.
//
//   - the mutation evidence record (MutationEvidence): the single source of
//     truth for artifact presence, patch presence, apply execution,
//     filesystem mutation and verification result.

// MutationStage is the semantic lifecycle stage of a mutation-capable
// execution. It is the shared vocabulary for the execution log and the
// presentation layer.
type MutationStage string

// Semantic lifecycle stages.
const (
	StagePlan     MutationStage = "plan"     // planned/intended action
	StageModel    MutationStage = "model"    // model artifact received
	StageArtifact MutationStage = "artifact" // a concrete mutation artifact exists
	StagePatch    MutationStage = "patch"    // a compiled diff / bounded patch exists
	StageApply    MutationStage = "apply"    // authorized mutation executed against the filesystem
	StageVerify   MutationStage = "verify"   // filesystem mutation verified
	StageResult   MutationStage = "result"   // terminal result
)

// Display returns the canonical stage label used by the renderer.
func (s MutationStage) Display() string {
	switch s {
	case StagePlan:
		return "Plan"
	case StageModel:
		return "Model"
	case StageArtifact:
		return "Artifact"
	case StagePatch:
		return "Patch"
	case StageApply:
		return "Apply"
	case StageVerify:
		return "Verify"
	case StageResult:
		return "Result"
	default:
		return string(s)
	}
}

// MutationOutcome is the semantic result of a mutation attempt. The status
// vocabulary is shared by the runtime and the renderer.
type MutationOutcome string

// Semantic outcomes. NO_CHANGE is only ever produced after a valid mutation
// artifact was compared against the actual filesystem and found byte-for-byte
// unchanged.
const (
	OutcomeChanged               MutationOutcome = "changed"
	OutcomeCreated               MutationOutcome = "created"
	OutcomeNoChange              MutationOutcome = "nochange"
	OutcomeNoArtifact            MutationOutcome = "no_artifact"
	OutcomePatchGenerationFailed MutationOutcome = "patch_failed"
	OutcomeApplyFailed           MutationOutcome = "apply_failed"
	OutcomeVerifyFailed          MutationOutcome = "verify_failed"
	OutcomeSkipped               MutationOutcome = "skipped"
	OutcomeCancelled             MutationOutcome = "cancelled"
)

// Display returns the human-readable outcome label.
func (o MutationOutcome) Display() string {
	switch o {
	case OutcomeChanged:
		return "changed"
	case OutcomeCreated:
		return "created"
	case OutcomeNoChange:
		return "nochange"
	case OutcomeNoArtifact:
		return "no artifact"
	case OutcomePatchGenerationFailed:
		return "patch generation failed"
	case OutcomeApplyFailed:
		return "apply failed"
	case OutcomeVerifyFailed:
		return "verify failed"
	case OutcomeSkipped:
		return "skipped"
	case OutcomeCancelled:
		return "cancelled"
	default:
		return string(o)
	}
}

// MutationSucceeded reports whether the outcome represents an actual
// filesystem mutation (changed/created). A planned intent, a nochange, a
// skipped apply, or a failure are NEVER a successful mutation.
func (o MutationOutcome) MutationSucceeded() bool {
	switch o {
	case OutcomeChanged, OutcomeCreated:
		return true
	default:
		return false
	}
}

// ParseMutationOutcome normalizes a free-form status string onto the semantic
// vocabulary. Unknown strings map to OutcomeNoArtifact so a vague status can
// never claim a mutation happened.
func ParseMutationOutcome(s string) MutationOutcome {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "changed", "modified":
		return OutcomeChanged
	case "created":
		return OutcomeCreated
	case "nochange", "unchanged", "no change":
		return OutcomeNoChange
	case "no_artifact", "no-artifact", "no artifact":
		return OutcomeNoArtifact
	case "patch_failed", "patch-failed", "patch generation failed":
		return OutcomePatchGenerationFailed
	case "apply_failed", "apply-failed", "apply failed":
		return OutcomeApplyFailed
	case "verify_failed", "verify-failed", "verify failed":
		return OutcomeVerifyFailed
	case "skipped":
		return OutcomeSkipped
	case "cancelled", "canceled":
		return OutcomeCancelled
	default:
		return OutcomeNoArtifact
	}
}

// MutationEvidence is the single source of truth for a mutation attempt's
// facts. The runtime fills it at every boundary; the renderer projects it.
// It is deliberately a plain value record with no presentation semantics.
type MutationEvidence struct {
	// Stage is the terminal lifecycle stage reached by the attempt.
	Stage MutationStage `json:"stage"`
	// File is the mutation target.
	File string `json:"file"`
	// Outcome is the semantic terminal outcome.
	Outcome MutationOutcome `json:"outcome"`

	// ArtifactPresent is true when the model produced a concrete mutation
	// artifact (unified diff / bounded SEARCH/REPLACE patch / full content).
	ArtifactPresent bool `json:"artifact_present"`
	// DiffPresent is true when a compiled, non-empty diff exists.
	DiffPresent bool `json:"diff_present"`
	// DiffAdds / DiffRemoves are the compiled diff line metrics.
	DiffAdds    int `json:"diff_adds,omitempty"`
	DiffRemoves int `json:"diff_removes,omitempty"`

	// ApplyExecuted is true when the apply step ran against the filesystem.
	ApplyExecuted bool `json:"apply_executed"`
	// FilesystemChanged reports whether the post-apply content differs
	// byte-for-byte from the pre-apply content.
	FilesystemChanged bool `json:"filesystem_changed"`

	// VerificationRun is true when a verification pass executed.
	VerificationRun bool `json:"verification_run"`
	// VerificationPassed reports the verification outcome.
	VerificationPassed bool `json:"verification_passed"`

	// Reason carries a free-form explanation (verification detail, failure
	// message). It never carries the outcome itself.
	Reason string `json:"reason,omitempty"`
}

// ApplyExecutedChanged reports whether the apply step both ran and mutated the
// filesystem. This is the only combination that represents a real mutation.
func (e MutationEvidence) ApplyExecutedChanged() bool {
	return e.ApplyExecuted && e.FilesystemChanged
}

// Verify reports whether verification ran and passed.
func (e MutationEvidence) Verify() bool {
	return e.VerificationRun && e.VerificationPassed
}

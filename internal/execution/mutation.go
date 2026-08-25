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
	// OutcomeArtifactRejected is the terminal outcome of an execution whose
	// model output produced an artifact that FAILED the artifact validation
	// boundary with an abort decision (malformed HTML/JSON/Go, raw patch markers).
	// It is distinct from OutcomePatchGenerationFailed: an artifact existed but
	// was rejected before any approval or mutation surface.
	OutcomeArtifactRejected          MutationOutcome = "artifact_rejected"
	OutcomeArtifactRetryableRejected MutationOutcome = "artifact_retryable_rejected"
	OutcomeTruncated                 MutationOutcome = "truncated"
	OutcomeApplyFailed               MutationOutcome = "apply_failed"
	OutcomeVerifyFailed              MutationOutcome = "verify_failed"
	// OutcomeOCCAborted is the terminal outcome of an execution whose Phase 3
	// OCC commit gate found the target state diverged from the admitted
	// baseline. Nothing was applied — the gate precedes the apply stage — and
	// the sealed evidence outcome is ABORTED_OCC with tainted mutations.
	OutcomeOCCAborted MutationOutcome = "occ_aborted"
	OutcomeSkipped    MutationOutcome = "skipped"
	OutcomeCancelled  MutationOutcome = "cancelled"
	// OutcomePendingApproval is the outcome of a targeted mutation that stopped
	// at the human-in-the-loop approval gate with a valid held artifact. It is
	// NEVER a terminal mutation outcome — the execution is paused, awaiting a
	// human decision (Approve/Reject).
	OutcomePendingApproval MutationOutcome = "pending_approval"
	// OutcomeRejected is the terminal outcome of an execution whose proposal
	// the human explicitly rejected at the approval gate. It is distinct from
	// OutcomeCancelled (an execution aborted mid-run): a rejection is a
	// deliberate decision on a held artifact.
	OutcomeRejected MutationOutcome = "rejected"
	// OutcomeFailed is the generic terminal execution failure (no artifact was
	// produced, or a non-apply stage failed). It is distinct from the
	// apply/verify-specific failures so evidence never overclaims a stage.
	OutcomeFailed MutationOutcome = "failed"
	// OutcomePreflightInfeasible is the terminal outcome of an execution that
	// was trapped at Boundary 2 (Preflight Guard, invariant I5): the estimated
	// generation budget exceeded max_output, so the request was refused BEFORE
	// any provider request. No artifact existed and none was attempted.
	OutcomePreflightInfeasible MutationOutcome = "preflight_infeasible"
	// OutcomeCompleted is the terminal outcome of a read-only execution that
	// produced an artifact (explanation / plan / investigation) with no
	// mutation. It never claims a filesystem change.
	OutcomeCompleted MutationOutcome = "completed"
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
	case OutcomeArtifactRejected:
		return "artifact rejected"
	case OutcomeArtifactRetryableRejected:
		return "artifact retryable rejected"
	case OutcomeTruncated:
		return "truncated"
	case OutcomeApplyFailed:
		return "apply failed"
	case OutcomeVerifyFailed:
		return "verify failed"
	case OutcomeOCCAborted:
		return "occ aborted"
	case OutcomeSkipped:
		return "skipped"
	case OutcomeCancelled:
		return "cancelled"
	case OutcomePendingApproval:
		return "pending approval"
	case OutcomeRejected:
		return "rejected"
	case OutcomePreflightInfeasible:
		return "preflight infeasible"
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

// AggregateMutationOutcome derives the aggregate semantic result of a
// multi-file mutation from its per-file evidence. The rules are explicit so a
// batch never overclaims:
//
//   - any changed file            → changed   (the mutation produced a mutation)
//   - otherwise any created file  → created
//   - otherwise any nochange file → nochange  (everything applied, nothing changed)
//   - otherwise                   → no_artifact (no apply evidence)
//
// A failure outcome is NEVER derived here: failures carry their own explicit
// aggregate (apply_failed / verify_failed / cancelled) chosen at the boundary
// that rolled back the transaction.
func AggregateMutationOutcome(evidence []MutationEvidence) MutationOutcome {
	nochange := false
	created := false
	changed := false
	for _, ev := range evidence {
		switch ev.Outcome {
		case OutcomeChanged:
			changed = true
		case OutcomeCreated:
			created = true
		case OutcomeNoChange:
			nochange = true
		}
	}
	switch {
	case changed:
		return OutcomeChanged
	case created:
		return OutcomeCreated
	case nochange:
		return OutcomeNoChange
	default:
		return OutcomeNoArtifact
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
	case "artifact_rejected", "artifact-rejected", "artifact rejected":
		return OutcomeArtifactRejected
	case "artifact_retryable_rejected", "artifact-retryable-rejected", "artifact retryable rejected":
		return OutcomeArtifactRetryableRejected
	case "truncated", "output_truncated", "output truncated":
		return OutcomeTruncated
	case "apply_failed", "apply-failed", "apply failed":
		return OutcomeApplyFailed
	case "verify_failed", "verify-failed", "verify failed":
		return OutcomeVerifyFailed
	case "occ_aborted", "occ-aborted", "occ aborted":
		return OutcomeOCCAborted
	case "skipped":
		return OutcomeSkipped
	case "cancelled", "canceled":
		return OutcomeCancelled
	case "pending_approval", "pending approval":
		return OutcomePendingApproval
	case "rejected":
		return OutcomeRejected
	case "failed", "execution_failed":
		return OutcomeFailed
	case "completed", "done":
		return OutcomeCompleted
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

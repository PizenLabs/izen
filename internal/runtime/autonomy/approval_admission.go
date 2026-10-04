package autonomy

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── APPROVAL ADMISSION (the gate before the human gate) ─────────────────────
//
// The state machine an approval boundary must obey is:
//
//	candidate valid
//	    ↓
//	authorization admissible
//	    ↓
//	approval required
//
// NOT:
//
//	candidate exists
//	    ↓
//	show approval
//	    ↓
//	discover authorization is impossible
//
// The second shape is a UI lie told by the runtime: it opens a human decision
// surface for a proposal the runtime already knows it cannot authorize, and the
// operator discovers the refusal only by pressing Approve.
//
// This file is the enforcement point. It runs INSIDE the driver, at the single
// choke point every parked boundary passes through (enrichBoundary), so the
// downgraded state is what any consumer of Driver.Boundary() observes — the TUI
// is a projection of an already-correct runtime state, not the thing being
// corrected.
//
// Two independent facts must hold before a boundary may be presented as an
// approval:
//
//  1. CANDIDATE VALIDITY — the held artifact still exists. A candidate is
//     executable only while the computation that produced it is live
//     (spec §14): a computation that FAILED, was SUPERSEDED, was CANCELLED or
//     exhausted its output budget without delivering bytes must leave nothing
//     executable. The answer comes from the executor's own pending-candidate map
//     — the same map Reject drains on supersession and Approve resolves — so no
//     second lineage system is introduced.
//
//  2. AUTHORIZATION ADMISSIBILITY — every NON-HUMAN clause of the production
//     AuthorizationEngine passes: scope containment, mutation budget
//     sufficiency, checkpoint availability, and the unified PolicyEngine. The
//     probe is the AuthorizationEngine's own AdmissibleBuild, so the pre-check
//     and the real authorization evaluate the same clause list and cannot drift.
//
// A refusal downgrades the boundary to a non-resumable informational park whose
// reason states the terminal authorization outcome. It never fabricates a
// completion and never mutates anything.

// ApprovalAdmissionFunc is the runtime's answer to "may this candidate reach a
// human approval gate at all?". It returns nil when the proposal is admissible,
// or a non-nil reason naming the clause that refuses it.
//
// targets is the boundary's authoritative target set and candidateID is the
// identity of the proposal being authorized: a MutationCandidate id for a held
// artifact, or a staged-plan identity for a DECOMPOSITION_PROPOSAL.
//
// The function MUST NOT consume budget or mutate state: it is a read of the
// runtime's own authorities, invoked before a human is asked.
type ApprovalAdmissionFunc func(targets []string, candidateID string) error

// CandidateReviewFunc is the runtime's read of the held candidate a human is
// being asked to authorize. It returns the concrete change — operation, targets,
// compiled diff, content digest — or ok=false when nothing previewable is held.
//
// It exists so the mutation review boundary presents the ACTUAL candidate instead
// of a target name. Like ApprovalAdmissionFunc it is a pure read: it consumes no
// budget and mutates nothing, because REVIEWING a candidate is not authorizing
// it.
type CandidateReviewFunc func(candidateID string) (execution.CandidatePreview, bool)

// WithCandidateReview overrides the candidate-preview authority.
//
// The DEFAULT is the adapter's own read of the executor's held-candidate record —
// the same map Approve consumes — so the reviewed bytes and the applied bytes are
// the same object by construction. There is deliberately no "unwired" mode: a
// driver always has a preview source, and if that source cannot show the change
// the boundary is refused rather than presented as a bare approval.
func WithCandidateReview(f CandidateReviewFunc) Option {
	return func(d *Driver) { d.review = f }
}

// WithApprovalAdmission binds the runtime's approval-admission authority.
//
// Passing nil leaves the driver's historical behaviour (an approval boundary
// derived purely from the loop's candidate identity) intact. The production
// composition root always binds it: it is the same AuthorizationEngine that
// issues the token on approve, so the pre-check cannot be stricter or laxer than
// the authorization it previews.
func WithApprovalAdmission(f ApprovalAdmissionFunc) Option {
	return func(d *Driver) { d.admission = f }
}

// admitApproval is the gate itself. It is idempotent and side-effect free: a
// boundary it has already downgraded is left alone, so repeated enrichBoundary
// calls at one park converge on the same state.
func (d *Driver) admitApproval(b *autonomy.HumanBoundary) {
	if d == nil || b == nil {
		return
	}
	switch b.Action {
	case autonomy.HumanBoundaryApproval:
		d.admitHeldCandidate(b)
	case autonomy.HumanBoundaryDecomposition:
		d.admitStagedPlan(b)
	}
}

// admitHeldCandidate gates an approval boundary whose proposal is an
// approval-held MutationCandidate.
func (d *Driver) admitHeldCandidate(b *autonomy.HumanBoundary) {
	// ── 1. CANDIDATE VALIDITY ──────────────────────────────────────────
	if b.PatchID == "" {
		d.refuseApproval(b, "the parked boundary carries no mutation candidate; there is nothing to authorize")
		return
	}
	if !d.adapter.CandidateHeld(b.PatchID) {
		d.refuseApproval(b, "mutation candidate "+b.PatchID+" is no longer held by the execution authority — "+
			"the computation that produced it failed, was superseded or was cancelled, so its artifact is not executable")
		return
	}
	// ── 2. THE CONCRETE CANDIDATE, so the review is answerable ─────────
	// A human cannot authorize a change they cannot see. The preview is the
	// runtime's own read of the held record, so the review and the apply are
	// guaranteed to describe the same bytes.
	d.attachCandidateReview(b)
	// ── 3. AUTHORIZATION ADMISSIBILITY ─────────────────────────────────
	if d.admission == nil {
		return
	}
	if err := d.admission(append([]string(nil), b.Targets...), b.PatchID); err != nil {
		d.refuseApproval(b, "mutation is not admissible: "+err.Error())
	}
}

// attachCandidateReview copies the held candidate's own facts onto the boundary
// and records the runtime's evidence checklist.
//
// Two things are load-bearing here:
//
//  1. The evidence checklist is built from what the runtime OBSERVED. The
//     "mutation applied" row is present and NOT satisfied, because nothing has
//     been applied — rendering it as a success is precisely the optimistic UI
//     this boundary exists to replace.
//  2. When nothing is previewable the boundary is refused. An approval surface
//     with no candidate to review is a ceremonial yes/no, not an authorization.
func (d *Driver) attachCandidateReview(b *autonomy.HumanBoundary) {
	// A boundary the runtime cannot preview must not be presented as an
	// authorization at all. The preview authority defaults to the adapter, so
	// this is unreachable in a correctly wired production composition; it stays
	// as a fail-closed guard rather than an optimistic default.
	if d.review == nil {
		d.refuseApproval(b, "the held mutation candidate could not be projected for review; "+
			"the runtime will not ask a human to authorize a change it cannot show")
		return
	}
	preview, ok := d.review(b.PatchID)
	if !ok {
		d.refuseApproval(b, "the held mutation candidate "+b.PatchID+" produced no reviewable change; "+
			"the runtime will not ask a human to authorize a change it cannot show")
		return
	}
	b.CandidateCandidateID = preview.CandidateID
	b.CandidateID = preview.CandidateID
	b.CandidateDigest = preview.Digest()
	b.CandidateOperation = preview.Operation
	b.CandidateOperationEvidence = preview.OperationEvidence
	b.CandidateTargets = append([]string(nil), preview.Targets...)
	b.CandidateDiff = preview.Diff
	b.CandidateAddedLines = preview.AddedLines
	b.CandidateRemovedLines = preview.RemovedLines
	b.CandidateContractID = preview.ContractID
	if len(b.Targets) == 0 {
		b.Targets = append([]string(nil), preview.Targets...)
	}
	b.CandidateEvidence = MutationReviewEvidence(preview)
}

// MutationReviewEvidence is the runtime's own checklist for a mutation review.
//
// Every row is a fact the runtime observed. The final row is present and
// deliberately UNSATISFIED: nothing has been written to the workspace yet, and a
// review that showed it as a green check would be reporting a mutation that has
// not occurred.
//
// It is exported and PURE so the presentation layer and the runtime agree on one
// checklist — a UI that rendered its own version could drift from the runtime's
// evidence, which is the very failure this boundary exists to prevent.
func MutationReviewEvidence(preview execution.CandidatePreview) []autonomy.BoundaryEvidence {
	rows := make([]autonomy.BoundaryEvidence, 0, 5)
	if len(preview.Targets) > 0 {
		rows = append(rows, autonomy.BoundaryEvidence{
			Label:     "target resolved",
			Satisfied: true,
			Detail:    strings.Join(preview.Targets, ", "),
		})
	}
	if preview.OperationEvidence != "" {
		rows = append(rows, autonomy.BoundaryEvidence{
			Label:     "operation classified",
			Satisfied: true,
			Detail:    preview.Operation + " — " + preview.OperationEvidence,
		})
	}
	rows = append(rows, autonomy.BoundaryEvidence{
		Label:     "artifact parsed",
		Satisfied: preview.ArtifactDigest != "",
		Detail:    "candidate fingerprint " + preview.ArtifactDigest,
	})
	rows = append(rows, autonomy.BoundaryEvidence{
		Label:     "change compiled",
		Satisfied: preview.Diff != "",
		Detail:    fmt.Sprintf("+%d / -%d lines", preview.AddedLines, preview.RemovedLines),
	})
	rows = append(rows, autonomy.BoundaryEvidence{
		Label:     "mutation applied",
		Satisfied: false,
		Detail:    "nothing has been written to the workspace",
	})
	return rows
}

// admitStagedPlan gates a DECOMPOSITION_PROPOSAL boundary. The proposal is a
// staged plan rather than an executor-held artifact, so the candidate-validity
// clause is "the staged plan is still the one the loop parked on" — the same
// object ResumeApproveProposal revalidates at the release seam. Everything else
// is the identical authorization-admissibility probe, because authorizing the
// plan and authorizing the mutation it will produce are governed by the same
// clauses.
func (d *Driver) admitStagedPlan(b *autonomy.HumanBoundary) {
	dag := d.Proposal()
	if dag == nil {
		d.refuseApproval(b, "the parked decomposition proposal is no longer staged; there is no plan to authorize")
		return
	}
	if d.admission == nil {
		return
	}
	if err := d.admission(append([]string(nil), dag.Targets()...), dagIdentity(dag)); err != nil {
		d.refuseApproval(b, "the staged plan is not admissible: "+err.Error())
	}
}

// refuseApproval downgrades an approval boundary to a truthful, non-resumable
// informational park. The candidate identity is dropped so the boundary can
// never be mistaken for an executable proposal by any later consumer, and the
// refusal is published as infrastructure telemetry so the trace records WHY the
// approval surface never appeared.
func (d *Driver) refuseApproval(b *autonomy.HumanBoundary, reason string) {
	b.Action = autonomy.HumanBoundaryInform
	b.Resumable = false
	b.PatchID = ""
	b.Proposal = nil
	b.Reason = reason
	if d.bus != nil {
		d.bus.Publish(events.NewActivity("[approval] admission refused: " + reason))
	}
	diagnosticf("[approval] AUTONOMY_APPROVAL_WITHHELD: %s", reason)
}

// dagIdentity is the staged plan's own lineage token: the workspace digest it
// was staged against plus its target set. It reuses the identity the DAG already
// carries — no new identifier is minted for the admission decision.
func dagIdentity(dag *planner.ExecutionDAG) string {
	if dag == nil {
		return ""
	}
	targets := strings.Join(dag.Targets(), ",")
	return "plan:" + dag.Target + "@" + short(dag.BaseTreeDigest) + "[" + targets + "]"
}

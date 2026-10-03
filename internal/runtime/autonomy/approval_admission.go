package autonomy

import (
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
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
	// ── 2. AUTHORIZATION ADMISSIBILITY ─────────────────────────────────
	if d.admission == nil {
		return
	}
	if err := d.admission(append([]string(nil), b.Targets...), b.PatchID); err != nil {
		d.refuseApproval(b, "mutation is not admissible: "+err.Error())
	}
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

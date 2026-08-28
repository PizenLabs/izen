package autonomy

import (
	"context"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
	"github.com/PizenLabs/izen/internal/execution/verifier"
)

// ── POST-DAG GLOBAL STRUCTURAL VERIFIER ─────────────────────────────────────
//
// Sub-tasks execute under strict region isolation: every per-unit boundary
// (3/4/5) gates an artifact against ITS OWN window only. Regressions that
// exist only in the aggregate — st-1 removing a CSS definition st-4's region
// still uses — sail through every unit gate. After the last sub-task lands,
// verifyGlobalObjective audits the WHOLE mutated document against the pre-DAG
// baseline and the machine-checkable intent:
//
//   - syntax remains valid;
//   - no orphaned class/ID references were introduced;
//   - requested removals actually reduced dead nodes.
//
// A failed audit overrides the DAG status to OBJECTIVE_UNRESOLVED and parks
// the loop at awaiting_human. Like NO-OP escalation (and unlike failure),
// applied units are PRESERVED: each landed through its own Boundary-5 digest
// chain, so discarding human-approved work would be a second false outcome.
// The decision belongs to the human.

// GlobalVerifyFunc runs the global audit over one target's pre-/post-DAG
// states. Injectable for tests; the default is verifier.AuditObjective.
type GlobalVerifyFunc func(intent verifier.IntentSpec, base, mutated []byte) verifier.Verdict

// defaultGlobalVerify delegates to the canonical deterministic audit.
func defaultGlobalVerify(intent verifier.IntentSpec, base, mutated []byte) verifier.Verdict {
	return verifier.AuditObjective(intent.Target, base, mutated, intent)
}

// WithGlobalVerify overrides (or with nil, disables) the post-DAG global
// structural verification. Disabling restores the pre-verifier behavior:
// a DAG whose units all applied claims completion unconditionally.
func WithGlobalVerify(f GlobalVerifyFunc) Option {
	return func(d *Driver) { d.globalVerify = f }
}

// verifyGlobalObjective runs after the last sub-task of a proposal DAG lands.
// It returns true when the audit REJECTED the final document state: the DAG is
// then OBJECTIVE_UNRESOLVED and the loop parked at awaiting_human (the caller
// must stop immediately — a parked loop has NO termination, exactly like the
// NO-OP escalation path). It returns false when the objective verified or the
// audit could not run.
func (d *Driver) verifyGlobalObjective(ctx context.Context, dag *planner.ExecutionDAG,
	originals map[string][]byte) bool {
	target := dag.Target
	base, ok := originals[target]
	if !ok {
		diagnosticf("[objective-verify] skipped: no pre-DAG snapshot for %s", target)
		return false
	}
	mutated, ok := d.adapter.ReadTargetFile(target)
	if !ok {
		diagnosticf("[objective-verify] skipped: post-DAG state of %s unreadable", target)
		return false
	}

	// ── EXECUTION INERTIA CIRCUIT BREAKER (false-positive resolution) ──────
	// An ACTIVE MODIFICATION intent whose DAG applied ZERO mutations (every
	// sub-task evaluated to no_op_objective_satisfied) must NEVER resolve the
	// objective — even when the pre-DAG baseline was already syntactically
	// invalid and the baseline-relaxation clause below would have let it
	// through as OBJECTIVE_RESOLVED with a zero-byte diff. The objective
	// demanded a change and none was delivered: that is execution inertia, not
	// success. Fail fast with EXECUTION_INERTIA_NO_OP so the runtime can retry
	// or the human can escalate scope — the loop is never marked complete.
	if d.requiresMutation() && len(dag.SubTasks) > 0 &&
		dag.NoOpSatisfiedSubTasks == len(dag.SubTasks) {
		reason := "active modification requested but all sub-tasks evaluated to no-op (zero mutations applied)"
		dag.Status = planner.ExecutionInertiaNoOp
		dag.FailureReason = reason
		diagnosticf("[objective-verify] EXECUTION_INERTIA_NO_OP target=%s — intent requires mutation but %d/%d sub-task(s) applied zero bytes; refusing OBJECTIVE_RESOLVED: %s",
			target, dag.NoOpSatisfiedSubTasks, len(dag.SubTasks), reason)
		b := &autonomy.HumanBoundary{
			Reason: "EXECUTION_INERTIA_NO_OP: " + reason +
				" — no diff was applied; retry with a scoped objective or escalate the change window",
			Targets: dag.Targets(),
		}
		autonomy.DeriveBoundaryAction(b)
		d.loop.AwaitHuman(*b)
		d.enrichBoundary()
		d.publish(ctx)
		return true
	}

	intent := verifier.IntentSpec{
		Objective: boundedEvidenceLine(d.prompt),
		Target:    target,
		Removals:  verifier.ExtractRemovalIntents(d.prompt),
	}
	v := d.globalVerify(intent, base, mutated)
	if v.Pass() {
		diagnosticf("[objective-verify] OBJECTIVE_VERIFIED target=%s nodes=%d→%d refs=%d→%d removals=%d",
			target, v.Base.Nodes, v.Mutated.Nodes, v.Base.References, v.Mutated.References, len(intent.Removals))
		return false
	}
	// ── BASELINE SYNTAX RELAXATION (pre-existing defect, no-op DAG) ─────
	// A failed syntax audit on a document whose PRE-DAG baseline was ALREADY
	// syntactically invalid, whose bytes (or syntax diagnostics) are unchanged
	// by the DAG, and whose sub-tasks ALL evaluated to no_op_objective_satisfied
	// is a PRE-EXISTING baseline condition — never a mutation regression. The
	// run completes with a warning instead of parking at awaiting_human with a
	// false OBJECTIVE_UNRESOLVED.
	if !dag.BaselineSyntaxValid &&
		dag.NoOpSatisfiedSubTasks == len(dag.SubTasks) &&
		onlyBaselineSyntaxFailure(v) &&
		execution.BaselineSyntaxRegression(target, base, mutated, dag.BaselineSyntaxValid) { //nolint:contextcheck // document syntax validation is pure content checking, no context needed
		diagnosticf("[objective-verify] %s target=%s — baseline was already syntactically invalid and the DAG mutated nothing (all %d/%d sub-task(s) evaluated to no_op_objective_satisfied); allowing OBJECTIVE_RESOLVED",
			execution.BaselineSyntaxPreexisting, target, dag.NoOpSatisfiedSubTasks, len(dag.SubTasks))
		return false
	}
	reason := fmt.Sprintf(
		"global structural audit rejected the mutated %s after all %d sub-tasks applied: %s",
		target, len(dag.SubTasks), v.Evidence())
	dag.Status = planner.ObjectiveUnresolved
	dag.FailureReason = reason
	diagnosticf("[objective-verify] OBJECTIVE_UNRESOLVED target=%s — decision returned to awaiting_human: %s",
		target, reason)
	b := &autonomy.HumanBoundary{
		Reason: "OBJECTIVE_UNRESOLVED: " + reason +
			fmt.Sprintf(" — %d/%d approved units are applied and preserved; the decision is held for your review",
				len(dag.SubTasks), len(dag.SubTasks)),
		Targets: dag.Targets(),
	}
	autonomy.DeriveBoundaryAction(b)
	d.loop.AwaitHuman(*b)
	d.enrichBoundary()
	d.publish(ctx)
	return true
}

// onlyBaselineSyntaxFailure reports whether a failed global audit verdict is
// attributable to document SYNTAX alone (the pre-existing-baseline relaxation
// target). The verifier short-circuits on the S1 syntax gate, so a broken
// mutated document carries exactly FailSyntaxInvalid; a verdict carrying any
// other failure (orphaned reference, unfulfilled removal, topology) is a real
// mutation regression and must never be relaxed.
func onlyBaselineSyntaxFailure(v verifier.Verdict) bool {
	if v.Pass() || len(v.Failures) == 0 {
		return false
	}
	for _, f := range v.Failures {
		if f.Code != verifier.FailSyntaxInvalid {
			return false
		}
	}
	return true
}

// requiresMutation reports whether the driver's objective is an ACTIVE
// MODIFICATION request (modification or refactoring intent). The post-DAG
// verifier consults it to distinguish a DAG that was authorized to mutate the
// workspace from a read-only inspection: an active-modification DAG that
// applied zero bytes is execution inertia, never a resolved objective.
func (d *Driver) requiresMutation() bool {
	if d == nil || strings.TrimSpace(d.prompt) == "" {
		return false
	}
	return autonomy.Classify(d.prompt, nil).RequiresMutation()
}

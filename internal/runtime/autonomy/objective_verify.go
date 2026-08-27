package autonomy

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/internal/autonomy"
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

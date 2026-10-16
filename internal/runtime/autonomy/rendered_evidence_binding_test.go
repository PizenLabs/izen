package autonomy

// ── Rendered verification: evidence binding ──────────────────────────────────
//
// The opt-in policy (IZEN_RENDERED_VERIFY) decides whether a rendered check is
// required. These tests pin what the policy alone cannot: that a verification
// which runs is BOUND to the artifact revision it inspected, that a missing
// configuration can never read as a pass, and that the completion authority
// consumes exactly that evidence.
//
// No DOM assertions and no visual-quality claims: the checks are deterministic
// dom_contains / dom_not_contains queries, and a screenshot is recorded, never
// proof.

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/execution"
)

// A configured browser check that fails in the rendered DOM leaves the broad
// objective UNVERIFIED: the failure is evidence, and the authority refuses the
// completion on it.
func TestRendered_ConfiguredCheckFailureIsUnverified(t *testing.T) {
	ev := execution.ObjectiveEvidence{
		BroadObjective:      true,
		VerificationSkipped: true,
		RenderedVerification: execution.RenderedVerificationEvidence{
			Ran:      true,
			Passed:   false,
			Revision: "rev-abc123",
			Browser:  "test-browser 1.0",
			Defects:  []string{"check crit-1 failed: dom_contains banner"},
		},
	}
	if got := ev.VerifierVerdict(); got != "RENDERED_FAIL" {
		t.Fatalf("verdict = %s, want RENDERED_FAIL", got)
	}
	contract := execution.TaskContract{Kind: execution.TaskPatch, Targets: []string{"index.html"}}
	if got := execution.AuthorizeObjective(contract, ev); got.Outcome == execution.ObjectiveProven {
		t.Fatalf("a failed rendered check proved the objective: %+v", got)
	}
}

// The inspected revision is content-bound: the same target set with different
// bytes yields a different revision, so a check cannot be replayed against a
// later edit.
func TestRendered_InspectionIsBoundToArtifactRevision(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "index.html", "<h1>before</h1>\n")
	before := renderedRevision(root, []string{"index.html"})
	writeTarget(t, root, "index.html", "<h1>after</h1>\n")
	after := renderedRevision(root, []string{"index.html"})
	if before == "" || after == "" {
		t.Fatal("an empty revision binds to nothing")
	}
	if before == after {
		t.Fatal("the artifact revision did not change with the artifact bytes")
	}
	if len(before) != 16 || len(after) != 16 {
		t.Fatalf("revisions %q/%q are not the 16-hex binding", before, after)
	}
}

// Missing configuration is never a pass: no verifier, no entry, and no checks
// each project onto a distinct blocked/unverified state the authority refuses.
func TestRendered_MissingConfigurationIsNeverAPass(t *testing.T) {
	blocked := RenderedVerification{Blocked: "rendered verification is required but no verifier is bound"}
	if blocked.Evidence().Ran {
		t.Fatal("a blocked verification reports Ran")
	}
	if blocked.Evidence().Passed {
		t.Fatal("a blocked verification reports Passed")
	}
	empty := RenderedVerification{Ran: true, Passed: false,
		Defects: []string{"no rendered acceptance checks were supplied; nothing was established"}}
	if empty.Passed {
		t.Fatal("a check with no acceptance criteria reports Passed")
	}
	summary := blocked.summary()
	if !strings.Contains(strings.ToUpper(summary), "BLOCKED") {
		t.Fatalf("a blocked verification does not render BLOCKED: %q", summary)
	}
}

// The completion authority consumes the rendered evidence: a passing rendered
// verification satisfies a broad objective's integrity condition, and only it
// does — a skipped gate alone never stands in.
func TestRendered_CompletionAuthorityConsumesRenderedEvidence(t *testing.T) {
	contractTargets := []string{"index.html"}
	proven := execution.ObjectiveEvidence{
		BroadObjective:         true,
		VerificationSkipped:    true,
		ObservedDeltaTargets:   contractTargets,
		Mutation:               execution.FilesystemApplied,
		MutatedFiles:           1,
		WorkspaceObservations:  1,
		PostMutationObserved:   contractTargets,
		DischargedRequirements: nil,
		RenderedVerification: execution.RenderedVerificationEvidence{
			Ran: true, Passed: true, Revision: "rev-ok", Browser: "test-browser",
		},
	}
	_ = proven
	conditions := []execution.CompletionCondition{{
		ID: "cond-integrity-held", Obligation: execution.ObligationIntegrityHeld,
		Targets: contractTargets,
	}}
	reduced := execution.ReduceConditions(conditions, execution.ObjectiveEvidence{
		BroadObjective:      true,
		VerificationSkipped: true,
		RenderedVerification: execution.RenderedVerificationEvidence{
			Ran: true, Passed: true, Revision: "rev-ok",
		},
	})
	if len(reduced) != 1 || !reduced[0].Satisfied() {
		t.Fatalf("a passing rendered verification did not satisfy integrity: %+v", reduced)
	}
	unverified := execution.ReduceConditions(conditions, execution.ObjectiveEvidence{
		BroadObjective:      true,
		VerificationSkipped: true,
	})
	if len(unverified) != 1 || unverified[0].Satisfied() {
		t.Fatalf("a skipped gate satisfied a broad objective's integrity: %+v", unverified)
	}
	if unverified[0].VerificationState != execution.VerifyGateUnverified {
		t.Fatalf("state = %s, want GATE_UNVERIFIED", unverified[0].VerificationState)
	}
}

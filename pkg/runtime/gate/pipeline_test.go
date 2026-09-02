package gate

import (
	"testing"

	"github.com/PizenLabs/izen/pkg/runtime/harness"
)

func tier1Evidence() harness.ArtifactEvidence {
	return harness.ArtifactEvidence{
		Tier:       harness.Tier1Strict,
		Confidence: 1.0,
		ExactParse: true,
	}
}

// TestAuthorizationAmbiguousRejects verifies ambiguous evidence is rejected
// immediately regardless of other factors (fail-closed).
func TestAuthorizationAmbiguousRejects(t *testing.T) {
	b := NewAuthorizationBoundary()
	ev := tier1Evidence()
	ev.Ambiguous = true

	d := b.Decision(ev, RiskLow, ScopeDriftResult{ScopeDriftScore: 0.0, WithinLimits: true})
	if !d.Rejected {
		t.Fatal("expected rejection for ambiguous evidence")
	}
	if d.Approved || d.EscalationRequired {
		t.Error("ambiguous evidence must be a hard rejection, not approval or escalation")
	}
}

// TestAuthorizationInferredRequiresConfirmation verifies Tier 3 inferred
// evidence can never auto-approve.
func TestAuthorizationInferredRequiresConfirmation(t *testing.T) {
	b := NewAuthorizationBoundary()
	ev := tier1Evidence()
	ev.Inferred = true
	ev.Confidence = 0.6

	d := b.Decision(ev, RiskLow, ScopeDriftResult{ScopeDriftScore: 0.0, WithinLimits: true})
	if !d.EscalationRequired {
		t.Fatal("expected escalation for inferred evidence")
	}
	if d.Approved || d.Rejected {
		t.Error("inferred evidence should escalate, not approve or reject")
	}
}

// TestAuthorizationAutoApprove verifies the only auto-approval path.
func TestAuthorizationAutoApprove(t *testing.T) {
	b := NewAuthorizationBoundary()
	ev := tier1Evidence() // Confidence 1.0 >= 0.95

	d := b.Decision(ev, RiskLow, ScopeDriftResult{ScopeDriftScore: 0.05, WithinLimits: true})
	if !d.Approved {
		t.Fatalf("expected automatic approval, got %+v", d)
	}
	if d.EscalationRequired || d.Rejected {
		t.Error("auto-approval must not escalate or reject")
	}
}

// TestAuthorizationHighRiskEscalates verifies high-risk mutations require
// confirmation even at high confidence.
func TestAuthorizationHighRiskEscalates(t *testing.T) {
	b := NewAuthorizationBoundary()
	ev := tier1Evidence()

	d := b.Decision(ev, RiskHigh, ScopeDriftResult{ScopeDriftScore: 0.0, WithinLimits: true})
	if !d.EscalationRequired {
		t.Fatal("expected escalation for high risk")
	}
	if d.Approved {
		t.Error("high risk must not auto-approve")
	}
}

// TestStructuralGateGoRejectsCorruption verifies the structural gate rejects a
// patch that corrupts Go syntax.
func TestStructuralGateGoRejectsCorruption(t *testing.T) {
	g := NewStructuralGate()
	original := []byte("package main\n\nfunc main() {}\n")
	// Candidate whose diff, when applied, produces invalid Go.
	cand := harness.CandidateArtifact{
		TargetFile: "main.go",
		Diff:       diffAddLine("main.go", "func main() {}", "func broken( {"),
	}
	if err := g.Validate(cand, original); err == nil {
		t.Fatal("expected structural rejection for corrupted Go")
	}
}

// TestStructuralGateGoAcceptsValid verifies the structural gate passes a
// syntax-preserving patch.
func TestStructuralGateGoAcceptsValid(t *testing.T) {
	g := NewStructuralGate()
	original := []byte("package main\n\nfunc main() {}\n")
	cand := harness.CandidateArtifact{
		TargetFile: "main.go",
		Diff:       diffAddLine("main.go", "func main() {}", "func helper() int { return 1 }"),
	}
	if err := g.Validate(cand, original); err != nil {
		t.Fatalf("expected structural pass, got %v", err)
	}
}

// TestScopeGateDrift verifies scope drift is computed from node counts.
func TestScopeGateDrift(t *testing.T) {
	g := NewScopeGate()
	original := []byte("package main\n\nfunc main() {\n\tprintln(1)\n}\n")
	cand := harness.CandidateArtifact{
		TargetFile: "main.go",
		Diff:       diffAddLine("main.go", "func main() {", "\tprintln(2)"),
	}
	res := g.Evaluate(cand, original)
	if res.ScopeDriftScore < 0 {
		t.Errorf("drift = %v, want >= 0", res.ScopeDriftScore)
	}
	if res.NodeDeletions != 0 {
		t.Errorf("node deletions = %d, want 0", res.NodeDeletions)
	}
}

// diffAddLine builds a unified diff that inserts a line after the given
// context (old) line.
func diffAddLine(name, contextLine, line string) string {
	return "--- a/" + name + "\n" +
		"+++ b/" + name + "\n" +
		"@@ -1,3 +1,4 @@\n" +
		" " + contextLine + "\n" +
		"+" + line + "\n"
}

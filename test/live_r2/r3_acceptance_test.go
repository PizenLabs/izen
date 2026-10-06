package live_r2

// ── R3 LIVE ACCEPTANCE — the proposal boundary, on the real composition ──────
//
// R3 asks whether IZEN can turn discovery evidence into a target proposal
// WITHOUT conflating DISCOVERED with AUTHORIZED. This arm runs the R3-A shape
// (one defective index.html, an objective that names no file) over the REAL
// production composition (compose.Wire → the bounded autonomy Driver → the
// RuntimeExecutor → a real local Ollama model) and asserts the accepted R3
// outcome:
//
//	objective → discovery → candidate → PROPOSAL → authority decision
//	          → DISAMBIGUATE (awaiting_human)   — and NOT inspection/mutation.
//
// It reads every fact from AUTHORITATIVE runtime events and the on-disk
// workspace, never from model prose. It is opt-in via IZEN_LIVE_FORENSICS=1,
// exactly like the other live arms.
//
// The R2 acceptance test remains the record of the FULL chain for a no-kind
// objective. It is deliberately still red: R3 does not widen authority, so a
// no-kind objective that names no file still cannot reach mutation without a
// human. This test asserts the correct, explicit boundary that replaces the
// "accidental zero-scope fallback".

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
)

func containsPath(list []string, want string) bool {
	for _, v := range list {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}

// TestLiveR3_ProposalIsExplicitNotAuthorized is the R3 acceptance arm.
func TestLiveR3_ProposalIsExplicitNotAuthorized(t *testing.T) {
	requireLiveModel(t)
	r := runObjective(t, r2Objective)
	tr := r.Trace
	t.Logf("\n%s", tr.Render())
	t.Logf("WORKSPACE index.html = %q", r.IndexHTML)
	t.Logf("FINAL state=%s boundary=%s", r.State, fmtBoundary(r.Boundary))

	// DISCOVERED: the bounded scan observed the candidate and it is recorded as
	// evidence in the frozen spec.
	if !containsPath(tr.Spec.DerivationCandidates, "index.html") {
		t.Fatalf("R3 DISCOVERED: no candidate recorded (derivation_candidates=%v spec=%+v)",
			tr.Spec.DerivationCandidates, tr.Spec)
	}

	// PROPOSED: exactly one observed candidate produced a NON-AUTHORITATIVE
	// proposal. PROPOSED is a distinct, recorded state — not an empty scope.
	if tr.Spec.ScopeState != "PROPOSED" {
		t.Fatalf("R3 PROPOSED: scope state = %q, want PROPOSED", tr.Spec.ScopeState)
	}

	// AUTHORITY DECISION: the gate refused to authorize and asked explicitly.
	if tr.Authorization.Verdict != "disambiguate" {
		t.Fatalf("R3 AUTHORITY: verdict = %q, want disambiguate", tr.Authorization.Verdict)
	}
	if tr.Authorization.Scope != "PROPOSED" {
		t.Fatalf("R3 AUTHORITY: scope = %q, want PROPOSED", tr.Authorization.Scope)
	}
	if !containsPath(tr.Authorization.Candidates, "index.html") {
		t.Fatalf("R3 AUTHORITY: candidates = %v, want the discovered index.html", tr.Authorization.Candidates)
	}

	// NOT AUTHORIZED: the proposal bound no target, put no target bytes in front
	// of the model, mutated no byte, and left the run parked for a human.
	//
	// The production composition runs ONE read-only requirement pass before the
	// gate (a mutation-free classification call, recorded in the R2 trace as
	// 512→2 tokens). It is not a target dispatch: the target is never bound as a
	// context channel and nothing is mutated. The decisive facts are the
	// channel set, the mutation set, and the bytes on disk.
	if len(tr.Spec.Targets) != 0 {
		t.Fatalf("R3 NOT-AUTHORIZED: a proposal bound targets %v", tr.Spec.Targets)
	}
	if len(tr.Spec.ContextChannels) != 0 {
		t.Fatalf("R3 NOT-AUTHORIZED: the target was bound as a context channel %v; inspection must not happen without authorization",
			tr.Spec.ContextChannels)
	}
	if len(tr.Mutations) != 0 {
		t.Fatalf("R3 NOT-AUTHORIZED: %d mutation(s) occurred for an unauthorized target", len(tr.Mutations))
	}
	if strings.Contains(r.IndexHTML, "Hello") {
		t.Fatalf("R3 NOT-AUTHORIZED: the workspace was mutated without authorization: %q", r.IndexHTML)
	}
	if r.State != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("R3 NOT-AUTHORIZED: final state = %s, want awaiting_human", r.State)
	}
	if r.Boundary == nil || r.Boundary.Action != autonomy.HumanBoundaryClarify {
		t.Fatalf("R3 NOT-AUTHORIZED: boundary = %s, want a clarification", fmtBoundary(r.Boundary))
	}
	if !containsPath(r.Boundary.Options, "index.html") {
		t.Fatalf("R3 NOT-AUTHORIZED: clarification options = %v, want the discovered candidate", r.Boundary.Options)
	}
}

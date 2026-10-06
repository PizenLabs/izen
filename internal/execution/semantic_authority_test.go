package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── §4 · Semantic intent at the authorization boundary ──────────────────────
//
// The canonical semantic verdict (internal/execution/strategy/semantics.go)
// decides what the user ASKED FOR. These tests pin what the Control Plane does
// with that answer at the one place authority is minted: IntentGateway.Gate.
//
// The distinction they protect is the whole point of the boundary:
//
//	semantic mutation intent  !=  mutation authorization
//	ambiguous intent          !=  mutation intent
//
// A MUTATION verdict produces a mutation PROFILE and then passes through scope
// provenance, admission, the grant and human approval exactly as before — the
// verdict confers nothing. An UNDETERMINED verdict stops before a provider is
// reachable at all.

func semanticGate(t *testing.T, root, line string) IntentResolution {
	t.Helper()
	gw := NewIntentGateway(root)
	_, res, err := gw.Gate(context.Background(), line)
	if err != nil {
		t.Fatalf("Gate(%q): %v", line, err)
	}
	if res.Profile.Strategy == "" {
		t.Fatalf("Gate(%q) produced no strategy", line)
	}
	return res
}

// carriesAppliedMutation reports whether the dispatched turn may write the
// workspace. It asks the CANONICAL projection (MutationSemanticsOf) rather than
// naming strategies, because a planning turn is a PROPOSAL: it requires a
// mutation contract for the lifecycle it may grow into, while the turn actually
// dispatched writes nothing. Collapsing the two would fail a request for having
// planned, which is the exact confusion the projection exists to prevent.
func carriesAppliedMutation(s strategy.ExecutionStrategy) bool {
	return strategy.MutationSemanticsOf(s).IsApplied()
}

func seedSemanticWorkspace(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html><body>hi</body></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, "index.html"
}

func TestGateway_AmbiguousRequestParksAtClarification(t *testing.T) {
	root, _ := seedSemanticWorkspace(t)
	res := semanticGate(t, root, "$prompt make this project better")

	if carriesAppliedMutation(res.Profile.Strategy) {
		t.Fatalf("ambiguous request resolved to mutation strategy %s", res.Profile.Strategy)
	}
	if res.Profile.Strategy != strategy.HumanClarification {
		t.Fatalf("strategy = %s, want %s (reason: %s)",
			res.Profile.Strategy, strategy.HumanClarification, res.Profile.StrategyReason)
	}
	if res.Profile.ModelRequired {
		t.Error("a clarification must not dispatch a model")
	}
	if res.Profile.ContextPolicy != strategy.ContextPolicyNone {
		t.Errorf("a clarification loaded %s context; it must load none", res.Profile.ContextPolicy)
	}
}

// TestGateway_MutationIntentReachesTheMutationPath is the counterpart: an
// explicit mutation must still reach the gated mutation path, or the fail-closed
// default above is indistinguishable from a classifier that stopped working.
func TestGateway_MutationIntentReachesTheMutationPath(t *testing.T) {
	root, target := seedSemanticWorkspace(t)
	res := semanticGate(t, root, "$prompt remove redundant content from @"+target)

	if !carriesAppliedMutation(res.Profile.Strategy) {
		t.Fatalf("explicit mutation resolved to %s, want a mutation strategy", res.Profile.Strategy)
	}
	// And the semantic layer agrees, independently of the routing above.
	if v := strategy.ClassifySemantic("remove redundant content from @" + target); !v.RequiresMutation() {
		t.Fatalf("semantic verdict = %s, want mutation intent", v.Intent)
	}
}

// TestGateway_ReviewOnlyRequestCarriesNoMutationAuthority proves a review does
// not become a mutation at the boundary.
func TestGateway_ReviewOnlyRequestCarriesNoMutationAuthority(t *testing.T) {
	root, _ := seedSemanticWorkspace(t)
	for _, line := range []string{
		"$prompt review this project and suggest improvements",
		"$prompt make this project better",
	} {
		res := semanticGate(t, root, line)
		if carriesAppliedMutation(res.Profile.Strategy) {
			t.Errorf("Gate(%q) resolved to mutation strategy %s", line, res.Profile.Strategy)
		}
	}
}

// TestGateway_ExplicitReadOnlyConstraintClosesTheMutationPath covers the
// remaining §4 case: a request that names a change AND states that nothing may
// change is read-only. The human's own negative constraint outranks the
// classifier's reading of a verb.
func TestGateway_ExplicitReadOnlyConstraintClosesTheMutationPath(t *testing.T) {
	root, target := seedSemanticWorkspace(t)
	res := semanticGate(t, root, "$prompt refactor @"+target+", read-only")

	if carriesAppliedMutation(res.Profile.Strategy) {
		t.Fatalf("an explicit read-only constraint still resolved to %s", res.Profile.Strategy)
	}
	if res.Profile.StrategyReason == "" {
		t.Error("the downgrade must record why it downgraded")
	}
}

// TestGateway_RuntimeComposedPromptIsNeverReadAsAConstraint is the regression
// for a live defect.
//
// strategy.Select runs twice per mutation: once at this gateway on the human's
// request, and again inside the RuntimeExecutor on the fully COMPILED provider
// prompt — which carries the runtime's own scoping instructions, including
// "do not modify any other region".
//
// When the read-only constraint was scanned inside Select, the runtime read its
// own prompt back as the user revoking mutation authority. Every decomposed
// sub-task was silently downgraded to read-only, applied no bytes, and the
// objective failed as UNSUBSTANTIATED with "no durable delta observed".
//
// The invariant: strategy.Select — the layer that sees runtime-composed text —
// must not consult a human constraint at all. Only the gateway may.
func TestGateway_RuntimeComposedPromptIsNeverReadAsAConstraint(t *testing.T) {
	root, _ := seedSemanticWorkspace(t)
	compiledPrompt := "refactor every handler in index.html change window lines 1 94 " +
		"produce exactly one anchored search replace block " +
		"do not modify any other region document outline context"

	// The differential assertion: appending the runtime's own instruction text
	// must change NOTHING about how the request routes. If it does, that
	// instruction is being read as a human statement.
	gw := NewIntentGateway(root)
	bare := gw.SelectStrategy("refactor every handler in index.html")
	compiled := gw.SelectStrategy(compiledPrompt)

	if compiled.Strategy != bare.Strategy {
		t.Fatalf("compiled instruction text changed the route: bare=%s compiled=%s. "+
			"strategy.Select is reading runtime-composed text as a human constraint",
			bare.Strategy, compiled.Strategy)
	}
	if !carriesAppliedMutation(bare.Strategy) {
		t.Fatalf("the bare mutation request routes to %s, so this differential proves nothing",
			bare.Strategy)
	}
}

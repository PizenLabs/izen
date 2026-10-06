package execution

import (
	"context"
	"testing"

	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── §9 · Authority invariants ───────────────────────────────────────────────
//
// Each test below states one invariant as something that would be IMPOSSIBLE if
// the invariant were broken. They are deliberately not "returns the expected
// value" tests: the expected value could change and the test would keep passing
// while the authority model rotted underneath it.
//
// Invariants already pinned by an existing suite are listed at the bottom of
// this file rather than re-implemented here.

// TestInvariant_SemanticMutationIntentAloneCannotAuthorize is invariant 1 and 8
// together:
//
//	Intent cannot grant authority.
//	Mutation cannot bypass the authorization boundary.
//
// A request that plainly asks for a mutation ("remove redundant content from
// @index.html") reads as MUTATION intent at the semantic boundary. It still
// cannot write: without a directive minting a mutation scope, the gateway
// compiles it to an observational graph. Semantic intent is a statement about
// TEXT; authority is minted by a human at the scope boundary.
func TestInvariant_SemanticMutationIntentAloneCannotAuthorize(t *testing.T) {
	root, target := seedSemanticWorkspace(t)

	v := strategy.ClassifySemantic("remove redundant content from @" + target)
	if !v.RequiresMutation() {
		t.Fatalf("precondition: the objective must read as mutation intent, got %s", v.Intent)
	}

	gw := NewIntentGateway(root)
	_, res, err := gw.Gate(context.Background(), "remove redundant content from @"+target)
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if carriesAppliedMutation(res.Profile.Strategy) {
		t.Fatalf("mutation intent reached an applied mutation (%s) with no scope authorized",
			res.Profile.Strategy)
	}
}

// TestInvariant_ReviewNeverBecomesMutation is the §4 invariant at the authority
// chain, not just at the classifier: a review request that names no change must
// not reach an applied mutation under ANY authorization.
func TestInvariant_ReviewNeverBecomesMutation(t *testing.T) {
	root, target := seedSemanticWorkspace(t)
	gw := NewIntentGateway(root)
	for _, line := range []string{
		"review this project and suggest improvements",
		"$prompt review this project and suggest improvements",
		"$prompt review @" + target + " and suggest improvements",
		"$prompt make this project better",
	} {
		_, res, err := gw.Gate(context.Background(), line)
		if err != nil {
			t.Fatalf("gate %q: %v", line, err)
		}
		if carriesAppliedMutation(res.Profile.Strategy) {
			t.Errorf("Gate(%q) reached applied mutation %s; a review or an ambiguous "+
				"improvement request must never write", line, res.Profile.Strategy)
		}
	}
}

// TestInvariant_UnresolvedTargetCannotBecomeCreate is invariants 2, 3 and 4 at
// the contract layer:
//
//	Objective cannot invent a target.
//	UNRESOLVED cannot become CREATE.
//
// The contract derives a kind from facts the runtime already owns. A mutation
// objective whose target is DEFERRED — the objective named no file and discovery
// has not run — has no evidence that the artifact is new. Reading the absence
// of a resolved target as evidence of creation is the category error that
// produces an empty-scope CREATE objective.
func TestInvariant_UnresolvedTargetCannotBecomeCreate(t *testing.T) {
	cases := []struct {
		name string
		in   TaskClassification
	}{
		{
			name: "no target at all",
			in: TaskClassification{
				Intent:           "modification",
				Objective:        "remove redundant content",
				RequiresMutation: true,
			},
		},
		{
			name: "target deferred, no observation made",
			in: TaskClassification{
				Intent:           "modification",
				Objective:        "remove redundant content from index.html",
				RequiresMutation: true,
				// TargetsExistedBefore nil means the runtime observed NOTHING.
			},
		},
		{
			name: "creation verb with no declared target",
			in: TaskClassification{
				Intent:           "modification",
				Objective:        "create a new file",
				RequiresMutation: true,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contract := DeriveTaskContract(tc.in)
			if contract.Kind == TaskCreate {
				t.Fatalf("an objective with no resolved target derived CREATE; " +
					"the absence of a target is not evidence that the artifact is new")
			}
			if len(contract.Targets) != 0 {
				t.Fatalf("contract invented targets %v; no target was declared", contract.Targets)
			}
		})
	}
}

// TestInvariant_AmbiguousCandidatesCannotBecomeScope is invariant 5. The
// derivation reports candidates as EVIDENCE and marks the verdict AMBIGUOUS;
// only a UNIQUE derivation may be bound as a scope. A non-empty candidate list
// is a question, not a resolution.
func TestInvariant_AmbiguousCandidatesCannotBecomeScope(t *testing.T) {
	ambiguous := Derivation{
		Status:  DerivationAmbiguous,
		Targets: []string{"a.html", "b.html", "index.html"},
		Kinds:   []string{"html"},
		Resolutions: []KindResolution{{
			Kind:      "html",
			Matches:   []string{"a.html", "b.html", "index.html"},
			Ambiguous: true,
		}},
	}
	if ambiguous.IsUnique() {
		t.Fatal("an ambiguous derivation reports itself unique")
	}
	if !ambiguous.IsAmbiguous() {
		t.Fatal("the AMBIGUOUS status did not survive")
	}
	if len(ambiguous.AmbiguousKinds()) != 1 {
		t.Fatalf("AmbiguousKinds = %v, want the declared html kind", ambiguous.AmbiguousKinds())
	}

	// The zero value must not read as resolved either: an unconstructed
	// derivation is UNRESOLVED, never UNIQUE.
	var zero Derivation
	if zero.IsUnique() {
		t.Fatal("an unconstructed derivation reports itself unique")
	}
	if !zero.IsUnresolved() {
		t.Fatal("an unconstructed derivation must be UNRESOLVED")
	}

	// And the vocabulary is closed, so no fourth meaning can appear locally.
	seen := map[DerivationStatus]bool{}
	for _, s := range AllDerivationStatuses() {
		if seen[s] {
			t.Errorf("duplicate derivation status %s", s)
		}
		seen[s] = true
	}
	if len(seen) != 3 {
		t.Errorf("derivation vocabulary = %d values, want 3", len(seen))
	}
}

// TestInvariant_StatedTargetsOutrankDerivation is the precedence that keeps
// derivation from ever overriding a target a human named. It is the same
// invariant as above seen from the other side: derivation proposes, and the
// gateway disposes.
func TestInvariant_StatedTargetsOutrankDerivation(t *testing.T) {
	observed := []string{"a.html", "b.html"}
	got := DeriveScope(DerivationRequest{
		Prompt: "rewrite the html",
		Profile: WorkspaceProfile{
			Candidates: []CandidateEvidence{{Path: observed[0]}, {Path: observed[1]}},
		},
		StatedTargets: []string{"a.html"},
	})
	if got.Status != DerivationUnresolved {
		t.Fatalf("status = %s, want %s; derivation never overrides a stated target",
			got.Status, DerivationUnresolved)
	}
	if len(got.Targets) != 0 {
		t.Fatalf("derivation returned %v for an already-proven target set", got.Targets)
	}
}

// TestInvariant_ObjectiveSemanticsKeepsDimensionsIndependent pins the model
// that makes "UNRESOLVED TARGET != CREATE" expressible at all: operation, scope,
// target disposition and discovery are four independent axes, and a mutation
// with an unresolved scope is a legitimate combination rather than a
// contradiction to be smoothed away.
func TestInvariant_ObjectiveSemanticsKeepsDimensionsIndependent(t *testing.T) {
	s := DeriveObjectiveSemantics(OperationModify, nil)
	if s.Scope != ScopeStateUnresolved {
		t.Fatalf("scope = %s, want %s for a mutation with no bound target", s.Scope, ScopeStateUnresolved)
	}
	if s.Operation == OperationCreate {
		t.Fatal("a deferred target was relabelled as a creation")
	}
	if !s.RequiresDiscovery() {
		t.Fatal("a mutating objective with an unresolved scope must require discovery")
	}
	if s.Discovery != DiscoveryRequired {
		t.Fatalf("discovery = %s, want %s", s.Discovery, DiscoveryRequired)
	}

	// The same operation once its scope IS resolved no longer requires
	// discovery, and that is the only thing that changed.
	resolved := DeriveObjectiveSemantics(OperationModify, []string{"index.html"})
	if resolved.RequiresDiscovery() {
		t.Fatal("a resolved scope still requires discovery")
	}
	if resolved.Operation != OperationModify {
		t.Fatalf("operation = %s, want the same operation the caller declared", resolved.Operation)
	}

	// A read-only operation never requires a mutation target whatever the scope.
	readOnly := DeriveObjectiveSemantics(OperationReview, nil)
	if readOnly.Operation.RequiresMutation() {
		t.Fatalf("REVIEW reported mutation semantics: %s", readOnly.Operation)
	}
}

// TestInvariant_StrategyProjectionCannotWidenAuthority pins the canonical
// projection over the strategy taxonomy. It is TOTAL on purpose: a strategy
// added to the taxonomy without deciding its mutation semantics must be unable
// to widen authority by omission.
func TestInvariant_StrategyProjectionCannotWidenAuthority(t *testing.T) {
	unknown := strategy.ExecutionStrategy("a_strategy_nobody_has_defined")
	semantics := strategy.MutationSemanticsOf(unknown)
	if semantics.IsApplied() {
		t.Fatalf("an undefined strategy projected to APPLIED mutation semantics (%s)", semantics)
	}
	if semantics.RequiresMutationContract() {
		t.Fatalf("an undefined strategy projected to %s; the projection must fail closed",
			semantics)
	}
	for _, s := range []strategy.ExecutionStrategy{
		strategy.TargetedMutation,
		strategy.DirectDeterministic,
		strategy.MultiFilePlanning,
		strategy.TargetedReasoning,
		strategy.RepositoryInvestigation,
		strategy.DirectResponse,
		strategy.HumanClarification,
		unknown,
	} {
		if semantics := strategy.MutationSemanticsOf(s); semantics.String() == "" {
			t.Errorf("strategy %s has no canonical mutation semantics", s)
		}
	}
}

// ── Invariants already pinned elsewhere ─────────────────────────────────────
//
// These are enforced and covered today; they are listed so a reader of this
// file can find the proof rather than assume it. Re-implementing them here
// would add test count without adding assurance.
//
//	 7. Stale ExecutionSpec cannot authorize changed scope
//	        runtime/kernel: the Grant is a value captured at admission and is
//	        never rewritten; Engine.Open refuses a second execution.
//	        internal/runtime/autonomy: TestClarification_SecondClarificationReplacesTheFirst
//	  9. Mutation without evidence cannot become PROVEN
//	        runtime/kernel/outcome.go: adjudicate requires per-target write
//	        evidence for every mutating clause; "all of nothing" never satisfies.
//	        internal/execution: TestAmbiguity_AmbiguousRunMutatesNothing
//	 10. Provider failure cannot become success
//	        runtime/kernel/dispatch.go: a failed or unknown verdict blocks the
//	        step and truncation forces the provider axis to TRUNCATED.
//	 11. Capability absence cannot produce simulated completion
//	        runtime/kernel/failure.go: FailureCapabilityUnavailable; and the
//	        executor classifies capability errors through typed sentinels rather
//	        than string matching.
//	 12. Recovery cannot resurrect stale authorization
//	        internal/runtime/autonomy: TestClarification_InvalidTargetFailsClosed
//	        and TestAmbiguity_ReplanOnAmbiguousScopeBindsNothing.

// keep os and filepath imported for the shared workspace helper above.

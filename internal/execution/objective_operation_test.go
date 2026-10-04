package execution

// ── Acceptance: the objective-compilation seam ──────────────────────────────
//
// These tests pin the invariant the audit established:
//
//	UNRESOLVED TARGET  ≠  CREATE
//
// The seam under test is the deterministic compilation of a natural-language
// objective into (a) an execution-shape TaskContract and (b) a semantic
// ObjectiveContract:
//
//	TaskClassification → DeriveTaskContract → ObjectiveDerivation → DeriveObjectiveContract
//
// Every case below is deterministic, calls no provider, and mutates nothing.

import "testing"

// classifyMutation mirrors the ONLY inputs the driver's taskContract() feeds to
// DeriveTaskContract for a prompt the classifier read as a mutation: the
// objective text, requires-mutation=true, and the resolved targets the gateway
// extracted (empty when the prompt names no file).
func classifyMutation(objective string, targets []string, existedBefore map[string]bool) TaskClassification {
	return TaskClassification{
		Intent:               "modification",
		Objective:            objective,
		RequiresMutation:     true,
		Targets:              targets,
		TargetsExistedBefore: existedBefore,
	}
}

// compile runs the full objective-compilation seam for a classification and
// returns both halves.
func compile(in TaskClassification) (TaskContract, ObjectiveContract) {
	contract := DeriveTaskContract(in)
	objective := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "obj-seam",
		Request:     in.Objective,
		Kind:        contract.Kind,
		Scope:       contract.Targets,
	})
	return contract, objective
}

// TestSeam1_TargetlessModificationIsModifyNotCreate is the real failing case:
// `$prompt check this project and rewrite it` names no file, so the classifier
// extracts no target. The absence of a target must NEVER become CREATE.
func TestSeam1_TargetlessModificationIsModifyNotCreate(t *testing.T) {
	const prompt = "check this project and rewrite it"

	contract, objective := compile(classifyMutation(prompt, nil, nil))

	if objective.Semantics.Operation != OperationModify {
		t.Fatalf("operation = %s, want %s", objective.Semantics.Operation, OperationModify)
	}
	if objective.Semantics.Operation == OperationCreate {
		t.Fatal("operation compiled to CREATE from an unresolved target")
	}
	if contract.Kind == TaskCreate || objective.TaskKind == TaskCreate {
		t.Fatalf("contract kind = %s, want a non-CREATE kind", contract.Kind)
	}
	if objective.Semantics.Target != TargetDeferred {
		t.Fatalf("target = %s, want %s", objective.Semantics.Target, TargetDeferred)
	}
	if objective.Semantics.Scope != ScopeStateUnresolved {
		t.Fatalf("scope = %s, want %s", objective.Semantics.Scope, ScopeStateUnresolved)
	}
	if objective.Semantics.Discovery != DiscoveryRequired {
		t.Fatalf("discovery = %s, want %s", objective.Semantics.Discovery, DiscoveryRequired)
	}

	// The original requirement survives compilation: the verbatim request and
	// its deterministic clause set are both present.
	if objective.Request != prompt {
		t.Fatalf("request = %q, want %q", objective.Request, prompt)
	}
	if len(objective.Clauses) == 0 {
		t.Fatal("the objective carries no clauses; the requirement was discarded")
	}
	var joined string
	for _, c := range objective.Clauses {
		joined += c.Text + " "
	}
	for _, want := range []string{"check this project", "rewrite it"} {
		if !containsSubstring(joined, want) {
			t.Fatalf("clause set %q lost %q", joined, want)
		}
	}

	// A CREATE contract authors a target-existence obligation; a deferred
	// modification must not.
	for _, c := range objective.Conditions {
		if c.ID == "cond-target-exists" {
			t.Fatal("a deferred modification authored a CREATE-only target-existence condition")
		}
	}
}

// TestSeam2_DiscoveryResolvesScopeFromEvidence proves the scope is resolved from
// OBSERVED workspace evidence and the objective's own declared artifact kinds,
// never from a hard-coded filename.
func TestSeam2_DiscoveryResolvesScopeFromEvidence(t *testing.T) {
	profile := WorkspaceProfile{
		Root: "/workspace",
		Candidates: []CandidateEvidence{
			{Path: "index.html", Kind: EvidenceKindCandidate, Depth: 1},
			{Path: "styles.css", Kind: EvidenceKindCandidate, Depth: 1},
			{Path: "readme.md", Kind: EvidenceKindCandidate, Depth: 1},
		},
		FilesScanned: 3,
	}

	// The objective declares HTML and CSS; it names no file.
	derivation := DeriveScope(DerivationRequest{
		Prompt:  "check this project and rewrite the HTML and CSS",
		Profile: profile,
	})
	if !derivation.Derivable {
		t.Fatalf("derivation was not attempted: %s", derivation.Reason)
	}
	want := map[string]bool{"index.html": true, "styles.css": true}
	if len(derivation.Targets) != len(want) {
		t.Fatalf("derived targets = %v, want the two evidence files", derivation.Targets)
	}
	for _, target := range derivation.Targets {
		if !want[target] {
			t.Fatalf("derivation invented target %q; observed=%v", target, profile.CandidatePaths())
		}
	}

	// The resolved scope is a function of the EVIDENCE, so the semantics flip
	// from UNRESOLVED to RESOLVED with the same operation.
	sem := DeriveObjectiveSemantics(OperationModify, derivation.Targets)
	if sem.Scope != ScopeStateResolved || sem.Target != TargetConcrete {
		t.Fatalf("after discovery semantics = %+v, want RESOLVED/CONCRETE", sem)
	}
	if sem.Discovery != DiscoveryNotRequired {
		t.Fatalf("discovery = %s, want NOT_REQUIRED once the scope is resolved", sem.Discovery)
	}
	if sem.Operation != OperationModify {
		t.Fatalf("operation = %s, want MODIFY across the transition", sem.Operation)
	}
}

// TestSeam3_ReplanConsumesDiscoveryEvidence proves a replan does not rebuild an
// empty scope from the original prompt: it consumes discovery evidence and
// re-authors the objective contract against the resolved scope.
func TestSeam3_ReplanConsumesDiscoveryEvidence(t *testing.T) {
	const prompt = "check this project and rewrite the HTML and CSS"

	// Initial objective: DEFERRED.
	initial := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "obj-replan",
		Request:     prompt,
		Kind:        TaskPatch,
		Scope:       nil,
	})
	if initial.Semantics.Discovery != DiscoveryRequired || initial.Semantics.Scope != ScopeStateUnresolved {
		t.Fatalf("initial semantics = %+v, want UNRESOLVED/REQUIRED", initial.Semantics)
	}

	// Replan observes the CURRENT workspace; the derived targets come from the
	// evidence, not from the prompt text.
	evidence := WorkspaceProfile{
		Candidates: []CandidateEvidence{
			{Path: "index.html", Kind: EvidenceKindCandidate, Depth: 1},
			{Path: "styles.css", Kind: EvidenceKindCandidate, Depth: 1},
		},
	}
	derivation := DeriveScope(DerivationRequest{Prompt: prompt, Profile: evidence})
	if len(derivation.Targets) == 0 {
		t.Fatal("discovery produced no evidence-bound target for the replan")
	}

	// The re-authored contract carries the resolved scope.
	resolved := DeriveObjectiveContract(ObjectiveDerivation{
		ObjectiveID: "obj-replan", // same objective identity
		Request:     prompt,
		Kind:        TaskPatch,
		Scope:       derivation.Targets,
	})
	if resolved.ObjectiveID != initial.ObjectiveID {
		t.Fatal("replan changed the objective identity; a replan is a new approach, not a new objective")
	}
	if resolved.Semantics.Scope != ScopeStateResolved || resolved.Semantics.Target != TargetConcrete {
		t.Fatalf("replanned semantics = %+v, want RESOLVED/CONCRETE", resolved.Semantics)
	}
	if len(resolved.Scope) == 0 {
		t.Fatal("replan reconstructed an empty scope from the original prompt")
	}
	for _, t2 := range resolved.Scope {
		if !containsSubstring(prompt, t2) {
			// The resolved target is evidence-derived; it must be one of the
			// observed candidates. (The prompt does not name it.)
			found := false
			for _, c := range evidence.CandidatePaths() {
				if c == t2 {
					found = true
				}
			}
			if !found {
				t.Fatalf("replanned scope %q is neither evidence nor prompt", t2)
			}
		}
	}
}

// TestSeam4_AmbiguousRequestFailsClosed proves an ambiguous objective is never
// compiled as `CREATE unknown` and cannot reach a mutation.
func TestSeam4_AmbiguousRequestFailsClosed(t *testing.T) {
	const prompt = "do something useful"

	// A read-only classification stays read-only and never becomes CREATE.
	readOnly := DeriveTaskContract(TaskClassification{
		Intent:    "explanation",
		Objective: prompt,
		// RequiresMutation false: the classifier read it as read-only.
	})
	if readOnly.Kind == TaskCreate {
		t.Fatal("an ambiguous read-only objective compiled as CREATE")
	}

	// Even if a caller forces mutation authority, an unresolved target is a
	// deferred MODIFY — never CREATE.
	forced := DeriveTaskContract(classifyMutation(prompt, nil, nil))
	if forced.Kind == TaskCreate {
		t.Fatal("a forced-mutation ambiguous objective compiled as CREATE")
	}
	if op := OperationForTaskKind(forced.Kind); op != OperationModify {
		t.Fatalf("operation = %s, want MODIFY for a targetless mutation", op)
	}
	// The pre-flight admission boundary that forbids the mutation is asserted in
	// the autonomy package (TestSeamAdmissionBlocksDeferredMutation), where the
	// AdmissionSpec type lives.
}

// TestSeam5_ExplicitCreateStaysCreate proves the fix did not collapse CREATE
// into MODIFY.
func TestSeam5_ExplicitCreateStaysCreate(t *testing.T) {
	contract, objective := compile(classifyMutation(
		"create a new file named example.txt",
		[]string{"example.txt"},
		map[string]bool{}, // the named file does not exist yet
	))

	if contract.Kind != TaskCreate || objective.TaskKind != TaskCreate {
		t.Fatalf("kind = %s/%s, want CREATE", contract.Kind, objective.TaskKind)
	}
	if objective.Semantics.Operation != OperationCreate {
		t.Fatalf("operation = %s, want CREATE", objective.Semantics.Operation)
	}
	if objective.Semantics.Scope != ScopeStateResolved || objective.Semantics.Target != TargetConcrete {
		t.Fatalf("semantics = %+v, want RESOLVED/CONCRETE", objective.Semantics)
	}
	if len(contract.Targets) != 1 || contract.Targets[0] != "example.txt" {
		t.Fatalf("targets = %v, want [example.txt]", contract.Targets)
	}
}

// TestSeam6_ExplicitModificationStaysModify proves an explicit modification
// compiles as MODIFY with a RESOLVED scope.
func TestSeam6_ExplicitModificationStaysModify(t *testing.T) {
	contract, objective := compile(classifyMutation(
		"update src/example.go",
		[]string{"src/example.go"},
		map[string]bool{"src/example.go": true},
	))

	if contract.Kind != TaskPatch {
		t.Fatalf("kind = %s, want PATCH", contract.Kind)
	}
	if objective.Semantics.Operation != OperationModify {
		t.Fatalf("operation = %s, want MODIFY", objective.Semantics.Operation)
	}
	if objective.Semantics.Scope != ScopeStateResolved || objective.Semantics.Target != TargetConcrete {
		t.Fatalf("semantics = %+v, want RESOLVED/CONCRETE", objective.Semantics)
	}
	if len(contract.Targets) != 1 || contract.Targets[0] != "src/example.go" {
		t.Fatalf("targets = %v, want [src/example.go]", contract.Targets)
	}
}

// TestSeam7_DeleteWithUnresolvedTargetIsStillDelete proves the same invariant
// holds for DELETE: deletion with a deferred target is a DELETE, never CREATE.
func TestSeam7_DeleteWithUnresolvedTargetIsStillDelete(t *testing.T) {
	contract, objective := compile(TaskClassification{
		Intent:           "modification",
		Objective:        "delete the generated artifacts",
		RequiresMutation: true,
		DeleteRequested:  true,
	})
	if contract.Kind != TaskDelete {
		t.Fatalf("kind = %s, want DELETE", contract.Kind)
	}
	if objective.Semantics.Operation != OperationDelete {
		t.Fatalf("operation = %s, want DELETE", objective.Semantics.Operation)
	}
	if objective.Semantics.Discovery != DiscoveryRequired {
		t.Fatalf("discovery = %s, want REQUIRED for a deferred deletion", objective.Semantics.Discovery)
	}
}

// containsSubstring is a local case-sensitive helper; the execution package's
// existing helpers are not exported to tests and this keeps the assertions
// explicit.
func containsSubstring(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

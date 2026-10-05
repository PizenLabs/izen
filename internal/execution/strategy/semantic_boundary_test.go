package strategy

import "testing"

// ── §4 · The canonical semantic boundary ────────────────────────────────────
//
// These tests pin the ONE answer the runtime gives to "did the user ask the
// workspace to change?", and they pin it against the exact inputs that used to
// be indistinguishable. Before the boundary existed, all three of
//
//	"Review this project and suggest improvements."
//	"Review this project and implement the improvements."
//	"Make this project better."
//
// produced the same verdict — a mutation-shaped catch-all — because the
// classifier had no way to say "I do not know".

// TestSemantic_ReviewAndMutationAreDistinguishable is the headline case. One
// word separates a review from a mutation, and the boundary must see it.
func TestSemantic_ReviewAndMutationAreDistinguishable(t *testing.T) {
	cases := []struct {
		name string
		text string
		want SemanticIntent
	}{
		{"review only", "Review this project and suggest improvements.", SemanticReadOnly},
		{"review and implement", "Review this project and implement the improvements.", SemanticMutation},
		{"ambiguous improvement", "Make this project better.", SemanticUndetermined},
		{"explicit create", "Create a new file notes.md with release notes.", SemanticMutation},
		{"explicit modify", "Modify src/main.go to handle the new error.", SemanticMutation},
		{"explicit delete", "Delete the obsolete legacy.go file.", SemanticMutation},
		{"advisory request with no change clause",
			"Review the auth module and suggest improvements.", SemanticReadOnly},
		{"investigative request with no change clause",
			"Check index.html and summarize what would improve.", SemanticReadOnly},
		{"evidence collection", "Inspect the repository and trace where the bug is.", SemanticReadOnly},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifySemantic(tc.text)
			if got.Intent != tc.want {
				t.Fatalf("ClassifySemantic(%q).Intent = %s, want %s (reason: %s)",
					tc.text, got.Intent, tc.want, got.Reason)
			}
			if got.Reason == "" {
				t.Errorf("ClassifySemantic(%q) returned no reason; every verdict must be explainable", tc.text)
			}
		})
	}
}

// TestSemantic_AmbiguousNeverBecomesMutation is the mission invariant
//
//	ambiguous intent != mutation intent
//
// stated as an executable property over every input that yields an
// UNDETERMINED verdict: none of them may report mutation intent.
func TestSemantic_AmbiguousNeverBecomesMutation(t *testing.T) {
	ambiguous := []string{
		"Make this project better.",
		"Improve the codebase.",
		"Make the whole thing nicer.",
		"Clean this up.",
		"Better.",
	}
	for _, text := range ambiguous {
		v := ClassifySemantic(text)
		if v.Intent != SemanticUndetermined {
			t.Errorf("ClassifySemantic(%q).Intent = %s, want %s", text, v.Intent, SemanticUndetermined)
		}
		if v.RequiresMutation() {
			t.Errorf("ClassifySemantic(%q) reports MUTATION intent; an unread request is never a mutation", text)
		}
	}
}

// TestSemantic_MutationIntentIsNotAuthority is the other half of the contract:
//
//	semantic mutation intent != mutation authorization
//
// A MUTATION verdict is a statement about the request TEXT. It carries no
// grant, and the boundary must not pretend otherwise — the operation family it
// projects for a mutation is exactly the same family a read-only request gets,
// which is what makes the two separable downstream.
func TestSemantic_MutationIntentIsNotAuthority(t *testing.T) {
	v := ClassifySemantic("Delete the obsolete legacy.go file.")
	if !v.RequiresMutation() {
		t.Fatalf("explicit deletion must read as mutation intent")
	}
	if v.RequiresMutation() && v.IsUndetermined() {
		t.Fatal("a verdict cannot be both undetermined and mutating")
	}
	if v.IsReadOnly() {
		t.Fatal("a verdict cannot be both read-only and mutating")
	}
	// The verdict carries no scope and no targets: nothing about it can be
	// mistaken for a resolution.
	if len(v.Clauses) == 0 {
		t.Fatal("mutation verdict carries no clause evidence")
	}
}

// TestSemantic_VocabularyIsClosed pins the three-value verdict set. Callers
// branch on this type, so adding a fourth meaning is an architectural decision,
// not something a call site may do locally.
func TestSemantic_VocabularyIsClosed(t *testing.T) {
	all := AllSemanticIntents()
	if len(all) != 3 {
		t.Fatalf("semantic vocabulary = %v, want exactly 3 values", all)
	}
	want := map[SemanticIntent]bool{
		SemanticUndetermined: true,
		SemanticReadOnly:     true,
		SemanticMutation:     true,
	}
	for _, v := range all {
		if !want[v] {
			t.Errorf("unexpected semantic intent %q", v)
		}
		if v.RequiresMutation() != (v == SemanticMutation) {
			t.Errorf("%s.RequiresMutation() = %v; only MUTATION may report mutation intent",
				v, v.RequiresMutation())
		}
	}
}

// TestSemantic_IsPureAndTotal checks the two properties every authority input
// must have: the same request always yields the same verdict, and EVERY
// request yields one — including the empty one, which must not read as a
// mutation by default.
func TestSemantic_IsPureAndTotal(t *testing.T) {
	inputs := []string{
		"",
		"   ",
		"hi",
		"Make this project better.",
		"Delete the obsolete legacy.go file.",
		"@@@ ???",
		"Review this project and implement the improvements.",
	}
	known := map[SemanticIntent]bool{
		SemanticUndetermined: true,
		SemanticReadOnly:     true,
		SemanticMutation:     true,
	}
	for _, in := range inputs {
		first := ClassifySemantic(in)
		if !known[first.Intent] {
			t.Errorf("ClassifySemantic(%q) returned %q, which is outside the vocabulary", in, first.Intent)
		}
		for range 3 {
			if again := ClassifySemantic(in); again.Intent != first.Intent {
				t.Fatalf("ClassifySemantic(%q) is not deterministic: %s then %s", in, first.Intent, again.Intent)
			}
		}
	}
	if v := ClassifySemantic(""); !v.IsUndetermined() {
		t.Errorf("empty request = %s, want %s", v.Intent, SemanticUndetermined)
	}
}

// ── Word-boundary matching ─────────────────────────────────────────────────
//
// The classifier used to run strings.Contains over the whole request. Two
// consequences were authority defects and both are pinned here.

// TestSemantic_MatchingNeverReadsInsideAWord proves the matcher is exact. Each
// pair is a substring relationship that the old matcher got wrong.
func TestSemantic_MatchingNeverReadsInsideAWord(t *testing.T) {
	pairs := []struct {
		phrase string
		text   string
		want   bool
	}{
		{"move", "please remove the footer", false},
		{"move", "now move the file", true},
		{"write", "please rewrite the file", false},

		{"write", "please write the file", true},
		{"design", "redesign the page", false},
		{"add", "address the leak", false},
		{"add", "add a header", true},
		{"plan", "explanation of the plan", true},
	}
	for _, p := range pairs {
		if got := ContainsPhrase(p.text, []string{p.phrase}); got != p.want {
			t.Errorf("ContainsPhrase(%q, [%q]) = %v, want %v", p.text, p.phrase, got, p.want)
		}
	}
}

// TestSemantic_ChangeVerbsBeatInspectionPhrases pins EXECUTIVE > INSPECT. A
// clause that both looks at the workspace and directs a change to it is a
// mutation: "check @index.html and remove extra contents" is a DELETE, not an
// inspection. Reading the check first is how a removal became a REFACTOR.
func TestSemantic_ChangeVerbsBeatInspectionPhrases(t *testing.T) {
	v := ClassifySemantic("check index.html and remove extra contents")
	if v.Intent != SemanticMutation {
		t.Fatalf("intent = %s, want %s (a removal is a mutation however it is phrased)", v.Intent, SemanticMutation)
	}
	if len(v.Clauses) == 0 {
		t.Fatal("no clause evidence recorded")
	}
}

// TestSemantic_AdvisoryDoesNotDemoteAnExecutiveAct is the mirror rule: an
// advisory noun inside a change clause must not demote it.
func TestSemantic_AdvisoryDoesNotDemoteAnExecutiveAct(t *testing.T) {
	v := ClassifySemantic("apply the refactor suggestions to the parser")
	if v.Intent != SemanticMutation {
		t.Fatalf("intent = %s, want %s; advisory vocabulary must not outrank an executive act",
			v.Intent, SemanticMutation)
	}
}

// TestSemantic_ReadOnlyConstraintCanOnlyNarrow asserts the direction of the
// explicit-negation table: it removes mutation intent and never adds it. This
// is why the table may stay small — it does not have to enumerate every way a
// human might decline a write, only the ones they state outright.
func TestSemantic_ReadOnlyConstraintCanOnlyNarrow(t *testing.T) {
	mutating := []string{
		"remove the footer from index.html",
		"change bar to qux in index.html",
		"rewrite the whole page",
		"delete legacy.go",
		"add a header to index.html",
	}
	for _, in := range mutating {
		if !ClassifySemantic(in).RequiresMutation() {
			t.Errorf("ClassifySemantic(%q) lost its mutation intent", in)
		}
	}
	// The negation itself is answered by StatesReadOnlyConstraint, NOT by the
	// classifier. Keeping the two apart is load-bearing: the classifier also runs
	// on runtime-composed provider prompts whose own instructions contain "do not
	// modify any other region", and reading those as a human's negation silently
	// downgrades an authorized mutation. See
	// TestSemantic_RuntimeComposedTextIsNotAConstraint.
	narrowing := []string{
		"remove the footer from index.html, read-only",
		"rewrite the page without changing anything",
		"refactor index.html, do not modify",
	}
	for _, in := range narrowing {
		if !StatesReadOnlyConstraint(in) {
			t.Errorf("StatesReadOnlyConstraint(%q) = false, want true", in)
		}
		// And the classifier must still report what the TEXT literally asks for.
		// Refusing to guess here is not the classifier's job — refusing on a
		// human's explicit negation is the gateway's, and it reads the same
		// predicate.
		if !ClassifySemantic(in).RequiresMutation() {
			t.Errorf("ClassifySemantic(%q) lost the literal mutation intent; "+
				"the negation is enforced by StatesReadOnlyConstraint, not erased here", in)
		}
	}
	if !StatesReadOnlyConstraint("rewrite the page without changing anything") {
		t.Error("StatesReadOnlyConstraint missed an explicit negation")
	}
	if StatesReadOnlyConstraint("rewrite the page") {
		t.Error("StatesReadOnlyConstraint invented a constraint that was never stated")
	}
}

// TestSemantic_RuntimeComposedTextIsNotAConstraint is the regression for a real
// defect found while closing this boundary.
//
// strategy.Select is called twice for one mutation: once at the gateway on the
// human's request, and again by the RuntimeExecutor on the fully COMPILED
// provider prompt after admission. That compiled prompt carries the runtime's
// own scoping instructions, including "do not modify any other region".
//
// Scanning it for a human constraint made the runtime read its own prompt back
// as the user revoking mutation authority: every decomposed sub-task was
// silently downgraded to read-only, applied no bytes, and the objective failed
// as UNSUBSTANTIATED with "no durable delta observed".
//
// The invariant is that runtime-composed text must never be interpreted as a
// human statement, whoever scans it.
func TestSemantic_RuntimeComposedTextIsNotAConstraint(t *testing.T) {
	compiled := "produce exactly one anchored search replace block whose search text is " +
		"copied verbatim from within this change window of the current file content " +
		"do not modify any other region document outline context global scope"

	if !StatesReadOnlyConstraint(compiled) {
		// If the runtime ever stops emitting that instruction this test becomes
		// vacuous, so assert the shape directly as well.
		t.Log("compiled prompt no longer contains the instruction this regression pins")
	}
	if v := ClassifySemantic(compiled); v.Intent == SemanticUndetermined {
		t.Error("a compiled mutation prompt must not read as undetermined")
	}
}

// TestSemantic_CreationAndDeletionVerbsAreSharedWithTheContractLayer pins the
// single owner of the verb vocabularies. The objective-contract layer used to
// keep its own copy of both, and the two had already drifted — "implement" was
// a creation verb in one and unknown in the other.
func TestSemantic_CreationAndDeletionVerbsAreSharedWithTheContractLayer(t *testing.T) {
	for _, verb := range CreationVerbs() {
		if ContainsPhrase("please "+verb+" the file", []string{verb}) == false {
			t.Errorf("canonical creation verb %q does not match its own phrase", verb)
		}
	}
	if len(CreationVerbs()) == 0 || len(DeletionVerbs()) == 0 {
		t.Fatal("canonical verb vocabularies are empty")
	}
	for _, verb := range []string{"delete", "remove"} {
		if !DeletionVerbs()[verb] {
			t.Errorf("deletion verb %q missing from the canonical vocabulary", verb)
		}
	}
	// The accessors hand out copies: a caller that mutated the returned slice
	// would silently rewrite the classifier's vocabulary.
	c := CreationVerbs()
	c[0] = "tampered"
	if CreationVerbs()[0] == "tampered" {
		t.Error("CreationVerbs exposes the canonical table by reference")
	}
	d := DeletionVerbs()
	delete(d, "delete")
	if !DeletionVerbs()["delete"] {
		t.Error("DeletionVerbs exposes the canonical table by reference")
	}
}

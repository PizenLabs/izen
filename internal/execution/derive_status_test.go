package execution

// ── Derivation status is a VERDICT, not a target count ───────────────────────
//
// DeriveScope had one shape of answer — a list — and reported ambiguity in prose.
// A caller that read the list length therefore could not tell:
//
//	"three html files exist and I do not know which one is meant"   (AMBIGUOUS)
//	"exactly one html file exists and it is the one"                (UNIQUE)
//	"no html file exists"                                            (UNRESOLVED)
//
// apart, and treated all three as "I have targets". These tests pin the three
// verdicts as three distinct, typed facts, and pin the distinction the audit
// called out between SEVERAL INTENDED TARGETS and SEVERAL CANDIDATE TARGETS.

import "testing"

// ambiguousWorkspace builds a.html/b.html/index.html + a.css/b.css/styles.css.
func ambiguousWorkspace(t *testing.T) WorkspaceProfile {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"a.html":     "<html></html>\n",
		"b.html":     "<html></html>\n",
		"index.html": "<html></html>\n",
		"a.css":      ".a {}\n",
		"b.css":      ".b {}\n",
		"styles.css": ":root {}\n",
	} {
		writeFile(t, root, name, body)
	}
	return NewWorkspaceDiscovery(root).Discover()
}

// uniqueWorkspace builds one file per declared artifact kind.
func uniqueWorkspace(t *testing.T) WorkspaceProfile {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "index.html", "<html></html>\n")
	writeFile(t, root, "styles.css", ":root {}\n")
	writeFile(t, root, "script.js", "1;\n")
	return NewWorkspaceDiscovery(root).Discover()
}

const kindObjective = "check this project and rewrite the HTML and CSS"

// TestDerivationStatus_Unique pins the positive verdict: one observed file per
// declared kind determines the target set, so the set may be bound.
func TestDerivationStatus_Unique(t *testing.T) {
	d := DeriveScope(DerivationRequest{Prompt: "check this project and rewrite the HTML and CSS", Profile: uniqueWorkspace(t)})

	if d.StatusOrUnresolved() != DerivationUnique {
		t.Fatalf("status = %s, want UNIQUE (%v / %s)", d.StatusOrUnresolved(), d.Targets, d.Reason)
	}
	if !d.IsUnique() || d.IsAmbiguous() || d.IsUnresolved() {
		t.Fatalf("predicates disagree with the status: %+v", d)
	}
	// Exactly the two declared kinds' unique matches. script.js is observed but
	// its kind was never declared: a UNIQUE verdict is still evidence-bounded.
	want := []string{"index.html", "styles.css"}
	if len(d.Targets) != len(want) {
		t.Fatalf("targets = %v, want exactly %v", d.Targets, want)
	}
	for _, w := range want {
		if !containsString(d.Targets, w) {
			t.Errorf("target %q missing from %v", w, d.Targets)
		}
	}
	if containsString(d.Targets, "script.js") {
		t.Errorf("targets = %v; an undeclared kind must not contribute", d.Targets)
	}
	if len(d.AmbiguousKinds()) != 0 {
		t.Errorf("ambiguous kinds = %v, want none", d.AmbiguousKinds())
	}
}

// TestDerivationStatus_Ambiguous pins the reported defect at the pure layer:
// several matches per declared kind produce an AMBIGUOUS verdict, and the
// non-empty candidate list is NOT the scope.
func TestDerivationStatus_Ambiguous(t *testing.T) {
	d := DeriveScope(DerivationRequest{Prompt: kindObjective, Profile: ambiguousWorkspace(t)})

	if d.StatusOrUnresolved() != DerivationAmbiguous {
		t.Fatalf("status = %s, want AMBIGUOUS (%v / %s)", d.StatusOrUnresolved(), d.Targets, d.Reason)
	}
	if d.IsUnique() || !d.IsAmbiguous() || d.IsUnresolved() {
		t.Fatalf("predicates disagree with the status: %+v", d)
	}
	// The candidates are still reported — the human needs to choose from them —
	// and that is precisely what makes the verdict, not the list, load-bearing.
	for _, want := range []string{"a.html", "b.html", "index.html", "a.css", "b.css", "styles.css"} {
		if !containsString(d.Targets, want) {
			t.Errorf("candidate %q missing from the disambiguation set %v", want, d.Targets)
		}
	}
	// The ambiguity is located per kind, not only narrated.
	ambiguous := d.AmbiguousKinds()
	if len(ambiguous) != 2 {
		t.Fatalf("ambiguous kinds = %v, want both css and html", ambiguous)
	}
	for _, kind := range []string{"css", "html"} {
		if !containsString(ambiguous, kind) {
			t.Errorf("kind %q is ambiguous but not reported: %v", kind, ambiguous)
		}
	}
	if len(d.Resolutions) != 2 {
		t.Fatalf("resolutions = %d, want one per declared kind", len(d.Resolutions))
	}
	for _, r := range d.Resolutions {
		if !r.Ambiguous {
			t.Errorf("resolution %q reports unambiguous with %v matches", r.Kind, r.Matches)
		}
	}
}

// TestDerivationStatus_AmbiguityIsNeverNarrowed pins that the ambiguous verdict
// does not degrade into a subset. Binding "the kinds I am sure about" would
// silently rewrite the objective, and preferring the first match is the exact
// heuristic this file exists to refuse.
func TestDerivationStatus_AmbiguityIsNeverNarrowed(t *testing.T) {
	// One declared kind is ambiguous, the other is unique. The whole derivation
	// must still be AMBIGUOUS: the objective asks about both kinds.
	root := t.TempDir()
	for name, body := range map[string]string{
		"a.html":   "<html></html>\n",
		"b.html":   "<html></html>\n",
		"only.css": "body {}\n",
	} {
		writeFile(t, root, name, body)
	}
	d := DeriveScope(DerivationRequest{
		Prompt:  kindObjective,
		Profile: NewWorkspaceDiscovery(root).Discover(),
	})
	if d.StatusOrUnresolved() != DerivationAmbiguous {
		t.Fatalf("status = %s, want AMBIGUOUS — one ambiguous kind makes the objective ambiguous", d.StatusOrUnresolved())
	}
	if d.IsUnique() {
		t.Fatal("a partially ambiguous objective was reported UNIQUE; that is narrowing by omission")
	}
	if !containsString(d.Targets, "only.css") || !containsString(d.Targets, "a.html") {
		t.Fatalf("candidates = %v, want the full observed set for disambiguation", d.Targets)
	}
}

// TestDerivationStatus_Unresolved pins the negative verdict and the
// never-invent-a-target invariant that came with it.
func TestDerivationStatus_Unresolved(t *testing.T) {
	t.Run("no declared kind", func(t *testing.T) {
		d := DeriveScope(DerivationRequest{Prompt: "check this project and rewrite it", Profile: uniqueWorkspace(t)})
		if d.StatusOrUnresolved() != DerivationUnresolved || !d.IsUnresolved() {
			t.Fatalf("status = %s, want UNRESOLVED", d.StatusOrUnresolved())
		}
		if d.Derivable {
			t.Error("derivation must not be attempted when the objective declares no kind")
		}
		if len(d.Targets) != 0 {
			t.Errorf("targets = %v, want none", d.Targets)
		}
	})
	t.Run("empty workspace", func(t *testing.T) {
		d := DeriveScope(DerivationRequest{Prompt: kindObjective, Profile: NewWorkspaceDiscovery(t.TempDir()).Discover()})
		if d.StatusOrUnresolved() != DerivationUnresolved {
			t.Fatalf("status = %s, want UNRESOLVED", d.StatusOrUnresolved())
		}
		if !d.Derivable {
			t.Error("the pass ran and observed nothing; that must stay distinguishable from never having looked")
		}
	})
	t.Run("declared kind with no observed match", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "main.go", "package main\n")
		d := DeriveScope(DerivationRequest{Prompt: kindObjective, Profile: NewWorkspaceDiscovery(root).Discover()})
		if d.StatusOrUnresolved() != DerivationUnresolved {
			t.Fatalf("status = %s, want UNRESOLVED", d.StatusOrUnresolved())
		}
		if len(d.Targets) != 0 {
			t.Fatalf("targets = %v; the runtime will not invent a target", d.Targets)
		}
	})
}

// TestDerivationStatus_StatedTargetsOutrankDerivation pins EXPLICIT > DERIVED. A
// user who names several files has stated a scope; that is not ambiguity, and
// discovery may neither replace it nor report the workspace's own multiplicity
// against it.
func TestDerivationStatus_StatedTargetsOutrankDerivation(t *testing.T) {
	d := DeriveScope(DerivationRequest{
		Prompt:        "rewrite the HTML and CSS",
		Profile:       ambiguousWorkspace(t),
		StatedTargets: []string{"a.html", "b.html"},
	})
	if d.StatusOrUnresolved() != DerivationUnresolved {
		t.Fatalf("status = %s; derivation must not run over a proven target set", d.StatusOrUnresolved())
	}
	if d.IsAmbiguous() {
		t.Fatal("a stated multi-target scope was reported ambiguous; several INTENDED targets is not a question")
	}
	if len(d.Targets) != 0 {
		t.Fatalf("derivation produced %v over an already-proven scope", d.Targets)
	}
}

// TestDerivationStatus_ZeroValueIsNeverUnique pins the fail-closed default: a
// Derivation nobody filled in (a nil adapter, a future caller that forgets a
// branch) must not read as a resolved scope.
func TestDerivationStatus_ZeroValueIsNeverUnique(t *testing.T) {
	var d Derivation
	if d.IsUnique() {
		t.Fatal("the zero value read as UNIQUE; an unconstructed derivation can never be a scope")
	}
	if d.StatusOrUnresolved() != DerivationUnresolved {
		t.Fatalf("zero status = %s, want UNRESOLVED", d.StatusOrUnresolved())
	}
	if !d.IsUnresolved() {
		t.Fatal("the zero value is neither unique nor ambiguous, so it must be unresolved")
	}
}

// TestDerivationStatus_VocabularyIsClosed pins the three-value vocabulary. A
// caller that branches on this field must not have to handle a fourth meaning
// that a future edit invents locally.
func TestDerivationStatus_VocabularyIsClosed(t *testing.T) {
	if len(AllDerivationStatuses()) != 3 {
		t.Fatalf("vocabulary has %d values, want exactly 3: %v", len(AllDerivationStatuses()), AllDerivationStatuses())
	}
	seen := map[DerivationStatus]bool{}
	for _, s := range AllDerivationStatuses() {
		if seen[s] {
			t.Fatalf("duplicate derivation status %s", s)
		}
		seen[s] = true
		if s.String() == "" {
			t.Errorf("status %q has no label", s)
		}
	}
}

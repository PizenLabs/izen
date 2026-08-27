package verifier

import (
	"strings"
	"testing"
)

// ── fixtures ────────────────────────────────────────────────────────────────

// htmlFixture renders a deterministic HTML document with a <style> block
// (CSS consumers), element definitions and anchor/script hooks. Every line
// is stable, so line-number assertions hold. CSS rules are INDENTED: a
// leading '.' at column zero is not a selector boundary, matching the Lea
// reference heuristics the audit mirrors.
func htmlFixture() string {
	return `<!doctype html>
<html>
<head>
<style>
  .card { color: red }
  .panel { color: blue }
  .hero { font-size: 2rem }
</style>
</head>
<body>
<section id="top">
<div class="card">a</div>
<div class="panel">b</div>
<a href="#bottom">down</a>
</section>
<footer id="bottom">
<script>var el = document.getElementById("top");</script>
</footer>
</body>
</html>
`
}

// mutateLines applies a 1-indexed line replacement table to source lines.
func mutateLines(src string, repl map[int]string) string {
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	for i := range lines {
		if v, ok := repl[i+1]; ok {
			lines[i] = v
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

const styleCardLine = 5 // "  .card { color: red }"

// ── S1 — syntax validity ────────────────────────────────────────────────────

func TestVerifySyntaxInvalidFailsClosed(t *testing.T) {
	mutated := `<html><body><section id="a"><div class="x">unclosed`
	v := AuditObjective("page.html", []byte(htmlFixture()), []byte(mutated), IntentSpec{})
	if v.Pass() {
		t.Fatal("an audit over a structurally broken mutation must fail")
	}
	if v.Failures[0].Code != FailSyntaxInvalid {
		t.Fatalf("failure = %s, want %s", v.Failures[0].Code, FailSyntaxInvalid)
	}
	if v.Status != StatusUnresolved {
		t.Fatalf("status = %s, want %s", v.Status, StatusUnresolved)
	}
}

func TestVerifyNilTreesFailClosed(t *testing.T) {
	v := VerifyGlobalObjective(nil, Parse("p.html", []byte(htmlFixture())), IntentSpec{})
	if v.Pass() || v.Failures[0].Code != FailNilTree {
		t.Fatalf("nil base tree must fail with %s: %+v", FailNilTree, v)
	}
	v = VerifyGlobalObjective(Parse("p.html", []byte(htmlFixture())), nil, IntentSpec{})
	if v.Pass() || v.Failures[0].Code != FailNilTree {
		t.Fatalf("nil mutated tree must fail with %s: %+v", FailNilTree, v)
	}
}

func TestVerifyTopologyFaultFailsClosed(t *testing.T) {
	bad := &DOMNode{Tag: "div", StartLine: 5, EndLine: 3} // inverted span
	root := &DOMNode{Tag: DocumentTag, Children: []*DOMNode{bad}}
	v := VerifyGlobalObjective(root, root, IntentSpec{})
	if v.Pass() || v.Failures[0].Code != FailTopologyInvalid {
		t.Fatalf("inverted span must fail with %s: %+v", FailTopologyInvalid, v)
	}
}

// ── S2a — dangling references ───────────────────────────────────────────────

func TestVerifyDanglingAnchorAfterIDRemoval(t *testing.T) {
	base := htmlFixture()
	// st-1 removes the footer that defines id="bottom" (lines 16 and 18);
	// the anchor at line 14 keeps pointing at it.
	mutated := mutateLines(base, map[int]string{16: "", 18: ""})
	v := AuditObjective("page.html", []byte(base), []byte(mutated), IntentSpec{})
	if v.Pass() {
		t.Fatal("anchor to a removed id must fail the audit")
	}
	found := false
	for _, f := range v.Failures {
		if f.Code == FailDanglingReference && f.Token == "#bottom" {
			found = true
			if !strings.Contains(f.Detail, "#bottom was removed but is still referenced") {
				t.Fatalf("dangling detail incomplete: %q", f.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("no dangling_reference #bottom failure in %+v", v.Failures)
	}
}

// ── S2b — orphaned definitions (the cross-subtask regression) ───────────────

func TestVerifyCrossSubtaskCSSRegressionCaught(t *testing.T) {
	base := htmlFixture()
	// st-1 rewrites the .card CSS rule away; st-4's region still carries
	// <div class="card">. Per-unit gates pass; only the aggregate is broken.
	mutated := mutateLines(base, map[int]string{
		styleCardLine: ".panel-legacy { color: red }",
	})
	v := AuditObjective("page.html", []byte(base), []byte(mutated), IntentSpec{})
	if v.Pass() {
		t.Fatal("removing a CSS definition still used elsewhere must fail the audit")
	}
	found := false
	for _, f := range v.Failures {
		if f.Code == FailOrphanedDefinition && f.Token == ".card" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no orphaned_definition .card failure in %+v", v.Failures)
	}
}

func TestVerifyConsistentRenamePasses(t *testing.T) {
	base := htmlFixture()
	// Both the consumer AND the definition move together — a correct refactor.
	repl := map[int]string{
		styleCardLine: ".info { color: red }",
	}
	lines := strings.Split(strings.TrimSuffix(base, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.ReplaceAll(lines[i], `class="card"`, `class="info"`)
	}
	mutated := mutateLines(strings.Join(lines, "\n")+"\n", repl)
	v := AuditObjective("page.html", []byte(base), []byte(mutated), IntentSpec{})
	if !v.Pass() {
		t.Fatalf("consistent rename must verify, got failures %+v", v.Failures)
	}
	if v.Status != StatusVerified {
		t.Fatalf("status = %s, want %s", v.Status, StatusVerified)
	}
	if v.Base.Nodes == 0 || v.Mutated.Definitions == 0 {
		t.Fatalf("stats not populated: base=%+v mutated=%+v", v.Base, v.Mutated)
	}
}

func TestVerifyRemovalOfUnusedRuleIsNotARegression(t *testing.T) {
	// `.hero` styles nothing in the fixture body: deleting the rule removes
	// an already-dead reference without touching any definition — cleanup,
	// never a regression.
	base := htmlFixture()
	mutated := mutateLines(base, map[int]string{7: ""})
	v := AuditObjective("page.html", []byte(base), []byte(mutated), IntentSpec{})
	if !v.Pass() {
		t.Fatalf("dead-CSS cleanup must verify, got failures %+v", v.Failures)
	}
}

func TestVerifyNewTokensExempt(t *testing.T) {
	base := htmlFixture()
	// The mutation introduces a brand-new class whose rule references
	// nothing pre-existing; nothing from BASE regressed.
	mutated := mutateLines(base, map[int]string{
		styleCardLine: ".badge { color: gold }",
		12:            `<div class="badge">a</div>`,
	})
	v := AuditObjective("page.html", []byte(base), []byte(mutated), IntentSpec{})
	if !v.Pass() {
		t.Fatalf("new-token introduction must verify, got failures %+v", v.Failures)
	}
}

// ── S3 — requested removals reduce dead nodes ───────────────────────────────

func TestVerifyRequestedRemovalNotReducedFails(t *testing.T) {
	base := htmlFixture()
	// The DAG only touched the PANEL rule; the objective asked for the card
	// component to go and nothing about card changed.
	mutated := mutateLines(base, map[int]string{6: "  .kept { color: blue }"})
	intent := IntentSpec{Removals: []RemovalIntent{{Token: "card", Kind: KindAny}}}
	v := AuditObjective("page.html", []byte(base), []byte(mutated), intent)
	failed := false
	for _, f := range v.Failures {
		if f.Code == FailRemovalNotReduced && strings.Contains(f.Token, "card") {
			failed = true
			// before = 1 element definition + 1 style rule reference = 2;
			// after is unchanged.
			if !strings.Contains(f.Detail, "(2 → 2)") {
				t.Fatalf("reduction evidence missing counts: %q", f.Detail)
			}
		}
	}
	if !failed {
		t.Fatalf("unfulfilled removal must fail: %+v", v.Failures)
	}
}

func TestVerifyRequestedRemovalReducedPasses(t *testing.T) {
	base := htmlFixture()
	// The objective asked for the whole card component: rule AND elements go.
	lines := strings.Split(strings.TrimSuffix(base, "\n"), "\n")
	out := lines[:0]
	for _, l := range lines {
		if strings.Contains(l, ".card {") || l == `<div class="card">a</div>` {
			continue
		}
		out = append(out, l)
	}
	mutated := strings.Join(out, "\n") + "\n"
	intent := IntentSpec{Removals: []RemovalIntent{{Token: "card", Kind: KindAny}}}
	v := AuditObjective("page.html", []byte(base), []byte(mutated), intent)
	if !v.Pass() {
		t.Fatalf("full removal must verify, got failures %+v", v.Failures)
	}
}

// ── degradation & determinism ───────────────────────────────────────────────

func TestVerifyUnscannableFormatDegradesToVerified(t *testing.T) {
	base := "package big\n\ntype Handler struct{}\n"
	v := AuditObjective("big.go", []byte(base), []byte(base), IntentSpec{})
	if !v.Pass() {
		t.Fatalf("formats without a Lea scanner fail open: %+v", v.Failures)
	}
	if v.Base.Nodes != 0 || v.Mutated.References != 0 {
		t.Fatalf("unscanned stats must be zero: %+v", v)
	}
}

func TestVerifyDeterministicFailureOrder(t *testing.T) {
	base := htmlFixture()
	// Two independent regressions: the footer's id is removed while the
	// anchor still points at it (dangling) AND the card rule is rewritten
	// away while the element survives (orphaned).
	mutated := mutateLines(base, map[int]string{
		styleCardLine: "  .legacy { color: red }",
		16:            "",
		18:            "",
	})
	a := AuditObjective("page.html", []byte(base), []byte(mutated), IntentSpec{})
	b := AuditObjective("page.html", []byte(base), []byte(mutated), IntentSpec{})
	if a.Evidence() != b.Evidence() {
		t.Fatalf("audit is not deterministic:\n%s\n%s", a.Evidence(), b.Evidence())
	}
	if len(a.Failures) < 2 {
		t.Fatalf("expected both failure classes, got %+v", a.Failures)
	}
	for i := 1; i < len(a.Failures); i++ {
		if a.Failures[i-1].Code > a.Failures[i].Code {
			t.Fatalf("failures not sorted by code: %+v", a.Failures)
		}
	}
}

func TestVerdictEvidenceBounded(t *testing.T) {
	v := Verdict{Status: StatusUnresolved}
	for i := 0; i < 100; i++ {
		v.Failures = append(v.Failures, Failure{
			Code:   FailDanglingReference,
			Token:  "#token-" + strings.Repeat("x", 40),
			Detail: "evidence evidence evidence",
		})
	}
	if got := v.Evidence(); len(got) > 400 {
		t.Fatalf("evidence unbounded: %d chars", len(got))
	}
}

// ── intent extraction ───────────────────────────────────────────────────────

func TestExtractRemovalIntents(t *testing.T) {
	tests := []struct {
		name      string
		objective string
		want      []RemovalIntent
	}{
		{
			name:      "quoted token after remove verb",
			objective: `remove every "DEPRECATED-MARKER" comment @big.go`,
			want:      []RemovalIntent{{Token: "DEPRECATED-MARKER", Kind: KindAny}},
		},
		{
			name:      "class selector",
			objective: "remove the .sidebar markup @index.html",
			want:      []RemovalIntent{{Token: "sidebar", Kind: KindClass}},
		},
		{
			name:      "id selector with delete verb",
			objective: "delete #hero section @index.html",
			want:      []RemovalIntent{{Token: "hero", Kind: KindID}},
		},
		{
			name:      "no removal verb extracts nothing",
			objective: `rename every "card" to "tile" @index.html`,
			want:      nil,
		},
		{
			name:      "verb without identity extracts nothing",
			objective: "remove all duplication @index.html",
			want:      nil,
		},
		{
			name:      "multiple identities in one sentence",
			objective: `remove the #hero block and the .banner strip @index.html`,
			want: []RemovalIntent{
				{Token: "banner", Kind: KindClass},
				{Token: "hero", Kind: KindID},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractRemovalIntents(tt.objective)
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("entry %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExtractRemovalIntentsIgnoresVerbInsideIdentifier(t *testing.T) {
	// "removed" / "undelete" carry the verb as a substring, not a word.
	got := ExtractRemovalIntents("the removed-marker field stays; keep .legacy")
	if len(got) != 0 {
		t.Fatalf("substring verbs must not trigger extraction: %+v", got)
	}
}

func TestParseBuildsPointerTree(t *testing.T) {
	root := Parse("page.html", []byte(htmlFixture()))
	if !root.Scanned || root.Malformed || root.Tag != DocumentTag {
		t.Fatalf("root = %+v", root)
	}
	nodes := 0
	root.Walk(func(n *DOMNode) {
		if n.Tag != DocumentTag {
			nodes++
		}
	})
	if nodes == 0 {
		t.Fatal("no nodes built from scan")
	}
	if len(root.Lines) == 0 {
		t.Fatal("root carries no source lines")
	}
}

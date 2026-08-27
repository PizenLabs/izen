package planner

import (
	"fmt"
	"strings"
	"testing"
)

// ── fixtures ────────────────────────────────────────────────────────────────

// semanticHTMLFixture is a well-formed document with head, hero section,
// content sections and a footer — the canonical semantic-split input.
const semanticHTMLFixture = `<!DOCTYPE html>
<html>
<head>
	<title>Portfolio</title>
	<link rel="stylesheet" href="style.css">
</head>
<body>
	<header id="top-nav">
		<a href="#hero">Home</a>
	</header>
	<section id="hero">
		<h1>Alex Josie</h1>
		<p>Engineer. Builder.</p>
	</section>
	<section id="work">
		<article class="card">One</article>
		<article class="card">Two</article>
	</section>
	<div id="orphan-panel">
		Nobody links here.
	</div>
	<!-- <section id="old-hero">
		<h1>Superseded markup kept in comments</h1>
	</section> -->
	<footer id="site-footer">
		<span>© 2026</span>
	</footer>
</body>
</html>
`

// malformedHTMLFixture has a stray close tag and unclosed elements: parsing
// must recover but flag low confidence so splitting falls back to syntax.
const malformedHTMLFixture = `<!DOCTYPE html>
<html>
<head><title>Broken</title></head>
<body>
	<section id="a">
		<p>unclosed paragraph
	</section>
	<section id="b">
		<div>never closed
</body>
</div>
</html>
`

// ── topology ────────────────────────────────────────────────────────────────

func TestLeaStructuralScan_HTMLTopology(t *testing.T) {
	src := []byte(semanticHTMLFixture)
	rep := LeaStructuralScan("index.html", src)
	if rep == nil {
		t.Fatal("index.html must carry a Lea scanner")
	}
	if rep.Format != "html" {
		t.Fatalf("format = %q, want html", rep.Format)
	}
	if rep.LowConfidence {
		t.Fatal("well-formed fixture must not flag low confidence")
	}
	if rep.TotalLines != len(splitKeepNewline(src)) {
		t.Fatalf("total lines = %d, want %d", rep.TotalLines, len(splitKeepNewline(src)))
	}
	byID := map[string]*DOMNode{}
	for i := range rep.Nodes {
		if id := rep.Nodes[i].ID; id != "" {
			byID[id] = &rep.Nodes[i]
		}
	}
	hero, ok := byID["hero"]
	if !ok {
		t.Fatal("topology missing #hero node")
	}
	if hero.Tag != "section" {
		t.Fatalf("hero tag = %q, want section", hero.Tag)
	}
	if got := hero.CSSSelector(); got != "section#hero" {
		t.Fatalf("hero selector = %q, want section#hero", got)
	}
	// Parent/child relations: both content sections live inside body.
	body := -1
	for i := range rep.Nodes {
		if rep.Nodes[i].Tag == "body" {
			body = i
		}
	}
	if body < 0 || len(rep.Nodes[body].Children) == 0 {
		t.Fatal("body node missing or childless")
	}
	if hero.Parent != body {
		t.Fatalf("#hero parent = %d, want body at %d", hero.Parent, body)
	}
	// Depth accounting: head sits under html, title under head.
	var htmlIdx, headIdx int
	for i := range rep.Nodes {
		switch rep.Nodes[i].Tag {
		case "html":
			htmlIdx = i
		case "head":
			headIdx = i
		}
	}
	if rep.Nodes[headIdx].Parent != htmlIdx {
		t.Fatal("<head> must nest inside <html>")
	}
}

func TestLeaStructuralScan_MalformedHTML_LowConfidence(t *testing.T) {
	rep := LeaStructuralScan("broken.html", []byte(malformedHTMLFixture))
	if rep == nil {
		t.Fatal("malformed HTML still gets a best-effort report")
	}
	if !rep.LowConfidence {
		t.Fatal("stray close tags / unclosed elements must flag low confidence")
	}
	if len(rep.Nodes) == 0 {
		t.Fatal("recovery should still salvage nodes")
	}
}

func TestLeaStructuralScan_UnsupportedFormatReturnsNil(t *testing.T) {
	if LeaStructuralScan("notes.md", []byte("# hi")) != nil {
		t.Fatal("markdown has no Lea scanner")
	}
	if LeaStructuralScan("app.go", []byte("package x")) != nil {
		t.Fatal("Go source has no DOM scanner")
	}
	if LeaStructuralScan("empty.html", []byte("   \n")) != nil {
		t.Fatal("blank source scans nothing")
	}
	if !LeaScannable("page.htm") || !LeaScannable("comp.tsx") || !LeaScannable("tpl.gohtml") {
		t.Fatal("scannable formats misclassified")
	}
	if LeaScannable("main.py") {
		t.Fatal("python must not be scannable")
	}
}

// ── JSX / Go template topologies ────────────────────────────────────────────

const jsxFixture = `import React from "react";

export function Hero({ name }) {
	return <section className="hero"><h1>{name}</h1></section>;
}

function CardRow(props) {
	return <div className="row">{props.children}</div>;
}

export default function Page() {
	return (
		<main>
			<Hero name="Alex" />
			<CardRow />
		</main>
	);
}
`

func TestLeaStructuralScan_JSXComponents(t *testing.T) {
	rep := LeaStructuralScan("Page.tsx", []byte(jsxFixture))
	if rep == nil || rep.Format != "jsx" {
		t.Fatal("tsx target must route to the jsx scanner")
	}
	names := make([]string, 0, len(rep.Nodes))
	for _, n := range rep.Nodes {
		names = append(names, fmt.Sprintf("%s [%d–%d]", n.Tag, n.StartLine, n.EndLine))
	}
	for _, want := range []string{"Hero", "CardRow", "Page"} {
		found := false
		for _, n := range names {
			if strings.Contains(n, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("component %s missing from topology %v", want, names)
		}
	}
	if rep.LowConfidence {
		t.Fatal("valid TSX must not flag low confidence")
	}
}

const goTemplateFixture = `{{define "header"}}<nav>{{.Title}}</nav>{{end}}
{{define "body"}}
<main>
{{if false}}
	<p>This branch can never render.</p>
{{end}}
</main>
{{end}}
`

func TestLeaStructuralScan_GoTemplate(t *testing.T) {
	rep := LeaStructuralScan("page.gohtml", []byte(goTemplateFixture))
	if rep == nil || rep.Format != "go_template" {
		t.Fatal("gohtml target must route to the template scanner")
	}
	var defines int
	for _, n := range rep.Nodes {
		if strings.HasPrefix(n.Tag, "define:") {
			defines++
		}
	}
	if defines != 2 {
		t.Fatalf("define nodes = %d, want 2", defines)
	}
	var dead bool
	for _, f := range rep.Findings {
		if f.Kind == FindingDeadCodePath {
			dead = true
		}
	}
	if !dead {
		t.Fatal("{{if false}} branch must be reported as a dead code path")
	}
}

// ── findings ────────────────────────────────────────────────────────────────

func TestLeaStructuralScan_Findings(t *testing.T) {
	src := []byte(semanticHTMLFixture)
	rep := LeaStructuralScan("index.html", src)

	kinds := map[FindingKind][]StructuralFinding{}
	for _, f := range rep.Findings {
		kinds[f.Kind] = append(kinds[f.Kind], f)
	}

	// #orphan-panel is referenced nowhere → unused element.
	unused := kinds[FindingUnusedElement]
	if len(unused) != 1 || unused[0].Label != "#orphan-panel" {
		t.Fatalf("unused elements = %v, want exactly [#orphan-panel]", unused)
	}
	// The commented-out superseded hero is dead code.
	if len(kinds[FindingDeadCodePath]) != 1 {
		t.Fatalf("dead paths = %v, want the commented-out hero block", kinds[FindingDeadCodePath])
	}

	// Duplicate ids are flagged as duplicate selectors.
	dupSrc := []byte(`<html><body>
<div id="dup">one</div>
<a href="#dup">jump</a>
<div id="dup">two</div>
</body></html>
`)
	dupRep := LeaStructuralScan("dup.html", dupSrc)
	var dupFound bool
	for _, f := range dupRep.Findings {
		if f.Kind == FindingDuplicateSelector && f.Label == "#dup" {
			dupFound = true
		}
	}
	if !dupFound {
		t.Fatal("duplicate id must produce a duplicate_selector finding")
	}

	// Duplicate CSS rules inside <style> are flagged too.
	cssSrc := []byte(`<html><head>
<style>
.card { color: red; }
.row { display: flex; }
.card { color: blue; }
</style>
</head><body><p class="card">x</p></body></html>
`)
	cssRep := LeaStructuralScan("styled.html", cssSrc)
	var cssDup bool
	for _, f := range cssRep.Findings {
		if f.Kind == FindingDuplicateSelector && strings.HasPrefix(f.Label, ".card") {
			cssDup = true
		}
	}
	if !cssDup {
		t.Fatalf("duplicate .card rule must be flagged, got %v", cssRep.Findings)
	}
}

// An element whose id IS used (anchor + style hook) is never "unused".
func TestLeaStructuralScan_ReferencedElementsNotFlaggedUnused(t *testing.T) {
	src := []byte(semanticHTMLFixture)
	rep := LeaStructuralScan("index.html", src)
	for _, f := range rep.Findings {
		if f.Kind == FindingUnusedElement && (f.Label == "#hero" || f.Label == "#top-nav" || f.Label == "#site-footer") {
			t.Fatalf("referenced element %s wrongly flagged unused", f.Label)
		}
	}
	// And the reference edges exist for the used tokens.
	var heroRef bool
	for _, r := range rep.References {
		if r.Kind == "id" && r.Name == "hero" && len(r.UsedAt) > 0 {
			heroRef = true
		}
	}
	if !heroRef {
		t.Fatal("#hero anchor usage missing from active references")
	}
}

// ── semantic unit splitting ─────────────────────────────────────────────────

func TestSemanticUnits_TileTheDocument(t *testing.T) {
	src := []byte(semanticHTMLFixture)
	rep := LeaStructuralScan("index.html", src)
	if len(rep.Units) < minSemanticUnits {
		t.Fatalf("units = %d, want >= %d", len(rep.Units), minSemanticUnits)
	}
	next := 1
	for _, u := range rep.Units {
		if u.Region.StartLine != next {
			t.Fatalf("unit %q starts at %d, want %d", u.Label, u.Region.StartLine, next)
		}
		next = u.Region.EndLine + 1
	}
	if next != rep.TotalLines+1 {
		t.Fatalf("units end at %d, want full coverage of %d lines", next-1, rep.TotalLines)
	}
	first := strings.TrimSpace(rep.Units[0].Label)
	second := ""
	if len(rep.Units) > 1 {
		second = rep.Units[1].Label
	}
	if first != "(document header)" {
		t.Fatalf("first unit label = %q, want the document header zone", first)
	}
	if second != "<head> metadata" {
		t.Fatalf("second unit label = %q, want `<head> metadata`", second)
	}
	joined := ""
	for _, u := range rep.Units {
		joined += u.Label + "|"
	}
	for _, want := range []string{"hero", "work", "site-footer"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("unit labels %q missing identity %q", joined, want)
		}
	}
}

func TestDecomposeTarget_HTMLSemanticSplitting(t *testing.T) {
	src := []byte(semanticHTMLFixture)
	const maxOutput = 256 // small budget: the semantic units cannot all merge
	dag, err := DecomposeTarget("polish the portfolio page", "index.html", src, "digest-base", maxOutput)
	if err != nil {
		t.Fatalf("DecomposeTarget: %v", err)
	}
	requireValidDAG(t, dag, src)
	if dag.Kind != SplitSemantic {
		t.Fatalf("kind = %s, want %s", dag.Kind, SplitSemantic)
	}
	if dag.LowSemanticConfidence {
		t.Fatal("well-formed HTML must split semantically, not fall back")
	}
	if len(dag.SubTasks) < minSemanticUnits {
		t.Fatalf("sub-tasks = %d, want semantic units", len(dag.SubTasks))
	}
	descs := ""
	for _, st := range dag.SubTasks {
		descs += st.Description + "|"
	}
	for _, want := range []string{"<head> metadata", "hero"} {
		if !strings.Contains(descs, want) {
			t.Fatalf("sub-task descriptions %q missing %q", descs, want)
		}
	}
}

func TestDecomposeTarget_MalformedSyntaxFallsBackWithLowConfidence(t *testing.T) {
	src := []byte(malformedHTMLFixture + strings.Repeat("<p>filler row of text to grow the file past one budget window</p>\n", 40))
	const maxOutput = 1024
	dag, err := DecomposeTarget("repair the broken page", "broken.html", src, "digest-base", maxOutput)
	if err != nil {
		t.Fatalf("DecomposeTarget fallback: %v", err)
	}
	requireValidDAG(t, dag, src)
	if !dag.LowSemanticConfidence {
		t.Fatal("malformed parse must record LowSemanticConfidence=true")
	}
	if dag.Kind == SplitSemantic {
		t.Fatalf("fallback kind = %s, want the syntactic strategy", dag.Kind)
	}
}

// A JSX document decomposes into per-component semantic units.
func TestDecomposeTarget_JSXComponentUnits(t *testing.T) {
	src := []byte(jsxFixture)
	dag, err := DecomposeTarget("extract the card row", "Page.tsx", src, "digest-base", 4096)
	if err != nil {
		t.Fatalf("DecomposeTarget: %v", err)
	}
	requireValidDAG(t, dag, src)
	if dag.Kind != SplitSemantic {
		t.Fatalf("kind = %s, want %s", dag.Kind, SplitSemantic)
	}
	descs := ""
	for _, st := range dag.SubTasks {
		descs += st.Description + "|"
	}
	if !strings.Contains(descs, "Hero") || !strings.Contains(descs, "CardRow") {
		t.Fatalf("descriptions %q must name components", descs)
	}
}

// One giant section with many children refines along CHILD boundaries into
// budget-fitted pieces instead of collapsing to raw line windows.
func TestDecomposeTarget_OversizeUnitRefinesAlongChildNodes(t *testing.T) {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head><title>t</title></head>\n<body>\n")
	b.WriteString("<section id=\"giant\">\n")
	for i := 0; b.Len() < 40*1024; i++ {
		fmt.Fprintf(&b, "\t<article class=\"card\"><p>card body %d with enough words to weigh bytes.</p></article>\n", i)
	}
	b.WriteString("</section>\n</body>\n</html>\n")
	src := []byte(b.String())
	const maxOutput = 8192

	dag, err := DecomposeTarget("deduplicate the cards", "giant.html", src, "digest-base", maxOutput)
	if err != nil {
		t.Fatalf("DecomposeTarget: %v", err)
	}
	requireValidDAG(t, dag, src)
	if dag.Kind != SplitSemantic {
		t.Fatalf("kind = %s, want %s", dag.Kind, SplitSemantic)
	}
	// Refinement must have produced multiple units from the single section.
	if len(dag.SubTasks) < 2 {
		t.Fatalf("sub-tasks = %d, want the giant section refined into several units", len(dag.SubTasks))
	}
	// At most ONE unit may carry raw bounded-lines windows (the tail piece);
	// every structural piece keeps the semantic contract.
	bounded := 0
	for _, st := range dag.SubTasks {
		if st.Kind == SplitBoundedLines {
			bounded++
		}
	}
	if bounded > 1 {
		t.Fatalf("%d sub-tasks fell back to line windows — refinement failed", bounded)
	}
}

// DecomposeTarget keeps the fail-closed sentinels of Decompose.
func TestDecomposeTarget_FailClosedSentinels(t *testing.T) {
	if _, err := DecomposeTarget("o", "f.txt", []byte("hello"), "d", 1000); err == nil {
		t.Fatal("unscannable+undecomposable format must error")
	}
	if _, err := DecomposeTarget("o", "f.html", []byte("   \n\t"), "d", 1000); err == nil ||
		(err != nil && !strings.Contains(err.Error(), "empty source")) {
		t.Fatalf("err = %v, want empty-source refusal", err)
	}
	if _, err := DecomposeTarget("o", "f.html", []byte("<p>x</p>"), "d", 0); err == nil ||
		(err != nil && !strings.Contains(err.Error(), "no per-sub-task budget")) {
		t.Fatalf("err = %v, want zero-budget refusal", err)
	}
}

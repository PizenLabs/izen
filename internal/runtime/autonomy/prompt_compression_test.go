package autonomy

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── fixtures ────────────────────────────────────────────────────────────────

// compressionHTMLFixture renders a multi-section document large enough that
// dumping it raw into every sub-task prompt would dominate the token budget.
func compressionHTMLFixture(sections int) []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head><title>Portfolio</title></head>\n<body>\n")
	b.WriteString("<nav id=\"top-nav\"><a href=\"#hero\">Home</a></nav>\n")
	for i := 0; i < sections; i++ {
		fmt.Fprintf(&b, "<section id=\"panel-%d\">\n", i)
		for j := 0; j < 14; j++ {
			fmt.Fprintf(&b, "\t<article class=\"card\"><p>compression fixture card %d.%d body text padded for bytes.</p></article>\n", i, j)
		}
		b.WriteString("</section>\n")
	}
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String())
}

// scopeFixtureHero returns the hero-like sub-task used by the unit tests.
func scopeFixtureHero(totalLines int) planner.SubTask {
	return planner.SubTask{
		ID:     "st-2",
		Index:  2,
		Region: planner.Region{StartLine: 6, EndLine: totalLines / 2},
	}
}

// ── unit: the compressor itself ─────────────────────────────────────────────

func TestCompressedContextCarriesTopologyScopeAndEvidence(t *testing.T) {
	src := []byte(`<!DOCTYPE html>
<html>
<head>
	<title>Portfolio</title>
</head>
<body>
	<header id="top-nav">
		<a href="#hero">Home</a>
	</header>
	<section id="hero">
		<h1>Alex Josie</h1>
		<!-- <h1>old hero markup kept dead</h1> -->
		<p>Engineer.</p>
	</section>
	<div id="orphan-widget">
		unreferenced
	</div>
	<footer id="site-footer">
		<span>2026</span>
	</footer>
</body>
</html>
`)
	st := planner.SubTask{ID: "st-2", Region: planner.Region{StartLine: 10, EndLine: 16}}
	c := buildCompressedStructuralContext("index.html", src, st)
	if c == nil {
		t.Fatal("HTML target must produce a compressed context")
	}
	rendered := c.Render()
	for _, want := range []string{
		"[STRUCTURAL CONTEXT st-2",
		"raw file is intentionally NOT included",
		"Assigned scope: lines 10–16",
		"section#hero",
		"DOCUMENT SKELETON",
		"ASSIGNED SCOPE",
		"PARENT/SIBLING RELATIONS",
		"#hero used at",
		"TARGETED STRUCTURAL EVIDENCE",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("compressed context missing %q:\n%s", want, rendered)
		}
	}
	// Targeted evidence: the dead markup and the orphan widget overlap or sit
	// near this scope only when their regions do — the hero comment does.
	foundDead := false
	for _, e := range c.Evidence {
		if strings.Contains(e, string(planner.FindingDeadCodePath)) {
			foundDead = true
		}
	}
	if !foundDead {
		t.Errorf("scope evidence must include the commented-out hero markup:\n%s", rendered)
	}
	// The orphan widget lives OUTSIDE the scope: targeted means withheld.
	for _, e := range c.Evidence {
		if strings.Contains(e, "#orphan-widget") && !strings.Contains(e, "dead_code_path") {
			t.Errorf("out-of-scope finding leaked into the evidence:\n%s", e)
		}
	}
	if len(rendered) > MaxCompressedContextBytes {
		t.Fatalf("rendered context = %d bytes, ceiling %d", len(rendered), MaxCompressedContextBytes)
	}
}

func TestCompressedContextTokenFootprintIsSublinear(t *testing.T) {
	src := compressionHTMLFixture(120) // ~40KB document
	st := scopeFixtureHero(len(strings.Split(string(src), "\n")))
	c := buildCompressedStructuralContext("index.html", src, st)
	if c == nil {
		t.Fatal("compressed context missing")
	}
	ctxTokens := c.EstimateTokens()
	sourceTokens := len(src) / 4
	if ctxTokens == 0 || ctxTokens > MaxCompressedContextBytes/4 {
		t.Fatalf("context estimate %d tokens exceeds its own byte ceiling", ctxTokens)
	}
	if float64(ctxTokens) > float64(sourceTokens)*0.05 {
		t.Fatalf("token footprint not compressed: context=%d source=%d tokens (>5%%)", ctxTokens, sourceTokens)
	}
}

func TestCompressedContextDegradesGracefully(t *testing.T) {
	if c := buildCompressedStructuralContext("notes.txt", []byte("just text"), planner.SubTask{ID: "st-1"}); c != nil {
		t.Fatal("unscannable formats must yield nil context")
	}
	if c := buildCompressedStructuralContext("x.html", nil, planner.SubTask{ID: "st-1"}); c != nil {
		t.Fatal("empty source must yield nil context")
	}
	// A nil context renders empty and leaves the prompt unchanged.
	var nilCtx *CompressedStructuralContext
	if got := nilCtx.Render(); got != "" {
		t.Fatalf("nil render = %q, want empty", got)
	}
}

// The skeleton elides instead of exploding on pathological documents.
func TestCompressedSkeletonElidesOnHugeDocuments(t *testing.T) {
	src := compressionHTMLFixture(200)
	st := scopeFixtureHero(len(strings.Split(string(src), "\n")))
	c := buildCompressedStructuralContext("index.html", src, st)
	if len(c.Skeleton) > maxSkeletonLines {
		t.Fatalf("skeleton = %d lines, cap %d", len(c.Skeleton), maxSkeletonLines)
	}
	joined := strings.Join(c.Skeleton, "\n")
	if c.Truncated && !strings.Contains(joined, "elided") {
		t.Fatalf("truncated skeleton must carry an elision marker:\n%s", joined)
	}
}

// ── unit: the prompt carries topology, never the raw dump ───────────────────

func TestSubTaskPromptCarriesCompressedTopologyNotRawSource(t *testing.T) {
	src := compressionHTMLFixture(80)
	dag := &planner.ExecutionDAG{
		Objective: "restyle the cards", Target: "index.html",
		Kind: planner.SplitSemantic, MaxOutputTokens: 4096,
	}
	lines := strings.Split(strings.TrimSuffix(string(src), "\n"), "\n")
	st := planner.SubTask{ID: "st-3", Index: 3, Kind: planner.SplitSemantic,
		Description: "<section#panel-5> panel 5", Region: planner.Region{StartLine: 20, EndLine: 60}}
	compressed := buildCompressedStructuralContext(dag.Target, src, st)
	prompt := subTaskPrompt("restyle the cards @index.html", dag, st, 3, 9, compressed)

	filler := "compression fixture card"
	rawCount := strings.Count(string(src), filler)
	promptCount := strings.Count(prompt, filler)
	if promptCount >= rawCount/4 {
		t.Fatalf("prompt echoes %d/%d filler lines — raw-source dump suspected", promptCount, rawCount)
	}
	for _, want := range []string{"[STRUCTURAL CONTEXT st-3", "DOCUMENT SKELETON", "<section#panel-5>"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if len(prompt) > 8192 {
		t.Fatalf("prompt = %d bytes for a %d-byte file — not compressed", len(prompt), len(src))
	}
	_ = lines
}

// ── integration: approved DAG runs with focused windows + compressed prompts

// TestDriver_DecompositionRunsRegionFocusedWithCompressedContext drives a
// semantic HTML decomposition through approval and asserts on the REAL wire
// contract every sub-task invocation received:
//
//  1. the prompt carries the compressed structural topology (never the raw
//     file dump);
//  2. the bounded-patch copyable window lies INSIDE the unit's assigned
//     region — a unit can neither see nor anchor on another unit's lines;
//  3. every unit lands and the plan completes atomically.
func TestDriver_DecompositionRunsRegionFocusedWithCompressedContext(t *testing.T) {
	root := t.TempDir()
	src := compressionHTMLFixture(30)
	writeTarget(t, root, "index.html", string(src))

	p := &dagProvider{root: root, target: "index.html"}
	bus := events.NewBus(events.DefaultBufferSize)
	x := testExecutor(t, root, p, bus)
	driver := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	term, err := driver.Run(context.Background(), "restyle every card @index.html")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	dag := driver.Proposal()
	if dag == nil {
		t.Fatal("no DECOMPOSITION_PROPOSAL staged")
	}
	if dag.Kind != planner.SplitSemantic {
		t.Fatalf("plan kind = %s, want %s", dag.Kind, planner.SplitSemantic)
	}
	if term != nil {
		t.Fatalf("run terminated (%+v) instead of parking", term)
	}

	before := readTarget(t, root, "index.html")
	term, err = driver.ResumeApproveProposal(context.Background())
	if err != nil {
		t.Fatalf("ResumeApproveProposal: %v", err)
	}
	if term == nil || term.State.String() == "" {
		t.Fatalf("no termination returned: %+v", term)
	}
	if driver.Plan().Status != planner.DagExecutionCompleted {
		t.Fatalf("plan status = %s (%s), want %s", driver.Plan().Status, driver.Plan().FailureReason, planner.DagExecutionCompleted)
	}
	if readTarget(t, root, "index.html") == before {
		t.Fatal("approved DAG mutated nothing")
	}

	// ── wire-contract assertions over EVERY recorded invocation ──────────
	prompts := p.recordedPrompts()
	if len(prompts) < len(dag.SubTasks) {
		t.Fatalf("recorded %d invocations for %d sub-tasks", len(prompts), len(dag.SubTasks))
	}
	filler := "compression fixture card"
	sawFocusedWindow := false
	for _, prompt := range prompts {
		if !strings.Contains(prompt, "STRUCTURAL CONTEXT st-") {
			t.Errorf("invocation prompt missing compressed structural context:\n%.400s", prompt)
		}
		if strings.Count(prompt, filler) > 40 {
			t.Errorf("prompt echoes %d filler lines — looks like a raw-source dump",
				strings.Count(prompt, filler))
		}
		// The copyable window must stay inside the assigned region.
		stID := stRe.FindStringSubmatch(prompt)
		win := windowRe.FindStringSubmatch(prompt)
		if stID == nil || win == nil {
			continue
		}
		taskSt := dag.Task(stID[1])
		if taskSt == nil {
			continue
		}
		var ws, we int
		if _, err := fmt.Sscanf(win[1]+" "+win[2], "%d %d", &ws, &we); err != nil {
			t.Fatalf("window parse: %v", err)
		}
		if ws < taskSt.Region.StartLine || we > taskSt.Region.EndLine {
			t.Errorf("%s saw window lines %d-%d outside its assigned region %s",
				stID[1], ws, we, taskSt.Region)
		}
		if we-ws+1 <= taskSt.Region.Lines() {
			sawFocusedWindow = true
		}
	}
	if !sawFocusedWindow {
		t.Error("no invocation carried a region-focused context window")
	}
}

// windowRe extracts the executor's copyable-context window declaration.
var windowRe = regexp.MustCompile(`CONTEXT WINDOW — \S+ lines (\d+)-(\d+) of`)

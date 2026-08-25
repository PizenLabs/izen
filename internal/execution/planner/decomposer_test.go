package planner

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/execution"
)

// ── fixtures ────────────────────────────────────────────────────────────────

// goFixture renders a deterministic Go source of at least minBytes: a package
// clause plus numbered top-level types and methods, each with its own doc
// comment so backward extension is exercised.
func goFixture(minBytes int) []byte {
	var b strings.Builder
	b.WriteString("// Package generated is a fixture for decomposition tests.\n")
	b.WriteString("package generated\n\n")
	for i := 0; len(b.String()) < minBytes; i++ {
		fmt.Fprintf(&b, "// Handler%d processes request kind %d.\ntype Handler%d struct {\n\tField%d string\n\tCount%d int\n}\n\n", i, i, i, i, i)
		fmt.Fprintf(&b, "// NewHandler%d builds the %d-th handler.\nfunc NewHandler%d(seed string) *Handler%d {\n", i, i, i, i)
		fmt.Fprintf(&b, "\tif seed == \"\" {\n\t\tpanic(\"seed required\")\n\t}\n")
		fmt.Fprintf(&b, "\treturn &Handler%d{Field%d: seed, Count%d: %d}\n}\n\n", i, i, i, i*7+1)
	}
	return []byte(b.String())
}

// htmlFixture renders a deterministic HTML document of at least minBytes:
// doctype + head + top-level sections with nested children.
func htmlFixture(minBytes int) []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n<head><title>fixture</title></head>\n<body>\n")
	for i := 0; len(b.String()) < minBytes; i++ {
		fmt.Fprintf(&b, "<section id=\"panel-%d\">\n\t<h2>Panel %d</h2>\n", i, i)
		for j := 0; j < 4; j++ {
			fmt.Fprintf(&b, "\t<article class=\"card\"><p>Card %d.%d body text.</p></article>\n", i, j)
		}
		b.WriteString("</section>\n")
	}
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String())
}

const mdFixture = `# Title

Intro paragraph before any code.

` + "```go" + `
# this heading inside a fence must NOT split
func main() {}
` + "```" + `

## Usage

Run it with arguments.

## Installation

Steps to install.

### Advanced

Nested heading under installation.

## FAQ

Questions.
`

const yamlFixture = `apiVersion: v1
kind: Config
metadata:
  name: fixture
---
apiVersion: v2
kind: Other
spec:
  replicas: 3
secrets:
  - one
  - two
`

const tomlFixture = `[server]
host = "localhost"
port = 8080

[database]
url = "postgres://localhost/app"
pool = 10

[[workers]]
name = "w1"

[logging]
level = "debug"
`

// ── shared assertions ───────────────────────────────────────────────────────

// requireValidDAG asserts every staged-plan invariant including the strict
// 0.7 budget rule and that EVERY sub-task passes the Boundary-2 preflight
// guard individually (the requirement that makes a plan executable).
func requireValidDAG(t *testing.T, dag *ExecutionDAG, source []byte) {
	t.Helper()
	if err := dag.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	budget := SubTaskBudget(dag.MaxOutputTokens)
	next := 1
	for _, st := range dag.SubTasks {
		if st.EstimatedTokens > budget {
			t.Fatalf("%s estimates %d tokens, exceeds the strict ceiling %d (max_output=%d × 0.7)",
				st.ID, st.EstimatedTokens, budget, dag.MaxOutputTokens)
		}
		if !PreflightFeasible(source, st.Region, dag.MaxOutputTokens) {
			t.Fatalf("%s region %s does not pass EvaluatePreflight individually (max_output=%d)",
				st.ID, st.Region, dag.MaxOutputTokens)
		}
		if st.Region.StartLine != next {
			t.Fatalf("%s starts at line %d, want contiguous %d", st.ID, st.Region.StartLine, next)
		}
		next = st.Region.EndLine + 1
	}
	if next != len(splitKeepNewline(source))+1 {
		t.Fatalf("regions end at line %d, want full coverage of %d lines", next-1, len(splitKeepNewline(source)))
	}
}

// preflightInfeasibleWholeFile proves the fixture genuinely trips Boundary 2
// as an unbounded rewrite under the same budget — the precondition for
// decomposition to be the correct expansion.
func preflightInfeasibleWholeFile(t *testing.T, source []byte, maxOutput int) {
	t.Helper()
	v := execution.EvaluatePreflight(execution.PreflightRequest{
		ArtifactBounded: false,
		TargetBytes:     len(source),
		MaxOutputTokens: maxOutput,
	})
	if v.Feasible {
		t.Fatalf("fixture of %d bytes should be preflight-infeasible under max_output=%d (estimated=%d)",
			len(source), maxOutput, v.EstimatedTokens)
	}
}

// ── 20KB Go source ──────────────────────────────────────────────────────────

func TestDecompose_GoSource20KB(t *testing.T) {
	source := goFixture(20 * 1024)
	const maxOutput = 8192
	preflightInfeasibleWholeFile(t, source, maxOutput)

	dag, err := Decompose("refactor the whole file", "big.go", source, "digest-base", maxOutput)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	requireValidDAG(t, dag, source)

	if dag.Kind != SplitAST {
		t.Fatalf("kind = %s, want %s", dag.Kind, SplitAST)
	}
	if len(dag.SubTasks) < 2 {
		t.Fatalf("sub-tasks = %d, want multiple structural units", len(dag.SubTasks))
	}
	if !strings.Contains(dag.SubTasks[0].Description, "(preamble)") {
		t.Fatalf("first sub-task %q should include the package/import preamble", dag.SubTasks[0].Description)
	}
	if !strings.Contains(dag.SubTasks[len(dag.SubTasks)-1].Description, "NewHandler") {
		t.Fatalf("last sub-task description %q should name its declaration", dag.SubTasks[len(dag.SubTasks)-1].Description)
	}
	// Dependency chain: strictly backwards.
	for i, st := range dag.TopologicalOrder() {
		want := ""
		if i > 0 {
			want = fmt.Sprintf("st-%d", i)
		}
		if i == 0 && len(st.Dependencies) != 0 {
			t.Fatalf("st-1 must have no dependencies, got %v", st.Dependencies)
		}
		if i > 0 && (len(st.Dependencies) != 1 || st.Dependencies[0] != want) {
			t.Fatalf("%s dependencies = %v, want [%s]", st.ID, st.Dependencies, want)
		}
	}
}

// A declaration whose body alone busts the budget no longer fails closed: the
// secondary fine-grained line-slicing fallback cuts the indivisible function
// into contiguous budget-bounded line windows bound to explicit intervals.
func TestDecompose_GoIndivisibleFunctionFallsBackToLineSlices(t *testing.T) {
	var b strings.Builder
	b.WriteString("package x\n\n")
	b.WriteString("// Giant holds one enormous function.\n")
	b.WriteString("func Giant() {\n")
	for i := 0; len(b.String()) < 24*1024; i++ {
		fmt.Fprintf(&b, "\tstep%d(%d)\n", i%50, i)
	}
	b.WriteString("}\n")
	source := []byte(b.String())
	const maxOutput = 4096

	dag, err := Decompose("rewrite giant func", "giant.go", source, "digest-base", maxOutput)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	requireValidDAG(t, dag, source)
	if len(dag.SubTasks) < 2 {
		t.Fatalf("sub-tasks = %d, want the giant function line-sliced into multiple units", len(dag.SubTasks))
	}
	// The structural preamble stays ast_structural; every window cut out of
	// the oversized function carries the bounded-lines contract.
	if dag.SubTasks[0].Kind != SplitAST {
		t.Fatalf("first sub-task kind = %s, want %s", dag.SubTasks[0].Kind, SplitAST)
	}
	for _, st := range dag.SubTasks[1:] {
		if st.Kind != SplitBoundedLines {
			t.Fatalf("%s kind = %s, want %s", st.ID, st.Kind, SplitBoundedLines)
		}
	}
}

// ── 10KB HTML document ──────────────────────────────────────────────────────

func TestDecompose_HTML10KB(t *testing.T) {
	source := htmlFixture(10 * 1024)
	const maxOutput = 4096
	preflightInfeasibleWholeFile(t, source, maxOutput)

	dag, err := Decompose("restyle the page", "index.html", source, "digest-base", maxOutput)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	requireValidDAG(t, dag, source)

	if dag.Kind != SplitBlock {
		t.Fatalf("kind = %s, want %s", dag.Kind, SplitBlock)
	}
	if len(dag.SubTasks) < 3 {
		t.Fatalf("sub-tasks = %d, want several top-level blocks", len(dag.SubTasks))
	}
	// The wrapper lines attach forward into the first section's group; every
	// section label must be an element identity, never raw content.
	for _, st := range dag.SubTasks {
		if !strings.HasPrefix(strings.TrimSpace(st.Description), "<") && !strings.Contains(st.Description, "more") {
			t.Fatalf("section description %q should start with a tag identity", st.Description)
		}
	}
}

// Script/style bodies are opaque: a "<div>" inside JavaScript never splits.
func TestSplit_HTMLScriptRawTextIsOpaque(t *testing.T) {
	src := []byte(`<html>
<body>
<script>
var x = "<section>";
if (x) { console.log(x); }
</script>
<section id="real">content</section>
<p>tail</p>
</body>
</html>
`)
	starts := htmlTopLevelStarts(splitKeepNewline(src))
	if len(starts) != 3 {
		t.Fatalf("top-level starts = %v, want script, section and p", starts)
	}
	sections, err := BlockDecomposer{}.Split("page.html", src)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	joined := ""
	for _, s := range sections {
		joined += string(SliceLines(src, s.Region))
	}
	if !strings.Contains(joined, "console.log") || !strings.Contains(joined, "</html>") {
		t.Fatal("sections must cover the entire document including raw scripts")
	}
}

// ── Rust / TS structural splitting ──────────────────────────────────────────

func TestSplit_RustDeclarations(t *testing.T) {
	src := []byte(`//! Crate docs.
use std::collections::HashMap;

/// A documented struct.
#[derive(Debug, Clone)]
pub struct Engine {
    size: usize,
}

pub impl Engine {
    pub fn new() -> Self { Self { size: 0 } }
}

trait Runnable {
    fn run(&self);
}

enum Mode { Fast, Slow }

mod tests {
    use super::*;
}
`)
	sections, err := ASTDecomposer{}.Split("engine.rs", src)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	labels := make([]string, 0, len(sections))
	for _, s := range sections {
		labels = append(labels, strings.TrimSpace(s.Label))
	}
	joined := strings.Join(labels, "|")
	for _, want := range []string{"use std::collections::HashMap;", "pub struct Engine", "trait Runnable", "mod tests"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing section %q in %v", want, labels)
		}
	}
	// The doc comment + attribute stay glued to their struct.
	firstStruct := sections[1]
	body := string(SliceLines(src, firstStruct.Region))
	if !strings.Contains(body, "#[derive(Debug, Clone)]") || !strings.Contains(body, "pub struct Engine") {
		t.Fatalf("struct section lost its attribute run:\n%s", body)
	}
}

func TestSplit_TypeScriptDeclarations(t *testing.T) {
	src := []byte(`import { Client } from "./client";

export interface Options {
  retries: number;
}

/** Documented factory. */
export function makeClient(o: Options): Client {
  return new Client(o);
}

export class Service {
  private opts: Options;
  constructor(o: Options) {
    this.opts = o;
  }
}

const DEFAULTS: Options = { retries: 3 };
`)
	sections, err := ASTDecomposer{}.Split("service.ts", src)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	labels := make([]string, 0, len(sections))
	for _, s := range sections {
		labels = append(labels, strings.TrimSpace(s.Label))
	}
	joined := strings.Join(labels, "|")
	for _, want := range []string{"export interface Options", "export function makeClient(o: Options): Client", "export class Service", "const DEFAULTS: Options = { retries: 3 };"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing section %q in %v", want, labels)
		}
	}
}

// Braces inside strings/comments never create false boundaries.
func TestScanState_BracesInStringsAndCommentsDoNotNest(t *testing.T) {
	src := []byte(`package p

var tpl = ` + "`" + `
func fake() { /* not real */ }
` + "`" + `

// } stray brace in comment { 
type Real struct{ N int }

func (r *Real) M() int { return r.N }
`)
	sections, err := ASTDecomposer{}.Split("p.go", src)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(sections) != 4 {
		t.Fatalf("sections = %d (%v), want preamble+var+type+method", len(sections), sections)
	}
	last := sections[len(sections)-1]
	if !strings.Contains(last.Label, "func (r *Real) M()") {
		t.Fatalf("last label %q, want the method", last.Label)
	}
}

// ── markdown / config block splitting ───────────────────────────────────────

func TestSplit_MarkdownHeadingsRespectFences(t *testing.T) {
	sections, err := BlockDecomposer{}.Split("guide.md", []byte(mdFixture))
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	labels := make([]string, 0, len(sections))
	for _, s := range sections {
		labels = append(labels, strings.TrimSpace(s.Label))
		body := string(SliceLines([]byte(mdFixture), s.Region))
		if strings.Contains(body, "heading inside a fence") &&
			!strings.Contains(body, "```") {
			t.Fatal("fenced code was separated from its heading context or split internally")
		}
	}
	joined := strings.Join(labels, "|")
	for _, want := range []string{"# Title", "## Usage", "## Installation", "### Advanced", "## FAQ"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing heading %q in %v", want, labels)
		}
	}
}

func TestSplit_YAMLTopLevelKeysAndSeparators(t *testing.T) {
	sections, err := BlockDecomposer{}.Split("cfg.yaml", []byte(yamlFixture))
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(sections) < 2 {
		t.Fatalf("sections = %d, want the two YAML documents", len(sections))
	}
	first := string(SliceLines([]byte(yamlFixture), sections[0].Region))
	if !strings.Contains(first, "apiVersion: v1") || strings.Contains(first, "apiVersion: v2") {
		t.Fatalf("first YAML document leaked across the --- separator:\n%s", first)
	}
}

func TestSplit_TOMLSections(t *testing.T) {
	sections, err := BlockDecomposer{}.Split("app.toml", []byte(tomlFixture))
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(sections) < 4 {
		t.Fatalf("sections = %d, want server/database/workers/logging", len(sections))
	}
	if got := strings.TrimSpace(sections[0].Label); got != "[server]" {
		t.Fatalf("first section = %q, want [server]", got)
	}
}

func TestSplit_JSONRootMembers(t *testing.T) {
	src := []byte(`{
  "name": "app",
  "scripts": {
    "build": "go build ./..."
  },
  "deps": ["a", "b"]
}
`)
	sections, err := BlockDecomposer{}.Split("manifest.json", src)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(sections) < 3 {
		t.Fatalf("sections = %d, want name/scripts/deps members", len(sections))
	}
	all := ""
	for _, s := range sections {
		all += string(SliceLines(src, s.Region))
	}
	if !strings.Contains(all, "\"deps\"") {
		t.Fatal("coverage lost the last member")
	}
}

// ── budget rule & validation ────────────────────────────────────────────────

func TestSubTaskBudget_IsSeventyPercent(t *testing.T) {
	cases := []struct{ max, want int }{
		{1000, 700},
		{3072, 2150}, // floor(2150.4)
		{5, 3},
		{0, 0},
		{-10, 0},
	}
	for _, c := range cases {
		if got := SubTaskBudget(c.max); got != c.want {
			t.Fatalf("SubTaskBudget(%d) = %d, want %d", c.max, got, c.want)
		}
	}
}

func TestAddTask_RejectsOverBudgetEstimate(t *testing.T) {
	dag := NewExecutionDAG("obj", "f.go", SplitAST, "digest", 1000)
	err := dag.AddTask(SubTask{EstimatedTokens: 701})
	if err == nil || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("err = %v, want strict-ceiling rejection", err)
	}
	if len(dag.SubTasks) != 0 {
		t.Fatal("refused sub-task must not join the DAG")
	}
	// Exactly at the ceiling passes (<= rule).
	if err := dag.AddTask(SubTask{EstimatedTokens: 700}); err != nil {
		t.Fatalf("boundary estimate rejected: %v", err)
	}
}

func TestValidate_RejectsBrokenPlans(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if err := (*ExecutionDAG)(nil).Validate(); err == nil {
			t.Fatal("nil DAG must fail validation")
		}
		if err := (&ExecutionDAG{}).Validate(); err == nil {
			t.Fatal("empty DAG must fail validation")
		}
	})
	t.Run("duplicate id", func(t *testing.T) {
		d := &ExecutionDAG{Target: "f.go", MaxOutputTokens: 1000}
		d.SubTasks = []SubTask{
			{ID: "st-1", Index: 1, Target: "f.go", EstimatedTokens: 10, Region: Region{1, 2}},
			{ID: "st-1", Index: 2, Target: "f.go", EstimatedTokens: 10, Region: Region{3, 4}},
		}
		if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("err = %v, want duplicate-id rejection", err)
		}
	})
	t.Run("forward dependency", func(t *testing.T) {
		d := &ExecutionDAG{Target: "f.go", MaxOutputTokens: 1000}
		d.SubTasks = []SubTask{
			{ID: "st-1", Index: 1, Target: "f.go", EstimatedTokens: 10, Region: Region{1, 2}, Dependencies: []string{"st-2"}},
			{ID: "st-2", Index: 2, Target: "f.go", EstimatedTokens: 10, Region: Region{3, 4}},
		}
		if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "earlier") {
			t.Fatalf("err = %v, want forward-dependency rejection", err)
		}
	})
	t.Run("over ceiling", func(t *testing.T) {
		d := &ExecutionDAG{Target: "f.go", MaxOutputTokens: 1000}
		d.SubTasks = []SubTask{{ID: "st-1", Index: 1, Target: "f.go", EstimatedTokens: 900, Region: Region{1, 2}}}
		if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "strict ceiling") {
			t.Fatalf("err = %v, want ceiling rejection", err)
		}
	})
	t.Run("gap in coverage", func(t *testing.T) {
		d := &ExecutionDAG{Target: "f.go", MaxOutputTokens: 100000}
		d.SubTasks = []SubTask{
			{ID: "st-1", Index: 1, Target: "f.go", EstimatedTokens: 10, Region: Region{1, 2}},
			{ID: "st-2", Index: 2, Target: "f.go", EstimatedTokens: 10, Region: Region{5, 6}},
		}
		if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "contiguous") {
			t.Fatalf("err = %v, want coverage-gap rejection", err)
		}
	})
}

// ── registry & fail-closed sentinels ────────────────────────────────────────

func TestForTarget_RegistryCoverage(t *testing.T) {
	astTargets := []string{"a.go", "b.rs", "c.ts", "d.tsx", "e.mts"}
	blockTargets := []string{"f.html", "g.htm", "h.md", "i.markdown",
		"j.json", "k.yaml", "l.yml", "m.toml", "n.ini", "o.cfg", "p.conf", "q.env"}
	for _, tgt := range astTargets {
		d := ForTarget(tgt)
		if d == nil {
			t.Fatalf("%s has no decomposer", tgt)
		}
		if _, ok := d.(ASTDecomposer); !ok {
			t.Fatalf("%s resolved to %T, want ASTDecomposer", tgt, d)
		}
	}
	for _, tgt := range blockTargets {
		d := ForTarget(tgt)
		if d == nil {
			t.Fatalf("%s has no decomposer", tgt)
		}
		if _, ok := d.(BlockDecomposer); !ok {
			t.Fatalf("%s resolved to %T, want BlockDecomposer", tgt, d)
		}
	}
	for _, tgt := range []string{"x.txt", "y.py", "z.rb", ""} {
		if Decomposable(tgt) {
			t.Fatalf("%q must not be decomposable", tgt)
		}
	}
}

func TestDecompose_FailClosedSentinels(t *testing.T) {
	if _, err := Decompose("o", "f.txt", []byte("hello"), "d", 1000); !errors.Is(err, ErrNoDecomposer) {
		t.Fatalf("err = %v, want ErrNoDecomposer", err)
	}
	if _, err := Decompose("o", "f.go", []byte("   \n\t"), "d", 1000); !errors.Is(err, ErrEmptySource) {
		t.Fatalf("err = %v, want ErrEmptySource", err)
	}
	if _, err := Decompose("o", "f.go", []byte("package x"), "d", 0); !errors.Is(err, ErrNotDecomposable) {
		t.Fatalf("err = %v, want zero-budget ErrNotDecomposable", err)
	}
}

// SliceLines round-trips regions losslessly against bytes.Split semantics.
func TestSliceLines_RoundTrip(t *testing.T) {
	src := []byte("alpha\nbeta\ngamma\n")
	cases := []struct {
		r    Region
		want string
	}{
		{Region{1, 1}, "alpha\n"},
		{Region{2, 3}, "beta\ngamma\n"},
		{Region{1, 3}, "alpha\nbeta\ngamma\n"},
		{Region{3, 9}, "gamma\n"}, // clamped
	}
	for _, c := range cases {
		if got := string(SliceLines(src, c.r)); got != c.want {
			t.Fatalf("SliceLines(%v) = %q, want %q", c.r, got, c.want)
		}
	}
}

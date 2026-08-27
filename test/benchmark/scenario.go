package benchmark

import (
	"strings"
)

// ── Standard refactor scenarios ─────────────────────────────────────────────
//
// Every scenario is a self-contained HTML/JSX document plus the objective a
// model must realize through the DAG pipeline. Expectations are machine-
// checkable: MustContain / MustNotContain tokens on the FINAL mutated source,
// and they deliberately encode SEMANTIC success (the consumer AND its
// definition move together), not merely "some diff landed".

// Scenario is one benchmarked refactor task.
type Scenario struct {
	// Name is the stable scenario identity ("html/remove-dead-css").
	Name string
	// Target is the workspace-relative file (must be decomposable:
	// .html/.htm/.jsx/.tsx/.gohtml).
	Target string
	// Source is the full initial file content.
	Source string
	// Objective is the user prompt driving the DAG.
	Objective string
	// MaxOutputTokens is the per-invocation ceiling the planner stages
	// under (mirrors Boundary-2 accounting; 0 selects the default).
	MaxOutputTokens int
	// Expectations scores semantic mutation accuracy.
	Expect Expectations
}

// Expectations is the oracle of one scenario.
type Expectations struct {
	// MustContain: every token must appear in the final source.
	MustContain []string
	// MustNotContain: no token may remain in the final source.
	MustNotContain []string
}

// Score computes semantic mutation accuracy in [0,1]: matched required
// tokens minus surviving forbidden ones, over the total assertion count.
func (e Expectations) Score(final string) float64 {
	total := len(e.MustContain) + len(e.MustNotContain)
	if total == 0 {
		return 1
	}
	hit := 0
	for _, tok := range e.MustContain {
		if strings.Contains(final, tok) {
			hit++
		}
	}
	for _, tok := range e.MustNotContain {
		if !strings.Contains(final, tok) {
			hit++
		}
	}
	return float64(hit) / float64(total)
}

const defaultBenchMaxOutputTokens = 1024

// forcedDecompMaxOutputTokens is deliberately tight so every standard
// fixture decomposes into MULTIPLE sub-tasks — the suite benchmarks DAG
// execution (cross-window coordination, retry behavior), not single-shot
// rewrites. 160 tokens ⇒ a 112-token per-sub-task ceiling under the strict
// 0.7 factor, i.e. ~150-byte windows.
const forcedDecompMaxOutputTokens = 160

// StandardScenarios returns the canonical HTML/JSX refactor suite.
func StandardScenarios() []Scenario {
	return []Scenario{
		htmlRemoveDeadCSS(),
		htmlRenameComponent(),
		htmlAddAnchorTarget(),
		jsxRenameComponent(),
	}
}

// htmlRemoveDeadCSS: the objective asks for ONE dead rule to go. A correct
// run deletes only .hero — never touching the live .card pair.
func htmlRemoveDeadCSS() Scenario {
	return Scenario{
		Name:   "html/remove-dead-css",
		Target: "index.html",
		Source: `<!doctype html>
<html>
<head>
<style>
  .card { color: red }
  .hero { font-size: 2rem }
</style>
</head>
<body>
<section id="top">
<div class="card">a</div>
<a href="#top">up</a>
</section>
<section class="panel">
<p>content</p>
</section>
</body>
</html>
`,
		Objective:       `remove the .hero rule from the stylesheet @index.html`,
		MaxOutputTokens: forcedDecompMaxOutputTokens,
		Expect: Expectations{
			MustContain:    []string{".card {", `class="card"`, `href="#top"`},
			MustNotContain: []string{".hero"},
		},
	}
}

// htmlRenameComponent: a coordinated rename across TWO regions — the CSS
// consumer lives in <style> while the definitions sit in later sections.
// Models that rename only one side fail accuracy AND the global verifier.
func htmlRenameComponent() Scenario {
	return Scenario{
		Name:   "html/rename-component",
		Target: "index.html",
		Source: `<!doctype html>
<html>
<head>
<style>
  .pill { border-radius: 9999px }
</style>
</head>
<body>
<header>
<span class="pill">beta</span>
</header>
<main>
<span class="pill">v2</span>
</main>
<footer>
<span class="pill">oss</span>
</footer>
</body>
</html>
`,
		Objective:       `rename every .pill to .chip everywhere @index.html`,
		MaxOutputTokens: forcedDecompMaxOutputTokens,
		Expect: Expectations{
			MustContain:    []string{".chip {", `class="chip"`},
			MustNotContain: []string{"pill"},
		},
	}
}

// htmlAddAnchorTarget: a CREATION scenario — a new id plus an anchor that
// references it. Introduces fresh identities; the verifier must stay green.
func htmlAddAnchorTarget() Scenario {
	return Scenario{
		Name:   "html/add-anchor-target",
		Target: "contact.html",
		Source: `<!doctype html>
<html>
<head><title>Contact</title></head>
<body>
<nav>
<a href="#form">jump</a>
</nav>
<main>
<h2>Reach us</h2>
<p>Prefer email.</p>
</main>
</body>
</html>
`,
		Objective:       `wrap the paragraph in a form section with id="form" @contact.html`,
		MaxOutputTokens: forcedDecompMaxOutputTokens,
		Expect: Expectations{
			MustContain: []string{`id="form"`, `href="#form"`},
		},
	}
}

// jsxRenameComponent: component-level refactor over the JSX topology
// scanner. The DAG decomposes along declaration boundaries; accuracy demands
// BOTH call sites follow the definition rename.
func jsxRenameComponent() Scenario {
	return Scenario{
		Name:   "jsx/rename-component",
		Target: "App.jsx",
		Source: `import React from "react";
function Badge({ text }) {
return <span className="badge">{text}</span>;
}
function Toolbar() {
return (
<div>
<Badge text="save" />
<Badge text="cancel" />
</div>
);
}
export default Toolbar;
`,
		Objective:       `rename every Badge to Chip @App.jsx`,
		MaxOutputTokens: forcedDecompMaxOutputTokens,
		Expect: Expectations{
			MustContain:    []string{"function Chip", "<Chip text="},
			MustNotContain: []string{"Badge"},
		},
	}
}

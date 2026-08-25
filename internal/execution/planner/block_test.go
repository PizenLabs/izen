package planner

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ── repro fixtures ──────────────────────────────────────────────────────────

// htmlMonolithFixture renders the exact failure geometry from the incident
// report: ONE contiguous top-level DOM section (<section id="monolith">)
// spanning 78 lines whose estimated generation cost is ~4188 tokens under the
// canonical accounting (bytes/4 × FullRewriteTokenMultiplier). With
// max_output = 2048 the strict sub-task ceiling is 1433 tokens
// (2048 × 0.7), so the single section used to fail closed with
// ErrNotDecomposable before the FallbackLineSlicer existed.
//
// Byte budget: 5585 body bytes ⇒ floor(5585/4)=1396 target tokens ×3 = 4188.
func htmlMonolithFixture() []byte {
	const (
		totalLines = 78
		totalBytes = 5585 // ⇒ EstimateRegionTokens == 4188 exactly
	)
	lines := make([]string, totalLines)
	lines[0] = `<section id="monolith">`
	for i := 1; i < totalLines-1; i++ {
		lines[i] = fmt.Sprintf(`<div class="r%d">x</div>`, i)
	}
	lines[totalLines-1] = `</section>`

	// Distribute deterministic text-node padding across the inner lines so
	// the file lands exactly on the repro byte geometry while every single
	// line stays far below the ceiling individually.
	size := 0
	for _, l := range lines {
		size += len(l) + 1
	}
	pad := totalBytes - size
	if pad < 0 {
		panic("htmlMonolithFixture: base template exceeds the repro byte geometry")
	}
	middle := totalLines - 2
	per, extra := pad/middle, pad%middle
	for i := 1; i <= middle; i++ {
		n := per
		if extra > 0 {
			n++
			extra--
		}
		if n > 0 {
			lines[i] += strings.Repeat("x", n)
		}
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// minifiedHTMLFixture renders the pathological control: ONE line of HTML so
// large that even the line-slicing fallback cannot fit it under the ceiling.
func minifiedHTMLFixture(fill int) []byte {
	return []byte(`<section>` + strings.Repeat("x", fill) + `</section>` + "\n")
}

// ── the repro: single oversize DOM section decomposes instead of failing ───

func TestDecomposeSingleOversizeBlockFallsBackToLineSlices(t *testing.T) {
	const maxOutput = 2048
	src := htmlMonolithFixture()

	// Repro preconditions: exactly ONE block section covering all 78 lines,
	// estimated at the incident's ~4188 tokens against the 1433 ceiling.
	if budget := SubTaskBudget(maxOutput); budget != 1433 {
		t.Fatalf("SubTaskBudget(%d) = %d, want 1433", maxOutput, budget)
	}
	sections, err := BlockDecomposer{}.Split("index.html", src)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(sections) != 1 || sections[0].Region.StartLine != 1 || sections[0].Region.EndLine != 78 {
		t.Fatalf("fixture must be ONE contiguous DOM section spanning lines 1–78, got %+v", sections)
	}
	if est := EstimateRegionTokens(src, Region{StartLine: 1, EndLine: 78}); est != 4188 {
		t.Fatalf("fixture estimate = %d, want the repro ~4188", est)
	}

	// Before the fallback this call returned ErrNotDecomposable; it must now
	// stage a valid DAG of ≥3 budget-bounded line-range sub-tasks.
	dag, err := Decompose("restyle every row", "index.html", src, "digest-abc", maxOutput)
	if err != nil {
		t.Fatalf("Decompose fell closed on a sliceable oversize block: %v", err)
	}

	// Re-verify every staged-plan invariant (V1–V5): acyclic dependencies,
	// contiguous line coverage over 1..78, sub-task ceiling compliance.
	if err := dag.Validate(); err != nil {
		t.Fatalf("staged DAG failed Validate: %v", err)
	}
	if last := dag.SubTasks[len(dag.SubTasks)-1].Region.EndLine; last != 78 {
		t.Fatalf("coverage ends at line %d, want 78", last)
	}
	if len(dag.SubTasks) < 3 {
		t.Fatalf("sub-tasks = %d, want >= 3 budget-bounded line-range windows", len(dag.SubTasks))
	}

	budget := SubTaskBudget(maxOutput)
	for _, st := range dag.SubTasks {
		// Explicit SEARCH_REPLACE_BOUNDED_LINES contract on line intervals.
		if st.Kind != SplitBoundedLines {
			t.Errorf("%s kind = %s, want %s", st.ID, st.Kind, SplitBoundedLines)
		}
		if st.EstimatedTokens <= 0 || st.EstimatedTokens > budget {
			t.Errorf("%s estimates %d tok, outside the strict ceiling (%d)", st.ID, st.EstimatedTokens, budget)
		}
		// Every sub-task passes the Boundary-2 preflight guard INDIVIDUALLY.
		if !PreflightFeasible(src, st.Region, maxOutput) {
			t.Errorf("%s region %s fails EvaluatePreflight individually", st.ID, st.Region)
		}
	}
}

// ── slicer contract ─────────────────────────────────────────────────────────

func TestFallbackLineSlicerWindowsAreContiguousAndBudgetBounded(t *testing.T) {
	src := htmlMonolithFixture()
	budget := SubTaskBudget(2048)

	slices, err := FallbackLineSlicer(src, Section{
		Region: Region{StartLine: 1, EndLine: LineCount(src)},
		Label:  `<section id="monolith">`,
	}, budget)
	if err != nil {
		t.Fatalf("FallbackLineSlicer: %v", err)
	}
	if len(slices) < 3 {
		t.Fatalf("slices = %d, want >= 3", len(slices))
	}
	next := 1
	for _, s := range slices {
		if !s.BoundedLines {
			t.Errorf("slice %s must carry the BoundedLines marker", s.Region)
		}
		if !strings.Contains(s.Label, "lines") {
			t.Errorf("slice label %q should name its explicit line interval", s.Label)
		}
		if est := EstimateRegionTokens(src, s.Region); est > budget {
			t.Errorf("slice %s estimates %d tok, outside ceiling %d", s.Region, est, budget)
		}
		if s.Region.StartLine != next {
			t.Fatalf("slice %s breaks contiguity at line %d", s.Region, next)
		}
		next = s.Region.EndLine + 1
	}
	if next-1 != LineCount(src) {
		t.Fatalf("slices end at line %d, want full coverage of %d", next-1, LineCount(src))
	}
}

// ── fail-closed control: a single indivisible LINE still refuses ────────────

func TestDecomposeStillFailsClosedOnIndivisibleSingleLine(t *testing.T) {
	const maxOutput = 2048 // ceiling 1433 ⇒ an 8KB single line cannot fit
	src := minifiedHTMLFixture(8000)

	dag, err := Decompose("restyling", "index.html", src, "digest", maxOutput)
	if !errors.Is(err, ErrNotDecomposable) {
		t.Fatalf("error = %v, want ErrNotDecomposable", err)
	}
	if dag != nil {
		t.Fatal("a fail-closed refusal must never return a partial plan")
	}
}

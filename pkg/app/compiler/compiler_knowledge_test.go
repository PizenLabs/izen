package compiler

import (
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/pkg/knowledge"
)

// TestCompileSharesKnowledgeGraphAcrossRuns proves the compiler serves the
// workspace state from a shared RuntimeKnowledge graph instead of re-walking
// the disk on every Compile: a file written between compiles is invisible
// until the graph is refreshed.
func TestCompileSharesKnowledgeGraphAcrossRuns(t *testing.T) {
	root := todoWorkspace(t)
	stub := &stubExtractor{out: `{"category":"create","target_type":"portfolio","entities":{},"technologies":[],"negated_targets":[]}`}

	kg := knowledge.NewKnowledgeGraph()
	c := NewIntentCompiler(root, stub, WithKnowledgeGraph(kg))

	first, err := c.Compile(t.Context(), "create a portfolio website")
	if err != nil {
		t.Fatalf("Compile #1: %v", err)
	}
	if !first.DecisionAmbiguity {
		t.Fatal("first compile must be ambiguous (portfolio over todo workspace)")
	}

	// A new portfolio workspace file lands on disk AFTER the first scan. The
	// cached graph must not observe it, so the second compile stays ambiguous.
	writeFile(t, filepath.Join(root, "portfolio.html"), "<title>Portfolio</title>")

	second, err := c.Compile(t.Context(), "create a portfolio website")
	if err != nil {
		t.Fatalf("Compile #2: %v", err)
	}
	if !second.DecisionAmbiguity {
		t.Error("second compile re-scanned the disk; the shared graph must cache the state")
	}

	// Refresh invalidates the cache: the portfolio marker is now visible and
	// the same request over a matching workspace is unambiguous.
	kg.Refresh()
	third, err := c.Compile(t.Context(), "create a portfolio website")
	if err != nil {
		t.Fatalf("Compile #3: %v", err)
	}
	if third.DecisionAmbiguity {
		t.Error("third compile must be unambiguous after refresh observes the portfolio file")
	}
}

// TestCompileSharedKnowledgeGraphIsolation proves two compilers sharing one
// graph agree on the workspace state, and that the graph is not mutated by a
// compiler's use of it.
func TestCompileSharedKnowledgeGraphIsolation(t *testing.T) {
	root := todoWorkspace(t)
	kg := knowledge.NewKnowledgeGraph()

	a := NewIntentCompiler(root, &stubExtractor{out: redesignPortfolioJSON}, WithKnowledgeGraph(kg))
	b := NewIntentCompiler(root, &stubExtractor{out: redesignPortfolioJSON}, WithKnowledgeGraph(kg))

	gotA, err := a.Compile(t.Context(), "Redesign website for Alex Josie as a software engineer portfolio, not a todo app")
	if err != nil {
		t.Fatalf("Compile A: %v", err)
	}
	gotB, err := b.Compile(t.Context(), "Redesign website for Alex Josie as a software engineer portfolio, not a todo app")
	if err != nil {
		t.Fatalf("Compile B: %v", err)
	}
	if gotA.DecisionAmbiguity != gotB.DecisionAmbiguity {
		t.Errorf("compilers disagree on ambiguity: A=%v B=%v", gotA.DecisionAmbiguity, gotB.DecisionAmbiguity)
	}
	if !gotA.DecisionAmbiguity {
		t.Error("expected ambiguity over the shared todo workspace")
	}
	if kg.SymbolCount() == 0 {
		t.Error("shared graph must have indexed symbols for the workspace")
	}
}

// TestCompilerKnowledgeGraphWithoutOptionStillWorks is the regression guard
// for the legacy constructor: no graph attached, the detector scans a
// transient graph on demand with identical results.
func TestCompilerKnowledgeGraphWithoutOptionStillWorks(t *testing.T) {
	stub := &stubExtractor{out: redesignPortfolioJSON}
	got := compileStub(t, todoWorkspace(t), stub,
		"Redesign website for Alex Josie as a software engineer portfolio, not a todo app")
	if !got.DecisionAmbiguity {
		t.Error("legacy compiler without a knowledge graph lost ambiguity detection")
	}
	if got.ClarificationQuestions[0].Options == nil {
		t.Error("legacy compiler produced no structured options")
	}
	if got.ClarificationQuestions[0].DefaultOptionID() != "merge_selective" {
		t.Errorf("DefaultOptionID = %q, want merge_selective", got.ClarificationQuestions[0].DefaultOptionID())
	}
}

// TestConflictDetectorSetKnowledgeDetaches guards SetKnowledge(nil) restoring
// the transient-scan fallback.
func TestConflictDetectorSetKnowledgeDetaches(t *testing.T) {
	d := NewConflictDetector()
	d.SetKnowledge(nil)
	ws := d.Detect(todoWorkspace(t))
	if !ws.AppTypes["todo_app"] {
		t.Errorf("detached detector = %+v, want todo_app", ws)
	}
}

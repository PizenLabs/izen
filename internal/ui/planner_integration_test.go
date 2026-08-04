package ui

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/lea"
)

// TestPlanContextForAskDegradesWithoutGraph verifies the planner seam is a
// strict no-op when no graph is ready (the common state in tests and early
// session startup).
func TestPlanContextForAskDegradesWithoutGraph(t *testing.T) {
	m := newTestModel() // graph == nil
	in := "explain the architecture of this project"
	if got := m.planContextForAsk(in); got != in {
		t.Errorf("planContextForAsk changed input without a graph:\n%s", got)
	}
	if m.planner != nil {
		t.Errorf("contextPlanner constructed a planner without a graph")
	}
}

// TestPlanContextForAskInjectsPlannedContext verifies the ask-flow seam
// injects budget-fitted planned context for a project question once a graph
// is ready.
func TestPlanContextForAskInjectsPlannedContext(t *testing.T) {
	g := lea.NewFileGraph(".")
	g.AddFile(lea.FileNode{
		Path:    "internal/core/service.go",
		Package: "core",
		Symbols: []lea.Symbol{
			{Name: "Service", Kind: lea.SymbolStruct, File: "internal/core/service.go", Line: 5, Exported: true},
		},
	})

	m := newTestModel()
	m.graph = g
	m.workspaceRoot = "."

	out := m.planContextForAsk("what is the Service struct in this project")
	if !strings.Contains(out, "PLANNED CONTEXT") {
		t.Fatalf("expected planned context header, got:\n%s", out)
	}
	if !strings.Contains(out, "SYMBOL DEFINITIONS") {
		t.Errorf("expected symbol definitions section, got:\n%s", out)
	}
	if !strings.Contains(out, "Service") {
		t.Errorf("expected the resolved symbol to appear, got:\n%s", out)
	}
	// The original question must still be present after the injected block.
	if !strings.Contains(out, "what is the Service struct") {
		t.Errorf("original question lost after planner injection:\n%s", out)
	}
	// The planner is cached for the session.
	if m.planner == nil {
		t.Error("contextPlanner did not cache the planner")
	}
}

// TestPlanContextForAskDoesNotInjectForUnknownSymbols verifies casual/unknown
// questions that yield no plan chunks leave the input untouched.
func TestPlanContextForAskDoesNotInjectForUnknownSymbols(t *testing.T) {
	m := newTestModel()
	m.graph = lea.NewFileGraph(".")
	m.workspaceRoot = "."

	in := "hello there, how are you doing today"
	if got := m.planContextForAsk(in); got != in {
		t.Errorf("casual question mutated by planner:\n%s", got)
	}
}

// TestPlanContextForAskHonorsConfiguredBudget verifies /ask context assembly
// honors the token budget set by Context Governance (Models.MaxTokens): the
// token count the planner reports for the injected block never exceeds the
// configured cap. This is the P3 budget-verification seam for the /ask flow.
func TestPlanContextForAskHonorsConfiguredBudget(t *testing.T) {
	g := lea.NewFileGraph(".")
	g.AddFile(lea.FileNode{
		Path:    "internal/core/service.go",
		Package: "core",
		Symbols: []lea.Symbol{
			{Name: "Service", Kind: lea.SymbolStruct, File: "internal/core/service.go", Line: 5, Exported: true},
		},
	})

	m := newTestModel()
	m.graph = g
	m.workspaceRoot = "."
	m.cfg.Models.MaxTokens = 200
	m.cfg.AI.MaxTokens = 200

	out := m.planContextForAsk("what is the Service struct in this project")
	if !strings.Contains(out, "PLANNED CONTEXT") {
		t.Fatalf("expected planned context header, got:\n%s", out)
	}
	if m.planner == nil {
		t.Fatal("contextPlanner did not cache the planner")
	}
	if m.planner.MaxTokens() != 200 {
		t.Errorf("planner budget = %d, want 200", m.planner.MaxTokens())
	}
	// The header carries the enforced token total ("... intent, N tokens)").
	// It must never exceed the Context Governance budget.
	re := regexp.MustCompile(`PLANNED CONTEXT \([^)]*?, (\d+) tokens\)`)
	match := re.FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("header missing enforced token total:\n%s", out)
	}
	reported, _ := strconv.Atoi(match[1])
	if reported > 200 {
		t.Errorf("/ask injected context = %d tokens, exceeds configured budget of 200", reported)
	}
}

package engine

import (
	"testing"

	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/graph"
)

func TestCalculateTokenWeight(t *testing.T) {
	scope := domain.ObjectiveScope{
		Files:    []string{"a.go", "b.go"},
		Symbols:  []string{"DoThing", "DoOther"},
		ASTNodes: []string{"function", "struct", "file:a.go"},
	}
	got := CalculateTokenWeight(scope)
	if got <= 0 {
		t.Fatalf("expected positive token weight, got %d", got)
	}
}

func TestBuildObjectiveContextRequiresApprovalWhenOversized(t *testing.T) {
	g := graph.NewGraph(".")
	g.AddFile(graph.FileNode{
		Path: "internal/ui/commands.go",
		Symbols: []graph.Symbol{
			{Name: "HandleObjective", Kind: graph.SymbolFunction, File: "internal/ui/commands.go"},
			{Name: "ObjectiveBudgetGuard", Kind: graph.SymbolFunction, File: "internal/ui/commands.go"},
		},
	})

	result := BuildObjectiveContext("objective budget commands", "qwen2.5-coder:7b", g)
	if len(result.Scope.Files) == 0 {
		t.Fatal("expected matched files in scope")
	}
	if result.Budget.Threshold <= 0 {
		t.Fatal("expected threshold to be set")
	}
}

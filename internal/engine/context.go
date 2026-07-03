package engine

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

type ObjectiveContextResult struct {
	Scope     domain.ObjectiveScope
	Budget    domain.ObjectiveTokenBudget
	Telemetry []string
}

func BuildObjectiveContext(rawIntent, modelName string, g *graph.Graph) ObjectiveContextResult {
	keywords := intentKeywords(rawIntent)
	scope := matchObjectiveScope(keywords, g)
	weight := CalculateTokenWeight(scope)
	threshold := objectiveThreshold(modelName)
	requiresApproval := weight > threshold

	telemetry := []string{
		fmt.Sprintf("objective.keywords=%d", len(keywords)),
		fmt.Sprintf("objective.scope.files=%d", len(scope.Files)),
		fmt.Sprintf("objective.scope.symbols=%d", len(scope.Symbols)),
		fmt.Sprintf("objective.scope.ast_nodes=%d", len(scope.ASTNodes)),
		fmt.Sprintf("objective.token.weight=%d", weight),
		fmt.Sprintf("objective.token.threshold=%d", threshold),
		fmt.Sprintf("objective.requires_approval=%t", requiresApproval),
	}

	return ObjectiveContextResult{
		Scope: scope,
		Budget: domain.ObjectiveTokenBudget{
			CurrentWeight:    weight,
			Threshold:        threshold,
			RequiresApproval: requiresApproval,
		},
		Telemetry: telemetry,
	}
}

func CalculateTokenWeight(scope domain.ObjectiveScope) int {
	fileWeight := len(scope.Files) * 260
	symbolWeight := len(scope.Symbols) * 42
	astWeight := len(scope.ASTNodes) * 24
	return fileWeight + symbolWeight + astWeight
}

func objectiveThreshold(modelName string) int {
	base := plan.TokenBudgetForModel(modelName)
	threshold := int(float64(base) * 0.35)
	if threshold < 800 {
		return 800
	}
	return threshold
}

func matchObjectiveScope(keywords []string, g *graph.Graph) domain.ObjectiveScope {
	if g == nil || len(keywords) == 0 {
		return domain.ObjectiveScope{}
	}

	fileSet := make(map[string]struct{})
	symbolSet := make(map[string]struct{})
	astNodeSet := make(map[string]struct{})

	for _, file := range g.Files {
		lowerPath := strings.ToLower(file.Path)
		basePath := strings.ToLower(filepath.Base(file.Path))

		matchedFile := false
		for _, kw := range keywords {
			if strings.Contains(lowerPath, kw) || strings.Contains(basePath, kw) {
				fileSet[file.Path] = struct{}{}
				matchedFile = true
				break
			}
		}

		for _, sym := range file.Symbols {
			lowerName := strings.ToLower(sym.Name)
			for _, kw := range keywords {
				if !strings.Contains(lowerName, kw) {
					continue
				}
				fileSet[file.Path] = struct{}{}
				symbolSet[sym.Name] = struct{}{}
				astNodeSet[sym.Kind.String()] = struct{}{}
				matchedFile = true
				break
			}
		}

		if matchedFile {
			astNodeSet["file:"+file.Path] = struct{}{}
		}
	}

	scope := domain.ObjectiveScope{
		Files:    toSortedSlice(fileSet, 24),
		Symbols:  toSortedSlice(symbolSet, 80),
		ASTNodes: toSortedSlice(astNodeSet, 120),
	}
	return scope
}

func intentKeywords(raw string) []string {
	cleaned := strings.NewReplacer(
		".", " ",
		",", " ",
		":", " ",
		";", " ",
		"(", " ",
		")", " ",
		"[", " ",
		"]", " ",
		"{", " ",
		"}", " ",
		"/", " ",
		"\\", " ",
		"-", " ",
		"_", " ",
		"\"", " ",
		"'", " ",
	).Replace(strings.ToLower(raw))

	seen := make(map[string]struct{})
	out := make([]string, 0, 16)
	for _, tok := range strings.Fields(cleaned) {
		if len(tok) < 3 {
			continue
		}
		if _, exists := seen[tok]; exists {
			continue
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	return out
}

func toSortedSlice(set map[string]struct{}, max int) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	if max > 0 && len(out) > max {
		return out[:max]
	}
	return out
}

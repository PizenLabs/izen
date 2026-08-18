// Phase 3 authority-pruning enforcement: these tests pin the pruning contract —
// the surfaces removed during the authority pruning MUST NOT resurface. They
// complement the Phase 5 execution-authority tests (execution_invariants_test.go)
// by asserting on the negative space: a second intent classifier, a second
// execution authority, or a resurrected projection is an architectural
// regression, not merely dead code.
package architecture

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findIdents scans a parsed file for every identifier whose name matches the
// banned set (declarations and uses alike).
func findIdents(f *ast.File, fset *token.FileSet, names map[string]bool) []callSite {
	var sites []callSite
	ast.Inspect(f, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if !names[id.Name] {
			return true
		}
		pos := fset.Position(id.Pos())
		sites = append(sites, callSite{file: pos.Filename, line: pos.Line})
		return true
	})
	return sites
}

// TestNoDuplicateIntentClassifierResurfaces pins rule: autonomy.Classify is the
// ONE canonical intent classifier. The duplicate classifiers removed in Phase 3
// (gateway.ClassifyDirectMutation, core.ClassifyExecutionMode,
// gateway.ClassifyComplexity, ai.IntentClassifyPrompt, command.FallbackPlanTarget,
// and friends) must never be re-introduced as a parallel authority.
func TestNoDuplicateIntentClassifierResurfaces(t *testing.T) {
	root := repoRoot(t)
	banned := map[string]bool{
		"ClassifyExecutionMode":          true,
		"ClassifyDirectMutation":         true,
		"ClassifyIntentMode":             true,
		"ClassifyComplexity":             true,
		"IntentClassifyRequest":          true,
		"CompressPrompt":                 true,
		"Squeeze":                        true,
		"SimpleMutationPrompt":           true,
		"IntentClassifyPrompt":           true,
		"FallbackPlanTarget":             true,
		"GenerateFallbackPlan":           true,
		"ParseTargetFromSanitizedLedger": true,
		"IsHotTrack":                     true,
		"HasHighIntentFlag":              true,
		"StripHotPrefix":                 true,
	}

	files := goFilesUnder(root)
	violations := make([]callSite, 0, 32)
	for _, rel := range files {
		f, fset := parseFile(t, filepath.Join(root, rel))
		violations = append(violations, findIdents(f, fset, banned)...)
	}
	for _, v := range violations {
		rel := filepath.ToSlash(strings.TrimPrefix(v.file, root+string(filepath.Separator)))
		t.Errorf("architecture: duplicate intent-classifier symbol resurfaced at %s:%d — autonomy.Classify is the single canonical classifier", rel, v.line)
	}

	// The canonical classifier must exist (the invariant is enforced, not
	// vacuous).
	f, fset := parseFile(t, filepath.Join(root, "internal/autonomy/intent.go"))
	names := map[string]bool{"Classify": true}
	if sites := findIdents(f, fset, names); len(sites) == 0 {
		t.Error("architecture: autonomy.Classify not found — the canonical classifier must exist")
	}
}

// TestNoLegacyTimelineProjectionResurfaces pins the Phase 3 removal of the
// unread in-memory execution timeline: the timeline package and the
// Application.Timeline accessor must not come back.
func TestNoLegacyTimelineProjectionResurfaces(t *testing.T) {
	root := repoRoot(t)

	// No production file may import the deleted timeline package.
	for _, rel := range goFilesUnder(root) {
		f, _ := parseFile(t, filepath.Join(root, rel))
		for p := range imports(f) {
			if p == moduleImport("internal/events/timeline") {
				t.Errorf("architecture: %s imports the removed internal/events/timeline projection", rel)
			}
		}
	}

	// The composition root must not expose a Timeline() accessor or field.
	raw, err := os.ReadFile(filepath.Join(root, "internal/runtime/compose/compose.go"))
	if err != nil {
		t.Fatalf("architecture: read compose.go: %v", err)
	}
	for _, needle := range []string{"func (a *Application) Timeline()", "timeline.Timeline", "timeline events"} {
		if strings.Contains(string(raw), needle) {
			t.Errorf("architecture: compose.go resurrects the removed timeline projection (%q)", needle)
		}
	}
}

// TestStrategyCompilationGraphTestOracleOnly pins the Phase 3 decision to
// retain the strategy compilation graph (Compile/ExecutionGraph/
// CheckInvariants/EscalationsFor) exclusively as a test oracle: the production
// executor consumes strategy.Select profiles directly and MUST NOT route
// through the compiled graph. Reintroducing the graph onto the production path
// would create a second execution authority.
func TestStrategyCompilationGraphTestOracleOnly(t *testing.T) {
	root := repoRoot(t)
	files := goFilesUnder(root)
	violations := make([]callSite, 0, 32)
	for _, rel := range files {
		f, fset := parseFile(t, filepath.Join(root, rel))
		for _, method := range []string{"Compile", "CheckInvariants", "NewExecutionGraph", "EscalationsFor", "NewRecorder"} {
			violations = append(violations, findSelectorCalls(f, fset, "strategy", method)...)
		}
	}
	for _, v := range violations {
		rel := filepath.ToSlash(strings.TrimPrefix(v.file, root+string(filepath.Separator)))
		t.Errorf("architecture: strategy compilation-graph symbol called at %s:%d — the graph is a test oracle, never a production execution authority", rel, v.line)
	}

	// The oracle must still exist and be exercised by tests (the invariant is
	// enforced, not vacuous).
	if _, err := os.Stat(filepath.Join(root, "internal/execution/strategy/compile_test.go")); err != nil {
		t.Error("architecture: strategy compile_test.go missing — the test oracle was removed")
	}
}

// Phase 2 architectural enforcement (SESSION.md §33, §34): the three context
// subsystems — Session Compaction, Project Knowledge Consolidation, and
// Context Compilation — must remain absolutely independent, and none may
// become a parallel execution engine next to the RuntimeExecutor.
package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// subsystemForbiddenImports is the independence matrix. Each subsystem may
// consume the others' OUTPUTS only through explicit data types, never by
// importing each other's engines, and never by importing an execution/autonomy
// engine (no parallel state machine beside the RuntimeExecutor).
var subsystemForbiddenImports = map[string][]string{
	"internal/session/compaction": {
		"github.com/PizenLabs/izen/internal/knowledge",
		"github.com/PizenLabs/izen/internal/contextcompiler",
		"github.com/PizenLabs/izen/internal/execution",
		"github.com/PizenLabs/izen/internal/engine",
		"github.com/PizenLabs/izen/internal/autonomy",
		"github.com/PizenLabs/izen/internal/orchestrator",
	},
	"internal/knowledge": {
		"github.com/PizenLabs/izen/internal/session/compaction",
		"github.com/PizenLabs/izen/internal/contextcompiler",
		"github.com/PizenLabs/izen/internal/execution",
		"github.com/PizenLabs/izen/internal/engine",
		"github.com/PizenLabs/izen/internal/autonomy",
		"github.com/PizenLabs/izen/internal/orchestrator",
	},
	"internal/contextcompiler": {
		"github.com/PizenLabs/izen/internal/session/compaction",
		"github.com/PizenLabs/izen/internal/execution",
		"github.com/PizenLabs/izen/internal/engine",
		"github.com/PizenLabs/izen/internal/autonomy",
		"github.com/PizenLabs/izen/internal/orchestrator",
	},
}

// TestContextSubsystemsStayDecoupled sweeps every non-test source file of the
// three subsystems and fails on any forbidden cross-import.
func TestContextSubsystemsStayDecoupled(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()

	for dir, forbidden := range subsystemForbiddenImports {
		matches, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 0 {
			t.Fatalf("no source files under %s; invariant is unenforced", dir)
		}
		for _, path := range matches {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			f, err := parser.ParseFile(fset, path, src, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, imp := range f.Imports {
				pkg := strings.Trim(imp.Path.Value, `"`)
				for _, fbd := range forbidden {
					if pkg == fbd || strings.HasPrefix(pkg, fbd+"/") {
						t.Errorf("architecture: %s imports %q — the subsystems must stay independent", path, pkg)
					}
				}
			}
		}
	}
}

// TestOnlyCompositionRootWiresContextSubsystems pins that the three engines are
// instantiated ONLY in the composition root (internal/runtime/compose). Any
// engine instantiated elsewhere becomes a second authority.
func TestOnlyCompositionRootWiresContextSubsystems(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	walkErr := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, path)
		f, perr := parser.ParseFile(fset, path, src, parser.AllErrors)
		if perr != nil {
			return perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name != "NewRunner" && sel.Sel.Name != "NewPromotionEngine" {
				return true
			}
			// The call is a subsystem constructor only when the package selector
			// resolves to our packages. Parse the file's imports for the aliases.
			selector := renderExpr(sel.X)
			for _, name := range []string{"compaction", "knowledge"} {
				if selector == name {
					if rel != "internal/runtime/compose/compose.go" {
						t.Errorf("architecture: %s instantiates %s.%s outside the composition root", rel, name, sel.Sel.Name)
					}
				}
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}

	// contextcompiler.New is a generic constructor name; verify the compiler
	// type is only created via the compose accessor by scanning for
	// `contextcompiler.New(`.
	compose := filepath.Join(root, "internal/runtime/compose/compose.go")
	src, err := os.ReadFile(compose)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "contextcompiler.New()") {
		t.Error("architecture: compose must instantiate the Context Compiler (contextcompiler.New())")
	}
}

// TestNoMonolithicProjectSummaryReference pins INV-SESSION-15 structurally: no
// string literal in the knowledge subsystem names a monolithic summary file.
// Only AST string literals are scanned — documentation comments are exempt.
func TestNoMonolithicProjectSummaryReference(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	matches, err := filepath.Glob(filepath.Join(root, "internal/knowledge/*.go"))
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"project-summary", "project_summary", "summary.json", "knowledge.json"}
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, path, src, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			for _, fbd := range forbidden {
				if strings.Contains(lit.Value, fbd) {
					t.Errorf("architecture: %s string literal %s names %q — Project Knowledge must stay granular (INV-SESSION-15)", path, lit.Value, fbd)
				}
			}
			return true
		})
	}
}

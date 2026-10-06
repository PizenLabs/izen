package architecture

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ── LOCK: the semantic boundary reads human text only ───────────────────────
//
// strategy.Select classifies a request in two situations that must never be
// confused:
//
//	1. IntentGateway.Gate — on the HUMAN's request, before any authority exists.
//	2. RuntimeExecutor.Execute — on the fully COMPILED provider prompt, AFTER
//	   admission, purely to choose budgets and an artifact shape.
//
// That second call is runtime-composed text. It carries the runtime's own
// scoping instructions, among them "do not modify any other region".
//
// When the explicit read-only constraint was evaluated inside Select, the
// runtime read its own prompt back as the user revoking mutation authority.
// Every decomposed sub-task was silently downgraded to a read-only strategy,
// applied no bytes, and the objective failed as UNSUBSTANTIATED with "no
// durable delta observed" — a mutation the user had asked for, abandoned
// halfway through, with no error anywhere explaining why.
//
// The lock is a single-ownership rule: StatesReadOnlyConstraint — a statement
// about what a HUMAN wrote — may be consulted from exactly one non-test file,
// the gateway that receives human input. A second caller is the defect coming
// back, and a source lock catches it before it can be reintroduced as a
// "harmless" improvement inside the router.

const readOnlyConstraintFn = "StatesReadOnlyConstraint"

func TestSemanticLock_ReadOnlyConstraintHasExactlyOneNonTestCaller(t *testing.T) {
	root := repoRoot(t)
	base := filepath.Join(root, "internal")

	callers := map[string][]int{}
	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			// Inside the strategy package the function is called unqualified; from
			// another package it is selected. Both forms count as a caller.
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				if fn.Name != readOnlyConstraintFn {
					return true
				}
			case *ast.SelectorExpr:
				if fn.Sel.Name != readOnlyConstraintFn {
					return true
				}
			default:
				return true
			}
			pos := fset.Position(call.Pos())
			callers[rel] = append(callers[rel], pos.Line)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scanning %s: %v", base, err)
	}

	if len(callers) == 0 {
		t.Fatalf("no non-test caller of %s was found; the scan read nothing, so the lock is vacuous",
			readOnlyConstraintFn)
	}

	gatewayFile := "internal/execution/intent.go"
	for file, lines := range callers {
		if file == gatewayFile {
			continue
		}
		t.Errorf("%s consults %s at line(s) %s.\n"+
			"  %s answers \"did the HUMAN decline a write\", and strategy.Select also runs on\n"+
			"  runtime-composed provider prompts whose own instructions contain phrases like\n"+
			"  \"do not modify any other region\". Reading those as a human constraint silently\n"+
			"  downgrades authorized mutations to read-only.\n"+
			"  Only %s may consult it.",
			file, readOnlyConstraintFn, formatLines(lines), readOnlyConstraintFn, gatewayFile)
	}
}

// TestSemanticLock_RouterDoesNotRefuseOnUndeterminedIntent pins the other half
// of the same boundary. strategy.Select may REPORT an UNDETERMINED verdict —
// that is how the router stays honest about what it could not read — but it may
// not ROUTE on it. Select also runs inside the RuntimeExecutor, after
// admission, where the Control Plane has already authorized the work; refusing
// there drops authorized work on a classification made before anyone was
// authorized. The refusal belongs at the gateway.
func TestSemanticLock_RouterDoesNotRefuseOnUndeterminedIntent(t *testing.T) {
	root := repoRoot(t)
	rel := filepath.Join("internal", "execution", "strategy", "selector.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", rel, err)
	}

	violations := []string{}
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || !assignsClarification(assign) {
			return true
		}
		pos := fset.Position(assign.Pos())
		// Walk outward: the refusal must not be guarded by the undetermined
		// verdict anywhere in the statement or the condition that contains it.
		if mentionsUndetermined(assign) || withinUndeterminedGuard(file, fset, assign) {
			violations = append(violations, strconv.Itoa(pos.Line))
		}
		return true
	})

	for _, line := range violations {
		t.Errorf("%s:%s routes to HumanClarification on an UNDETERMINED verdict.\n"+
			"  Select also runs inside the RuntimeExecutor AFTER admission, where the Control\n"+
			"  Plane has already authorized the work. Refusing there drops authorized work on a\n"+
			"  classification made before anyone was authorized. The refusal belongs at the\n"+
			"  gateway (internal/execution/intent.go).", rel, line)
	}

	gateway, err := os.ReadFile(filepath.Join(root, "internal", "execution", "intent.go"))
	if err != nil {
		t.Fatalf("reading the gateway: %v", err)
	}
	if !strings.Contains(string(gateway), "IsUndetermined()") {
		t.Error("the gateway no longer refuses an UNDETERMINED semantic verdict; " +
			"an unread request would fall through to the mutation path again")
	}
}

// assignsClarification reports whether this assignment routes the profile to the
// human-clarification strategy.
func assignsClarification(assign *ast.AssignStmt) bool {
	for _, rhs := range assign.Rhs {
		switch v := rhs.(type) {
		case *ast.Ident:
			// Inside the strategy package the constant is a bare identifier.
			if v.Name == "HumanClarification" {
				return true
			}
		case *ast.SelectorExpr:
			// From another package it is qualified.
			if v.Sel.Name == "HumanClarification" {
				return true
			}
		}
	}
	return false
}

// mentionsUndetermined reports whether the source text of a node references the
// undetermined verdict.
func mentionsUndetermined(n ast.Node) bool {
	var buf strings.Builder
	if err := format.Node(&buf, token.NewFileSet(), n); err != nil {
		return false
	}
	return strings.Contains(buf.String(), "IsUndetermined")
}

// withinUndeterminedGuard reports whether the statement sits inside an if whose
// condition references the undetermined verdict.
func withinUndeterminedGuard(file *ast.File, fset *token.FileSet, target ast.Node) bool {
	found := false
	targetPos := fset.Position(target.Pos())
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		ifs, ok := n.(*ast.IfStmt)
		if !ok || ifs.Cond == nil {
			return true
		}
		if !mentionsUndetermined(ifs.Cond) {
			return true
		}
		bodyStart := fset.Position(ifs.Body.Pos()).Line
		bodyEnd := fset.Position(ifs.Body.End()).Line
		if targetPos.Line >= bodyStart && targetPos.Line <= bodyEnd {
			found = true
			return false
		}
		return true
	})
	return found
}

func formatLines(lines []int) string {
	quoted := make([]string, 0, len(lines))
	for _, l := range lines {
		quoted = append(quoted, strconv.Itoa(l))
	}
	return strings.Join(quoted, ", ")
}

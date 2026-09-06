package domain_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDomainOrthogonality ensures no single struct in internal/core/domain
// fuses fields across multiple orthogonal dimensions (WorkflowState,
// ArtifactStore, CapabilitySet, ExecutionState).
func TestDomainOrthogonality(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "domain")

	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	// Orthogonal dimension keywords — each corresponds to one dimension.
	// A struct that mentions fields from >=2 dimensions without a projection
	// seam is flagged.
	dimensionSignals := map[string]string{
		"WorkflowState":  "workflow",
		"ArtifactStore":  "artifact",
		"CapabilitySet":  "capability",
		"ExecutionState": "execution",
		// Alternate signals
		"Workflow":   "workflow",
		"Artifact":   "artifact",
		"Capability": "capability",
		"Execution":  "execution",
	}

	// Explicit allowlist: known projection types or legitimate cross-dimension
	// carriers (e.g., AuthorizationInput bundles evidence for a single decision).
	allowlist := map[string]bool{
		// Authorization decision bundles are single-purpose inputs, not state fusion
		"AuthorizationInput":     true,
		"AuthorizationDecision":  true,
		"AuthorityRule":          true,
		"ExecutionEnvironment":   true,
		"ExecutionUnit":          true,
		"ExecutionFrame":         true,
		"ExecutionObservation":   true,
		"ExecutionStrategy":      true,
		"TerminalState":          true,
	}

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		if filepath.Base(path) == "domain_orthogonality_test.go" {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				if allowlist[ts.Name.Name] {
					continue
				}
				dims := make(map[string]bool)
				for _, field := range st.Fields.List {
					// inspect type expression string
					tstr := typeString(field.Type)
					for sig, dim := range dimensionSignals {
						if strings.Contains(tstr, sig) {
							dims[dim] = true
						}
					}
					// also check field names
					for _, name := range field.Names {
						for sig, dim := range dimensionSignals {
							if strings.Contains(name.Name, sig) {
								dims[dim] = true
							}
						}
					}
				}
				if len(dims) >= 2 {
					t.Errorf("orthogonality violation: struct %s in %s fuses dimensions %v — decompose via projection seam (ARCH:1.3, V2-A)", ts.Name.Name, filepath.Base(path), keys(dims))
				}
			}
		}
	}
}

func typeString(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return typeString(v.X) + "." + v.Sel.Name
	case *ast.StarExpr:
		return typeString(v.X)
	case *ast.ArrayType:
		return typeString(v.Elt)
	case *ast.MapType:
		return typeString(v.Key) + ":" + typeString(v.Value)
	default:
		return ""
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate module root")
		}
		dir = parent
	}
}

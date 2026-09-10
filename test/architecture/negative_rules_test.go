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

// NegativeArchitectureAudit asserts that production execution packages do not
// reference forbidden static model identifiers or fallback/default patterns.
func TestNegativeArchitecture_NoStaticModelDefaults(t *testing.T) {
	// Scan internal/ production execution packages only (not test, not docs).
	productionDirs := []string{
		"internal/engine",
		"internal/execution",
		"internal/runtime",
		"internal/providers",
		"internal/modes",
		"internal/app/model",
		"internal/core",
	}

	forbiddenPatterns := []string{"defaultModel", "fallbackModel", "default_model", "fallback_model"}
	forbiddenLiterals := []string{"dots-3-note-preview:free", "qwen2.5-coder:7b", "claude-sonnet-4", "gpt-4o"}

	var hits []string

	for _, dir := range productionDirs {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.Contains(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if parseErr != nil {
				return nil //nolint:nilerr // ignore unparseable files
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Ident:
					name := x.Name
					for _, p := range forbiddenPatterns {
						if name == p {
							hits = append(hits, path+":"+name)
						}
					}
				case *ast.BasicLit:
					if x.Kind == token.STRING {
						s := strings.Trim(x.Value, "`\"")
						for _, lit := range forbiddenLiterals {
							if s == lit || strings.Contains(s, lit) {
								hits = append(hits, path+":\""+s+"\"")
							}
						}
					}
				}
				return true
			})
			return nil
		})
	}

	if len(hits) > 0 {
		t.Errorf("Negative architecture violation: forbidden static model patterns found in production packages:\n%s", strings.Join(hits, "\n"))
	}
}

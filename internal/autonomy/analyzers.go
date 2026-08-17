package autonomy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// analyzeCodeAST is the AST analyzer for Go. The stdlib go/ast parser is
// compiled into the runtime, so Go findings are produced by a real parser and
// carry the highest confidence tier. On parse failure it degrades to the
// AST-lite scanner and records a code.parse_error finding — it never panics
// and never returns nil.
func analyzeCodeAST(path, content string) *CodeUnderstanding {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, content, parser.ParseComments)
	if err != nil {
		u := compileCode(path, content)
		u.Findings = append([]Finding{{
			Type:       "code.parse_error",
			Severity:   SeverityError,
			Confidence: 1.0,
			Detail:     "go/ast could not parse the file: " + err.Error(),
		}}, u.Findings...)
		return u
	}

	u := &CodeUnderstanding{
		Dependencies: make([]string, 0, len(f.Imports)),
	}
	for _, imp := range f.Imports {
		if imp.Path != nil {
			u.Dependencies = append(u.Dependencies, strings.Trim(imp.Path.Value, `"`))
		}
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name != nil {
				u.Symbols = append(u.Symbols, CodeSymbol{
					Name:     d.Name.Name,
					Kind:     "function",
					Line:     fset.Position(d.Pos()).Line,
					Language: "go",
				})
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name != nil {
					kind := "type"
					switch ts.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					u.Symbols = append(u.Symbols, CodeSymbol{
						Name:     ts.Name.Name,
						Kind:     kind,
						Line:     fset.Position(ts.Pos()).Line,
						Language: "go",
					})
				}
			}
		}
	}
	u.AffectedScope = affectedScope(u.Symbols, strings.Split(content, "\n"))
	return u
}

package extractors

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

type goExtractor struct{}

func NewGoExtractor() symbol.LanguageExtractor {
	return &goExtractor{}
}

func (e *goExtractor) DetectLanguage(rootPath string) (symbol.LanguageID, bool) {
	if fileExists(rootPath, "go.mod") || fileExists(rootPath, "go.sum") {
		return symbol.LangGo, true
	}
	return "", false
}

func (e *goExtractor) ExtractSymbols(filePath string, content []byte) (*symbol.FileASTInfo, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, content, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	info := &symbol.FileASTInfo{
		FilePath: filePath,
		Language: symbol.LangGo,
	}

	for _, imp := range f.Imports {
		info.Imports = append(info.Imports, symbol.DependencyEdge{
			SourcePackage: "",
			TargetPackage: strings.Trim(imp.Path.Value, `"`),
			ImportPath:    strings.Trim(imp.Path.Value, `"`),
			RelationType:  symbol.RelationImports,
		})
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			pos := fset.Position(node.Pos())
			end := fset.Position(node.End())
			kind := symbol.SymbolFunction
			if node.Recv != nil && len(node.Recv.List) > 0 {
				kind = symbol.SymbolMethod
			}
			name := node.Name.Name
			sig := signatureFromDecl(node)
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      kind,
				FilePath:  filePath,
				StartLine: pos.Line,
				EndLine:   end.Line,
				Exported:  ast.IsExported(name),
				Signature: sig,
			}
			if node.Recv != nil && len(node.Recv.List) > 0 {
				sym.Receiver = exprString(node.Recv.List[0].Type)
				sym.Parent = sym.Receiver
			}
			info.Symbols = append(info.Symbols, sym)
			info.Functions = append(info.Functions, sym)

		case *ast.GenDecl:
			for _, spec := range node.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					pos := fset.Position(s.Pos())
					end := fset.Position(s.End())
					kind := symbol.SymbolStruct
					switch s.Type.(type) {
					case *ast.InterfaceType:
						kind = symbol.SymbolInterface
					case *ast.StructType:
						kind = symbol.SymbolStruct
					}
					name := s.Name.Name
					sym := symbol.SymbolNode{
						Name:      name,
						Kind:      kind,
						FilePath:  filePath,
						StartLine: pos.Line,
						EndLine:   end.Line,
						Exported:  ast.IsExported(name),
						Signature: string(kind),
					}
					info.Symbols = append(info.Symbols, sym)
					if kind == symbol.SymbolStruct {
						info.Classes = append(info.Classes, sym)
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						pos := fset.Position(s.Pos())
						sym := symbol.SymbolNode{
							Name:      name.Name,
							Kind:      symbol.SymbolVariable,
							FilePath:  filePath,
							StartLine: pos.Line,
							Exported:  ast.IsExported(name.Name),
						}
						info.Symbols = append(info.Symbols, sym)
					}
				}
			}
		}
		return true
	})

	return info, nil
}

func (e *goExtractor) ExtractPackages(rootPath string) ([]symbol.PackageNode, error) {
	var packages []symbol.PackageNode
	seen := make(map[string]bool)

	_ = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(path, "vendor/") || strings.Contains(path, ".izen/") {
			return nil
		}

		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil //nolint:nilerr
		}

		pkg, parseErr := extractPackageInfo(path, src)
		if parseErr != nil {
			return nil //nolint:nilerr
		}
		if pkg == "" {
			return nil
		}

		if !seen[pkg] {
			seen[pkg] = true
			packages = append(packages, symbol.PackageNode{
				Name:     pkg,
				RootPath: rootPath,
				Files:    []string{path},
			})
		} else {
			for i := range packages {
				if packages[i].Name == pkg {
					packages[i].Files = append(packages[i].Files, path)
					break
				}
			}
		}
		return nil
	})

	return packages, nil
}

func (e *goExtractor) DetectArchitecturePattern(nodes []symbol.PackageNode) (symbol.PatternInfo, error) {
	return symbol.PatternInfo{
		Name:        "Go Package Structure",
		Confidence:  "medium",
		Description: "Standard Go module with internal package organization",
	}, nil
}

func extractPackageInfo(path string, src []byte) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return "", err
	}
	return f.Name.Name, nil
}

func signatureFromDecl(decl *ast.FuncDecl) string {
	var b strings.Builder
	if decl.Recv != nil && len(decl.Recv.List) > 0 {
		b.WriteString(exprString(decl.Recv.List[0].Type))
		b.WriteString(".")
	}
	b.WriteString(decl.Name.Name)
	b.WriteString("(")
	if decl.Type.Params != nil {
		for i, p := range decl.Type.Params.List {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(paramTypeString(p.Type))
		}
	}
	b.WriteString(")")
	if decl.Type.Results != nil && len(decl.Type.Results.List) > 0 {
		b.WriteString(" ")
		if len(decl.Type.Results.List) == 1 && decl.Type.Results.List[0].Names == nil {
			b.WriteString(paramTypeString(decl.Type.Results.List[0].Type))
		} else {
			b.WriteString("(")
			for i, r := range decl.Type.Results.List {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(paramTypeString(r.Type))
			}
			b.WriteString(")")
		}
	}
	return b.String()
}

func paramTypeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + paramTypeString(t.X)
	case *ast.SelectorExpr:
		return paramTypeString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + paramTypeString(t.Elt)
	case *ast.MapType:
		return "map[" + paramTypeString(t.Key) + "]" + paramTypeString(t.Value)
	case *ast.InterfaceType:
		return "interface{}"
	default:
		return "?"
	}
}

func exprString(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return "*" + exprString(x.X)
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprString(x.Elt)
	default:
		return "?"
	}
}

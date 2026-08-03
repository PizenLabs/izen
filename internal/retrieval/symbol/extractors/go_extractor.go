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
		Package:  f.Name.Name,
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
					switch t := s.Type.(type) {
					case *ast.InterfaceType:
						kind = symbol.SymbolInterface
						if methods := interfaceMethods(t); len(methods) > 0 {
							info.Symbols = append(info.Symbols, symbol.SymbolNode{
								Name:      s.Name.Name,
								Kind:      kind,
								FilePath:  filePath,
								StartLine: pos.Line,
								EndLine:   end.Line,
								Exported:  ast.IsExported(s.Name.Name),
								Signature: "interface",
								Methods:   methods,
							})
							continue
						}
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

	info.Calls = extractGoCalls(f, fset)
	info.Routes = extractGoRoutes(f, fset)

	return info, nil
}

// interfaceMethods returns the declared method names of an interface type.
func interfaceMethods(it *ast.InterfaceType) []string {
	var methods []string
	for _, field := range it.Methods.List {
		if len(field.Names) == 0 {
			// Embedded interface; skip for method-set purposes.
			continue
		}
		for _, name := range field.Names {
			methods = append(methods, name.Name)
		}
	}
	return methods
}

// extractGoCalls walks the AST and records every function/method invocation
// site attributed to its enclosing function or method. Unnamed closures are
// skipped; the callee is recorded as written so the graph layer can resolve it
// to an in-repo definition.
func extractGoCalls(f *ast.File, fset *token.FileSet) []symbol.CallSite {
	var calls []symbol.CallSite

	// scope is the enclosing function qualified name while inside a FuncDecl.
	var scope string
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			qual := node.Name.Name
			if node.Recv != nil && len(node.Recv.List) > 0 {
				qual = strings.TrimPrefix(exprString(node.Recv.List[0].Type), "*") + "." + qual
			}
			prev := scope
			scope = qual
			if node.Body != nil {
				collectGoCallsIn(node.Body, qual, fset, &calls)
			}
			scope = prev
			return false
		case *ast.CallExpr:
			if scope != "" {
				if _, isLit := node.Fun.(*ast.FuncLit); isLit {
					return true
				}
				name := calleeName(node.Fun)
				if name == "" {
					return true
				}
				pos := fset.Position(node.Pos())
				calls = append(calls, symbol.CallSite{
					Name:   name,
					InFunc: scope,
					Line:   pos.Line,
					Column: pos.Column,
				})
			}
		}
		return true
	})
	return calls
}

// collectGoCallsIn records call sites inside a single function body.
func collectGoCallsIn(body *ast.BlockStmt, scope string, fset *token.FileSet, calls *[]symbol.CallSite) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if _, isLit := call.Fun.(*ast.FuncLit); isLit {
			return true
		}
		name := calleeName(call.Fun)
		if name == "" {
			return true
		}
		pos := fset.Position(call.Pos())
		*calls = append(*calls, symbol.CallSite{
			Name:   name,
			InFunc: scope,
			Line:   pos.Line,
			Column: pos.Column,
		})
		return true
	})
}

// calleeName renders the target of a call expression as written.
func calleeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	default:
		return ""
	}
}

// routeRegisterVerbs maps a registration call name to the HTTP method it
// registers. HandleFunc/Handle register the pattern for any method.
var routeRegisterVerbs = map[string]string{
	"HandleFunc": "ANY",
	"Handle":     "ANY",
	"GET":        "GET",
	"POST":       "POST",
	"PUT":        "PUT",
	"PATCH":      "PATCH",
	"DELETE":     "DELETE",
	"HEAD":       "HEAD",
	"OPTIONS":    "OPTIONS",
	"Any":        "ANY",
	"Route":      "ANY",
	"Group":      "",
}

// extractGoRoutes detects HTTP route registrations (net/http, Gin, Echo,
// Fiber) and maps each path/verb to its handler reference.
func extractGoRoutes(f *ast.File, fset *token.FileSet) []symbol.HTTPRoute {
	var routes []symbol.HTTPRoute
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		method, known := routeRegisterVerbs[sel.Sel.Name]
		if !known || method == "" {
			return true
		}
		args := call.Args
		if len(args) < 2 {
			return true
		}
		pathLit, ok := args[0].(*ast.BasicLit)
		if !ok || pathLit.Kind != token.STRING {
			return true
		}
		path := strings.Trim(pathLit.Value, `"`)
		if !strings.HasPrefix(path, "/") {
			return true
		}
		handler := handlerName(args[1])
		pos := fset.Position(call.Pos())
		routes = append(routes, symbol.HTTPRoute{
			Path:    path,
			Method:  method,
			Handler: handler,
			Line:    pos.Line,
		})
		return true
	})
	return routes
}

// handlerName extracts the handler reference name from a route registration
// argument (identifier, selector or inline closure).
func handlerName(arg ast.Expr) string {
	switch a := arg.(type) {
	case *ast.Ident:
		return a.Name
	case *ast.SelectorExpr:
		return exprString(a.X) + "." + a.Sel.Name
	default:
		return ""
	}
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
		if symbol.ShouldIgnorePath(path, rootPath) {
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

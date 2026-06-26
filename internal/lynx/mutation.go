package lynx

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/graph"
)

type MutationPoint struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Column     int    `json:"column"`
	VarName    string `json:"var_name"`
	Kind       string `json:"kind"`
	TypeName   string `json:"type_name,omitempty"`
	Expr       string `json:"expr,omitempty"`
	SymbolName string `json:"symbol_name,omitempty"`
}

type ImpactEdge struct {
	SourceFile  string `json:"source_file"`
	SourceLine  int    `json:"source_line"`
	TargetFile  string `json:"target_file"`
	TargetLine  int    `json:"target_line"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

type MutationTracer struct {
	root  string
	graph *graph.Graph
	fset  *token.FileSet
	files map[string]*ast.File
}

func NewMutationTracer(root string, g *graph.Graph) *MutationTracer {
	return &MutationTracer{
		root:  root,
		graph: g,
		fset:  token.NewFileSet(),
		files: make(map[string]*ast.File),
	}
}

func (mt *MutationTracer) loadFile(path string) (*ast.File, error) {
	if f, ok := mt.files[path]; ok {
		return f, nil
	}

	fullPath := filepath.Join(mt.root, path)
	src, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, err
	}

	f, err := parser.ParseFile(mt.fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	mt.files[path] = f
	return f, nil
}

func (mt *MutationTracer) TraceAssignments(symbolName string) ([]MutationPoint, error) {
	g := mt.graph
	if g == nil {
		return nil, nil
	}

	symbols := g.LookupSymbol(symbolName)
	if len(symbols) == 0 {
		return nil, fmt.Errorf("symbol %q not found in graph", symbolName)
	}

	var points []MutationPoint

	for _, sym := range symbols {
		f, err := mt.loadFile(sym.File)
		if err != nil {
			continue
		}

		ast.Inspect(f, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}

			for i, lhs := range assign.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if ident.Name != symbolName {
					continue
				}

				pos := mt.fset.Position(ident.Pos())
				var rhsStr string
				if i < len(assign.Rhs) {
					rhsStr = mt.exprString(assign.Rhs[i])
				}

				kind := "assign"
				if assign.Tok == token.DEFINE {
					kind = "define"
				}

				points = append(points, MutationPoint{
					File:       sym.File,
					Line:       pos.Line,
					Column:     pos.Column,
					VarName:    symbolName,
					Kind:       kind,
					TypeName:   mt.resolveType(sym.File, ident),
					Expr:       rhsStr,
					SymbolName: symbolName,
				})
			}
			return true
		})
	}

	if len(points) == 0 {
		return points, nil
	}

	sort.Slice(points, func(i, j int) bool {
		if points[i].File != points[j].File {
			return points[i].File < points[j].File
		}
		return points[i].Line < points[j].Line
	})

	return points, nil
}

func (mt *MutationTracer) TraceImpact(symbolName string) ([]ImpactEdge, error) {
	g := mt.graph
	if g == nil {
		return nil, nil
	}

	symbols := g.LookupSymbol(symbolName)
	if len(symbols) == 0 {
		return nil, fmt.Errorf("symbol %q not found in graph", symbolName)
	}

	var edges []ImpactEdge
	seen := make(map[string]bool)

	for _, sym := range symbols {
		deps := g.Dependents[sym.File]
		for _, dep := range deps {
			key := sym.File + "->" + dep
			if seen[key] {
				continue
			}
			seen[key] = true
			edges = append(edges, ImpactEdge{
				SourceFile:  sym.File,
				TargetFile:  dep,
				Kind:        "import_dependency",
				Description: fmt.Sprintf("%s imports %s", filepath.Base(dep), filepath.Base(sym.File)),
			})
		}
	}

	for _, sym := range symbols {
		f, err := mt.loadFile(sym.File)
		if err != nil {
			continue
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

			recv, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}

			fullName := recv.Name + "." + sel.Sel.Name
			if !strings.HasSuffix(fullName, symbolName) && fullName != symbolName {
				return true
			}

			pos := mt.fset.Position(call.Pos())
			edges = append(edges, ImpactEdge{
				SourceFile:  sym.File,
				SourceLine:  pos.Line,
				TargetFile:  sym.File,
				TargetLine:  mt.findSymbolLine(sym.File, sel.Sel.Name),
				Kind:        "method_call",
				Description: fmt.Sprintf("%s calls %s", filepath.Base(sym.File), fullName),
			})
			return true
		})
	}

	return edges, nil
}

func (mt *MutationTracer) TraceCrossFile(symbolName string) ([]ImpactEdge, error) {
	return mt.TraceImpact(symbolName)
}

func (mt *MutationTracer) resolveType(file string, ident *ast.Ident) string {
	f, err := mt.loadFile(file)
	if err != nil {
		return ""
	}

	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if typeSpec.Name.Name == ident.Name {
				return mt.typeExprString(typeSpec.Type)
			}
		}
	}

	return ""
}

func (mt *MutationTracer) findSymbolLine(file, name string) int {
	if mt.graph == nil {
		return 0
	}
	symbols := mt.graph.LookupSymbol(name)
	for _, sym := range symbols {
		if sym.File == file {
			return sym.Line
		}
	}
	return 0
}

func (mt *MutationTracer) exprString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.BasicLit:
		return e.Value
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			return mt.exprString(sel.X) + "." + sel.Sel.Name + "(...)"
		}
		if ident, ok := e.Fun.(*ast.Ident); ok {
			return ident.Name + "(...)"
		}
		return "fn(...)"
	case *ast.SelectorExpr:
		return mt.exprString(e.X) + "." + e.Sel.Name
	case *ast.BinaryExpr:
		return mt.exprString(e.X) + " " + e.Op.String() + " " + mt.exprString(e.Y)
	case *ast.UnaryExpr:
		return e.Op.String() + mt.exprString(e.X)
	case *ast.StarExpr:
		return "*" + mt.exprString(e.X)
	case *ast.IndexExpr:
		return mt.exprString(e.X) + "[...]"
	case *ast.SliceExpr:
		return mt.exprString(e.X) + "[...]"
	case *ast.CompositeLit:
		typ := mt.exprString(e.Type)
		return typ + "{...}"
	case *ast.FuncLit:
		return "func(...) {...}"
	default:
		return "?"
	}
}

func (mt *MutationTracer) typeExprString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + mt.typeExprString(e.X)
	case *ast.SelectorExpr:
		return mt.typeExprString(e.X) + "." + e.Sel.Name
	case *ast.ArrayType:
		return "[]" + mt.typeExprString(e.Elt)
	case *ast.MapType:
		return "map[" + mt.typeExprString(e.Key) + "]" + mt.typeExprString(e.Value)
	case *ast.StructType:
		return "struct{...}"
	case *ast.InterfaceType:
		return "interface{...}"
	case *ast.FuncType:
		return "func(...)"
	case *ast.IndexExpr:
		return mt.typeExprString(e.X) + "[" + mt.typeExprString(e.Index) + "]"
	default:
		return "?"
	}
}

type TypeInfo struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Methods    []string `json:"methods,omitempty"`
	Fields     []string `json:"fields,omitempty"`
	Implements []string `json:"implements,omitempty"`
}

func TypeCheck(root string) (*types.Package, *token.FileSet, error) {
	fset := token.NewFileSet()

	var pkgs []*ast.Package
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(path, "vendor/") || strings.Contains(path, ".izen/") {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		pkg, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return nil
		}
		pkgs = append(pkgs, &ast.Package{
			Name:  pkg.Name.Name,
			Files: map[string]*ast.File{path: pkg},
		})
		return nil
	})

	if len(pkgs) == 0 {
		return nil, nil, fmt.Errorf("no Go packages found")
	}

	conf := types.Config{Importer: &passThroughImporter{}}

	var allFiles []*ast.File
	for _, f := range pkgs[0].Files {
		allFiles = append(allFiles, f)
	}

	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Implicits:  make(map[ast.Node]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:     make(map[ast.Node]*types.Scope),
		InitOrder:  nil,
	}

	pkg, err := conf.Check("izen", fset, allFiles, info)
	if err != nil {
		return nil, nil, fmt.Errorf("typecheck: %w", err)
	}

	return pkg, fset, nil
}

type passThroughImporter struct{}

func (i *passThroughImporter) Import(path string) (*types.Package, error) {
	return types.NewPackage(path, path), nil
}

func CollectTypes() ([]TypeInfo, error) {
	targetDir := "."
	fset := token.NewFileSet()

	pkgs, err := parser.ParseDir(fset, targetDir, func(info os.FileInfo) bool {
		if strings.Contains(info.Name(), "_test") {
			return false
		}
		return true
	}, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse dir: %w", err)
	}

	var typesInfo []TypeInfo

	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				gen, ok := n.(*ast.GenDecl)
				if !ok {
					return true
				}

				for _, spec := range gen.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}

					pos := fset.Position(typeSpec.Pos())
					info := TypeInfo{
						Name: typeSpec.Name.Name,
						File: pos.Filename,
						Line: pos.Line,
					}

					switch t := typeSpec.Type.(type) {
					case *ast.StructType:
						info.Kind = "struct"
						for _, field := range t.Fields.List {
							if len(field.Names) > 0 {
								for _, name := range field.Names {
									info.Fields = append(info.Fields, name.Name)
								}
							}
						}
					case *ast.InterfaceType:
						info.Kind = "interface"
						for _, method := range t.Methods.List {
							if len(method.Names) > 0 {
								for _, name := range method.Names {
									info.Methods = append(info.Methods, name.Name)
								}
							}
						}
					default:
						info.Kind = "type"
					}

					typesInfo = append(typesInfo, info)
				}
				return true
			})
		}
	}

	sort.Slice(typesInfo, func(i, j int) bool {
		return typesInfo[i].Name < typesInfo[j].Name
	})

	return typesInfo, nil
}

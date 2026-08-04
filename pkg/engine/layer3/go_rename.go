package layer3

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/pkg/engine/layer2"
)

// goRename renames the symbol described by sym to newName within src using a
// lexically-scoped AST rewrite. It returns the rewritten source and whether
// anything changed. Methods are renamed across their receiver and selector
// sites plus matching interface declarations; all other package-level symbols
// are renamed with a scope-aware walk that respects shadowing.
func (h *ASTRewriteHandler) goRename(src []byte, sym layer2.SymbolInfo, newName string) ([]byte, bool, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, sym.File, src, parser.ParseComments)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %s: %w", ErrParse, sym.File, err)
	}
	g := &goRenamer{
		fset:       fset,
		sym:        sym,
		newName:    newName,
		pkgAliases: h.packageAliases(sym.Package, f),
	}
	if sym.Kind == "method" {
		g.renameMethod(f)
	} else {
		g.renameScoped(f)
	}
	if !g.changed {
		return nil, false, nil
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), true, nil
}

// packageAliases returns the import aliases in f that resolve to pkgDir.
func (h *ASTRewriteHandler) packageAliases(pkgDir string, f *ast.File) map[string]bool {
	aliases := make(map[string]bool)
	if pkgDir == "" {
		return aliases
	}
	for _, imp := range f.Imports {
		path := unquote(imp.Path.Value)
		if path == "" || !h.importPointsTo(path, pkgDir) {
			continue
		}
		if name := defaultImportName(imp); name != "" {
			aliases[name] = true
		}
	}
	return aliases
}

// importPointsTo reports whether an import path refers to pkgDir, matching by
// longest in-repo package-dir suffix.
func (h *ASTRewriteHandler) importPointsTo(importPath, pkgDir string) bool {
	if importPath == pkgDir {
		return true
	}
	if !strings.HasSuffix(importPath, "/"+pkgDir) {
		return false
	}
	for _, d := range h.pkgDirs() {
		if len(d) <= len(pkgDir) {
			continue
		}
		if strings.HasSuffix(importPath, "/"+d) {
			return false
		}
	}
	return true
}

// pkgDirs returns the distinct package directories indexed by the SoR.
func (h *ASTRewriteHandler) pkgDirs() []string {
	seen := make(map[string]bool)
	var dirs []string
	for _, f := range h.sor.Files() {
		p := h.sor.Package(f)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		dirs = append(dirs, p)
	}
	sort.Strings(dirs)
	return dirs
}

// goRenamer is the state of one lexically-scoped Go rename pass.
type goRenamer struct {
	fset       *token.FileSet
	sym        layer2.SymbolInfo
	newName    string
	pkgAliases map[string]bool
	changed    bool
}

func (g *goRenamer) line(pos token.Pos) int {
	return g.fset.Position(pos).Line
}

func (g *goRenamer) renameIdent(id *ast.Ident) {
	if id == nil || id.Name != g.sym.Name || id.Name == g.newName {
		return
	}
	id.Name = g.newName
	g.changed = true
}

// renameScoped renames a package-level function, type or variable: the
// declaration itself plus every non-shadowed reference in the file's function
// bodies.
func (g *goRenamer) renameScoped(f *ast.File) {
	g.renamePackageDecls(f)
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok {
			g.walkFunc(fd)
		}
	}
}

func (g *goRenamer) renamePackageDecls(f *ast.File) {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if g.isTargetFunc(d) {
				g.renameIdent(d.Name)
			}
		case *ast.GenDecl:
			g.renameTargetSpec(d)
		}
	}
}

func (g *goRenamer) isTargetFunc(d *ast.FuncDecl) bool {
	if d.Name.Name != g.sym.Name || d.Recv != nil {
		return false
	}
	return g.line(d.Name.Pos()) == g.sym.Line
}

func (g *goRenamer) renameTargetSpec(d *ast.GenDecl) {
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.ValueSpec:
			for _, n := range s.Names {
				if n.Name == g.sym.Name && g.line(n.Pos()) == g.sym.Line {
					g.renameIdent(n)
				}
			}
		case *ast.TypeSpec:
			if s.Name.Name == g.sym.Name && g.line(s.Name.Pos()) == g.sym.Line {
				g.renameIdent(s.Name)
			}
		}
	}
}

// renameMethod renames a method: the receiver's declarations, matching
// interface method declarations and selector sites throughout the file.
func (g *goRenamer) renameMethod(f *ast.File) {
	recv := g.targetMethodReceiver(f)
	if recv == "" {
		g.renameMethodByLine(f)
	} else {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name.Name == g.sym.Name && d.Recv != nil && receiverBase(d.Recv) == recv {
					g.renameIdent(d.Name)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						g.rewriteInterfaceMethod(ts)
					}
				}
			}
		}
	}

	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != g.sym.Name {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && g.pkgAliases[id.Name] {
			return true
		}
		g.renameIdent(sel.Sel)
		return true
	})
}

// renameMethodByLine is the fallback when the receiver cannot be resolved:
// it renames the declaration whose name and line match the symbol.
func (g *goRenamer) renameMethodByLine(f *ast.File) {
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != g.sym.Name || g.line(fd.Name.Pos()) != g.sym.Line {
			continue
		}
		g.renameIdent(fd.Name)
		return
	}
}

// targetMethodReceiver resolves the receiver base type of the target method
// by name and declaration line.
func (g *goRenamer) targetMethodReceiver(f *ast.File) string {
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Name.Name != g.sym.Name {
			continue
		}
		if g.line(fd.Name.Pos()) == g.sym.Line {
			return receiverBase(fd.Recv)
		}
	}
	return ""
}

func receiverBase(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	t := recv.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
		case *ast.IndexExpr:
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

// rewriteInterfaceMethod renames interface method declarations matching the
// target method name.
func (g *goRenamer) rewriteInterfaceMethod(ts *ast.TypeSpec) {
	iface, ok := ts.Type.(*ast.InterfaceType)
	if !ok {
		return
	}
	for _, m := range iface.Methods.List {
		for _, n := range m.Names {
			if n.Name == g.sym.Name {
				g.renameIdent(n)
			}
		}
	}
}

// scopeStack is a stack of lexical scopes. Each frame records the identifiers
// declared in that scope; declares reports whether any enclosing frame holds
// the name.
type scopeStack struct {
	frames []map[string]bool
}

func (s *scopeStack) push(names ...string) {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		if n != "" {
			m[n] = true
		}
	}
	s.frames = append(s.frames, m)
}

func (s *scopeStack) pop() {
	if len(s.frames) > 0 {
		s.frames = s.frames[:len(s.frames)-1]
	}
}

func (s *scopeStack) declare(name string) {
	if name == "" || len(s.frames) == 0 {
		return
	}
	s.frames[len(s.frames)-1][name] = true
}

func (s *scopeStack) declares(name string) bool {
	for i := len(s.frames) - 1; i >= 0; i-- {
		if s.frames[i][name] {
			return true
		}
	}
	return false
}

// walkFunc walks a function declaration: its receiver/parameter/result types
// and its body with proper lexical scoping.
func (g *goRenamer) walkFunc(fd *ast.FuncDecl) {
	sc := &scopeStack{}
	sc.push(funcDeclNames(fd)...)
	if fd.Recv != nil {
		for _, f := range fd.Recv.List {
			g.walkTypeExpr(f.Type, sc)
		}
	}
	g.walkFuncType(fd.Type, sc)
	if fd.Body != nil {
		g.walkBlock(fd.Body, sc)
	}
}

func (g *goRenamer) walkBlock(blk *ast.BlockStmt, sc *scopeStack) {
	if blk == nil {
		return
	}
	g.walkStmtList(blk.List, sc)
}

func (g *goRenamer) walkStmtList(list []ast.Stmt, sc *scopeStack) {
	sc.push()
	for _, st := range list {
		g.walkStmt(st, sc)
	}
	sc.pop()
}

func (g *goRenamer) walkStmt(st ast.Stmt, sc *scopeStack) {
	switch s := st.(type) {
	case *ast.BlockStmt:
		g.walkBlock(s, sc)
	case *ast.AssignStmt:
		g.walkAssign(s, sc)
	case *ast.IfStmt:
		if s.Init != nil {
			sc.push()
			g.walkInit(s.Init, sc)
		}
		g.walkExpr(s.Cond, sc)
		g.walkBlock(s.Body, sc)
		if s.Else != nil {
			g.walkStmt(s.Else, sc)
		}
		if s.Init != nil {
			sc.pop()
		}
	case *ast.ForStmt:
		if s.Init != nil {
			sc.push()
			g.walkInit(s.Init, sc)
		}
		g.walkExpr(s.Cond, sc)
		if s.Post != nil {
			g.walkStmt(s.Post, sc)
		}
		g.walkBlock(s.Body, sc)
		if s.Init != nil {
			sc.pop()
		}
	case *ast.RangeStmt:
		g.walkRange(s, sc)
	case *ast.SwitchStmt:
		if s.Init != nil {
			sc.push()
			g.walkInit(s.Init, sc)
		}
		g.walkExpr(s.Tag, sc)
		sc.push()
		for _, c := range s.Body.List {
			g.walkStmt(c, sc)
		}
		sc.pop()
		if s.Init != nil {
			sc.pop()
		}
	case *ast.TypeSwitchStmt:
		g.walkTypeSwitch(s, sc)
	case *ast.SelectStmt:
		sc.push()
		for _, c := range s.Body.List {
			g.walkStmt(c, sc)
		}
		sc.pop()
	case *ast.CaseClause:
		for _, e := range s.List {
			g.walkExpr(e, sc)
		}
		g.walkStmtList(s.Body, sc)
	case *ast.CommClause:
		if s.Comm != nil {
			g.walkStmt(s.Comm, sc)
		}
		g.walkStmtList(s.Body, sc)
	case *ast.LabeledStmt:
		g.walkStmt(s.Stmt, sc)
	default:
		g.walkStmtDefault(s, sc)
	}
}

func (g *goRenamer) walkStmtDefault(st ast.Stmt, sc *scopeStack) {
	switch s := st.(type) {
	case *ast.ExprStmt:
		g.walkExpr(s.X, sc)
	case *ast.ReturnStmt:
		g.walkExprs(s.Results, sc)
	case *ast.DeclStmt:
		if gd, ok := s.Decl.(*ast.GenDecl); ok {
			g.walkGenDecl(gd, sc)
		}
	case *ast.SendStmt:
		g.walkExpr(s.Chan, sc)
		g.walkExpr(s.Value, sc)
	case *ast.IncDecStmt:
		g.walkExpr(s.X, sc)
	case *ast.GoStmt:
		g.walkExpr(s.Call, sc)
	case *ast.DeferStmt:
		g.walkExpr(s.Call, sc)
	default:
		// BranchStmt (label reference), EmptyStmt, FallthroughStmt and
		// BadStmt introduce no value references worth renaming.
	}
}

func (g *goRenamer) walkInit(init ast.Stmt, sc *scopeStack) {
	if as, ok := init.(*ast.AssignStmt); ok {
		g.walkAssign(as, sc)
		return
	}
	g.walkStmt(init, sc)
}

func (g *goRenamer) walkRange(s *ast.RangeStmt, sc *scopeStack) {
	if s.Tok == token.DEFINE {
		g.walkExpr(s.X, sc)
		sc.push()
		for _, v := range []ast.Expr{s.Key, s.Value} {
			if id, ok := v.(*ast.Ident); ok {
				sc.declare(id.Name)
			}
		}
		g.walkBlock(s.Body, sc)
		sc.pop()
		return
	}
	g.walkExpr(s.Key, sc)
	g.walkExpr(s.Value, sc)
	g.walkExpr(s.X, sc)
	g.walkBlock(s.Body, sc)
}

func (g *goRenamer) walkTypeSwitch(s *ast.TypeSwitchStmt, sc *scopeStack) {
	sc.push()
	if as, ok := s.Assign.(*ast.AssignStmt); ok {
		if as.Tok == token.DEFINE {
			for _, lhs := range as.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					sc.declare(id.Name)
				}
			}
			g.walkExprs(as.Rhs, sc)
		} else {
			g.walkAssign(as, sc)
		}
	} else {
		g.walkStmt(s.Assign, sc)
	}
	for _, c := range s.Body.List {
		cc, ok := c.(*ast.CaseClause)
		if !ok {
			continue
		}
		sc.push()
		for _, e := range cc.List {
			g.walkExpr(e, sc)
		}
		g.walkStmtList(cc.Body, sc)
		sc.pop()
	}
	sc.pop()
}

func (g *goRenamer) walkAssign(s *ast.AssignStmt, sc *scopeStack) {
	if s.Tok == token.DEFINE {
		// The RHS is evaluated before the new variables come into scope.
		g.walkExprs(s.Rhs, sc)
		for _, lhs := range s.Lhs {
			if id, ok := lhs.(*ast.Ident); ok {
				sc.declare(id.Name)
			} else {
				g.walkExpr(lhs, sc)
			}
		}
		return
	}
	g.walkExprs(s.Lhs, sc)
	g.walkExprs(s.Rhs, sc)
}

func (g *goRenamer) walkGenDecl(d *ast.GenDecl, sc *scopeStack) {
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.ValueSpec:
			if s.Type != nil {
				g.walkTypeExpr(s.Type, sc)
			}
			g.walkExprs(s.Values, sc)
			for _, n := range s.Names {
				sc.declare(n.Name)
			}
		case *ast.TypeSpec:
			g.walkTypeExpr(s.Type, sc)
			sc.declare(s.Name.Name)
		}
	}
}

func (g *goRenamer) walkExpr(e ast.Expr, sc *scopeStack) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *ast.Ident:
		if x.Name == g.sym.Name && !sc.declares(x.Name) {
			g.renameIdent(x)
		}
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && g.pkgAliases[id.Name] && x.Sel.Name == g.sym.Name {
			g.renameIdent(x.Sel)
			return
		}
		g.walkExpr(x.X, sc)
	case *ast.CallExpr:
		g.walkExpr(x.Fun, sc)
		g.walkExprs(x.Args, sc)
	case *ast.CompositeLit:
		g.walkExpr(x.Type, sc)
		g.walkExprs(x.Elts, sc)
	case *ast.KeyValueExpr:
		if id, ok := x.Key.(*ast.Ident); ok {
			_ = id // composite literal field name, not a value reference
		} else {
			g.walkExpr(x.Key, sc)
		}
		g.walkExpr(x.Value, sc)
	case *ast.FuncLit:
		sc.push(funcTypeNames(x.Type)...)
		g.walkFuncType(x.Type, sc)
		if x.Body != nil {
			g.walkBlock(x.Body, sc)
		}
		sc.pop()
	case *ast.UnaryExpr:
		g.walkExpr(x.X, sc)
	case *ast.BinaryExpr:
		g.walkExpr(x.X, sc)
		g.walkExpr(x.Y, sc)
	case *ast.ParenExpr:
		g.walkExpr(x.X, sc)
	case *ast.IndexExpr:
		g.walkExpr(x.X, sc)
		g.walkExpr(x.Index, sc)
	case *ast.IndexListExpr:
		g.walkExpr(x.X, sc)
		g.walkExprs(x.Indices, sc)
	case *ast.SliceExpr:
		g.walkExpr(x.X, sc)
		g.walkExpr(x.Low, sc)
		g.walkExpr(x.High, sc)
		g.walkExpr(x.Max, sc)
	case *ast.TypeAssertExpr:
		g.walkExpr(x.X, sc)
		if x.Type != nil {
			g.walkTypeExpr(x.Type, sc)
		}
	case *ast.StarExpr:
		g.walkExpr(x.X, sc)
	case *ast.Ellipsis:
		if x.Elt != nil {
			g.walkTypeExpr(x.Elt, sc)
		}
	default:
		g.walkTypeExpr(e, sc)
	}
}

func (g *goRenamer) walkExprs(list []ast.Expr, sc *scopeStack) {
	for _, e := range list {
		g.walkExpr(e, sc)
	}
}

// walkTypeExpr walks type expressions treating identifiers as references,
// except struct field names and interface method names which are
// declarations in their own scope.
func (g *goRenamer) walkTypeExpr(e ast.Expr, sc *scopeStack) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *ast.Ident:
		if x.Name == g.sym.Name && !sc.declares(x.Name) {
			g.renameIdent(x)
		}
	case *ast.StarExpr:
		g.walkTypeExpr(x.X, sc)
	case *ast.ArrayType:
		if x.Len != nil {
			g.walkExpr(x.Len, sc)
		}
		g.walkTypeExpr(x.Elt, sc)
	case *ast.MapType:
		g.walkTypeExpr(x.Key, sc)
		g.walkTypeExpr(x.Value, sc)
	case *ast.ChanType:
		g.walkTypeExpr(x.Value, sc)
	case *ast.StructType:
		for _, f := range x.Fields.List {
			g.walkTypeExpr(f.Type, sc)
		}
	case *ast.InterfaceType:
		for _, m := range x.Methods.List {
			if ft, ok := m.Type.(*ast.FuncType); ok {
				g.walkFuncType(ft, sc)
			}
		}
	case *ast.FuncType:
		g.walkFuncType(x, sc)
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && g.pkgAliases[id.Name] && x.Sel.Name == g.sym.Name {
			g.renameIdent(x.Sel)
			return
		}
		g.walkTypeExpr(x.X, sc)
	case *ast.ParenExpr:
		g.walkTypeExpr(x.X, sc)
	case *ast.Ellipsis:
		g.walkTypeExpr(x.Elt, sc)
	case *ast.IndexExpr:
		g.walkTypeExpr(x.X, sc)
		g.walkExpr(x.Index, sc)
	case *ast.IndexListExpr:
		g.walkTypeExpr(x.X, sc)
		g.walkExprs(x.Indices, sc)
	}
}

// walkFuncType walks function parameter and result types. Parameter names are
// declarations; only their types are visited.
func (g *goRenamer) walkFuncType(ft *ast.FuncType, sc *scopeStack) {
	if ft == nil {
		return
	}
	if ft.TypeParams != nil {
		for _, f := range ft.TypeParams.List {
			g.walkTypeExpr(f.Type, sc)
		}
	}
	if ft.Params != nil {
		for _, f := range ft.Params.List {
			g.walkTypeExpr(f.Type, sc)
		}
	}
	if ft.Results != nil {
		for _, f := range ft.Results.List {
			g.walkTypeExpr(f.Type, sc)
		}
	}
}

func funcDeclNames(fd *ast.FuncDecl) []string {
	var names []string
	if fd.Recv != nil {
		names = append(names, fieldNames(fd.Recv)...)
	}
	names = append(names, funcTypeNames(fd.Type)...)
	return names
}

func funcTypeNames(ft *ast.FuncType) []string {
	var names []string
	if ft == nil {
		return names
	}
	if ft.TypeParams != nil {
		names = append(names, fieldNames(ft.TypeParams)...)
	}
	names = append(names, fieldNames(ft.Params)...)
	names = append(names, fieldNames(ft.Results)...)
	return names
}

func fieldNames(fl *ast.FieldList) []string {
	var names []string
	if fl == nil {
		return names
	}
	for _, f := range fl.List {
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
	}
	return names
}

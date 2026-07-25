package context

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// FilteredFile carries the compact AST-derived representation of a Go source
// file, designed for LLM consumption. It strips function bodies, redundant
// comments, and boilerplate while preserving signatures and structure.
type FilteredFile struct {
	Path    string
	Package string
	Imports []string
	Types   []TypeSig
	Funcs   []FuncSig
	Vars    []string
	Consts  []string
}

// TypeSig is a compact type definition signature.
type TypeSig struct {
	Name     string
	Kind     string // "struct", "interface", "type alias", etc.
	Methods  []FuncSig
	Exported bool
	Comment  string
}

// FuncSig is a compact function/method signature without body.
type FuncSig struct {
	Name     string
	Params   string
	Results  string
	Recv     string
	Exported bool
	Comment  string
}

// FilterGoSource parses a Go source file at the given path and returns a
// compact FilteredFile representation. It strips:
//   - Function/method bodies (keeps only signatures)
//   - Non-doc comments
//   - Long literal blocks
//   - Unreferenced code sections
//
// It preserves:
//   - Package declaration
//   - Import statements
//   - Function/method signatures with receiver, params, results
//   - Type definitions (struct fields as compact list, interface methods)
//   - Const/var declarations (names and types only, not values)
//   - Export status
func FilterGoSource(path string, src []byte) (*FilteredFile, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("ast filter: parse %s: %w", path, err)
	}

	ff := &FilteredFile{
		Path:    path,
		Package: f.Name.Name,
	}

	// Collect imports.
	if f.Imports != nil {
		ff.Imports = make([]string, 0, len(f.Imports))
		for _, imp := range f.Imports {
			if imp.Path != nil {
				path := imp.Path.Value
				if imp.Name != nil {
					ff.Imports = append(ff.Imports, imp.Name.Name+" "+path)
				} else {
					ff.Imports = append(ff.Imports, path)
				}
			}
		}
	}

	// Collect top-level declarations.
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			ff.collectGenDecl(d)
		case *ast.FuncDecl:
			ff.collectFuncDecl(d)
		}
	}

	return ff, nil
}

func (ff *FilteredFile) collectGenDecl(d *ast.GenDecl) {
	switch d.Tok {
	case token.IMPORT:
		return // handled above
	case token.CONST:
		for _, spec := range d.Specs {
			if vs, ok := spec.(*ast.ValueSpec); ok {
				for _, name := range vs.Names {
					repr := name.Name
					if vs.Type != nil {
						repr += " " + typeExprString(vs.Type)
					}
					ff.Consts = append(ff.Consts, repr)
				}
			}
		}
	case token.VAR:
		for _, spec := range d.Specs {
			if vs, ok := spec.(*ast.ValueSpec); ok {
				for _, name := range vs.Names {
					repr := name.Name
					if vs.Type != nil {
						repr += " " + typeExprString(vs.Type)
					}
					ff.Vars = append(ff.Vars, repr)
				}
			}
		}
	case token.TYPE:
		for _, spec := range d.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok {
				ff.collectTypeSpec(ts)
			}
		}
	}
}

func (ff *FilteredFile) collectTypeSpec(ts *ast.TypeSpec) {
	t := TypeSig{
		Name:     ts.Name.Name,
		Exported: ts.Name.IsExported(),
	}
	if ts.Doc != nil {
		t.Comment = commentText(ts.Doc)
	}

	switch tt := ts.Type.(type) {
	case *ast.StructType:
		t.Kind = "struct"
		t.Comment = fieldListCompact(tt.Fields)
	case *ast.InterfaceType:
		t.Kind = "interface"
		t.Comment = interfaceCompact(tt.Methods)
	default:
		t.Kind = typeExprString(ts.Type)
	}

	ff.Types = append(ff.Types, t)
}

func (ff *FilteredFile) collectFuncDecl(d *ast.FuncDecl) {
	f := FuncSig{
		Name:     d.Name.Name,
		Exported: d.Name.IsExported(),
	}
	if d.Doc != nil {
		f.Comment = commentText(d.Doc)
	}
	if d.Recv != nil && len(d.Recv.List) > 0 {
		f.Recv = fieldCompact(d.Recv.List[0])
	}
	if d.Type.Params != nil {
		f.Params = fieldListCompactShort(d.Type.Params)
	}
	if d.Type.Results != nil {
		switch d.Type.Results.NumFields() {
		case 0:
			f.Results = ""
		case 1:
			f.Results = fieldCompact(d.Type.Results.List[0])
		default:
			f.Results = fieldListCompactShort(d.Type.Results)
		}
	}

	ff.Funcs = append(ff.Funcs, f)
}

// FilterSourceForLLM is a convenience wrapper that parses Go source bytes and
// returns the compact filtered representation as a string. If the source cannot
// be parsed (e.g., invalid Go), it returns the original source unchanged to
// avoid data loss.
func FilterSourceForLLM(path string, src []byte) string {
	ff, err := FilterGoSource(path, src)
	if err != nil || ff == nil {
		return string(src)
	}
	return ff.Compact()
}

// Compact renders the filtered file as a concise string for LLM consumption.
// Output format:
//
//	package <name>
//
//	import (
//	  <imports>
//	)
//	<types>
//	<funcs>
func (ff *FilteredFile) Compact() string {
	var b bytes.Buffer

	fmt.Fprintf(&b, "package %s\n\n", ff.Package)

	if len(ff.Imports) > 0 {
		b.WriteString("import (\n")
		for _, imp := range ff.Imports {
			fmt.Fprintf(&b, "  %s\n", imp)
		}
		b.WriteString(")\n\n")
	}

	for _, t := range ff.Types {
		switch t.Kind {
		case "struct":
			fmt.Fprintf(&b, "type %s struct { %s }\n", t.Name, t.Comment)
		case "interface":
			fmt.Fprintf(&b, "type %s interface { %s }\n", t.Name, t.Comment)
		default:
			if t.Comment != "" {
				fmt.Fprintf(&b, "// %s\n", t.Comment)
			}
			fmt.Fprintf(&b, "type %s %s\n", t.Name, t.Kind)
		}
	}

	for _, f := range ff.Funcs {
		if f.Comment != "" {
			b.WriteString(f.Comment)
			b.WriteByte('\n')
		}
		if f.Exported && f.Recv == "" {
			b.WriteString("// exported\n")
		}
		b.WriteString("func ")
		if f.Recv != "" {
			fmt.Fprintf(&b, "(%s) ", f.Recv)
		}
		fmt.Fprintf(&b, "%s(%s)", f.Name, f.Params)
		if f.Results != "" {
			fmt.Fprintf(&b, " %s", f.Results)
		}
		b.WriteString("\n\n")
	}

	for _, c := range ff.Consts {
		fmt.Fprintf(&b, "const %s\n", c)
	}
	for _, v := range ff.Vars {
		fmt.Fprintf(&b, "var %s\n", v)
	}

	return strings.TrimSpace(b.String())
}

// ── helpers ───────────────────────────────────────────────────────────

func typeExprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return typeExprString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + typeExprString(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + typeExprString(t.Elt)
		}
		return "[...]" + typeExprString(t.Elt)
	case *ast.MapType:
		return "map[" + typeExprString(t.Key) + "]" + typeExprString(t.Value)
	case *ast.ChanType:
		return "chan " + typeExprString(t.Value)
	case *ast.FuncType:
		return "func(" + fieldListCompactShort(t.Params) + ")" + resultString(t.Results)
	case *ast.InterfaceType:
		return "interface{...}"
	case *ast.StructType:
		return "struct{...}"
	case *ast.Ellipsis:
		return "..." + typeExprString(t.Elt)
	default:
		return fmt.Sprintf("%T", expr)
	}
}

func resultString(fields *ast.FieldList) string {
	if fields == nil || fields.NumFields() == 0 {
		return ""
	}
	if fields.NumFields() == 1 {
		return " " + fieldCompact(fields.List[0])
	}
	return " (" + fieldListCompactShort(fields) + ")"
}

func fieldListCompact(fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	parts := make([]string, 0, len(fl.List))
	for _, f := range fl.List {
		parts = append(parts, fieldCompact(f))
	}
	return strings.Join(parts, "; ")
}

func fieldListCompactShort(fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	parts := make([]string, 0, len(fl.List))
	for _, f := range fl.List {
		parts = append(parts, fieldCompact(f))
	}
	return strings.Join(parts, ", ")
}

func fieldCompact(f *ast.Field) string {
	var names string
	if len(f.Names) > 0 {
		n := make([]string, len(f.Names))
		for i, name := range f.Names {
			n[i] = name.Name
		}
		names = strings.Join(n, ", ") + " "
	}
	return names + typeExprString(f.Type)
}

func interfaceCompact(fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	parts := make([]string, 0, len(fl.List))
	for _, f := range fl.List {
		s := fieldCompact(f)
		parts = append(parts, s+";")
	}
	return strings.Join(parts, " ")
}

func commentText(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	var b bytes.Buffer
	for _, c := range cg.List {
		text := strings.TrimLeft(c.Text, "/ ")
		if text != "" {
			b.WriteString("// ")
			b.WriteString(text)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

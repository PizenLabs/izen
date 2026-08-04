package layer3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/PizenLabs/izen/pkg/engine/layer2"
)

// Sentinel errors returned by the ASTRewriteHandler.
var (
	ErrUnsupportedFormat   = errors.New("layer3: no formatter for language")
	ErrUnsupportedLanguage = errors.New("layer3: unsupported source language")
	ErrUnsupportedIntent   = errors.New("layer3: intent not handled by the AST rewriter")
	ErrTargetNotFound      = errors.New("layer3: target not found in workspace")
	ErrSymbolNotFound      = errors.New("layer3: symbol not found")
	ErrParse               = errors.New("layer3: source parse failure")
	ErrRenamedSameName     = fmt.Errorf("%w: new name must differ from old name", ErrInvalidRequest)
)

// OpType classifies a deterministic mutation produced by the rewriter.
type OpType string

const (
	// OpRename is a symbol rename.
	OpRename OpType = "rename"
	// OpFormat is a source formatting pass.
	OpFormat OpType = "format"
	// OpAddImport is an explicit import insertion.
	OpAddImport OpType = "add_import"
	// OpRemoveImport is an unused import removal.
	OpRemoveImport OpType = "remove_import"
)

// FilePatch is a proposed before/after view of a single file. It carries both
// contents so a downstream apply step can write or diff them; the rewriter
// itself never writes to disk.
type FilePatch struct {
	Path         string
	Language     string
	Old          string
	New          string
	LinesAdded   int
	LinesRemoved int
	Changed      bool
}

// PatchOp records a single deterministic mutation.
type PatchOp struct {
	Type   OpType
	Path   string
	Detail string
}

// PatchResult is the structured output of a deterministic rewrite. It is
// immutable after construction; callers must not mutate its slices.
type PatchResult struct {
	Ops     []PatchOp
	Files   []FilePatch
	Changed bool
	Skipped int
}

// ChangedCount returns the number of files with non-empty patches.
func (r *PatchResult) ChangedCount() int {
	if r == nil {
		return 0
	}
	n := 0
	for _, f := range r.Files {
		if f.Changed {
			n++
		}
	}
	return n
}

// FormatFunc formats a source file in place. language is the lowercase
// language id (e.g. "typescript"), path the relative file path and src the
// original content. It returns the formatted content.
type FormatFunc func(ctx context.Context, language, path string, src []byte) ([]byte, error)

// ASTRewriteHandler performs deterministic, in-process AST mutations over the
// Layer 2 SoR. It is immutable after construction and safe for concurrent
// use. All operations read the workspace and return PatchResult values; the
// handler never invokes an LLM and never mutates system state directly
// (patches are applied by the caller or by ApplyPatches).
type ASTRewriteHandler struct {
	sor       *layer2.Sor
	formatter FormatFunc
}

// ASTRewriteHandlerOption configures an ASTRewriteHandler.
type ASTRewriteHandlerOption func(*ASTRewriteHandler)

// WithFormatter installs the formatter used for non-Go languages.
func WithFormatter(f FormatFunc) ASTRewriteHandlerOption {
	return func(h *ASTRewriteHandler) { h.formatter = f }
}

// NewASTRewriteHandler returns a handler backed by the given SoR. A nil Sor
// is allowed at construction; operations return ErrInvalidRequest until one
// is provided via WithSor.
func NewASTRewriteHandler(sor *layer2.Sor, opts ...ASTRewriteHandlerOption) *ASTRewriteHandler {
	h := &ASTRewriteHandler{sor: sor}
	for _, o := range opts {
		o(h)
	}
	return h
}

// WithSor attaches the Layer 2 SoR backing the handler. It exists for
// composition when the SoR is constructed after the handler.
func (h *ASTRewriteHandler) WithSor(sor *layer2.Sor) *ASTRewriteHandler {
	h.sor = sor
	return h
}

// Sor returns the SoR the handler reads from.
func (h *ASTRewriteHandler) Sor() *layer2.Sor { return h.sor }

// Handle dispatches a deterministic request to the matching mutation. It
// returns ErrUnsupportedIntent when the request is not deterministic.
func (h *ASTRewriteHandler) Handle(ctx context.Context, req Request) (*PatchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch req.Intent {
	case IntentRename:
		return h.RenameSymbol(ctx, RenameRequest{
			Name:    req.TargetSymbol,
			NewName: req.NewName,
			Paths:   req.Scope,
		})
	case IntentFormat:
		return h.FormatFile(ctx, req.TargetFile)
	case IntentAddImport:
		return h.AddImport(ctx, req.TargetFile, req.NewImport)
	case IntentRemoveImport:
		return h.RemoveUnusedImports(ctx, req.TargetFile)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedIntent, req.Intent)
	}
}

// RenameRequest describes a precise cross-file symbol rename.
type RenameRequest struct {
	// Name is the simple symbol name to rename.
	Name string
	// QualName optionally disambiguates when Name is ambiguous.
	QualName string
	// NewName is the replacement identifier.
	NewName string
	// Paths optionally restricts the rewrite to the given files. When empty
	// the impact set is derived from the SoR (declaration file plus
	// dependents).
	Paths []string
}

// RenameSymbol renames the resolved symbol to NewName across its impact set.
// Go files are rewritten with a lexically-scoped AST pass that respects
// shadowing; other languages use a token-aware rewriter that skips comments
// and string literals.
func (h *ASTRewriteHandler) RenameSymbol(ctx context.Context, req RenameRequest) (*PatchResult, error) {
	if req.Name == "" || req.NewName == "" {
		return nil, fmt.Errorf("%w: rename requires name and new name", ErrInvalidRequest)
	}
	if req.Name == req.NewName {
		return nil, ErrRenamedSameName
	}
	if h.sor == nil {
		return nil, fmt.Errorf("%w: rewriter has no sor", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sym, err := h.resolveSymbol(req)
	if err != nil {
		return nil, err
	}
	if sym.File == "" {
		return nil, fmt.Errorf("%w: %q", ErrSymbolNotFound, req.Name)
	}

	scope, err := h.renameScope(sym.File, req.Paths)
	if err != nil {
		return nil, err
	}

	result := &PatchResult{}
	for _, path := range scope {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		src, err := h.sor.Source(path)
		if err != nil {
			result.Skipped++
			continue
		}
		lang := h.sor.Language(path)
		var out []byte
		var changed bool
		switch lang {
		case "go":
			out, changed, err = h.goRename(src, sym, req.NewName)
		default:
			if lang == "" {
				result.Skipped++
				continue
			}
			out, changed, err = tokenRename(lang, src, req.Name, req.NewName)
		}
		if err != nil {
			return nil, err
		}
		if !changed {
			result.Skipped++
			continue
		}
		result.Files = append(result.Files, buildPatch(path, lang, src, out))
		result.Ops = append(result.Ops, PatchOp{
			Type:   OpRename,
			Path:   path,
			Detail: req.Name + " -> " + req.NewName,
		})
		result.Changed = true
	}

	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	sort.Slice(result.Ops, func(i, j int) bool {
		if result.Ops[i].Path != result.Ops[j].Path {
			return result.Ops[i].Path < result.Ops[j].Path
		}
		return result.Ops[i].Type < result.Ops[j].Type
	})
	return result, nil
}

// resolveSymbol resolves a rename target to its representative symbol.
func (h *ASTRewriteHandler) resolveSymbol(req RenameRequest) (layer2.SymbolInfo, error) {
	if req.QualName != "" {
		if si, ok := h.sor.LookupQual(req.QualName); ok {
			return si, nil
		}
	}
	if si, ok := h.sor.Symbol(req.Name); ok {
		return si, nil
	}
	return layer2.SymbolInfo{}, fmt.Errorf("%w: %q", ErrSymbolNotFound, req.Name)
}

// renameScope computes the impact set of a rename: the declaration file plus
// either the explicitly requested paths or the SoR dependents.
func (h *ASTRewriteHandler) renameScope(declFile string, paths []string) ([]string, error) {
	seen := make(map[string]bool)
	if declFile != "" {
		seen[declFile] = true
	}
	if len(paths) > 0 {
		for _, p := range paths {
			seen[filepath.ToSlash(p)] = true
		}
	} else {
		for _, d := range h.sor.Dependents(declFile) {
			seen[d] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// FormatFile reformats the given file. Go sources are formatted with the Go
// standard formatter; other languages use the injected FormatFunc.
func (h *ASTRewriteHandler) FormatFile(ctx context.Context, path string) (*PatchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h.sor == nil {
		return nil, fmt.Errorf("%w: rewriter has no sor", ErrInvalidRequest)
	}
	src, err := h.sor.Source(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrTargetNotFound, path, err)
	}
	lang := h.sor.Language(path)
	var out []byte
	switch lang {
	case "go":
		out, err = formatGo(src)
	default:
		if lang == "" {
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedLanguage, path)
		}
		if h.formatter == nil {
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, lang)
		}
		out, err = h.formatter(ctx, lang, path, src)
	}
	if err != nil {
		return nil, err
	}
	if bytes.Equal(out, src) {
		return &PatchResult{Skipped: 1}, nil
	}
	patch := buildPatch(path, lang, src, out)
	return &PatchResult{
		Changed: true,
		Files:   []FilePatch{patch},
		Ops:     []PatchOp{{Type: OpFormat, Path: path, Detail: lang}},
	}, nil
}

// AddImport inserts an explicit import into a source file. Inserting an
// import that is already present is a no-op.
func (h *ASTRewriteHandler) AddImport(ctx context.Context, path, importPath string) (*PatchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if importPath == "" {
		return nil, fmt.Errorf("%w: import path required", ErrInvalidRequest)
	}
	if h.sor == nil {
		return nil, fmt.Errorf("%w: rewriter has no sor", ErrInvalidRequest)
	}
	src, err := h.sor.Source(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrTargetNotFound, path, err)
	}
	lang := h.sor.Language(path)
	switch lang {
	case "go":
		return h.addGoImport(src, path, importPath)
	case "typescript", "javascript":
		return addTSImport(src, path, importPath)
	default:
		if lang == "" {
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedLanguage, path)
		}
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedLanguage, lang)
	}
}

// RemoveUnusedImports removes imports whose package is not referenced by the
// file body. Blank and dot imports are preserved because they carry side
// effects. Currently only Go sources are supported.
func (h *ASTRewriteHandler) RemoveUnusedImports(ctx context.Context, path string) (*PatchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h.sor == nil {
		return nil, fmt.Errorf("%w: rewriter has no sor", ErrInvalidRequest)
	}
	src, err := h.sor.Source(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrTargetNotFound, path, err)
	}
	lang := h.sor.Language(path)
	if lang != "go" {
		if lang == "" {
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedLanguage, path)
		}
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedLanguage, lang)
	}
	return h.removeGoImports(src, path)
}

func formatGo(src []byte) ([]byte, error) {
	out, err := format.Source(src)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParse, err)
	}
	return out, nil
}

// addGoImport inserts an import spec via the Go AST and re-formats the file.
func (h *ASTRewriteHandler) addGoImport(src []byte, path, importPath string) (*PatchResult, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrParse, path, err)
	}
	if hasImport(f, importPath) {
		return &PatchResult{Skipped: 1}, nil
	}

	imp := &ast.ImportSpec{Path: &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(importPath)}}
	decl, ok := findImportDecl(f)
	if !ok {
		decl = &ast.GenDecl{Tok: token.IMPORT, Specs: []ast.Spec{imp}}
		f.Decls = append([]ast.Decl{decl}, f.Decls...)
	} else {
		decl.Specs = append(decl.Specs, imp)
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	if bytes.Equal(buf.Bytes(), src) {
		return &PatchResult{Skipped: 1}, nil
	}
	patch := buildPatch(path, "go", src, buf.Bytes())
	return &PatchResult{
		Changed: true,
		Files:   []FilePatch{patch},
		Ops:     []PatchOp{{Type: OpAddImport, Path: path, Detail: importPath}},
	}, nil
}

// removeGoImports drops unused import specs from the file AST.
func (h *ASTRewriteHandler) removeGoImports(src []byte, path string) (*PatchResult, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrParse, path, err)
	}
	if len(f.Imports) == 0 {
		return &PatchResult{Skipped: 1}, nil
	}

	used := usedImports(f)
	removed := removeUnusedImportSpecs(f, used)
	if removed == 0 {
		return &PatchResult{Skipped: 1}, nil
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	patch := buildPatch(path, "go", src, buf.Bytes())
	return &PatchResult{
		Changed: true,
		Files:   []FilePatch{patch},
		Ops:     []PatchOp{{Type: OpRemoveImport, Path: path, Detail: strconv.Itoa(removed)}},
	}, nil
}

// usedImports returns the set of import paths referenced by the file body.
func usedImports(f *ast.File) map[string]bool {
	aliasToPath := make(map[string]string)
	for _, imp := range f.Imports {
		name := defaultImportName(imp)
		if name == "" || name == "_" || name == "." {
			continue
		}
		aliasToPath[name] = unquote(imp.Path.Value)
	}
	used := make(map[string]bool)
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if p, ok := aliasToPath[id.Name]; ok {
			used[p] = true
		}
		return true
	})
	return used
}

// removeUnusedImportSpecs rebuilds the import declarations, dropping unused
// specs while preserving blank and dot imports. It returns the number of
// removed imports.
func removeUnusedImportSpecs(f *ast.File, used map[string]bool) int {
	removed := 0
	keptDecls := f.Decls[:0]
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			keptDecls = append(keptDecls, d)
			continue
		}
		keptSpecs := gd.Specs[:0]
		for _, sp := range gd.Specs {
			imp, ok := sp.(*ast.ImportSpec)
			if !ok {
				keptSpecs = append(keptSpecs, sp)
				continue
			}
			name := defaultImportName(imp)
			if name == "_" || name == "." || used[unquote(imp.Path.Value)] {
				keptSpecs = append(keptSpecs, sp)
				continue
			}
			removed++
		}
		if len(keptSpecs) > 0 {
			gd.Specs = keptSpecs
			keptDecls = append(keptDecls, gd)
		}
	}
	f.Decls = keptDecls
	return removed
}

// addTSImport inserts a side-effect import for importPath after the leading
// import region of a TypeScript/JavaScript file.
func addTSImport(src []byte, path, importPath string) (*PatchResult, error) {
	text := string(src)
	if strings.Contains(text, `"`+importPath+`"`) {
		return &PatchResult{Skipped: 1}, nil
	}
	lines := strings.Split(text, "\n")
	insertAt := len(lines)
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") ||
			strings.HasPrefix(t, "*") || strings.HasPrefix(t, "import ") ||
			strings.HasPrefix(t, "from ") || strings.HasPrefix(t, "} from") || t == "}":
			continue
		default:
			insertAt = i
		}
		break
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:insertAt]...)
	out = append(out, `import "`+importPath+`";`)
	out = append(out, lines[insertAt:]...)
	newText := strings.Join(out, "\n")
	patch := buildPatch(path, "typescript", src, []byte(newText))
	return &PatchResult{
		Changed: true,
		Files:   []FilePatch{patch},
		Ops:     []PatchOp{{Type: OpAddImport, Path: path, Detail: importPath}},
	}, nil
}

func findImportDecl(f *ast.File) (*ast.GenDecl, bool) {
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			return gd, true
		}
	}
	return nil, false
}

func hasImport(f *ast.File, importPath string) bool {
	for _, imp := range f.Imports {
		if unquote(imp.Path.Value) == importPath {
			return true
		}
	}
	return false
}

func defaultImportName(imp *ast.ImportSpec) string {
	if imp.Name != nil {
		switch imp.Name.Name {
		case "_", ".":
			return ""
		default:
			return imp.Name.Name
		}
	}
	base := unquote(imp.Path.Value)
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	return base
}

func unquote(s string) string {
	u, err := strconv.Unquote(s)
	if err != nil {
		return strings.Trim(s, `"`)
	}
	return u
}

func buildPatch(path, lang string, oldSrc, newSrc []byte) FilePatch {
	added, removed := diffLineCounts(string(oldSrc), string(newSrc))
	return FilePatch{
		Path:         path,
		Language:     lang,
		Old:          string(oldSrc),
		New:          string(newSrc),
		LinesAdded:   added,
		LinesRemoved: removed,
		Changed:      !bytes.Equal(oldSrc, newSrc),
	}
}

func diffLineCounts(oldS, newS string) (added, removed int) {
	oldLines := strings.Count(oldS, "\n")
	newLines := strings.Count(newS, "\n")
	if newLines > oldLines {
		return newLines - oldLines, 0
	}
	return 0, oldLines - newLines
}

// ApplyResult reports an ApplyPatches pass.
type ApplyResult struct {
	Applied int
}

// ApplyPatches writes the proposed file contents to disk relative to root.
// It is the only writer in this package: every mutation returns a PatchResult
// and application is an explicit, deterministic step. Patches whose paths
// escape root are rejected.
func ApplyPatches(ctx context.Context, root string, patches []FilePatch) (*ApplyResult, error) {
	applied := 0
	for _, p := range patches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !p.Changed || p.Path == "" {
			continue
		}
		abs, err := resolveWithin(root, p.Path)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(abs, []byte(p.New), 0o644); err != nil {
			return nil, err
		}
		applied++
	}
	return &ApplyResult{Applied: applied}, nil
}

// resolveWithin resolves a relative path against root and rejects escapes.
func resolveWithin(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("%w: empty patch path", ErrInvalidRequest)
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(base, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	r, err := filepath.Rel(base, full)
	if err != nil {
		return "", err
	}
	if r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: patch path escapes root: %q", ErrInvalidRequest, rel)
	}
	return full, nil
}

// tokenRename replaces identifier tokens equal to oldName with newName,
// skipping comments and string literals. It is used for languages without an
// in-process AST (TypeScript, JavaScript, Python, ...); precision is
// token-level rather than scope-level.
func tokenRename(lang string, src []byte, oldName, newName string) ([]byte, bool, error) {
	var out bytes.Buffer
	out.Grow(len(src))
	changed := false
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		switch c {
		case '"', '\'', '`':
			j := skipString(src, i, c)
			out.Write(src[i:j])
			i = j
		case '/':
			if i+1 < n && src[i+1] == '/' {
				j := bytes.IndexByte(src[i:], '\n')
				if j < 0 {
					j = n - i
				} else {
					j++
				}
				out.Write(src[i : i+j])
				i += j
				continue
			}
			if i+1 < n && src[i+1] == '*' {
				j := bytes.Index(src[i+2:], []byte("*/"))
				if j < 0 {
					out.Write(src[i:])
					i = n
					continue
				}
				j += 2 + 2
				out.Write(src[i : i+j])
				i += j
				continue
			}
			out.WriteByte(c)
			i++
		case '#':
			if lang == "python" {
				j := bytes.IndexByte(src[i:], '\n')
				if j < 0 {
					j = n - i
				} else {
					j++
				}
				out.Write(src[i : i+j])
				i += j
				continue
			}
			out.WriteByte(c)
			i++
		default:
			if isIdentStart(c) {
				j := i
				for j < n && isIdentPart(src[j]) {
					j++
				}
				if string(src[i:j]) == oldName {
					out.WriteString(newName)
					changed = true
				} else {
					out.Write(src[i:j])
				}
				i = j
				continue
			}
			out.WriteByte(c)
			i++
		}
	}
	return out.Bytes(), changed, nil
}

func skipString(src []byte, i int, quote byte) int {
	n := len(src)
	for i++; i < n; i++ {
		switch src[i] {
		case '\\':
			i++
		case quote:
			return i + 1
		case '\n':
			if quote != '`' {
				return i
			}
		}
	}
	return n
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

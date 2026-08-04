package layer4

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/pkg/engine/layer2"
)

// StructuralValidator is the zero-CLI, RAM-only structural validator of the
// validation engine. It inspects a proposed mutation set entirely in memory
// over the Layer 2 SoR:
//
//	syntax errors       - every affected Go source is parsed in RAM.
//	broken imports      - in-repo imports must resolve to a surviving package.
//	dangling references - references to deleted/renamed symbols are reported
//	                      at the referencing file, along with path containment.
//
// It never shells out to a command and never writes to disk. A
// StructuralValidator is immutable after construction and safe for concurrent
// use; each Validate call builds its own read-only view of the workspace.
type StructuralValidator struct {
	sor  *layer2.Sor
	root string
}

// StructuralOption configures a StructuralValidator.
type StructuralOption func(*StructuralValidator)

// WithStructuralRoot sets the workspace root used for path containment and
// go.mod discovery.
func WithStructuralRoot(root string) StructuralOption {
	return func(v *StructuralValidator) {
		if root != "" {
			v.root = root
		}
	}
}

// NewStructuralValidator returns a structural validator over the given SoR.
// The SoR is the canonical System of Record: it supplies the indexed symbol
// table, call graph and package layout. When sor is nil, Validate returns
// ErrNoSor.
func NewStructuralValidator(sor *layer2.Sor, opts ...StructuralOption) *StructuralValidator {
	v := &StructuralValidator{sor: sor}
	if sor != nil {
		v.root = sor.Root()
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

// Name implements Validator.
func (v *StructuralValidator) Name() string { return "structural" }

// Validate implements Validator. It returns a single structured result: the
// first structural defect found, or a passing result when the proposed state
// is structurally sound.
func (v *StructuralValidator) Validate(ctx context.Context, patches []Patch) (*ValidationResult, error) {
	if v.sor == nil {
		return nil, ErrNoSor
	}
	view, err := v.buildView(patches)
	if err != nil {
		return failStructural(err.Error()), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	module := v.moduleName()
	affected := view.affectedFiles(v.sor)

	// Check 1: syntax errors, in RAM, over every affected Go file.
	for _, path := range affected {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !isGoPath(path) {
			continue
		}
		content, ok := view.source(path)
		if !ok {
			continue
		}
		if loc := goParseError(path, []byte(content)); loc != "" {
			return failStructural(loc + ": syntax error"), nil
		}
	}

	// Check 2: broken imports in the proposed state.
	if loc := v.checkImports(view, affected, module); loc != "" {
		return failStructural(loc + ": broken import"), nil
	}

	// Check 3: dangling references to deleted files and symbols.
	if loc := v.checkDangling(view, affected, module); loc != "" {
		return failStructural(loc + ": dangling reference"), nil
	}

	return resultPass(StageStructural, fmt.Sprintf("%d file(s) structurally sound", len(view.content))), nil
}

// wsView is the proposed workspace state produced by overlaying a mutation
// set onto the SoR. It is immutable after construction.
type wsView struct {
	content map[string]string
	old     map[string]string
	deleted map[string]bool
	patched map[string]bool
}

// buildView overlays the patches onto the indexed workspace. Deletions remove
// a file; additions and edits replace its content. The pre-patch content of a
// patched file is retained in old for rename/deletion analysis.
func (v *StructuralValidator) buildView(patches []Patch) (*wsView, error) {
	view := &wsView{
		content: make(map[string]string),
		old:     make(map[string]string),
		deleted: make(map[string]bool),
		patched: make(map[string]bool),
	}
	for _, p := range patches {
		if p.Path == "" {
			return nil, fmt.Errorf("empty patch path")
		}
		if p.Path != filepath.ToSlash(filepath.Clean(p.Path)) {
			return nil, fmt.Errorf("non-clean patch path %q", p.Path)
		}
		if v.root != "" {
			if _, err := resolveWithin(v.root, p.Path); err != nil {
				return nil, err
			}
		}
		if p.New == "" && p.Changed {
			view.deleted[p.Path] = true
			delete(view.content, p.Path)
			continue
		}
		view.content[p.Path] = p.New
		view.patched[p.Path] = true
		if p.Old != "" {
			view.old[p.Path] = p.Old
		}
	}
	return view, nil
}

// source returns the proposed content of a path: patched content first, then
// the indexed on-disk content. ok is false when neither exists.
func (view *wsView) source(path string) (string, bool) {
	c, ok := view.content[path]
	return c, ok
}

// files returns every path present in the proposed state: patched files plus
// surviving indexed files.
func (view *wsView) files(sor *layer2.Sor) []string {
	seen := make(map[string]bool)
	for _, f := range sor.Files() {
		if !view.deleted[f] {
			seen[f] = true
		}
	}
	for f := range view.content {
		seen[f] = true
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// pkgDirs returns the surviving in-repo package directories of the proposed
// state, including directories introduced by new patched files.
func (view *wsView) pkgDirs(sor *layer2.Sor) []string {
	seen := make(map[string]bool)
	for _, f := range view.files(sor) {
		if dir := packageDir(f); dir != "" {
			seen[dir] = true
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// deadPackages returns the package directories with no surviving files.
func (view *wsView) deadPackages(sor *layer2.Sor) []string {
	alive := make(map[string]bool)
	for _, f := range sor.Files() {
		if !view.deleted[f] {
			alive[packageDir(f)] = true
		}
	}
	for f := range view.content {
		alive[packageDir(f)] = true
	}
	var dead []string
	for _, d := range view.pkgDirs(sor) {
		if !alive[d] {
			dead = append(dead, d)
		}
	}
	sort.Strings(dead)
	return dead
}

// affectedFiles returns the files whose validity the mutation set can change:
// patched files, dependents of deleted files, and files importing a package
// that no longer survives.
func (view *wsView) affectedFiles(sor *layer2.Sor) []string {
	seen := make(map[string]bool)
	for f := range view.patched {
		seen[f] = true
	}
	dead := view.deadPackages(sor)
	for _, f := range view.files(sor) {
		if view.deleted[f] {
			continue
		}
		for imp := range view.importsOf(sor, f) {
			if resolvePkgDir(imp, dead) != "" {
				seen[f] = true
			}
		}
	}
	for del := range view.deleted {
		for _, d := range sor.Dependents(del) {
			if !view.deleted[d] {
				seen[d] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// importsOf returns the raw import paths a file declares in the proposed
// state, parsed in RAM.
func (view *wsView) importsOf(sor *layer2.Sor, path string) map[string]bool {
	out := make(map[string]bool)
	if content, ok := view.content[path]; ok {
		collectImports([]byte(content), out)
		return out
	}
	src, err := sor.Source(path)
	if err != nil {
		return out
	}
	collectImports(src, out)
	return out
}

// collectImports fills out with every import path declared by src.
func collectImports(src []byte, out map[string]bool) {
	f, err := parser.ParseFile(token.NewFileSet(), "collect.go", src, parser.SkipObjectResolution)
	if err != nil {
		return
	}
	for _, imp := range f.Imports {
		if p := unquoteImport(imp.Path.Value); p != "" {
			out[p] = true
		}
	}
}

// checkImports reports the location of the first broken import, if any. An
// import is broken when it is in-repo (module-prefixed or matching a known
// package) yet resolves to no surviving package directory.
func (v *StructuralValidator) checkImports(view *wsView, affected []string, module string) string {
	surviving := view.pkgDirs(v.sor)
	for _, path := range affected {
		imports := view.importsOf(v.sor, path)
		paths := make([]string, 0, len(imports))
		for imp := range imports {
			paths = append(paths, imp)
		}
		sort.Strings(paths)
		for _, imp := range paths {
			if resolvePkgDir(imp, surviving) != "" {
				continue
			}
			if !isInRepoImport(imp, module, surviving) {
				continue
			}
			return positionOfImport(path, imp, v.contentFor(view, path))
		}
	}
	return ""
}

// contentFor returns the proposed content of a file, or the on-disk content.
func (v *StructuralValidator) contentFor(view *wsView, path string) string {
	if c, ok := view.content[path]; ok {
		return c
	}
	src, err := v.sor.Source(path)
	if err != nil {
		return ""
	}
	return string(src)
}

// checkDangling reports the location of the first dangling reference, if any.
// Dangling references are references to symbols or files that the mutation
// set deletes: files that still import a deleted package, unpatched callers
// of deleted symbols, and qualified references to missing symbols in patched
// files.
func (v *StructuralValidator) checkDangling(view *wsView, affected []string, module string) string {
	dead := view.deadPackages(v.sor)
	surviving := view.pkgDirs(v.sor)

	// Deleted files: every affected file still importing a dead package.
	for _, path := range affected {
		if view.deleted[path] {
			continue
		}
		for imp := range view.importsOf(v.sor, path) {
			if resolvePkgDir(imp, dead) != "" {
				return positionOfImport(path, imp, v.contentFor(view, path))
			}
		}
	}

	// Deleted symbols: unpatched callers of a symbol removed by a patch.
	// Renames and in-place deletions are detected by diffing declared names.
	for _, path := range view.patchedPaths() {
		if !isGoPath(path) {
			continue
		}
		oldSrc, ok := v.oldSource(view, path)
		if !ok {
			continue
		}
		newSrc := view.content[path]
		for name := range deletedNames(oldSrc, newSrc) {
			pkg := packageDir(path)
			for _, c := range v.sor.Callers(name) {
				if c.File == path || view.deleted[c.File] || view.patched[c.File] {
					continue
				}
				if packageDir(c.File) != pkg && !v.fileImportsPackage(view, c.File, pkg) {
					continue
				}
				return fmt.Sprintf("%s:%d: references deleted symbol %q", c.File, c.Line, name)
			}
		}
	}

	// Qualified references in patched files: alias.Symbol must exist in the
	// target package's proposed symbol set.
	for _, path := range view.patchedPaths() {
		if !isGoPath(path) {
			continue
		}
		if loc := missingQualifiedReference(view, v.sor, path, surviving, module); loc != "" {
			return loc
		}
	}
	return ""
}

// patchedPaths returns the patched file paths in sorted order.
func (view *wsView) patchedPaths() []string {
	out := make([]string, 0, len(view.patched))
	for f := range view.patched {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// oldSource returns the pre-patch content of a patched file: the patch's Old
// content when present, otherwise the indexed on-disk content.
func (v *StructuralValidator) oldSource(view *wsView, path string) (string, bool) {
	if old, ok := view.old[path]; ok && old != "" {
		return old, true
	}
	src, err := v.sor.Source(path)
	if err != nil {
		return "", false
	}
	return string(src), true
}

// fileImportsPackage reports whether a file's proposed content imports pkgDir.
func (v *StructuralValidator) fileImportsPackage(view *wsView, path, pkgDir string) bool {
	if pkgDir == "" {
		return false
	}
	for imp := range view.importsOf(v.sor, path) {
		if resolvePkgDir(imp, []string{pkgDir}) == pkgDir {
			return true
		}
	}
	return false
}

// missingQualifiedReference scans a patched file for alias.Symbol references
// whose target symbol does not exist in the target package's proposed state.
func missingQualifiedReference(view *wsView, sor *layer2.Sor, path string, surviving []string, module string) string {
	content := view.content[path]
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, []byte(content), parser.SkipObjectResolution)
	if err != nil {
		return ""
	}
	aliases := importAliases(f)
	self := declaredNames(f)
	pkgSyms := make(map[string]map[string]bool)
	names := func(pkgDir string) map[string]bool {
		if syms, ok := pkgSyms[pkgDir]; ok {
			return syms
		}
		syms := packageSymbols(view, sor, pkgDir)
		pkgSyms[pkgDir] = syms
		return syms
	}
	for _, sel := range selectorsWithIdentX(f) {
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			continue
		}
		imp, ok := aliases[x.Name]
		if !ok {
			continue
		}
		if self[sel.Sel.Name] {
			continue
		}
		pkgDir := resolvePkgDir(imp, surviving)
		if pkgDir == "" || !isInRepoImport(imp, module, surviving) {
			continue
		}
		if !names(pkgDir)[sel.Sel.Name] {
			pos := fset.Position(sel.Sel.Pos())
			return fmt.Sprintf("%s:%d:%d: %s.%s references missing symbol", path, pos.Line, pos.Column, x.Name, sel.Sel.Name)
		}
	}
	return ""
}

// moduleName returns the workspace module path parsed from go.mod, empty when
// unavailable.
func (v *StructuralValidator) moduleName() string {
	if v.root == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(v.root, "go.mod"))
	if err != nil {
		return ""
	}
	return parseModuleName(data)
}

// parseModuleName extracts the "module <path>" declaration from go.mod.
func parseModuleName(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if !strings.HasPrefix(line, "module") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		if i := strings.Index(rest, "//"); i >= 0 {
			rest = strings.TrimSpace(rest[:i])
		}
		rest = strings.Trim(rest, `"`)
		if rest == "" {
			continue
		}
		return rest
	}
	return ""
}

// goParseError parses src as Go and returns a "path:line:col" location of the
// first syntax error, or "" when the source parses cleanly.
func goParseError(path string, src []byte) string {
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err == nil {
		return ""
	}
	var el scanner.ErrorList
	if errors.As(err, &el) && len(el) > 0 {
		p := el[0].Pos
		return fmt.Sprintf("%s:%d:%d", p.Filename, p.Line, p.Column)
	}
	return path + ":parse error"
}

// deletedNames returns top-level names declared in oldSrc but absent from
// newSrc, i.e. symbols removed or renamed by a patch.
func deletedNames(oldSrc, newSrc string) map[string]bool {
	oldF, errOld := parser.ParseFile(token.NewFileSet(), "old.go", []byte(oldSrc), parser.SkipObjectResolution)
	newF, errNew := parser.ParseFile(token.NewFileSet(), "new.go", []byte(newSrc), parser.SkipObjectResolution)
	if errOld != nil || errNew != nil {
		return nil
	}
	oldNames := declaredNames(oldF)
	newNames := declaredNames(newF)
	out := make(map[string]bool)
	for n := range oldNames {
		if !newNames[n] {
			out[n] = true
		}
	}
	return out
}

// declaredNames collects the top-level identifiers declared by a parsed file.
func declaredNames(f *ast.File) map[string]bool {
	out := make(map[string]bool)
	for _, d := range f.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			if decl.Name != nil {
				out[decl.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name != nil {
						out[s.Name.Name] = true
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						out[n.Name] = true
					}
				}
			}
		}
	}
	return out
}

// importAliases maps every usable import alias (explicit or default) to its
// import path for a parsed file.
func importAliases(f *ast.File) map[string]string {
	out := make(map[string]string)
	for _, imp := range f.Imports {
		path := unquoteImport(imp.Path.Value)
		if path == "" {
			continue
		}
		if imp.Name != nil {
			switch imp.Name.Name {
			case "_", ".":
				continue
			default:
				out[imp.Name.Name] = path
			}
			continue
		}
		out[defaultImportName(path)] = path
	}
	return out
}

// defaultImportName derives the default package name of an import path.
func defaultImportName(path string) string {
	parts := strings.Split(path, "/")
	name := parts[len(parts)-1]
	if i := strings.IndexByte(name, '-'); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// selectorsWithIdentX returns every SelectorExpr whose selector target X is a
// bare identifier.
func selectorsWithIdentX(f *ast.File) []*ast.SelectorExpr {
	var out []*ast.SelectorExpr
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if _, isIdent := sel.X.(*ast.Ident); isIdent {
				out = append(out, sel)
			}
		}
		return true
	})
	return out
}

// packageSymbols returns the top-level symbol names declared by a package
// directory in the proposed state.
func packageSymbols(view *wsView, sor *layer2.Sor, pkgDir string) map[string]bool {
	syms := make(map[string]bool)
	for _, f := range sor.Files() {
		if packageDir(f) != pkgDir || view.deleted[f] {
			continue
		}
		content, ok := view.content[f]
		if !ok {
			src, err := sor.Source(f)
			if err != nil {
				continue
			}
			content = string(src)
		}
		collectDeclared(content, syms)
	}
	for f := range view.content {
		if packageDir(f) != pkgDir {
			continue
		}
		collectDeclared(view.content[f], syms)
	}
	return syms
}

// collectDeclared adds every top-level name declared by src to syms.
func collectDeclared(src string, syms map[string]bool) {
	f, err := parser.ParseFile(token.NewFileSet(), "collect.go", []byte(src), parser.SkipObjectResolution)
	if err != nil {
		return
	}
	for n := range declaredNames(f) {
		syms[n] = true
	}
}

// positionOfImport locates the line/column of an import statement in content.
func positionOfImport(path, impPath, content string) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, []byte(content), parser.SkipObjectResolution)
	if err != nil {
		return path
	}
	for _, imp := range f.Imports {
		if unquoteImport(imp.Path.Value) != impPath {
			continue
		}
		pos := fset.Position(imp.Pos())
		return fmt.Sprintf("%s:%d:%d", path, pos.Line, pos.Column)
	}
	return path
}

// unquoteImport strips the raw string literal quotes from an import path.
func unquoteImport(raw string) string {
	return strings.Trim(raw, `"`)
}

// resolvePkgDir matches an import path to an in-repo package directory by
// longest path suffix, mirroring the Layer 2 resolution strategy.
func resolvePkgDir(importPath string, pkgDirs []string) string {
	if importPath == "" {
		return ""
	}
	for _, d := range pkgDirs {
		if d == importPath {
			return d
		}
	}
	best := ""
	for _, d := range pkgDirs {
		if d == "root" {
			continue
		}
		if strings.HasSuffix(importPath, d) && len(d) > len(best) {
			best = d
		}
	}
	return best
}

// isInRepoImport reports whether an import is a candidate in-repo import:
// either matching a known surviving package directory or module-prefixed.
func isInRepoImport(imp, module string, pkgDirs []string) bool {
	if resolvePkgDir(imp, pkgDirs) != "" {
		return true
	}
	if module == "" {
		return false
	}
	return imp == module || strings.HasPrefix(imp, module+"/")
}

// packageDir derives a package directory from a file path.
func packageDir(path string) string {
	dir := filepath.ToSlash(filepath.Dir(path))
	if dir == "." {
		return "root"
	}
	return dir
}

// resolveWithin verifies that the path is contained within root and returns
// the absolute path when it is.
func resolveWithin(root, path string) (string, error) {
	abs, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("path %q escapes workspace root", path)
	}
	return abs, nil
}

// failStructural returns a failing structural result with the given message.
func failStructural(msg string) *ValidationResult {
	return &ValidationResult{OK: false, Stage: StageStructural, Summary: msg, ExitCode: -1}
}
